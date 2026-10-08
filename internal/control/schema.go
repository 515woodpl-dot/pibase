package control

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

const schemaSQL = `
CREATE SCHEMA IF NOT EXISTS control;
REVOKE ALL ON SCHEMA control FROM PUBLIC;
CREATE TABLE IF NOT EXISTS control.accounts (
  id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
  email         text        NOT NULL UNIQUE CHECK (email = lower(trim(email))),
  password_hash text        NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS control.sessions (
  token_hash  bytea       PRIMARY KEY,
  account_id  uuid        NOT NULL REFERENCES control.accounts(id) ON DELETE CASCADE,
  expires_at  timestamptz NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now()
);
-- Organization the account belongs to, shown in the dashboard. Accounts created
-- before this column existed have an empty name and are asked to add one.
ALTER TABLE control.accounts ADD COLUMN IF NOT EXISTS organization text NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS sessions_account_idx ON control.sessions(account_id);
CREATE INDEX IF NOT EXISTS sessions_expires_idx ON control.sessions(expires_at);
CREATE TABLE IF NOT EXISTS control.projects (
  id text PRIMARY KEY CHECK (id ~ '^[0-9a-f]{8}$'),
  name text NOT NULL,
  database_name text NOT NULL UNIQUE,
  anon_role text NOT NULL UNIQUE,
  authenticated_role text NOT NULL UNIQUE,
  service_role text NOT NULL UNIQUE,
  admin_role text NOT NULL UNIQUE,
  jwt_secret_encrypted bytea NOT NULL,
  anon_key_hash bytea NOT NULL,
  service_key_hash bytea NOT NULL,
  schema_version integer NOT NULL DEFAULT 0 CHECK (schema_version >= 0),
  schema_error text,
  max_database_bytes bigint NOT NULL DEFAULT 1073741824 CHECK (max_database_bytes > 0),
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE control.projects ADD COLUMN IF NOT EXISTS max_database_bytes bigint NOT NULL DEFAULT 1073741824;
ALTER TABLE control.projects ADD COLUMN IF NOT EXISTS schema_version integer NOT NULL DEFAULT 0;
ALTER TABLE control.projects ADD COLUMN IF NOT EXISTS schema_error text;
ALTER TABLE control.projects ADD COLUMN IF NOT EXISTS owner_id uuid REFERENCES control.accounts(id);
-- Encrypted copies of the current API keys so the dashboard can show them
-- again. NULL for keys issued before this column existed; rotating fills it.
ALTER TABLE control.projects ADD COLUMN IF NOT EXISTS anon_key_encrypted bytea;
ALTER TABLE control.projects ADD COLUMN IF NOT EXISTS service_key_encrypted bytea;
-- Per-minute request counts by service and HTTP status class, written by the
-- gateway in batches and pruned after 8 days.
CREATE TABLE IF NOT EXISTS control.project_activity (
  project_id text NOT NULL REFERENCES control.projects(id) ON DELETE CASCADE,
  minute timestamptz NOT NULL,
  service text NOT NULL CHECK (service IN ('database','auth','storage','realtime')),
  status_class smallint NOT NULL CHECK (status_class BETWEEN 1 AND 5),
  requests bigint NOT NULL CHECK (requests > 0),
  PRIMARY KEY (project_id,minute,service,status_class)
);
CREATE INDEX IF NOT EXISTS project_activity_minute_idx ON control.project_activity(minute);
CREATE TABLE IF NOT EXISTS control.project_usage (
  project_id text NOT NULL REFERENCES control.projects(id) ON DELETE CASCADE,
  day date NOT NULL DEFAULT current_date,
  requests bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id,day)
);
-- This view is gateway-owned. Recreate it so upgrades can change its column
-- order as the projects table evolves; CREATE OR REPLACE VIEW cannot reorder
-- or insert columns before existing view columns.
DROP VIEW IF EXISTS control.project_catalog;
CREATE VIEW control.project_catalog AS
  SELECT id, database_name, anon_role, authenticated_role, service_role,
         admin_role, jwt_secret_encrypted, anon_key_hash, service_key_hash,
         max_database_bytes, name, created_at, schema_version, schema_error,
         owner_id
  FROM control.projects;
GRANT USAGE ON SCHEMA control TO authenticator;
GRANT SELECT ON control.project_catalog TO authenticator;
GRANT SELECT,INSERT,UPDATE ON control.project_usage TO authenticator;
GRANT SELECT,INSERT,UPDATE,DELETE ON control.project_activity TO authenticator;
`

const mfaUpgradeSQL = `
ALTER TABLE auth.refresh_tokens
  ADD COLUMN IF NOT EXISTS aal text NOT NULL DEFAULT 'aal1'
  CHECK (aal IN ('aal1','aal2'));
CREATE TABLE IF NOT EXISTS auth.mfa_factors (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
  factor_type text NOT NULL CHECK (factor_type = 'totp'),
  status text NOT NULL DEFAULT 'unverified' CHECK (status IN ('unverified','verified')),
  friendly_name text,
  secret_encrypted bytea NOT NULL,
  last_used_counter bigint,
  last_challenged_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE auth.mfa_factors DROP CONSTRAINT IF EXISTS mfa_factors_user_id_friendly_name_key;
CREATE UNIQUE INDEX IF NOT EXISTS mfa_factors_user_friendly_idx ON auth.mfa_factors(user_id,COALESCE(friendly_name,''));
CREATE INDEX IF NOT EXISTS mfa_factors_user_idx ON auth.mfa_factors(user_id);
CREATE TABLE IF NOT EXISTS auth.mfa_challenges (
  id uuid PRIMARY KEY,
  factor_id uuid NOT NULL REFERENCES auth.mfa_factors(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  verified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS mfa_challenges_factor_idx
  ON auth.mfa_challenges(factor_id,created_at DESC);
REVOKE ALL ON auth.mfa_factors,auth.mfa_challenges FROM PUBLIC;
`

func Bootstrap(ctx context.Context, adminURL, password string) error {
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("connect admin database: %w", err)
	}
	defer conn.Close(ctx)

	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='authenticator')`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		if _, err := conn.Exec(ctx, `ALTER ROLE authenticator WITH LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD `+literal(password)); err != nil {
			return err
		}
	} else {
		if _, err := conn.Exec(ctx, `CREATE ROLE authenticator LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE PASSWORD `+literal(password)); err != nil {
			return err
		}
	}
	var dbExists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname='_control')`).Scan(&dbExists); err != nil {
		return err
	}
	if !dbExists {
		if _, err := conn.Exec(ctx, `CREATE DATABASE _control`); err != nil {
			return err
		}
	}
	if _, err := conn.Exec(ctx, `REVOKE CONNECT ON DATABASE _control FROM PUBLIC`); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, `GRANT CONNECT ON DATABASE _control TO authenticator`); err != nil {
		return err
	}

	controlURL, err := withDatabase(adminURL, "_control")
	if err != nil {
		return err
	}
	controlConn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		return fmt.Errorf("connect control database: %w", err)
	}
	defer controlConn.Close(ctx)
	if _, err := controlConn.Exec(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply control schema: %w", err)
	}
	return upgradeProjectSchemas(ctx, controlConn, adminURL)
}

// upgradeProjectSchemas applies all gateway-owned schema changes to every
// managed project. Schema-less M0 measurement databases are recorded as
// version zero so background workers can skip them without probing each DB.
func upgradeProjectSchemas(ctx context.Context, controlConn *pgx.Conn, adminURL string) error {
	rows, err := controlConn.Query(ctx, `SELECT id,database_name,anon_role,authenticated_role,service_role,admin_role FROM control.projects ORDER BY id`)
	if err != nil {
		return fmt.Errorf("list projects for schema upgrade: %w", err)
	}
	var targets []projectUpgradeTarget
	for rows.Next() {
		var item projectUpgradeTarget
		if err := rows.Scan(&item.id, &item.database, &item.anon, &item.authenticated, &item.service, &item.admin); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	failures := 0
	recordFailure := func(item projectUpgradeTarget, projectErr error) error {
		failures++
		message := boundedSchemaError(projectErr)
		slog.Warn("project schema upgrade skipped", "project", item.id, "database", item.database, "error", message)
		_, err := controlConn.Exec(ctx, `UPDATE control.projects SET schema_error=$2 WHERE id=$1`, item.id, message)
		return err
	}
	for _, item := range targets {
		version, err := applyProjectSchemaUpgrade(ctx, adminURL, item)
		if err != nil {
			if recordErr := recordFailure(item, err); recordErr != nil {
				return fmt.Errorf("record project %s schema failure: %w", item.database, recordErr)
			}
			continue
		}
		if _, err = controlConn.Exec(ctx, `UPDATE control.projects SET schema_version=$2,schema_error=NULL WHERE id=$1`, item.id, version); err != nil {
			return fmt.Errorf("record project %s schema: %w", item.database, err)
		}
	}
	if failures > 0 {
		slog.Warn("project schema upgrades completed with skipped projects", "failures", failures)
	}
	return nil
}

type projectUpgradeTarget struct{ id, database, anon, authenticated, service, admin string }

func boundedSchemaError(projectErr error) string {
	message := projectErr.Error()
	if len(message) > 2000 {
		message = message[:2000]
	}
	return message
}

func applyProjectSchemaUpgrade(ctx context.Context, adminURL string, item projectUpgradeTarget) (int, error) {
	projectURL, err := withDatabase(adminURL, item.database)
	if err != nil {
		return 0, err
	}
	project, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		return 0, fmt.Errorf("connect for schema upgrade: %w", err)
	}
	defer project.Close(ctx)
	var authReady bool
	if err = project.QueryRow(ctx, `SELECT to_regclass('auth.users') IS NOT NULL AND to_regclass('auth.refresh_tokens') IS NOT NULL`).Scan(&authReady); err != nil {
		return 0, fmt.Errorf("inspect schema: %w", err)
	}
	// M0 measurement databases intentionally predate the auth schema. They
	// remain useful for resource measurements but have nothing to migrate.
	if !authReady {
		return 0, nil
	}
	// Install the database-enforced extension boundary separately so a
	// customer-damaged managed schema cannot roll it back with its upgrade.
	if _, err = project.Exec(ctx, extensionGuardSQL); err != nil {
		return 0, fmt.Errorf("install extension guard: %w", err)
	}
	tx, err := project.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin schema upgrade: %w", err)
	}
	defer tx.Rollback(ctx)
	if err = ensureNoCustomerManagedObjects(ctx, tx); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, managedProjectUpgradeSQL(item.anon, item.authenticated, item.service, item.admin)); err != nil {
		return 0, fmt.Errorf("upgrade schema: %w", err)
	}
	if err = revokeStrayCreateGrants(ctx, tx); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit schema upgrade: %w", err)
	}
	return currentProjectSchemaVersion, nil
}

// UpgradeProjectSchema reapplies the canonical managed schema after a restore.
// It records the same version/error state as bootstrap but only touches the
// requested project.
func UpgradeProjectSchema(ctx context.Context, adminURL, id string) error {
	controlURL, err := withDatabase(adminURL, "_control")
	if err != nil {
		return err
	}
	controlConn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		return fmt.Errorf("connect control database: %w", err)
	}
	defer controlConn.Close(ctx)
	var item projectUpgradeTarget
	err = controlConn.QueryRow(ctx, `SELECT id,database_name,anon_role,authenticated_role,service_role,admin_role FROM control.projects WHERE id=$1`, id).
		Scan(&item.id, &item.database, &item.anon, &item.authenticated, &item.service, &item.admin)
	if err == pgx.ErrNoRows {
		return fmt.Errorf("project %q not found", id)
	}
	if err != nil {
		return err
	}
	version, upgradeErr := applyProjectSchemaUpgrade(ctx, adminURL, item)
	if upgradeErr != nil {
		message := boundedSchemaError(upgradeErr)
		if _, err = controlConn.Exec(ctx, `UPDATE control.projects SET schema_error=$2 WHERE id=$1`, id, message); err != nil {
			return fmt.Errorf("record project %s schema failure: %w", item.database, err)
		}
		return upgradeErr
	}
	if _, err = controlConn.Exec(ctx, `UPDATE control.projects SET schema_version=$2,schema_error=NULL WHERE id=$1`, id, version); err != nil {
		return fmt.Errorf("record project %s schema: %w", item.database, err)
	}
	return nil
}

func literal(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }

func withDatabase(raw, database string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse database URL: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("database URL must use postgres or postgresql scheme")
	}
	parsed.Path = "/" + database
	return parsed.String(), nil
}
