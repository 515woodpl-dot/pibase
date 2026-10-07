package control

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Project schema versions are recorded in _control so workers can distinguish
// managed customer databases from the intentionally schema-less M0 measurement
// databases. Version 5 introduced realtime/cron; version 6 added TOTP MFA;
// version 8 moved managed schemas from the customer admin to the operator;
// version 9 narrowed admin table grants and data-driven CREATE revoke; version
// 10 adds public-table API defaults and RLS for owner-created tables.
const currentProjectSchemaVersion = 10

var managedSchemas = []string{"auth", "storage", "realtime", "cron"}

const extensionGuardSQL = `
CREATE OR REPLACE FUNCTION pg_catalog.pibase_guard_extension_ddl() RETURNS event_trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog
AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname=session_user AND rolsuper) THEN
    RAISE EXCEPTION 'extensions can only be managed by the operator';
  END IF;
END $$;
REVOKE ALL ON FUNCTION pg_catalog.pibase_guard_extension_ddl() FROM PUBLIC;
DROP EVENT TRIGGER IF EXISTS pibase_guard_extension_ddl;
CREATE EVENT TRIGGER pibase_guard_extension_ddl
ON ddl_command_start
WHEN TAG IN ('CREATE EXTENSION','ALTER EXTENSION','DROP EXTENSION')
EXECUTE FUNCTION pg_catalog.pibase_guard_extension_ddl();

CREATE OR REPLACE FUNCTION pg_catalog.pibase_enable_public_table_rls() RETURNS event_trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog
AS $$
DECLARE command record; role_name text;
BEGIN
  IF (SELECT rolsuper FROM pg_roles WHERE rolname=session_user) THEN
    RETURN;
  END IF;
  FOR command IN SELECT * FROM pg_event_trigger_ddl_commands() LOOP
    IF command.schema_name = 'public' AND command.object_type = 'table' THEN
      EXECUTE format('ALTER TABLE %s ENABLE ROW LEVEL SECURITY', command.object_identity);
    ELSIF command.schema_name = 'public' AND command.object_type = 'view' THEN
      EXECUTE format('ALTER VIEW %s SET (security_invoker = true)', command.object_identity);
    ELSIF command.schema_name = 'public' AND command.object_type = 'materialized view' THEN
      FOR role_name IN
        SELECT DISTINCT r.rolname
        FROM pg_class c
        CROSS JOIN LATERAL aclexplode(COALESCE(c.relacl, acldefault('r', c.relowner))) acl
        JOIN pg_roles r ON r.oid = acl.grantee
        WHERE c.oid = command.objid
          AND NOT r.rolsuper AND NOT r.rolbypassrls
          AND r.oid <> c.relowner
      LOOP
        EXECUTE format('REVOKE ALL PRIVILEGES ON %s FROM %I', command.object_identity, role_name);
      END LOOP;
    END IF;
  END LOOP;
END $$;
REVOKE ALL ON FUNCTION pg_catalog.pibase_enable_public_table_rls() FROM PUBLIC;
DROP EVENT TRIGGER IF EXISTS pibase_enable_public_table_rls;
CREATE EVENT TRIGGER pibase_enable_public_table_rls
ON ddl_command_end WHEN TAG IN ('CREATE TABLE','CREATE TABLE AS','SELECT INTO','CREATE VIEW','CREATE MATERIALIZED VIEW')
EXECUTE FUNCTION pg_catalog.pibase_enable_public_table_rls();
`

func managedProjectUpgradeSQL(anon, authenticated, service, admin string) string {
	q := func(role string) string { return pgx.Identifier{role}.Sanitize() }
	return `
CREATE SCHEMA IF NOT EXISTS storage;
ALTER SCHEMA storage OWNER TO CURRENT_USER;
REVOKE ALL ON SCHEMA storage FROM PUBLIC;
CREATE TABLE IF NOT EXISTS storage.buckets (
  id text PRIMARY KEY,
  name text NOT NULL UNIQUE,
  owner_id uuid,
  public boolean NOT NULL DEFAULT false,
  file_size_limit bigint,
  allowed_mime_types text[],
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS storage.objects (
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
CREATE INDEX IF NOT EXISTS storage_objects_bucket_idx ON storage.objects(bucket_id,name);
REVOKE ALL ON ALL TABLES IN SCHEMA storage FROM PUBLIC;
GRANT USAGE ON SCHEMA storage TO ` + q(anon) + `, ` + q(authenticated) + `, ` + q(service) + `, ` + q(admin) + `;
GRANT SELECT ON storage.buckets,storage.objects TO ` + q(anon) + `, ` + q(authenticated) + `;
GRANT INSERT,UPDATE,DELETE ON storage.objects TO ` + q(authenticated) + `;
GRANT SELECT,INSERT,UPDATE,DELETE ON storage.buckets,storage.objects TO ` + q(service) + `;
GRANT SELECT,INSERT,UPDATE,DELETE ON storage.buckets,storage.objects TO ` + q(admin) + `;
REVOKE TRIGGER,REFERENCES,TRUNCATE ON storage.buckets,storage.objects FROM ` + q(admin) + `;
ALTER TABLE storage.buckets ENABLE ROW LEVEL SECURITY;
ALTER TABLE storage.objects ENABLE ROW LEVEL SECURITY;
CREATE OR REPLACE FUNCTION auth.uid() RETURNS uuid
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, auth
AS $$ SELECT CASE WHEN current_setting('request.jwt.claims', true) ~ '"sub"\s*:\s*"[0-9a-fA-F-]{36}"' THEN
  (current_setting('request.jwt.claims', true)::jsonb ->> 'sub')::uuid ELSE NULL END $$;
REVOKE ALL ON FUNCTION auth.uid() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION auth.uid() TO ` + q(anon) + `, ` + q(authenticated) + `, ` + q(service) + `, ` + q(admin) + `;
DROP POLICY IF EXISTS storage_bucket_read ON storage.buckets;
CREATE POLICY storage_bucket_read ON storage.buckets FOR SELECT TO ` + q(anon) + `, ` + q(authenticated) + ` USING (public OR owner_id = auth.uid());
DROP POLICY IF EXISTS storage_bucket_service ON storage.buckets;
CREATE POLICY storage_bucket_service ON storage.buckets FOR ALL TO ` + q(service) + ` USING (true) WITH CHECK (true);
DROP POLICY IF EXISTS storage_object_read ON storage.objects;
CREATE POLICY storage_object_read ON storage.objects FOR SELECT TO ` + q(anon) + `, ` + q(authenticated) + ` USING (EXISTS (SELECT 1 FROM storage.buckets b WHERE b.id = bucket_id AND (b.public OR b.owner_id = auth.uid())) OR owner_id = auth.uid());
DROP POLICY IF EXISTS storage_object_owner ON storage.objects;
CREATE POLICY storage_object_owner ON storage.objects FOR ALL TO ` + q(authenticated) + ` USING (owner_id = auth.uid()) WITH CHECK (owner_id = auth.uid());
DROP POLICY IF EXISTS storage_object_service ON storage.objects;
CREATE POLICY storage_object_service ON storage.objects FOR ALL TO ` + q(service) + ` USING (true) WITH CHECK (true);

CREATE SCHEMA IF NOT EXISTS realtime;
ALTER SCHEMA realtime OWNER TO CURRENT_USER;
REVOKE ALL ON SCHEMA realtime FROM PUBLIC;
CREATE OR REPLACE FUNCTION realtime.notify_change() RETURNS trigger
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
CREATE OR REPLACE FUNCTION realtime.enable_table(target regclass) RETURNS void
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
GRANT USAGE ON SCHEMA realtime TO ` + q(admin) + `;
GRANT EXECUTE ON FUNCTION realtime.enable_table(regclass) TO ` + q(admin) + `;

CREATE SCHEMA IF NOT EXISTS cron;
ALTER SCHEMA cron OWNER TO CURRENT_USER;
REVOKE ALL ON SCHEMA cron FROM PUBLIC;
CREATE TABLE IF NOT EXISTS cron.jobs (
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
CREATE TABLE IF NOT EXISTS cron.webhook_outbox (
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
CREATE INDEX IF NOT EXISTS webhook_outbox_ready_idx ON cron.webhook_outbox(next_attempt_at) WHERE delivered_at IS NULL AND dead_lettered_at IS NULL;
CREATE INDEX IF NOT EXISTS webhook_outbox_pending_idx ON cron.webhook_outbox(id) WHERE delivered_at IS NULL AND dead_lettered_at IS NULL;
CREATE OR REPLACE FUNCTION cron.enforce_outbox_limit() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,cron
AS $$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtext('pibase:cron.webhook_outbox'));
  IF (SELECT count(*) FROM cron.webhook_outbox WHERE delivered_at IS NULL AND dead_lettered_at IS NULL) >= 256 THEN
    RAISE EXCEPTION 'cron webhook outbox pending limit (256) reached';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS cron_webhook_outbox_limit ON cron.webhook_outbox;
CREATE TRIGGER cron_webhook_outbox_limit BEFORE INSERT ON cron.webhook_outbox
FOR EACH ROW EXECUTE FUNCTION cron.enforce_outbox_limit();
CREATE OR REPLACE FUNCTION cron.enqueue_webhook() RETURNS trigger
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
GRANT USAGE ON SCHEMA cron TO ` + q(admin) + `;
GRANT SELECT,INSERT,UPDATE,DELETE ON cron.jobs,cron.webhook_outbox TO ` + q(admin) + `;
REVOKE TRIGGER,REFERENCES,TRUNCATE ON cron.jobs,cron.webhook_outbox FROM ` + q(admin) + `;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA cron TO ` + q(admin) + `;
GRANT EXECUTE ON FUNCTION cron.enqueue_webhook() TO ` + q(admin) + `;
` + mfaUpgradeSQL + `

ALTER SCHEMA auth OWNER TO CURRENT_USER;
ALTER SCHEMA storage OWNER TO CURRENT_USER;
ALTER SCHEMA realtime OWNER TO CURRENT_USER;
ALTER SCHEMA cron OWNER TO CURRENT_USER;
GRANT USAGE ON SCHEMA auth,storage TO ` + q(anon) + `, ` + q(authenticated) + `, ` + q(service) + `, ` + q(admin) + `;
GRANT USAGE ON SCHEMA realtime,cron TO ` + q(admin) + `;
ALTER DEFAULT PRIVILEGES FOR ROLE ` + q(admin) + ` IN SCHEMA public
GRANT SELECT,INSERT,UPDATE,DELETE ON TABLES TO ` + q(anon) + `, ` + q(authenticated) + `, ` + q(service) + `;
ALTER DEFAULT PRIVILEGES FOR ROLE ` + q(admin) + ` IN SCHEMA public
GRANT USAGE,SELECT ON SEQUENCES TO ` + q(anon) + `, ` + q(authenticated) + `, ` + q(service) + `;
`
}

// revokeStrayCreateGrants revokes CREATE on managed schemas from every
// non-operator grantee found in the schema ACL, not only from named roles.
func revokeStrayCreateGrants(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `
SELECT DISTINCT namespace.nspname, grantee_role.rolname
FROM pg_namespace namespace
CROSS JOIN LATERAL aclexplode(COALESCE(namespace.nspacl, acldefault('n', namespace.nspowner))) privilege
LEFT JOIN pg_roles grantee_role ON grantee_role.oid = privilege.grantee
WHERE namespace.nspname = ANY($1::text[])
  AND privilege.privilege_type = 'CREATE'
  AND privilege.grantee <> (SELECT oid FROM pg_roles WHERE rolname = current_user)`, managedSchemas)
	if err != nil {
		return fmt.Errorf("inspect managed schema CREATE grants: %w", err)
	}
	defer rows.Close()
	type grant struct{ schema, role string }
	var stray []grant
	for rows.Next() {
		var schema string
		var role *string
		if err := rows.Scan(&schema, &role); err != nil {
			return err
		}
		if role == nil {
			stray = append(stray, grant{schema, "PUBLIC"})
		} else {
			stray = append(stray, grant{schema, *role})
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, g := range stray {
		target := "PUBLIC"
		if g.role != "PUBLIC" {
			target = pgx.Identifier{g.role}.Sanitize()
		}
		if _, err := tx.Exec(ctx, "REVOKE CREATE ON SCHEMA "+pgx.Identifier{g.schema}.Sanitize()+" FROM "+target); err != nil {
			return fmt.Errorf("revoke CREATE on %s from %s: %w", g.schema, g.role, err)
		}
	}
	return nil
}

// customerManagedObjects returns objects not owned by the operator inside a
// managed schema. Before M6c the admin could grant CREATE to another project
// role as well as create objects directly. Moving any such object would
// silently claim customer state, so the migration fails closed and names it.
func customerManagedObjects(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
SELECT identified.type,identified.identity
FROM pg_shdepend dependency
CROSS JOIN LATERAL pg_identify_object(
  dependency.classid,dependency.objid,dependency.objsubid
) AS identified
WHERE dependency.refclassid='pg_authid'::regclass
  AND dependency.dbid=(SELECT oid FROM pg_database WHERE datname=current_database())
  AND dependency.refobjid<>(SELECT oid FROM pg_roles WHERE rolname=current_user)
  AND dependency.deptype='o'
  AND identified.schema=ANY($1::text[])
ORDER BY identified.schema,identified.type,identified.identity`, managedSchemas)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var objects []string
	for rows.Next() {
		var objectType, identity string
		if err := rows.Scan(&objectType, &identity); err != nil {
			return nil, err
		}
		objects = append(objects, fmt.Sprintf("%s %s", objectType, identity))
	}
	return objects, rows.Err()
}

func ensureNoCustomerManagedObjects(ctx context.Context, tx pgx.Tx) error {
	objects, err := customerManagedObjects(ctx, tx)
	if err != nil {
		return fmt.Errorf("inspect managed schemas for customer objects: %w", err)
	}
	if len(objects) != 0 {
		return fmt.Errorf("customer-owned objects in managed schemas: %s", strings.Join(objects, ", "))
	}
	return nil
}
