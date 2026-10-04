package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
)

func TestRestorePreM6cBackupReappliesManagedOwnership(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	if err := control.Bootstrap(ctx, adminURL, "integration-auth-password"); err != nil {
		t.Fatal(err)
	}
	project, err := control.CreateProject(ctx, adminURL, "pre-m6c-restore", bytes.Repeat([]byte{12}, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := control.DeleteProject(context.Background(), adminURL, project.ID); err != nil {
			t.Errorf("cleanup project: %v", err)
		}
	}()
	if err := control.EnablePostGIS(ctx, adminURL, project.ID); err != nil {
		t.Fatal(err)
	}
	if err := control.EnablePGVector(ctx, adminURL, project.ID); err != nil {
		t.Fatal(err)
	}

	projectURL := withDatabase(adminURL, project.Database)
	conn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	adminRole := "p_" + project.ID + "_admin"
	if _, err = conn.Exec(ctx, `
CREATE TABLE public.restore_probe(id integer PRIMARY KEY, location geometry(Point,4326), embedding vector(3));
INSERT INTO public.restore_probe VALUES (1,ST_SetSRID(ST_MakePoint(-73.5,40.7),4326),'[1,2,3]');
ALTER SCHEMA auth OWNER TO `+pgx.Identifier{adminRole}.Sanitize()+`;
ALTER SCHEMA storage OWNER TO `+pgx.Identifier{adminRole}.Sanitize()+`;
ALTER SCHEMA realtime OWNER TO `+pgx.Identifier{adminRole}.Sanitize()+`;
ALTER SCHEMA cron OWNER TO `+pgx.Identifier{adminRole}.Sanitize()+`;`); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	conn.Close(ctx)
	controlConn, err := pgx.Connect(ctx, withDatabase(adminURL, "_control"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = controlConn.Exec(ctx, `UPDATE control.projects SET schema_version=7 WHERE id=$1`, project.ID); err != nil {
		controlConn.Close(ctx)
		t.Fatal(err)
	}
	controlConn.Close(ctx)

	root := t.TempDir()
	storageRoot := filepath.Join(root, "storage")
	projectStorage := filepath.Join(storageRoot, project.ID)
	if err := os.MkdirAll(projectStorage, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectStorage, "object.bin"), []byte("before-m6c"), 0o600); err != nil {
		t.Fatal(err)
	}
	backupDir := filepath.Join(root, "backups")
	var key [32]byte
	copy(key[:], bytes.Repeat([]byte{13}, 32))
	if err := Backup(ctx, adminURL, project.ID, storageRoot, backupDir, key); err != nil {
		t.Fatal(err)
	}
	archives, err := filepath.Glob(filepath.Join(backupDir, "*.pib"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("backup archives=%v err=%v", archives, err)
	}

	if err := control.UpgradeProjectSchema(ctx, adminURL, project.ID); err != nil {
		t.Fatal(err)
	}
	conn, err = pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `DELETE FROM public.restore_probe`); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	conn.Close(ctx)
	if err := os.WriteFile(filepath.Join(projectStorage, "object.bin"), []byte("after-m6c"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Restore(ctx, adminURL, project.ID, storageRoot, archives[0], key); err != nil {
		t.Fatal(err)
	}
	conn, err = pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var rows, dimensions, version int
	var longitude float64
	var owners, guard bool
	if err := conn.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM public.restore_probe),
  (SELECT ST_X(location) FROM public.restore_probe WHERE id=1),
  (SELECT vector_dims(embedding) FROM public.restore_probe WHERE id=1),
  (SELECT bool_and(pg_get_userbyid(nspowner)=current_user) FROM pg_namespace WHERE nspname=ANY($1::text[])),
  EXISTS(SELECT 1 FROM pg_event_trigger WHERE evtname='pibase_guard_extension_ddl')
`, []string{"auth", "storage", "realtime", "cron"}).Scan(&rows, &longitude, &dimensions, &owners, &guard); err != nil {
		t.Fatal(err)
	}
	controlConn, err = pgx.Connect(ctx, withDatabase(adminURL, "_control"))
	if err != nil {
		t.Fatal(err)
	}
	defer controlConn.Close(ctx)
	if err := controlConn.QueryRow(ctx, `SELECT schema_version FROM control.projects WHERE id=$1`, project.ID).Scan(&version); err != nil {
		t.Fatal(err)
	}
	storageBytes, err := os.ReadFile(filepath.Join(projectStorage, "object.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 || longitude != -73.5 || dimensions != 3 || !owners || !guard || version != 8 || string(storageBytes) != "before-m6c" {
		t.Fatalf("restore rows=%d longitude=%v dimensions=%d owners=%v guard=%v version=%d storage=%q", rows, longitude, dimensions, owners, guard, version, storageBytes)
	}
}
