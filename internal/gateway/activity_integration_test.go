package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
)

func TestActivityRecordsRealRequestsPerService(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "activity-integration-password"
	masterKey := bytes.Repeat([]byte{7}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, adminURL, "gateway-activity", masterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, p)
	t.Setenv("PIBASE_STORAGE_ROOT", t.TempDir())
	server, err := New(ctx, databaseURL(t, adminURL, "_control", "authenticator", password), masterKey, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	do := func(method, path, key string, want int) {
		t.Helper()
		req, err := http.NewRequest(method, httpServer.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if key != "" {
			req.Header.Set("apikey", key)
		}
		resp, err := httpServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status %d, want %d", method, path, resp.StatusCode, want)
		}
	}
	// database: 2 client errors (missing table and missing function).
	do(http.MethodGet, "/rest/v1/missing_table", p.ServiceKey, http.StatusNotFound)
	do(http.MethodPost, "/rest/v1/rpc/missing_fn", p.ServiceKey, http.StatusNotFound)
	// auth: 1 client error (no user token).
	do(http.MethodGet, "/auth/v1/user", p.AnonKey, http.StatusUnauthorized)
	// storage: 2 successes.
	do(http.MethodGet, "/storage/v1/bucket", p.ServiceKey, http.StatusOK)
	do(http.MethodGet, "/storage/v1/bucket", p.ServiceKey, http.StatusOK)
	// Not attributable to any project, so never counted.
	do(http.MethodGet, "/rest/v1/missing_table", "not-a-key", http.StatusUnauthorized)
	do(http.MethodGet, "/healthz", "", http.StatusOK)

	// realtime: counted as soon as the upgrade succeeds, while still open.
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/realtime/v1/websocket?apikey=" + p.AnonKey
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// A project deleted before the flush must not break the batch.
	server.activity.add("deadbeef", "database", http.StatusOK, time.Now())
	if err := server.flushActivity(ctx); err != nil {
		t.Fatal(err)
	}

	a, err := control.ProjectActivity(ctx, adminURL, p.ID, time.Hour, time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][3]int64{}
	for _, s := range a.Services {
		got[s.Service] = [3]int64{s.Requests, s.Warnings, s.Errors}
	}
	want := map[string][3]int64{
		"database": {2, 2, 0},
		"auth":     {1, 1, 0},
		"storage":  {2, 0, 0},
		"realtime": {1, 0, 0},
	}
	for service, w := range want {
		if got[service] != w {
			t.Errorf("%s: got requests/warnings/errors %v, want %v", service, got[service], w)
		}
	}
	if a.Requests != 6 || a.Warnings != 3 || a.Errors != 0 {
		t.Fatalf("totals: %d requests, %d warnings, %d errors", a.Requests, a.Warnings, a.Errors)
	}
	if a.SuccessRate == nil || *a.SuccessRate != 0.5 {
		t.Fatalf("success rate: %v", a.SuccessRate)
	}
	if len(a.Total) != 60 || a.Total[59].Requests != 6 {
		t.Fatalf("series: %d buckets, last %+v", len(a.Total), a.Total[len(a.Total)-1])
	}

	// Counts accumulate across flushes rather than overwriting.
	do(http.MethodGet, "/storage/v1/bucket", p.ServiceKey, http.StatusOK)
	if err := server.flushActivity(ctx); err != nil {
		t.Fatal(err)
	}
	a, err = control.ProjectActivity(ctx, adminURL, p.ID, time.Hour, time.Minute, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if a.Requests != 7 {
		t.Fatalf("after second flush: %d requests", a.Requests)
	}
}

func TestProjectKeysCanBeViewedAgain(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	masterKey := bytes.Repeat([]byte{8}, 32)
	if err := control.Bootstrap(ctx, adminURL, "keys-integration-password"); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, adminURL, "gateway-keys", masterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, p)
	k, err := control.GetProjectKeys(ctx, adminURL, p.ID, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	if k.AnonKey != p.AnonKey || k.ServiceKey != p.ServiceKey {
		t.Fatal("stored keys differ from the keys issued at creation")
	}
	secret, err := base64.StdEncoding.DecodeString(k.JWTSecret)
	if err != nil || !bytes.Equal(secret, gatewayJWTSecret(t, adminURL, p.ID, masterKey)) {
		t.Fatal("jwt_secret does not decode to the project signing secret")
	}
	anon, service, err := control.RotateKeys(ctx, adminURL, p.ID, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	k, err = control.GetProjectKeys(ctx, adminURL, p.ID, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	if k.AnonKey != anon || k.ServiceKey != service || k.AnonKey == p.AnonKey {
		t.Fatal("rotated keys are not the ones returned by GetProjectKeys")
	}
	// Projects created before keys were stored show the secret but no keys.
	conn, err := pgx.Connect(ctx, databaseURL(t, adminURL, "_control", parsedUsername(t, adminURL), urlPassword(t, adminURL)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `UPDATE control.projects SET anon_key_encrypted=NULL,service_key_encrypted=NULL WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	k, err = control.GetProjectKeys(ctx, adminURL, p.ID, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	if k.AnonKey != "" || k.ServiceKey != "" || k.JWTSecret == "" {
		t.Fatalf("legacy project keys: %+v", k)
	}
	if _, err := control.GetProjectKeys(ctx, adminURL, "00000000", masterKey); err != control.ErrNotFound {
		t.Fatalf("missing project: %v", err)
	}
}
