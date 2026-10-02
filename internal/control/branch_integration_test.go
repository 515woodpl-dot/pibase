package control

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestBranchProjectRemapsCatalogSecurity(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump/psql are required for the branch integration test")
	}
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("pg_dump/psql are required for the branch integration test")
	}
	ctx := context.Background()
	if err := Bootstrap(ctx, adminURL, "integration-auth-password"); err != nil {
		t.Fatal(err)
	}
	source, err := CreateProject(ctx, adminURL, "branch-source", bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupProject(t, adminURL, source)
	sourceInfo, err := branchInfo(ctx, adminURL, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	sourceURL, err := withDatabase(adminURL, sourceInfo.database)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	setup := `SET ROLE ` + pgx.Identifier{sourceInfo.admin}.Sanitize() + `;
CREATE TABLE public.branch_rows (id integer PRIMARY KEY, owner text NOT NULL, secret text NOT NULL);
ALTER TABLE public.branch_rows ENABLE ROW LEVEL SECURITY;
GRANT SELECT ON public.branch_rows TO ` + pgx.Identifier{sourceInfo.authenticated}.Sanitize() + ` WITH GRANT OPTION;
CREATE POLICY row_read ON public.branch_rows FOR SELECT TO ` + pgx.Identifier{sourceInfo.authenticated}.Sanitize() + ` USING (true);
INSERT INTO public.branch_rows VALUES (1, 'source', 'not-auth-storage');`
	if _, err := conn.Exec(ctx, setup); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	conn.Close(ctx)

	branch, err := BranchProject(ctx, adminURL, source.ID, "branch-copy", bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupProject(t, adminURL, branch)
	targetInfo, err := branchInfo(ctx, adminURL, branch.ID)
	if err != nil {
		t.Fatal(err)
	}
	targetURL, err := withDatabase(adminURL, targetInfo.database)
	if err != nil {
		t.Fatal(err)
	}
	targetConn, err := pgx.Connect(ctx, targetURL)
	if err != nil {
		t.Fatal(err)
	}
	defer targetConn.Close(ctx)
	var targetSelect, sourceSelect, targetPolicy, sourcePolicy bool
	if err := targetConn.QueryRow(ctx, `SELECT has_table_privilege($1,'public.branch_rows','SELECT'), has_table_privilege($2,'public.branch_rows','SELECT'), EXISTS (SELECT 1 FROM pg_policy p JOIN pg_class c ON c.oid=p.polrelid JOIN pg_roles r ON r.oid=ANY(p.polroles) WHERE c.relname='branch_rows' AND p.polname='row_read' AND r.rolname=$1), EXISTS (SELECT 1 FROM pg_policy p JOIN pg_class c ON c.oid=p.polrelid JOIN pg_roles r ON r.oid=ANY(p.polroles) WHERE c.relname='branch_rows' AND p.polname='row_read' AND r.rolname=$2)`, targetInfo.authenticated, sourceInfo.authenticated).Scan(&targetSelect, &sourceSelect, &targetPolicy, &sourcePolicy); err != nil {
		t.Fatal(err)
	}
	if !targetSelect || sourceSelect || !targetPolicy || sourcePolicy {
		t.Fatalf("security was not remapped: target grant=%v source grant=%v target policy=%v source policy=%v", targetSelect, sourceSelect, targetPolicy, sourcePolicy)
	}
	var rows, authRows, storageRows int
	if err := targetConn.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.branch_rows), (SELECT count(*) FROM auth.users), (SELECT count(*) FROM storage.objects)`).Scan(&rows, &authRows, &storageRows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || authRows != 0 || storageRows != 0 {
		t.Fatalf("branch contents unexpected: public=%d auth=%d storage=%d", rows, authRows, storageRows)
	}
}
