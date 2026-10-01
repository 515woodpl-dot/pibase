package control

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
)

const schemaSQL = `
CREATE SCHEMA IF NOT EXISTS control;
REVOKE ALL ON SCHEMA control FROM PUBLIC;
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
  max_database_bytes bigint NOT NULL DEFAULT 1073741824 CHECK (max_database_bytes > 0),
  created_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE control.projects ADD COLUMN IF NOT EXISTS max_database_bytes bigint NOT NULL DEFAULT 1073741824;
CREATE TABLE IF NOT EXISTS control.project_usage (
  project_id text NOT NULL REFERENCES control.projects(id) ON DELETE CASCADE,
  day date NOT NULL DEFAULT current_date,
  requests bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id,day)
);
CREATE OR REPLACE VIEW control.project_catalog AS
  SELECT id, database_name, anon_role, authenticated_role, service_role,
         admin_role, jwt_secret_encrypted, anon_key_hash, service_key_hash,
         max_database_bytes, name, created_at
  FROM control.projects;
GRANT USAGE ON SCHEMA control TO authenticator;
GRANT SELECT ON control.project_catalog TO authenticator;
GRANT SELECT,INSERT,UPDATE ON control.project_usage TO authenticator;
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
