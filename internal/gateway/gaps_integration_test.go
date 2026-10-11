package gateway

// Follow-ups to the audit fixes: a refresh racing a sign-out, bans, and
// realtime connections whose token expires or whose sign-in ends.

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
)

// do is safe to call from any goroutine: it never stops the test itself.
func (e *auditEnv) do(method, path, bearer, body string) (int, []byte, error) {
	req, err := http.NewRequest(method, e.h.URL+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("apikey", e.p.AnonKey)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.h.Client().Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	content, err := io.ReadAll(resp.Body)
	return resp.StatusCode, content, err
}

func TestBanEndsSessionsAndBlocksRefresh(t *testing.T) {
	e := newAuditEnv(t, nil)
	id := e.createUser("banned@example.test")
	tokens := e.login("banned@example.test")
	header := http.Header{"Authorization": {"Bearer " + tokens.Access}}
	wsURL := "ws" + strings.TrimPrefix(e.h.URL, "http") + "/realtime/v1/websocket?apikey=" + e.p.AnonKey
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	requestAuthJSON(t, e.h.Client(), http.MethodPut, e.h.URL+"/auth/v1/admin/users/"+id, e.p.ServiceKey, `{"ban_duration":"24h"}`, http.StatusOK)
	if got := e.status("GET", "/auth/v1/user", tokens.Access, ""); got != http.StatusUnauthorized {
		t.Fatalf("access token after ban: %d", got)
	}
	e.refresh(tokens.Refresh, http.StatusBadRequest)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("realtime connection stayed open after ban")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("realtime connection was not closed by the ban")
	}
	// Lifting the ban lets the user sign in again.
	requestAuthJSON(t, e.h.Client(), http.MethodPut, e.h.URL+"/auth/v1/admin/users/"+id, e.p.ServiceKey, `{"ban_duration":"none"}`, http.StatusOK)
	again := e.login("banned@example.test")
	if got := e.status("GET", "/auth/v1/user", again.Access, ""); got != http.StatusOK {
		t.Fatalf("after unban: %d", got)
	}
	// A ban written straight into the database (for example by a project
	// migration) also takes effect on the next request.
	e.mustSQL(`UPDATE auth.users SET banned_until = now() + interval '1 hour' WHERE id = '` + id + `'`)
	if got := e.status("GET", "/auth/v1/user", again.Access, ""); got != http.StatusUnauthorized {
		t.Fatalf("after a database ban: %d", got)
	}
}

func TestRealtimeAcceptsRefreshedTokensAndDropsEndedOnes(t *testing.T) {
	e := newAuditEnv(t, nil)
	userID := e.createUser("socket@example.test")
	tokens := e.login("socket@example.test")
	wsURL := "ws" + strings.TrimPrefix(e.h.URL, "http") + "/realtime/v1/websocket?apikey=" + e.p.AnonKey
	// Connect with the anon key only, then sign in through a channel join,
	// as supabase-js does.
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	send := func(ref, topic, event string, payload any) {
		t.Helper()
		if err := conn.WriteJSON([]any{"1", ref, topic, event, payload}); err != nil {
			t.Fatal(err)
		}
	}
	reply := func(ref string) string {
		t.Helper()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for {
			var frame []json.RawMessage
			if err := conn.ReadJSON(&frame); err != nil {
				t.Fatalf("waiting for reply %s: %v", ref, err)
			}
			var got string
			_ = json.Unmarshal(frame[1], &got)
			if got != ref {
				continue
			}
			var body struct {
				Status string `json:"status"`
			}
			_ = json.Unmarshal(frame[4], &body)
			return body.Status
		}
	}
	send("1", "realtime:room", "phx_join", map[string]any{"access_token": tokens.Access, "config": map[string]any{}})
	if got := reply("1"); got != "ok" {
		t.Fatalf("join with access token: %s", got)
	}
	// supabase-js pushes a refreshed token with an access_token message.
	refreshed := e.refresh(tokens.Refresh, http.StatusOK)
	send("2", "realtime:room", "access_token", map[string]any{"access_token": refreshed.Access})
	if got := reply("2"); got != "ok" {
		t.Fatalf("access_token update: %s", got)
	}
	send("3", "realtime:room", "access_token", map[string]any{"access_token": "not-a-token"})
	if got := reply("3"); got != "error" {
		t.Fatalf("invalid access_token update: %s", got)
	}

	// A sign-in that ends without this gateway being told (expiry, another
	// gateway, a ban in SQL) is noticed by the periodic check.
	e.mustSQL(`UPDATE auth.refresh_tokens SET revoked_at = now() WHERE user_id = '` + userID + `'`)
	e.s.realtime.revalidate()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("socket of an ended sign-in was not closed")
			}
			break
		}
	}

	// An expired token is dropped too, even without a session.
	short, err := keys.SignJWT(gatewayJWTSecret(t, e.adminURL, e.p.ID, e.master), map[string]any{
		"iss": "pibase", "aud": "authenticated", "role": "authenticated", "sub": userID,
		"iat": time.Now().Unix(), "exp": time.Now().Add(2 * time.Second).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{"Authorization": {"Bearer " + short}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	time.Sleep(2500 * time.Millisecond)
	e.s.realtime.revalidate()
	_ = conn2.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		if _, _, err := conn2.ReadMessage(); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("socket with an expired token was not closed")
			}
			break
		}
	}
}

// A sign-out that arrives while a refresh is still writing its new token must
// end that token too. Today the gateway gives each project one database
// connection, which runs the two one after the other; this test keeps that
// guarantee if the pool ever grows. A test-only trigger holds the refresh open
// for 0.4 s after the new token row is written, so the sign-out lands inside it.
func TestSignOutDuringSlowRefreshEndsTheRefreshedToken(t *testing.T) {
	e := newAuditEnv(t, nil)
	ctx := context.Background()
	db, err := pgx.Connect(ctx, databaseURL(t, e.adminURL, e.p.Database, parsedUsername(t, e.adminURL), urlPassword(t, e.adminURL)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	e.createUser("slow@example.test")
	tokens := e.login("slow@example.test")
	if _, err := db.Exec(ctx, `CREATE FUNCTION public.slow_refresh() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.4); RETURN NEW; END $$;
CREATE TRIGGER slow_refresh AFTER INSERT ON auth.refresh_tokens FOR EACH ROW EXECUTE FUNCTION public.slow_refresh();`); err != nil {
		t.Fatal(err)
	}
	var refreshStatus int
	var refreshBody []byte
	done := make(chan struct{})
	go func() {
		defer close(done)
		refreshStatus, refreshBody, _ = e.do("POST", "/auth/v1/token?grant_type=refresh_token", "", `{"refresh_token":"`+tokens.Refresh+`"}`)
	}()
	time.Sleep(150 * time.Millisecond)
	if status, _, err := e.do("POST", "/auth/v1/logout", tokens.Access, "{}"); err != nil || status != http.StatusNoContent {
		t.Fatalf("logout: %d %v", status, err)
	}
	<-done
	if refreshStatus != http.StatusOK {
		t.Fatalf("slow refresh: %d %s", refreshStatus, refreshBody)
	}
	next := parseTokens(t, refreshBody)
	if status, _, _ := e.do("GET", "/auth/v1/user", next.Access, ""); status != http.StatusUnauthorized {
		t.Fatalf("token from a refresh that raced the sign-out still works: %d", status)
	}
	if status, _, _ := e.do("POST", "/auth/v1/token?grant_type=refresh_token", "", `{"refresh_token":"`+next.Refresh+`"}`); status == http.StatusOK {
		t.Fatal("refresh token from a refresh that raced the sign-out still works")
	}
}
