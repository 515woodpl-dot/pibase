package control

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/515woodpl-dot/pibase/internal/adminsql"
	"github.com/jackc/pgx/v5"
)

func TestManagedSchemaOwnershipBlocksAdminDDL(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "integration-auth-password"
	if err := Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	project, err := CreateProject(ctx, adminURL, "managed-ownership-boundary", bytes.Repeat([]byte{10}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupProject(t, adminURL, project)
	authURL := authenticatorTestURL(t, adminURL, password)
	attacks := []string{
		`DROP TABLE storage.objects`,
		`DROP TABLE storage.buckets`,
		`DROP TABLE auth.users`,
		`DROP TABLE cron.webhook_outbox`,
		`CREATE TABLE storage.x(id integer)`,
		`CREATE FUNCTION auth.x() RETURNS integer LANGUAGE sql AS 'SELECT 1'`,
		`ALTER TABLE auth.users ADD COLUMN attacked integer`,
		`DROP SCHEMA cron CASCADE`,
		`ALTER SCHEMA storage RENAME TO stolen_storage`,
		`DROP FUNCTION realtime.notify_change()`,
	}
	for i, attack := range attacks {
		t.Run(fmt.Sprintf("sql-%02d", i), func(t *testing.T) {
			if _, err := adminsql.Run(ctx, authURL, project.ID, attack); !isOwnershipError(err) {
				t.Fatalf("pibase sql accepted %q: %v", attack, err)
			}
		})
		t.Run(fmt.Sprintf("migration-%02d", i), func(t *testing.T) {
			err := adminsql.ApplyMigration(ctx, authURL, project.ID, fmt.Sprintf("m6c-attack-%02d", i), attack)
			if !isOwnershipError(err) {
				t.Fatalf("migration-apply accepted %q: %v", attack, err)
			}
		})
	}

	projectURL, _ := withDatabase(adminURL, project.Database)
	conn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var intact, adminCanCreate bool
	if err := conn.QueryRow(ctx, `SELECT
  to_regclass('storage.objects') IS NOT NULL
    AND to_regclass('storage.buckets') IS NOT NULL
    AND to_regclass('auth.users') IS NOT NULL
    AND to_regclass('cron.webhook_outbox') IS NOT NULL
    AND to_regprocedure('realtime.notify_change()') IS NOT NULL,
  bool_or(has_schema_privilege($1,schema_name,'CREATE'))
FROM unnest($2::text[]) AS schema_name`, "p_"+project.ID+"_admin", managedSchemas).Scan(&intact, &adminCanCreate); err != nil {
		t.Fatal(err)
	}
	if !intact || adminCanCreate {
		t.Fatalf("managed boundary intact=%v admin_create=%v", intact, adminCanCreate)
	}
	assertOnlyOperatorCanCreateManagedSchemas(t, conn, parsedURLUser(t, adminURL))
	var databaseOwner string
	if err := conn.QueryRow(ctx, `SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname=current_database()`).Scan(&databaseOwner); err != nil {
		t.Fatal(err)
	}
	if databaseOwner != "p_"+project.ID+"_admin" {
		t.Fatalf("database owner=%q", databaseOwner)
	}
	operator := parsedURLUser(t, adminURL)
	rows, err := conn.Query(ctx, `SELECT n.nspname,p.oid::regprocedure::text,r.rolname,
  array_to_string(p.proconfig,','),
  EXISTS (
    SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) privilege
    WHERE privilege.grantee=0
  )
FROM pg_proc p
JOIN pg_namespace n ON n.oid=p.pronamespace
JOIN pg_roles r ON r.oid=p.proowner
WHERE n.nspname=ANY($1::text[]) AND p.prosecdef
ORDER BY n.nspname,p.oid::regprocedure::text`, managedSchemas)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	wantSearchPath := map[string]string{
		"auth.uid()":                      "search_path=pg_catalog, auth",
		"cron.enforce_outbox_limit()":     "search_path=pg_catalog, cron",
		"cron.enqueue_webhook()":          "search_path=pg_catalog, cron",
		"realtime.enable_table(regclass)": "search_path=pg_catalog",
		"realtime.notify_change()":        "search_path=pg_catalog",
	}
	seen := 0
	for rows.Next() {
		var schema, identity, owner, searchPath string
		var publicExecute bool
		if err := rows.Scan(&schema, &identity, &owner, &searchPath, &publicExecute); err != nil {
			t.Fatal(err)
		}
		if owner != operator || publicExecute || searchPath != wantSearchPath[identity] {
			t.Fatalf("security definer %s schema=%s owner=%s search_path=%q public_execute=%v", identity, schema, owner, searchPath, publicExecute)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != len(wantSearchPath) {
		t.Fatalf("security definer count=%d want %d", seen, len(wantSearchPath))
	}
}

func TestManagedSchemaUpgradeSkipsCustomerObjectsAndRollback(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "integration-auth-password"
	if err := Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	healthy, err := CreateProject(ctx, adminURL, "managed-upgrade-healthy", bytes.Repeat([]byte{11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	broken, err := CreateProject(ctx, adminURL, "managed-customer-object", bytes.Repeat([]byte{11}, 32))
	if err != nil {
		cleanupProject(t, adminURL, healthy)
		t.Fatal(err)
	}
	defer cleanupProject(t, adminURL, broken)
	defer cleanupProject(t, adminURL, healthy)

	brokenInfo, err := branchInfo(ctx, adminURL, broken.ID)
	if err != nil {
		t.Fatal(err)
	}
	healthyInfo, err := branchInfo(ctx, adminURL, healthy.ID)
	if err != nil {
		t.Fatal(err)
	}
	rollbackProjectToV7(t, adminURL, brokenInfo, broken.ID)
	rollbackProjectToV7(t, adminURL, healthyInfo, healthy.ID)
	healthyURL, _ := withDatabase(adminURL, healthy.Database)
	healthyConn, err := pgx.Connect(ctx, healthyURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = healthyConn.Exec(ctx, `SET ROLE `+pgx.Identifier{healthyInfo.admin}.Sanitize()+`;
GRANT CREATE ON SCHEMA auth TO PUBLIC;
GRANT CREATE ON SCHEMA storage TO `+pgx.Identifier{healthyInfo.anon}.Sanitize()+`;
GRANT CREATE ON SCHEMA realtime TO `+pgx.Identifier{healthyInfo.authenticated}.Sanitize()+`;
GRANT CREATE ON SCHEMA cron TO `+pgx.Identifier{healthyInfo.service}.Sanitize()+`;`); err != nil {
		healthyConn.Close(ctx)
		t.Fatal(err)
	}
	healthyConn.Close(ctx)
	grantsBefore := managedObjectGrantSnapshot(t, adminURL, healthy.Database)
	brokenURL, _ := withDatabase(adminURL, broken.Database)
	brokenConn, err := pgx.Connect(ctx, brokenURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = brokenConn.Exec(ctx, `SET ROLE `+pgx.Identifier{brokenInfo.admin}.Sanitize()+`;
CREATE TABLE storage.customer_keep(id integer);
GRANT CREATE ON SCHEMA auth TO `+pgx.Identifier{brokenInfo.authenticated}.Sanitize()+`;
SET ROLE `+pgx.Identifier{brokenInfo.authenticated}.Sanitize()+`;
CREATE FUNCTION auth.customer_role_keep() RETURNS integer LANGUAGE sql AS 'SELECT 1'`); err != nil {
		brokenConn.Close(ctx)
		t.Fatal(err)
	}
	brokenConn.Close(ctx)

	if err := Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatalf("bootstrap aborted on customer object: %v", err)
	}
	controlURL, _ := withDatabase(adminURL, "_control")
	controlConn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		t.Fatal(err)
	}
	defer controlConn.Close(ctx)
	var brokenVersion, healthyVersion int
	var schemaError string
	if err := controlConn.QueryRow(ctx, `SELECT schema_version,COALESCE(schema_error,'') FROM control.projects WHERE id=$1`, broken.ID).Scan(&brokenVersion, &schemaError); err != nil {
		t.Fatal(err)
	}
	if err := controlConn.QueryRow(ctx, `SELECT schema_version FROM control.projects WHERE id=$1`, healthy.ID).Scan(&healthyVersion); err != nil {
		t.Fatal(err)
	}
	if brokenVersion != 7 || healthyVersion != 8 || !strings.Contains(schemaError, "storage.customer_keep") || !strings.Contains(schemaError, "auth.customer_role_keep()") {
		t.Fatalf("versions broken=%d healthy=%d error=%q", brokenVersion, healthyVersion, schemaError)
	}
	brokenConn, err = pgx.Connect(ctx, brokenURL)
	if err != nil {
		t.Fatal(err)
	}
	defer brokenConn.Close(ctx)
	var customerTableExists, customerFunctionExists bool
	if err := brokenConn.QueryRow(ctx, `SELECT to_regclass('storage.customer_keep') IS NOT NULL,to_regprocedure('auth.customer_role_keep()') IS NOT NULL`).Scan(&customerTableExists, &customerFunctionExists); err != nil || !customerTableExists || !customerFunctionExists {
		t.Fatalf("customer objects table=%v function=%v err=%v", customerTableExists, customerFunctionExists, err)
	}

	assertManagedSchemaOwners(t, adminURL, healthy.Database, parsedURLUser(t, adminURL))
	healthyConn, err = pgx.Connect(ctx, healthyURL)
	if err != nil {
		t.Fatal(err)
	}
	assertOnlyOperatorCanCreateManagedSchemas(t, healthyConn, parsedURLUser(t, adminURL))
	healthyConn.Close(ctx)
	grantsAfter := managedObjectGrantSnapshot(t, adminURL, healthy.Database)
	if grantsAfter != grantsBefore {
		t.Fatalf("managed grants changed during v7 upgrade\nbefore:\n%s\nafter:\n%s", grantsBefore, grantsAfter)
	}
	rollbackProjectToV7(t, adminURL, healthyInfo, healthy.ID)
	assertManagedSchemaOwners(t, adminURL, healthy.Database, healthyInfo.admin)
	if err := UpgradeProjectSchema(ctx, adminURL, healthy.ID); err != nil {
		t.Fatal(err)
	}
	assertManagedSchemaOwners(t, adminURL, healthy.Database, parsedURLUser(t, adminURL))
}

func isOwnershipError(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "must be owner"))
}

func rollbackProjectToV7(t *testing.T, adminURL string, info branchProjectInfo, id string) {
	t.Helper()
	ctx := context.Background()
	projectURL, _ := withDatabase(adminURL, info.database)
	conn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range managedSchemas {
		if _, err = conn.Exec(ctx, `ALTER SCHEMA `+pgx.Identifier{schema}.Sanitize()+` OWNER TO `+pgx.Identifier{info.admin}.Sanitize()); err != nil {
			conn.Close(ctx)
			t.Fatal(err)
		}
	}
	conn.Close(ctx)
	controlURL, _ := withDatabase(adminURL, "_control")
	controlConn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		t.Fatal(err)
	}
	defer controlConn.Close(ctx)
	if _, err = controlConn.Exec(ctx, `UPDATE control.projects SET schema_version=7,schema_error=NULL WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
}

func assertManagedSchemaOwners(t *testing.T, adminURL, database, want string) {
	t.Helper()
	ctx := context.Background()
	projectURL, _ := withDatabase(adminURL, database)
	conn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT n.nspname,r.rolname FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname=ANY($1::text[]) ORDER BY n.nspname`, managedSchemas)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var schema, owner string
		if err := rows.Scan(&schema, &owner); err != nil {
			t.Fatal(err)
		}
		if owner != want {
			t.Fatalf("schema %s owner=%q want %q", schema, owner, want)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count != len(managedSchemas) {
		t.Fatalf("managed schema count=%d want %d", count, len(managedSchemas))
	}
}

func assertOnlyOperatorCanCreateManagedSchemas(t *testing.T, conn *pgx.Conn, operator string) {
	t.Helper()
	var unexpected int
	err := conn.QueryRow(context.Background(), `
SELECT count(*)
FROM pg_namespace namespace
CROSS JOIN LATERAL aclexplode(COALESCE(namespace.nspacl,acldefault('n',namespace.nspowner))) privilege
WHERE namespace.nspname=ANY($1::text[])
  AND privilege.privilege_type='CREATE'
  AND privilege.grantee<>(SELECT oid FROM pg_roles WHERE rolname=$2)`, managedSchemas, operator).Scan(&unexpected)
	if err != nil {
		t.Fatal(err)
	}
	if unexpected != 0 {
		t.Fatalf("non-operator managed-schema CREATE grants=%d", unexpected)
	}
}

func managedObjectGrantSnapshot(t *testing.T, adminURL, database string) string {
	t.Helper()
	ctx := context.Background()
	projectURL, _ := withDatabase(adminURL, database)
	conn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var snapshot string
	err = conn.QueryRow(ctx, `
SELECT string_agg(object_type||' '||identity||'='||acl,E'\n' ORDER BY object_type,identity)
FROM (
  SELECT 'relation' AS object_type,n.nspname||'.'||c.relname AS identity,COALESCE(c.relacl::text,'') AS acl
  FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname=ANY($1::text[]) AND c.relkind IN ('r','p','v','m','S','f')
  UNION ALL
  SELECT 'function',p.oid::regprocedure::text,COALESCE(p.proacl::text,'')
  FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
  WHERE n.nspname=ANY($1::text[])
) inventory`, managedSchemas).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
