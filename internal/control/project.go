package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

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
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_user_idx ON auth.refresh_tokens(user_id);
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
	_, err = projectDB.Exec(ctx, setup)
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
 (id,name,database_name,anon_role,authenticated_role,service_role,admin_role,jwt_secret_encrypted,anon_key_hash,service_key_hash)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, id, name, database, anon, authenticated, service, admin, encrypted, keys.Hash(anonKey), keys.Hash(serviceKey))
	if err != nil {
		return Project{}, err
	}
	return Project{ID: id, Name: name, Database: database, AnonKey: anonKey, ServiceKey: serviceKey}, nil
}

func JSON(project Project) ([]byte, error) { return json.MarshalIndent(project, "", "  ") }

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
	root, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		return err
	}
	defer root.Close(ctx)
	if _, err := root.Exec(ctx, `DROP DATABASE `+pgx.Identifier{database}.Sanitize()+` WITH (FORCE)`); err != nil {
		return err
	}
	controlDB, err = pgx.Connect(ctx, controlURL)
	if err != nil {
		return err
	}
	if _, err := controlDB.Exec(ctx, `DELETE FROM control.projects WHERE id=$1`, id); err != nil {
		controlDB.Close(ctx)
		return err
	}
	controlDB.Close(ctx)
	for _, role := range roles {
		if _, err := root.Exec(ctx, `DROP ROLE `+pgx.Identifier{role}.Sanitize()); err != nil {
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
	return nil
}
