package gateway

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
)

func TestSchedulerSkipsSchemaLessMeasurementProject(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	t.Setenv("PIBASE_SCHEDULER_DISABLED", "false")
	ctx := context.Background()
	const password = "integration-auth-password"
	masterKey := bytes.Repeat([]byte{9}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, adminURL, "scheduler-measurement-skip", masterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, p)
	projectURL := databaseURL(t, adminURL, p.Database, parsedUsername(t, adminURL), urlPassword(t, adminURL))
	conn, err := pgx.Connect(ctx, projectURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `DROP SCHEMA IF EXISTS auth CASCADE; DROP SCHEMA IF EXISTS storage CASCADE; DROP SCHEMA IF EXISTS realtime CASCADE; DROP SCHEMA IF EXISTS cron CASCADE`); err != nil {
		conn.Close(ctx)
		t.Fatal(err)
	}
	conn.Close(ctx)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}

	server, err := New(ctx, databaseURL(t, adminURL, "_control", "authenticator", password), masterKey, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	time.Sleep(1500 * time.Millisecond)
	server.poolMu.Lock()
	_, opened := server.pools[p.ID]
	server.poolMu.Unlock()
	if opened {
		t.Fatal("scheduler opened a pool for a schema-less measurement project")
	}
}
