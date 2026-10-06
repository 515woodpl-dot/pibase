package control

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/515woodpl-dot/pibase/internal/adminsql"
	"github.com/jackc/pgx/v5"
)

func TestCreateProjectIsolation(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	if err := Bootstrap(ctx, adminURL, "integration-auth-password"); err != nil {
		t.Fatal(err)
	}
	a, err := CreateProject(ctx, adminURL, "test-a", bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	b, err := CreateProject(ctx, adminURL, "test-b", bytes.Repeat([]byte{7}, 32))
	if err != nil {
		cleanupProject(t, adminURL, a)
		t.Fatal(err)
	}
	defer cleanupProject(t, adminURL, a)
	defer cleanupProject(t, adminURL, b)
	if a.AnonKey == "" || a.ServiceKey == "" || a.AnonKey == a.ServiceKey {
		t.Fatal("keys missing or equal")
	}

	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var cross, own, superuser, inherit bool
	if err := conn.QueryRow(ctx, `SELECT has_database_privilege($1,$2,'CONNECT'), has_database_privilege($1,$3,'CONNECT')`, "p_"+a.ID+"_anon", b.Database, a.Database).Scan(&cross, &own); err != nil {
		t.Fatal(err)
	}
	if cross || !own {
		t.Fatalf("database isolation: cross=%v own=%v", cross, own)
	}
	if err := conn.QueryRow(ctx, `SELECT rolsuper, rolinherit FROM pg_roles WHERE rolname='authenticator'`).Scan(&superuser, &inherit); err != nil {
		t.Fatal(err)
	}
	if superuser || inherit {
		t.Fatalf("authenticator attributes: superuser=%v inherit=%v", superuser, inherit)
	}
	projectURL, _ := withDatabase(adminURL, a.Database)
	projectConn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	defer projectConn.Close(ctx)
	var authTable bool
	if err := projectConn.QueryRow(ctx, `SELECT has_table_privilege($1,'auth.users','SELECT')`, "p_"+a.ID+"_anon").Scan(&authTable); err != nil {
		t.Fatal(err)
	}
	if authTable {
		t.Fatal("anon role has auth table access")
	}
	controlURL, _ := withDatabase(adminURL, "_control")
	controlConn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		t.Fatal(err)
	}
	defer controlConn.Close(ctx)
	var encrypted []byte
	if err := controlConn.QueryRow(ctx, `SELECT jwt_secret_encrypted FROM control.projects WHERE id=$1`, a.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if len(encrypted) <= 32 {
		t.Fatalf("JWT secret was not stored as AES-GCM ciphertext: %d bytes", len(encrypted))
	}
}

func TestBootstrapUpgradesExistingProjectSchema(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "integration-auth-password"
	if err := Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := CreateProject(ctx, adminURL, "upgrade-mfa", bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupProject(t, adminURL, p)
	projectURL, _ := withDatabase(adminURL, p.Database)
	conn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `
DROP SCHEMA storage CASCADE;
DROP SCHEMA realtime CASCADE;
DROP SCHEMA cron CASCADE;
DROP TABLE auth.mfa_challenges,auth.mfa_factors;
ALTER TABLE auth.refresh_tokens DROP COLUMN aal`); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	conn.Close(ctx)

	if err := Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	conn, err = pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var factorOwner, storageOwner, cronOwner string
	var hasAAL, hasStorage, hasRealtime, hasCron, storageRLS bool
	var schemaVersion int
	if err := conn.QueryRow(ctx, `SELECT
  (SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='auth.mfa_factors'::regclass),
	(SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='storage.objects'::regclass),
	(SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='cron.jobs'::regclass),
  EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='auth' AND table_name='refresh_tokens' AND column_name='aal'),
	to_regclass('storage.objects') IS NOT NULL,
	to_regprocedure('realtime.enable_table(regclass)') IS NOT NULL,
	to_regclass('cron.webhook_outbox') IS NOT NULL,
	(SELECT relrowsecurity FROM pg_class WHERE oid='storage.objects'::regclass)`).Scan(&factorOwner, &storageOwner, &cronOwner, &hasAAL, &hasStorage, &hasRealtime, &hasCron, &storageRLS); err != nil {
		t.Fatal(err)
	}
	wantOwner := parsedURLUser(t, adminURL)
	if factorOwner != wantOwner || storageOwner != wantOwner || cronOwner != wantOwner || !hasAAL || !hasStorage || !hasRealtime || !hasCron || !storageRLS {
		t.Fatalf("upgrade owners factors=%q storage=%q cron=%q aal=%v storage=%v realtime=%v cron=%v rls=%v", factorOwner, storageOwner, cronOwner, hasAAL, hasStorage, hasRealtime, hasCron, storageRLS)
	}
	controlURL, _ := withDatabase(adminURL, "_control")
	controlConn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		t.Fatal(err)
	}
	defer controlConn.Close(ctx)
	if err := controlConn.QueryRow(ctx, `SELECT schema_version FROM control.projects WHERE id=$1`, p.ID).Scan(&schemaVersion); err != nil {
		t.Fatal(err)
	}
	if schemaVersion != currentProjectSchemaVersion {
		t.Fatalf("schema version = %d, want %d", schemaVersion, currentProjectSchemaVersion)
	}
}

func TestBootstrapSkipsSchemaLessMeasurementProject(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "integration-auth-password"
	if err := Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := CreateProject(ctx, adminURL, "measurement-upgrade-skip", bytes.Repeat([]byte{6}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupProject(t, adminURL, p)
	projectURL, _ := withDatabase(adminURL, p.Database)
	conn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `DROP SCHEMA auth,storage,realtime,cron CASCADE`); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	conn.Close(ctx)
	if err := Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	controlURL, _ := withDatabase(adminURL, "_control")
	controlConn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		t.Fatal(err)
	}
	defer controlConn.Close(ctx)
	var version int
	if err := controlConn.QueryRow(ctx, `SELECT schema_version FROM control.projects WHERE id=$1`, p.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("measurement schema version = %d, want 0", version)
	}
}

func TestBootstrapContinuesPastBrokenProjectSchema(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "integration-auth-password"
	if err := Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	first, err := CreateProject(ctx, adminURL, "bootstrap-resilience-a", bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	second, err := CreateProject(ctx, adminURL, "bootstrap-resilience-b", bytes.Repeat([]byte{3}, 32))
	if err != nil {
		cleanupProject(t, adminURL, first)
		t.Fatal(err)
	}
	defer cleanupProject(t, adminURL, first)
	defer cleanupProject(t, adminURL, second)
	broken, healthy := first, second
	if broken.ID > healthy.ID {
		broken, healthy = healthy, broken
	}
	brokenInfo, err := branchInfo(ctx, adminURL, broken.ID)
	if err != nil {
		t.Fatal(err)
	}
	brokenURL, _ := withDatabase(adminURL, broken.Database)
	brokenConn, err := pgx.Connect(ctx, brokenURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = brokenConn.Exec(ctx, `ALTER SCHEMA storage OWNER TO `+pgx.Identifier{brokenInfo.admin}.Sanitize()+`;
SET ROLE `+pgx.Identifier{brokenInfo.admin}.Sanitize()+`;
DROP TABLE storage.objects;
DROP TABLE storage.buckets;
CREATE TABLE storage.buckets(id text primary key);`)
	brokenConn.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	healthyURL, _ := withDatabase(adminURL, healthy.Database)
	healthyConn, err := pgx.Connect(ctx, healthyURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = healthyConn.Exec(ctx, `DROP TABLE auth.mfa_challenges,auth.mfa_factors`); err != nil {
		healthyConn.Close(ctx)
		t.Fatal(err)
	}
	healthyConn.Close(ctx)

	if err := Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatalf("bootstrap aborted on one broken project: %v", err)
	}
	healthyConn, err = pgx.Connect(ctx, healthyURL)
	if err != nil {
		t.Fatal(err)
	}
	defer healthyConn.Close(ctx)
	var upgraded bool
	if err := healthyConn.QueryRow(ctx, `SELECT to_regclass('auth.mfa_factors') IS NOT NULL`).Scan(&upgraded); err != nil || !upgraded {
		t.Fatalf("healthy later project was not upgraded: upgraded=%v err=%v", upgraded, err)
	}
	controlURL, _ := withDatabase(adminURL, "_control")
	controlConn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		t.Fatal(err)
	}
	defer controlConn.Close(ctx)
	var schemaError string
	if err := controlConn.QueryRow(ctx, `SELECT COALESCE(schema_error,'') FROM control.projects WHERE id=$1`, broken.ID).Scan(&schemaError); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(schemaError, "storage.buckets") {
		t.Fatalf("broken project schema error = %q", schemaError)
	}
	projects, err := ListProjects(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	var listedError *string
	for _, project := range projects {
		if project.ID == broken.ID {
			listedError = project.SchemaError
			break
		}
	}
	if listedError == nil || *listedError != schemaError {
		t.Fatalf("project-list schema_error = %v, want %q", listedError, schemaError)
	}
	brokenConn, err = pgx.Connect(ctx, brokenURL)
	if err != nil {
		t.Fatal(err)
	}
	defer brokenConn.Close(ctx)
	var guarded bool
	if err := brokenConn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_event_trigger WHERE evtname='pibase_guard_extension_ddl')`).Scan(&guarded); err != nil || !guarded {
		t.Fatalf("broken project extension guard installed=%v err=%v", guarded, err)
	}
}

func TestExtensionGuardRejectsDynamicCustomerDDL(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "integration-auth-password"
	if err := Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := CreateProject(ctx, adminURL, "extension-guard", bytes.Repeat([]byte{8}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupProject(t, adminURL, p)
	authURL := authenticatorTestURL(t, adminURL, password)
	dynamicSQL := `DO $$ BEGIN EXECUTE 'CREATE EXTENSION hstore'; END $$`
	if _, err := adminsql.Run(ctx, authURL, p.ID, dynamicSQL); err == nil {
		t.Fatal("SQL tab accepted dynamic extension DDL")
	}
	// Migrations skip the SQL-tab filter, so this reaches the database guard.
	if err := adminsql.ApplyMigration(ctx, authURL, p.ID, "extension-dynamic", dynamicSQL); err == nil || !strings.Contains(err.Error(), "extensions can only be managed by the operator") {
		t.Fatalf("dynamic migration extension DDL error = %v", err)
	}
	escapeSQL := `SELECT E'\''; CREATE EXTENSION citext; --'`
	if err := adminsql.ApplyMigration(ctx, authURL, p.ID, "extension-escape", escapeSQL); err == nil || !strings.Contains(err.Error(), "extensions can only be managed by the operator") {
		t.Fatalf("escape-string migration extension DDL error = %v", err)
	}
	projectURL, _ := withDatabase(adminURL, p.Database)
	conn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var installed bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname IN ('hstore','citext'))`).Scan(&installed); err != nil {
		t.Fatal(err)
	}
	if installed {
		t.Fatal("customer installed an extension despite the database guard")
	}
}

func TestOperatorOptionalExtensions(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	if err := Bootstrap(ctx, adminURL, "integration-auth-password"); err != nil {
		t.Fatal(err)
	}
	p, err := CreateProject(ctx, adminURL, "optional-extensions", bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupProject(t, adminURL, p)
	for i := 0; i < 2; i++ {
		if err := EnablePostGIS(ctx, adminURL, p.ID); err != nil {
			t.Fatal(err)
		}
		if err := EnablePGVector(ctx, adminURL, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	projectURL, _ := withDatabase(adminURL, p.Database)
	conn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var longitude float64
	var dimensions int
	if err := conn.QueryRow(ctx, `SELECT ST_X(ST_SetSRID(ST_MakePoint(-73.5,40.7),4326)), vector_dims('[1,2,3]'::vector)`).Scan(&longitude, &dimensions); err != nil {
		t.Fatal(err)
	}
	if longitude != -73.5 || dimensions != 3 {
		t.Fatalf("extension results longitude=%v dimensions=%d", longitude, dimensions)
	}
	if err := EnablePostGIS(ctx, adminURL, "missing-project"); err == nil {
		t.Fatal("enabled PostGIS for an unknown project")
	}
}

func parsedURLUser(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.User.Username()
}

func authenticatorTestURL(t *testing.T, raw, password string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/_control"
	parsed.User = url.UserPassword("authenticator", password)
	return parsed.String()
}

func cleanupProject(t *testing.T, adminURL string, p Project) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close(ctx)
	_, _ = conn.Exec(ctx, `DROP DATABASE IF EXISTS `+pgx.Identifier{p.Database}.Sanitize()+` WITH (FORCE)`)
	for _, suffix := range []string{"_anon", "_authenticated", "_service_role", "_admin"} {
		_, _ = conn.Exec(ctx, `DROP ROLE IF EXISTS `+pgx.Identifier{"p_" + p.ID + suffix}.Sanitize())
	}
	controlURL, _ := withDatabase(adminURL, "_control")
	c, err := pgx.Connect(ctx, controlURL)
	if err == nil {
		_, _ = c.Exec(ctx, `DELETE FROM control.projects WHERE id=$1`, p.ID)
		c.Close(ctx)
	}
}
