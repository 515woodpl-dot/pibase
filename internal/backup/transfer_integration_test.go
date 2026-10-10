package backup

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
)

// An import's tables are restored without server rights: a dump holding
// something only a superuser may create (here a function in the "internal"
// language, which could call any server function) is refused, and nothing
// from it remains.
func TestImportRestoresTablesWithoutServerRights(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	if err := control.Bootstrap(ctx, adminURL, "integration-auth-password"); err != nil {
		t.Fatal(err)
	}
	root, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close(ctx)
	_, _ = root.Exec(ctx, `DROP DATABASE IF EXISTS transfer_evil WITH (FORCE)`)
	if _, err := root.Exec(ctx, `CREATE DATABASE transfer_evil`); err != nil {
		t.Fatal(err)
	}
	defer root.Exec(context.Background(), `DROP DATABASE IF EXISTS transfer_evil WITH (FORCE)`)
	evil, err := pgx.Connect(ctx, withDatabase(adminURL, "transfer_evil"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evil.Exec(ctx, `CREATE TABLE public.harmless (id int);
CREATE FUNCTION public.sneaky(text) RETURNS int AS 'textlen' LANGUAGE internal IMMUTABLE STRICT;`); err != nil {
		t.Fatal(err)
	}
	evil.Close(ctx)
	dump := filepath.Join(t.TempDir(), "public.dump")
	if out, err := exec.Command("pg_dump", "--format=custom", "--schema=public", "--no-owner", "--file", dump, withDatabase(adminURL, "transfer_evil")).CombinedOutput(); err != nil {
		t.Fatalf("pg_dump: %v %s", err, out)
	}
	project, err := control.CreateProject(ctx, adminURL, "restore rights", bytes.Repeat([]byte{51}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer control.DeleteProject(context.Background(), adminURL, project.ID)
	err = restorePublic(ctx, adminURL, project.ID, project.Database, dump)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("superuser-only object restored: %v", err)
	}
	db, err := pgx.Connect(ctx, withDatabase(adminURL, project.Database))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	var left int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_proc WHERE proname='sneaky'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("function left behind: %d %v", left, err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relname='harmless'`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("partial restore left behind: %d %v", left, err)
	}
	if err := root.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname=$1`, "p_"+project.ID+"_import").Scan(&left); err != nil || left != 0 {
		t.Fatalf("temporary login left behind: %d %v", left, err)
	}
}
