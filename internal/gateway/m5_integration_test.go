package gateway

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/adminsql"
	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
)

func TestM5RealtimeRLSAndCronOutbox(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	t.Setenv("PIBASE_SCHEDULER_DISABLED", "true")
	ctx := context.Background()
	const password = "integration-auth-password"
	masterKey := bytes.Repeat([]byte{5}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, adminURL, "gateway-m5", masterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, p)
	prefix := "p_" + p.ID
	fixture := `CREATE TABLE public.realtime_rows (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, owner_id text NOT NULL, note text NOT NULL);
CREATE TABLE public.cron_runs (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, note text NOT NULL);
ALTER TABLE public.realtime_rows ENABLE ROW LEVEL SECURITY;
GRANT SELECT ON public.realtime_rows TO ` + pgx.Identifier{prefix + "_authenticated"}.Sanitize() + `;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.realtime_rows TO ` + pgx.Identifier{prefix + "_service_role"}.Sanitize() + `;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + pgx.Identifier{prefix + "_service_role"}.Sanitize() + `;
CREATE POLICY realtime_owner ON public.realtime_rows FOR SELECT TO ` + pgx.Identifier{prefix + "_authenticated"}.Sanitize() + ` USING (owner_id=current_setting('request.jwt.claims',true)::jsonb->>'sub');
CREATE POLICY realtime_service ON public.realtime_rows FOR ALL TO ` + pgx.Identifier{prefix + "_service_role"}.Sanitize() + ` USING (true) WITH CHECK (true);`
	authURL := databaseURL(t, adminURL, "_control", "authenticator", password)
	if err := adminsql.ApplyMigration(ctx, authURL, p.ID, "0001_m5_fixture", fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := adminsql.Run(ctx, authURL, p.ID, `SELECT realtime.enable_table('public.realtime_rows'::regclass)`); err != nil {
		t.Fatal(err)
	}

	server, err := New(ctx, authURL, masterKey, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	secret := gatewayJWTSecret(t, adminURL, p.ID, masterKey)
	userA, err := keys.SignJWT(secret, map[string]any{"iss": "pibase", "aud": "authenticated", "role": "authenticated", "sub": "user-a", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	userB, err := keys.SignJWT(secret, map[string]any{"iss": "pibase", "aud": "authenticated", "role": "authenticated", "sub": "user-b", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	a := joinRealtime(t, httpServer.URL, p.AnonKey, userA, "a")
	defer a.Close()
	b := joinRealtime(t, httpServer.URL, p.AnonKey, userB, "b")
	defer b.Close()
	// The join starts an asynchronous LISTEN connection. Give it one bounded
	// retry interval before writing the row that drives the end-to-end test.
	time.Sleep(1100 * time.Millisecond)
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/rest/v1/realtime_rows", p.ServiceKey, `{"owner_id":"user-a","note":"only A"}`, "return=representation", http.StatusCreated)
	event := readRealtimeEvent(t, a, 3*time.Second)
	data, _ := event[4].(map[string]any)["data"].(map[string]any)
	record, _ := data["record"].(map[string]any)
	if record["owner_id"] != "user-a" || record["note"] != "only A" {
		t.Fatalf("unexpected realtime record: %#v", event)
	}
	_ = b.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	var hidden any
	if err := b.ReadJSON(&hidden); err == nil {
		t.Fatalf("subscriber received another user's row: %#v", hidden)
	}

	request, err := http.NewRequest(http.MethodGet, httpServer.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("apikey", p.ServiceKey)
	who, err := server.authenticate(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = adminsql.Run(ctx, authURL, p.ID, `INSERT INTO cron.jobs(schedule,sql) VALUES ('* * * * *','INSERT INTO public.cron_runs(note) VALUES (''ran'')')`); err != nil {
		t.Fatal(err)
	}
	server.runCronProject(ctx, who.project)
	projectConn, err := pgx.Connect(ctx, databaseURL(t, adminURL, p.Database, "postgres", urlPassword(t, adminURL)))
	if err != nil {
		t.Fatal(err)
	}
	defer projectConn.Close(ctx)
	var runs int
	if err := projectConn.QueryRow(ctx, `SELECT count(*) FROM public.cron_runs`).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("cron SQL run count=%d err=%v", runs, err)
	}
	if _, err = adminsql.Run(ctx, authURL, p.ID, `INSERT INTO cron.jobs(schedule,webhook_url) VALUES ('* * * * *','http://127.0.0.1/blocked')`); err != nil {
		t.Fatal(err)
	}
	server.runCronProject(ctx, who.project)
	server.deliverProject(ctx, who.project)
	var lastError string
	if err := projectConn.QueryRow(ctx, `SELECT last_error FROM cron.webhook_outbox ORDER BY id DESC LIMIT 1`).Scan(&lastError); err != nil || !strings.Contains(lastError, "not a public address") {
		t.Fatalf("webhook SSRF guard error=%q err=%v", lastError, err)
	}
	if _, err = adminsql.Run(ctx, authURL, p.ID, `CREATE TRIGGER m5_event_outbox AFTER INSERT ON public.cron_runs FOR EACH ROW EXECUTE FUNCTION cron.enqueue_webhook('http://127.0.0.1/event')`); err != nil {
		t.Fatal(err)
	}
	if _, err = adminsql.Run(ctx, authURL, p.ID, `INSERT INTO public.cron_runs(note) VALUES ('event')`); err != nil {
		t.Fatal(err)
	}
	var eventURL, eventBody string
	if err := projectConn.QueryRow(ctx, `SELECT url,convert_from(body,'UTF8') FROM cron.webhook_outbox ORDER BY id DESC LIMIT 1`).Scan(&eventURL, &eventBody); err != nil {
		t.Fatal(err)
	}
	if eventURL != "http://127.0.0.1/event" || !strings.Contains(eventBody, `"type" : "INSERT"`) && !strings.Contains(eventBody, `"type":"INSERT"`) {
		t.Fatalf("event trigger did not enqueue expected payload: url=%q body=%s", eventURL, eventBody)
	}
	if _, err = adminsql.Run(ctx, authURL, p.ID, `INSERT INTO cron.webhook_outbox(url)
SELECT 'https://hooks.example/filled' FROM generate_series(1, GREATEST(0, 256 - (SELECT count(*) FROM cron.webhook_outbox WHERE delivered_at IS NULL AND dead_lettered_at IS NULL)))`); err != nil {
		t.Fatal(err)
	}
	if _, err = adminsql.Run(ctx, authURL, p.ID, `INSERT INTO cron.jobs(schedule,webhook_url) VALUES ('* * * * *','https://hooks.example/backpressure')`); err != nil {
		t.Fatal(err)
	}
	server.runCronProject(ctx, who.project)
	if err := projectConn.QueryRow(ctx, `SELECT last_error FROM cron.jobs WHERE webhook_url='https://hooks.example/backpressure'`).Scan(&lastError); err != nil || !strings.Contains(lastError, "pending limit") {
		t.Fatalf("scheduler did not record outbox backpressure: %q err=%v", lastError, err)
	}
	if _, err = adminsql.Run(ctx, authURL, p.ID, `INSERT INTO cron.webhook_outbox(url) VALUES ('https://hooks.example/overflow')`); err == nil || !strings.Contains(err.Error(), "pending limit") {
		t.Fatalf("outbox pending cap was not enforced: %v", err)
	}
}

func joinRealtime(t *testing.T, baseURL, apiKey, token, id string) *websocket.Conn {
	t.Helper()
	endpoint := "ws" + strings.TrimPrefix(baseURL, "http") + "/realtime/v1/websocket?apikey=" + url.QueryEscape(apiKey)
	conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"config": map[string]any{"postgres_changes": []map[string]string{{"id": id, "event": "INSERT", "schema": "public", "table": "realtime_rows"}}}, "access_token": token}
	if err := conn.WriteJSON([]any{"1", "1", "realtime:" + id, "phx_join", payload}); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var reply []any
	if err := conn.ReadJSON(&reply); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if len(reply) != 5 || reply[3] != "phx_reply" {
		conn.Close()
		t.Fatalf("unexpected realtime join reply: %#v", reply)
	}
	return conn
}

func readRealtimeEvent(t *testing.T, conn *websocket.Conn, timeout time.Duration) []any {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	for {
		var frame []any
		if err := conn.ReadJSON(&frame); err != nil {
			t.Fatal(err)
		}
		if len(frame) == 5 && frame[3] == "postgres_changes" {
			return frame
		}
	}
}
