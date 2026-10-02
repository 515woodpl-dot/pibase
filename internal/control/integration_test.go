package control

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"testing"

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

func TestBootstrapUpgradesExistingProjectMFA(t *testing.T) {
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
	if _, err = conn.Exec(ctx, `DROP TABLE auth.mfa_challenges,auth.mfa_factors; ALTER TABLE auth.refresh_tokens DROP COLUMN aal`); err != nil {
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
	var factorOwner, challengeOwner string
	var hasAAL bool
	if err := conn.QueryRow(ctx, `SELECT
  (SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='auth.mfa_factors'::regclass),
  (SELECT pg_get_userbyid(relowner) FROM pg_class WHERE oid='auth.mfa_challenges'::regclass),
  EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema='auth' AND table_name='refresh_tokens' AND column_name='aal')`).Scan(&factorOwner, &challengeOwner, &hasAAL); err != nil {
		t.Fatal(err)
	}
	wantOwner := parsedURLUser(t, adminURL)
	if factorOwner != wantOwner || challengeOwner != wantOwner || !hasAAL {
		t.Fatalf("upgrade ownership factors=%q challenges=%q aal=%v", factorOwner, challengeOwner, hasAAL)
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
