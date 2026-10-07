package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/515woodpl-dot/pibase/internal/secretbox"
	"github.com/jackc/pgx/v5"
)

type Project struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Database   string `json:"database"`
	AnonKey    string `json:"anon_key"`
	ServiceKey string `json:"service_key"`
}

// ProjectSummary is the operator-facing, non-secret project inventory record.
// API keys and encrypted signing material are deliberately excluded.
type ProjectSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Database    string    `json:"database"`
	CreatedAt   time.Time `json:"created_at"`
	MaxDatabase int64     `json:"max_database_bytes"`
	SchemaError *string   `json:"schema_error"`
}

type ProjectUsage struct {
	ProjectID string    `json:"project_id"`
	Day       time.Time `json:"day"`
	Requests  int64     `json:"requests"`
}

type ProjectStat struct {
	ProjectSummary
	DatabaseBytes int64  `json:"database_bytes"`
	StorageBytes  int64  `json:"storage_bytes"`
	Requests30d   int64  `json:"requests_30d"`
	APIURL        string `json:"api_url,omitempty"`
}

// ProjectStats returns bounded operator telemetry without keys or encrypted material.
// ProjectStats returns operator telemetry for all projects.
func ProjectStats(ctx context.Context, adminURL, storageRoot string) ([]ProjectStat, error) {
	return projectStats(ctx, adminURL, storageRoot, "")
}

// ProjectStatsByOwner returns telemetry for projects owned by the given account.
func ProjectStatsByOwner(ctx context.Context, adminURL, storageRoot, ownerID string) ([]ProjectStat, error) {
	return projectStats(ctx, adminURL, storageRoot, ownerID)
}

func projectStats(ctx context.Context, adminURL, storageRoot, ownerID string) ([]ProjectStat, error) {
	controlURL, err := withDatabase(adminURL, "_control")
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	const base = `SELECT p.id,p.name,p.database_name,p.created_at,p.max_database_bytes,p.schema_error,
pg_database_size(p.database_name),COALESCE((SELECT sum(requests) FROM control.project_usage u WHERE u.project_id=p.id AND u.day>=current_date-29),0)
	FROM control.project_catalog p`
	var rows pgx.Rows
	if ownerID != "" {
		rows, err = conn.Query(ctx, base+` WHERE p.owner_id=$1 ORDER BY p.created_at,p.id`, ownerID)
	} else {
		rows, err = conn.Query(ctx, base+` ORDER BY p.created_at,p.id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ProjectStat
	for rows.Next() {
		var item ProjectStat
		if err := rows.Scan(&item.ID, &item.Name, &item.Database, &item.CreatedAt, &item.MaxDatabase, &item.SchemaError, &item.DatabaseBytes, &item.Requests30d); err != nil {
			return nil, err
		}
		root := filepath.Join(storageRoot, item.ID)
		err := filepath.Walk(root, func(_ string, info os.FileInfo, walkErr error) error {
			if os.IsNotExist(walkErr) {
				return nil
			}
			if walkErr != nil {
				return walkErr
			}
			if !info.IsDir() && !strings.Contains(info.Name(), ".tmp-") {
				item.StorageBytes += info.Size()
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func CreateProject(ctx context.Context, adminURL, name string, masterKey []byte) (_ Project, err error) {
	if name == "" {
		return Project{}, fmt.Errorf("project name is required")
	}
	idBytes := make([]byte, 4)
	if _, err := rand.Read(idBytes); err != nil {
		return Project{}, err
	}
	id := hex.EncodeToString(idBytes)
	prefix := "p_" + id
	database := prefix
	anon, authenticated := prefix+"_anon", prefix+"_authenticated"
	service, admin := prefix+"_service_role", prefix+"_admin"

	root, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return Project{}, fmt.Errorf("connect admin database: %w", err)
	}
	defer root.Close(ctx)
	created := []string{}
	cleanup := func() {
		_, _ = root.Exec(context.Background(), `DROP DATABASE IF EXISTS `+pgx.Identifier{database}.Sanitize()+` WITH (FORCE)`)
		for i := len(created) - 1; i >= 0; i-- {
			_, _ = root.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{created[i]}.Sanitize())
		}
	}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()

	roles := []struct{ name, attrs string }{{anon, "NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE"}, {authenticated, "NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE"}, {service, "NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE BYPASSRLS"}, {admin, "NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE"}}
	for _, role := range roles {
		if _, err = root.Exec(ctx, `CREATE ROLE `+pgx.Identifier{role.name}.Sanitize()+` `+role.attrs); err != nil {
			return Project{}, err
		}
		created = append(created, role.name)
	}
	for _, role := range []string{anon, authenticated, service, admin} {
		if _, err = root.Exec(ctx, `GRANT `+pgx.Identifier{role}.Sanitize()+` TO authenticator`); err != nil {
			return Project{}, err
		}
	}
	if _, err = root.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{database}.Sanitize()+` OWNER `+pgx.Identifier{admin}.Sanitize()); err != nil {
		return Project{}, err
	}
	if _, err = root.Exec(ctx, `REVOKE CONNECT ON DATABASE `+pgx.Identifier{database}.Sanitize()+` FROM PUBLIC`); err != nil {
		return Project{}, err
	}
	for _, role := range []string{"authenticator", anon, authenticated, service, admin} {
		if _, err = root.Exec(ctx, `GRANT CONNECT ON DATABASE `+pgx.Identifier{database}.Sanitize()+` TO `+pgx.Identifier{role}.Sanitize()); err != nil {
			return Project{}, err
		}
	}

	projectURL, err := withDatabase(adminURL, database)
	if err != nil {
		return Project{}, err
	}
	projectDB, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		return Project{}, err
	}
	setup := `REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA auth AUTHORIZATION ` + pgx.Identifier{admin}.Sanitize() + `;
CREATE SCHEMA storage AUTHORIZATION ` + pgx.Identifier{admin}.Sanitize() + `;
REVOKE ALL ON SCHEMA auth, storage FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO ` + pgx.Identifier{anon}.Sanitize() + `, ` + pgx.Identifier{authenticated}.Sanitize() + `, ` + pgx.Identifier{service}.Sanitize() + `;
CREATE TABLE auth.users (
  id uuid PRIMARY KEY,
  email text NOT NULL UNIQUE,
  password_hash text NOT NULL,
  email_confirmed_at timestamptz,
  confirmation_token_hash bytea,
  confirmation_expires_at timestamptz,
  reset_token_hash bytea,
  reset_expires_at timestamptz,
  raw_user_meta_data jsonb NOT NULL DEFAULT '{}'::jsonb,
  banned_until timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE auth.refresh_tokens (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  revoked_at timestamptz,
  replaced_by uuid REFERENCES auth.refresh_tokens(id),
  aal text NOT NULL DEFAULT 'aal1' CHECK (aal IN ('aal1','aal2')),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_idx ON auth.refresh_tokens(user_id);
CREATE TABLE auth.mfa_factors (
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
CREATE UNIQUE INDEX mfa_factors_user_friendly_idx ON auth.mfa_factors(user_id,COALESCE(friendly_name,''));
CREATE INDEX mfa_factors_user_idx ON auth.mfa_factors(user_id);
CREATE TABLE auth.mfa_challenges (
  id uuid PRIMARY KEY,
  factor_id uuid NOT NULL REFERENCES auth.mfa_factors(id) ON DELETE CASCADE,
  user_id uuid NOT NULL REFERENCES auth.users(id) ON DELETE CASCADE,
  expires_at timestamptz NOT NULL,
  verified_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mfa_challenges_factor_idx ON auth.mfa_challenges(factor_id,created_at DESC);
CREATE TABLE storage.buckets (
  id text PRIMARY KEY,
  name text NOT NULL UNIQUE,
  owner_id uuid,
  public boolean NOT NULL DEFAULT false,
  file_size_limit bigint,
  allowed_mime_types text[],
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE storage.objects (
  id uuid PRIMARY KEY,
  bucket_id text NOT NULL REFERENCES storage.buckets(id) ON DELETE CASCADE,
  name text NOT NULL,
  owner_id uuid,
  metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  size bigint NOT NULL CHECK (size >= 0),
  mime_type text,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE(bucket_id,name)
);
CREATE INDEX storage_objects_bucket_idx ON storage.objects(bucket_id,name);
REVOKE ALL ON SCHEMA auth FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA auth FROM PUBLIC;
GRANT USAGE ON SCHEMA auth TO ` + pgx.Identifier{anon}.Sanitize() + `, ` + pgx.Identifier{authenticated}.Sanitize() + `, ` + pgx.Identifier{service}.Sanitize() + `, ` + pgx.Identifier{admin}.Sanitize() + `;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA auth TO ` + pgx.Identifier{admin}.Sanitize() + `;
REVOKE ALL ON SCHEMA storage FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA storage FROM PUBLIC;
GRANT USAGE ON SCHEMA storage TO ` + pgx.Identifier{anon}.Sanitize() + `, ` + pgx.Identifier{authenticated}.Sanitize() + `, ` + pgx.Identifier{service}.Sanitize() + `, ` + pgx.Identifier{admin}.Sanitize() + `;
GRANT SELECT ON storage.buckets,storage.objects TO ` + pgx.Identifier{anon}.Sanitize() + `, ` + pgx.Identifier{authenticated}.Sanitize() + `;
GRANT INSERT,UPDATE,DELETE ON storage.objects TO ` + pgx.Identifier{authenticated}.Sanitize() + `;
GRANT SELECT,INSERT,UPDATE,DELETE ON storage.buckets,storage.objects TO ` + pgx.Identifier{service}.Sanitize() + `;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA storage TO ` + pgx.Identifier{admin}.Sanitize() + `;
ALTER TABLE storage.buckets ENABLE ROW LEVEL SECURITY;
ALTER TABLE storage.objects ENABLE ROW LEVEL SECURITY;
CREATE OR REPLACE FUNCTION auth.uid() RETURNS uuid
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, auth
AS $$ SELECT CASE WHEN current_setting('request.jwt.claims', true) ~ '"sub"\s*:\s*"[0-9a-fA-F-]{36}"' THEN
  (current_setting('request.jwt.claims', true)::jsonb ->> 'sub')::uuid ELSE NULL END $$;
CREATE POLICY storage_bucket_read ON storage.buckets FOR SELECT TO ` + pgx.Identifier{anon}.Sanitize() + `, ` + pgx.Identifier{authenticated}.Sanitize() + ` USING (public OR owner_id = auth.uid());
CREATE POLICY storage_bucket_service ON storage.buckets FOR ALL TO ` + pgx.Identifier{service}.Sanitize() + ` USING (true) WITH CHECK (true);
CREATE POLICY storage_object_read ON storage.objects FOR SELECT TO ` + pgx.Identifier{anon}.Sanitize() + `, ` + pgx.Identifier{authenticated}.Sanitize() + ` USING (EXISTS (SELECT 1 FROM storage.buckets b WHERE b.id = bucket_id AND (b.public OR b.owner_id = auth.uid())) OR owner_id = auth.uid());
CREATE POLICY storage_object_owner ON storage.objects FOR ALL TO ` + pgx.Identifier{authenticated}.Sanitize() + ` USING (owner_id = auth.uid()) WITH CHECK (owner_id = auth.uid());
CREATE POLICY storage_object_service ON storage.objects FOR ALL TO ` + pgx.Identifier{service}.Sanitize() + ` USING (true) WITH CHECK (true);
CREATE OR REPLACE FUNCTION auth.uid() RETURNS uuid
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, auth
AS $$ SELECT CASE WHEN current_setting('request.jwt.claims', true) ~ '"sub"\s*:\s*"[0-9a-fA-F-]{36}"' THEN
  (current_setting('request.jwt.claims', true)::jsonb ->> 'sub')::uuid ELSE NULL END $$;
REVOKE ALL ON FUNCTION auth.uid() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth.uid() TO ` + pgx.Identifier{anon}.Sanitize() + `, ` + pgx.Identifier{authenticated}.Sanitize() + `, ` + pgx.Identifier{service}.Sanitize() + `, ` + pgx.Identifier{admin}.Sanitize() + `;`
	setup += `
CREATE SCHEMA realtime AUTHORIZATION ` + pgx.Identifier{admin}.Sanitize() + `;
REVOKE ALL ON SCHEMA realtime FROM PUBLIC;
CREATE FUNCTION realtime.notify_change() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog
AS $$
DECLARE change_id text;
BEGIN
  change_id := COALESCE(to_jsonb(NEW)->>'id', to_jsonb(OLD)->>'id');
  IF change_id IS NOT NULL THEN
    PERFORM pg_notify('pibase_changes', json_build_object('table',TG_TABLE_NAME,'op',TG_OP,'id',change_id)::text);
  END IF;
  RETURN COALESCE(NEW, OLD);
END $$;
CREATE FUNCTION realtime.enable_table(target regclass) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog
AS $$
DECLARE target_schema text; target_table text;
BEGIN
  SELECT n.nspname,c.relname INTO target_schema,target_table
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE c.oid=target;
  IF target_schema <> 'public' OR target_table IS NULL THEN
    RAISE EXCEPTION 'only public tables can be enabled for realtime';
  END IF;
  EXECUTE format('DROP TRIGGER IF EXISTS pibase_realtime_change ON %I.%I',target_schema,target_table);
  EXECUTE format('CREATE TRIGGER pibase_realtime_change AFTER INSERT OR UPDATE OR DELETE ON %I.%I FOR EACH ROW EXECUTE FUNCTION realtime.notify_change()',target_schema,target_table);
END $$;
REVOKE ALL ON FUNCTION realtime.notify_change(), realtime.enable_table(regclass) FROM PUBLIC;
GRANT USAGE ON SCHEMA realtime TO ` + pgx.Identifier{admin}.Sanitize() + `;
GRANT EXECUTE ON FUNCTION realtime.enable_table(regclass) TO ` + pgx.Identifier{admin}.Sanitize() + `;`
	setup += `
CREATE SCHEMA cron AUTHORIZATION ` + pgx.Identifier{admin}.Sanitize() + `;
REVOKE ALL ON SCHEMA cron FROM PUBLIC;
CREATE TABLE cron.jobs (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  schedule text NOT NULL,
  sql text,
  webhook_url text,
  webhook_method text NOT NULL DEFAULT 'POST',
  webhook_headers jsonb NOT NULL DEFAULT '{}'::jsonb,
  webhook_body bytea NOT NULL DEFAULT ''::bytea,
  enabled boolean NOT NULL DEFAULT true,
  next_run_at timestamptz NOT NULL DEFAULT now(),
  last_run_at timestamptz,
  last_error text,
  CHECK ((sql IS NOT NULL) <> (webhook_url IS NOT NULL)),
  CHECK (webhook_method IN ('POST','PUT','PATCH','DELETE')),
  CHECK (octet_length(webhook_body) <= 1048576)
);
CREATE TABLE cron.webhook_outbox (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  url text NOT NULL,
  method text NOT NULL DEFAULT 'POST' CHECK (method IN ('POST','PUT','PATCH','DELETE')),
  headers jsonb NOT NULL DEFAULT '{}'::jsonb,
  body bytea NOT NULL DEFAULT ''::bytea CHECK (octet_length(body) <= 1048576),
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  leased_until timestamptz,
  delivered_at timestamptz,
  dead_lettered_at timestamptz,
  last_error text,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX webhook_outbox_ready_idx ON cron.webhook_outbox(next_attempt_at) WHERE delivered_at IS NULL AND dead_lettered_at IS NULL;
CREATE INDEX webhook_outbox_pending_idx ON cron.webhook_outbox(id) WHERE delivered_at IS NULL AND dead_lettered_at IS NULL;
-- A project database is the tenancy boundary, so this is a per-project cap.
-- Serializing the count and insert prevents concurrent event triggers from
-- exceeding it. At the 1 MiB body limit, 256 pending items bound payload
-- storage to roughly 256 MiB before Postgres overhead.
CREATE FUNCTION cron.enforce_outbox_limit() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,cron
AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtext('pibase:cron.webhook_outbox'));
  IF (SELECT count(*) FROM cron.webhook_outbox WHERE delivered_at IS NULL AND dead_lettered_at IS NULL) >= 256 THEN
    RAISE EXCEPTION 'cron webhook outbox pending limit (256) reached';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER cron_webhook_outbox_limit
BEFORE INSERT ON cron.webhook_outbox FOR EACH ROW EXECUTE FUNCTION cron.enforce_outbox_limit();
-- Attach this function to an owner-created AFTER row trigger to place an
-- event in the same durable outbox used by scheduled webhooks.  The target
-- URL and optional method are trigger arguments, so no network action occurs
-- in the customer transaction.
CREATE FUNCTION cron.enqueue_webhook() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,cron
AS $$
DECLARE target_url text := TG_ARGV[0]; target_method text := COALESCE(NULLIF(TG_ARGV[1], ''), 'POST');
BEGIN
  IF target_url IS NULL OR target_url = '' THEN
    RAISE EXCEPTION 'cron.enqueue_webhook requires a URL trigger argument';
  END IF;
  IF target_method NOT IN ('POST','PUT','PATCH','DELETE') THEN
    RAISE EXCEPTION 'cron.enqueue_webhook method must be POST, PUT, PATCH, or DELETE';
  END IF;
  INSERT INTO cron.webhook_outbox(url,method,body)
  VALUES (target_url, target_method,
    convert_to(json_build_object(
      'schema', TG_TABLE_SCHEMA, 'table', TG_TABLE_NAME, 'type', TG_OP,
      'record', CASE WHEN TG_OP = 'DELETE' THEN to_jsonb(OLD) ELSE to_jsonb(NEW) END
    )::text, 'UTF8'));
  RETURN COALESCE(NEW, OLD);
END $$;
REVOKE ALL ON ALL TABLES IN SCHEMA cron FROM PUBLIC;
REVOKE ALL ON FUNCTION cron.enforce_outbox_limit(), cron.enqueue_webhook() FROM PUBLIC;
GRANT USAGE ON SCHEMA cron TO ` + pgx.Identifier{admin}.Sanitize() + `;
GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA cron TO ` + pgx.Identifier{admin}.Sanitize() + `;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA cron TO ` + pgx.Identifier{admin}.Sanitize() + `;
GRANT EXECUTE ON FUNCTION cron.enqueue_webhook() TO ` + pgx.Identifier{admin}.Sanitize() + `;`
	_, err = projectDB.Exec(ctx, setup)
	if err == nil {
		_, err = projectDB.Exec(ctx, extensionGuardSQL)
	}
	if err == nil {
		// Keep fresh projects on the same canonical schema path used to upgrade
		// existing projects, preventing milestone fixes from applying to only
		// one of those paths.
		_, err = projectDB.Exec(ctx, managedProjectUpgradeSQL(anon, authenticated, service, admin))
	}
	projectDB.Close(ctx)
	if err != nil {
		return Project{}, err
	}

	jwtSecret := make([]byte, 32)
	if _, err = rand.Read(jwtSecret); err != nil {
		return Project{}, err
	}
	anonKey, err := keys.JWT(jwtSecret, "anon")
	if err != nil {
		return Project{}, err
	}
	serviceKey, err := keys.JWT(jwtSecret, "service_role")
	if err != nil {
		return Project{}, err
	}
	encrypted, err := secretbox.Encrypt(masterKey, jwtSecret)
	if err != nil {
		return Project{}, err
	}
	anonEncrypted, serviceEncrypted, err := encryptKeys(masterKey, anonKey, serviceKey)
	if err != nil {
		return Project{}, err
	}

	controlURL, err := withDatabase(adminURL, "_control")
	if err != nil {
		return Project{}, err
	}
	controlDB, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		return Project{}, err
	}
	defer controlDB.Close(ctx)
	_, err = controlDB.Exec(ctx, `INSERT INTO control.projects
 (id,name,database_name,anon_role,authenticated_role,service_role,admin_role,jwt_secret_encrypted,anon_key_hash,service_key_hash,schema_version,anon_key_encrypted,service_key_encrypted)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, id, name, database, anon, authenticated, service, admin, encrypted, keys.Hash(anonKey), keys.Hash(serviceKey), currentProjectSchemaVersion, anonEncrypted, serviceEncrypted)
	if err != nil {
		return Project{}, err
	}
	return Project{ID: id, Name: name, Database: database, AnonKey: anonKey, ServiceKey: serviceKey}, nil
}

func JSON(project Project) ([]byte, error) { return json.MarshalIndent(project, "", "  ") }

// RotateKeys replaces the project's JWT signing secret with a fresh random one,
// then signs new anon and service_role keys with it. The old secret, and
// therefore all previously issued API keys and user auth tokens, are
// immediately invalidated. The keys are stored encrypted with the master key
// so the owner can view them again; they are never stored in plaintext.
func RotateKeys(ctx context.Context, adminURL, projectID string, masterKey []byte) (anonKey, serviceKey string, err error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return "", "", err
	}
	defer conn.Close(ctx)
	// Verify project exists before generating new material.
	var exists bool
	err = conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM control.projects WHERE id=$1)`, projectID).Scan(&exists)
	if err != nil {
		return "", "", err
	}
	if !exists {
		return "", "", ErrNotFound
	}
	jwtSecret := make([]byte, 32)
	if _, err = rand.Read(jwtSecret); err != nil {
		return "", "", err
	}
	anonKey, err = keys.JWT(jwtSecret, "anon")
	if err != nil {
		return "", "", err
	}
	serviceKey, err = keys.JWT(jwtSecret, "service_role")
	if err != nil {
		return "", "", err
	}
	encrypted, err := secretbox.Encrypt(masterKey, jwtSecret)
	if err != nil {
		return "", "", fmt.Errorf("encrypt jwt secret: %w", err)
	}
	anonEncrypted, serviceEncrypted, err := encryptKeys(masterKey, anonKey, serviceKey)
	if err != nil {
		return "", "", err
	}
	tag, err := conn.Exec(ctx,
		`UPDATE control.projects SET jwt_secret_encrypted=$1, anon_key_hash=$2, service_key_hash=$3, anon_key_encrypted=$4, service_key_encrypted=$5 WHERE id=$6`,
		encrypted, keys.Hash(anonKey), keys.Hash(serviceKey), anonEncrypted, serviceEncrypted, projectID,
	)
	if err != nil {
		return "", "", err
	}
	if tag.RowsAffected() == 0 {
		return "", "", ErrNotFound
	}
	return anonKey, serviceKey, nil
}

// RenameProject updates the display name of an existing project.
func RenameProject(ctx context.Context, adminURL, projectID, newName string) error {
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return fmt.Errorf("name is required")
	}
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tag, err := conn.Exec(ctx, `UPDATE control.projects SET name=$1 WHERE id=$2`, newName, projectID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EnablePGVector is deliberately an operator-plane action. Customer requests
// never receive a database-superuser connection or an extension allowlist.
func EnablePGVector(ctx context.Context, adminURL, id string) error {
	return enableOperatorExtension(ctx, adminURL, id, "vector", "pgvector")
}

// EnablePostGIS is deliberately limited to the core PostGIS extension. Raster,
// topology, geocoding, and arbitrary customer-selected extensions are outside
// this operator action.
func EnablePostGIS(ctx context.Context, adminURL, id string) error {
	return enableOperatorExtension(ctx, adminURL, id, "postgis", "PostGIS")
}

func enableOperatorExtension(ctx context.Context, adminURL, id, extension, label string) error {
	controlURL, err := withDatabase(adminURL, "_control")
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	var database string
	if err = conn.QueryRow(ctx, `SELECT database_name FROM control.projects WHERE id=$1`, id).Scan(&database); err != nil {
		return fmt.Errorf("project %q not found: %w", id, err)
	}
	projectURL, err := withDatabase(adminURL, database)
	if err != nil {
		return err
	}
	project, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		return err
	}
	defer project.Close(ctx)
	var available bool
	if err = project.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_available_extensions WHERE name=$1)`, extension).Scan(&available); err != nil {
		return fmt.Errorf("check %s availability: %w", label, err)
	}
	if !available {
		return fmt.Errorf("enable %s: extension binaries are not installed in the PostgreSQL image", label)
	}
	if _, err = project.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS `+pgx.Identifier{extension}.Sanitize()); err != nil {
		return fmt.Errorf("enable %s: %w", label, err)
	}
	return nil
}

// ListProjects returns inventory metadata only. It must be called with the
// operator/admin database URL; the authenticator role cannot read this table.
func ListProjects(ctx context.Context, adminURL string) ([]ProjectSummary, error) {
	controlURL, err := withDatabase(adminURL, "_control")
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		return nil, fmt.Errorf("connect control database: %w", err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT id,name,database_name,created_at,max_database_bytes,schema_error FROM control.projects ORDER BY created_at,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []ProjectSummary
	for rows.Next() {
		var project ProjectSummary
		if err := rows.Scan(&project.ID, &project.Name, &project.Database, &project.CreatedAt, &project.MaxDatabase, &project.SchemaError); err != nil {
			return nil, err
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return projects, nil
}

// Usage returns request counters for one project. Missing projects and missing
// days are reported as errors/zero rows rather than silently mixing projects.
func Usage(ctx context.Context, adminURL, projectID string, from, to time.Time) ([]ProjectUsage, error) {
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	controlURL, err := withDatabase(adminURL, "_control")
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		return nil, fmt.Errorf("connect control database: %w", err)
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM control.projects WHERE id=$1)`, projectID).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("project %q not found", projectID)
	}
	if from.IsZero() {
		from = time.Now().UTC().AddDate(0, 0, -30)
	}
	if to.IsZero() {
		to = time.Now().UTC().AddDate(0, 0, 1)
	}
	rows, err := conn.Query(ctx, `SELECT project_id,day,requests FROM control.project_usage WHERE project_id=$1 AND day >= $2::date AND day < $3::date ORDER BY day`, projectID, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var usage []ProjectUsage
	for rows.Next() {
		var item ProjectUsage
		if err := rows.Scan(&item.ProjectID, &item.Day, &item.Requests); err != nil {
			return nil, err
		}
		usage = append(usage, item)
	}
	return usage, rows.Err()
}

// SetLimits updates the per-project resource caps. Only non-zero fields are
// applied; pass maxDatabaseBytes > 0 to change the database quota.
func SetLimits(ctx context.Context, adminURL, projectID string, maxDatabaseBytes int64) error {
	if projectID == "" {
		return fmt.Errorf("project id is required")
	}
	if maxDatabaseBytes <= 0 {
		return fmt.Errorf("max_database_bytes must be positive")
	}
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tag, err := conn.Exec(ctx, `UPDATE control.projects SET max_database_bytes=$1 WHERE id=$2`, maxDatabaseBytes, projectID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func DeleteProject(ctx context.Context, adminURL, id string) error {
	controlURL, err := withDatabase(adminURL, "_control")
	if err != nil {
		return err
	}
	controlDB, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		return err
	}
	var database string
	var roles []string
	err = controlDB.QueryRow(ctx, `SELECT database_name,ARRAY[anon_role,authenticated_role,service_role,admin_role] FROM control.projects WHERE id=$1`, id).Scan(&database, &roles)
	controlDB.Close(ctx)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("project %q not found", id)
		}
		return err
	}
	// Every step below is repeatable and the control row goes last, so a delete
	// that fails part way (DROP DATABASE cannot run in a transaction) is finished
	// by simply calling DeleteProject again.
	root, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer root.Close(ctx)
	if _, err := root.Exec(ctx, `DROP DATABASE IF EXISTS `+pgx.Identifier{database}.Sanitize()+` WITH (FORCE)`); err != nil {
		return err
	}
	for _, role := range roles {
		if _, err := root.Exec(ctx, `DROP ROLE IF EXISTS `+pgx.Identifier{role}.Sanitize()); err != nil {
			return err
		}
	}
	storageRoot := os.Getenv("PIBASE_STORAGE_ROOT")
	if storageRoot == "" {
		storageRoot = "/data/files"
	}
	if err := os.RemoveAll(filepath.Join(storageRoot, id)); err != nil {
		return fmt.Errorf("remove project storage: %w", err)
	}
	controlDB, err = pgx.Connect(ctx, controlURL)
	if err != nil {
		return err
	}
	defer controlDB.Close(ctx)
	_, err = controlDB.Exec(ctx, `DELETE FROM control.projects WHERE id=$1`, id)
	return err
}
