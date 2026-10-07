package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/gorilla/websocket"
)

func TestM5bBroadcastPresenceIsolation(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	t.Setenv("PIBASE_SCHEDULER_DISABLED", "true")
	ctx := context.Background()
	const password = "integration-auth-password"
	masterKey := bytes.Repeat([]byte{6}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	projectA, err := control.CreateProject(ctx, adminURL, "gateway-m5b-a", masterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, projectA)
	projectB, err := control.CreateProject(ctx, adminURL, "gateway-m5b-b", masterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, projectB)

	authURL := databaseURL(t, adminURL, "_control", "authenticator", password)
	server, err := New(ctx, authURL, masterKey, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	a1 := dialRealtime(t, httpServer.URL, projectA.AnonKey)
	defer a1.Close()
	a2 := dialRealtime(t, httpServer.URL, projectA.AnonKey)
	defer a2.Close()
	b1 := dialRealtime(t, httpServer.URL, projectB.AnonKey)
	defer b1.Close()
	otherTopic := dialRealtime(t, httpServer.URL, projectA.AnonKey)
	defer otherTopic.Close()
	joinFeatureChannel(t, a1, "realtime:room", "a", false, true)
	joinFeatureChannel(t, a2, "realtime:room", "a2", false, true)
	joinFeatureChannel(t, b1, "realtime:room", "b", false, true)
	joinFeatureChannel(t, otherTopic, "realtime:other", "other", false, true)

	pushRealtime(t, a1, "2", "realtime:room", "broadcast", map[string]any{"type": "broadcast", "event": "cursor", "payload": map[string]any{"x": 12}})
	assertRealtimeEvent(t, a2, "broadcast", 2*time.Second)
	assertNextReply(t, a1, "2", "ok")

	pushRealtime(t, a1, "3", "realtime:room", "presence", map[string]any{"type": "presence", "event": "track", "payload": map[string]any{"online_at": "now"}})
	assertRealtimeEvent(t, a1, "presence_diff", 2*time.Second)
	assertReply(t, a1, "3", "ok")
	joinDiff := assertRealtimeEvent(t, a2, "presence_diff", 2*time.Second)
	if !strings.Contains(string(joinDiff), `"a"`) || !strings.Contains(string(joinDiff), `"phx_ref"`) {
		t.Fatalf("presence join missing key/ref: %s", joinDiff)
	}

	a3 := dialRealtime(t, httpServer.URL, projectA.AnonKey)
	defer a3.Close()
	state := joinFeatureChannel(t, a3, "realtime:room", "a3", true, true)
	if !strings.Contains(string(state), `"a"`) {
		t.Fatalf("presence state omitted tracked client: %s", state)
	}

	pushRealtime(t, a1, "4", "realtime:room", "presence", map[string]any{"type": "presence", "event": "untrack"})
	assertRealtimeEvent(t, a1, "presence_diff", 2*time.Second)
	assertReply(t, a1, "4", "ok")
	leaveDiff := assertRealtimeEvent(t, a3, "presence_diff", 2*time.Second)
	if !strings.Contains(string(leaveDiff), `"leaves":{"a"`) {
		t.Fatalf("presence untrack missing leave: %s", leaveDiff)
	}

	joinFeatureChannel(t, a1, "realtime:room", "a", true, true)
	pushRealtime(t, a1, "5", "realtime:room", "broadcast", map[string]any{"type": "broadcast", "event": "self", "payload": map[string]any{"ok": true}})
	assertRealtimeEvent(t, a1, "broadcast", 2*time.Second)
	assertReply(t, a1, "5", "ok")

	pushRealtime(t, a2, "6", "realtime:room", "presence", map[string]any{"type": "presence", "event": "track", "payload": map[string]any{"online": true}})
	assertRealtimeEvent(t, a2, "presence_diff", 2*time.Second)
	assertReply(t, a2, "6", "ok")
	assertRealtimeEvent(t, a3, "presence_diff", 2*time.Second)
	_ = a2.Close()
	disconnectDiff := assertRealtimeEvent(t, a3, "presence_diff", 2*time.Second)
	if !strings.Contains(string(disconnectDiff), `"leaves":{"a2"`) {
		t.Fatalf("disconnect did not remove presence: %s", disconnectDiff)
	}

	pushRealtime(t, a1, "7", "realtime:room", "presence", map[string]any{"type": "presence", "event": "track", "payload": map[string]any{"second": true}})
	assertRealtimeEvent(t, a1, "presence_diff", 2*time.Second)
	assertReply(t, a1, "7", "ok")
	assertRealtimeEvent(t, a3, "presence_diff", 2*time.Second)
	pushRealtime(t, a1, "8", "realtime:room", "phx_leave", map[string]any{})
	assertReply(t, a1, "8", "ok")
	channelLeave := assertRealtimeEvent(t, a3, "presence_diff", 2*time.Second)
	if !strings.Contains(string(channelLeave), `"leaves":{"a"`) {
		t.Fatalf("channel leave did not remove presence: %s", channelLeave)
	}

	pushRealtime(t, a3, "9", "realtime:room", "broadcast", map[string]any{"type": "broadcast", "event": "too-big", "payload": strings.Repeat("x", maxRealtimePayloadBytes+1)})
	assertReply(t, a3, "9", "error")
	private := dialRealtime(t, httpServer.URL, projectA.AnonKey)
	defer private.Close()
	pushRealtime(t, private, "private", "realtime:private", "phx_join", map[string]any{"config": map[string]any{"private": true}})
	assertReply(t, private, "private", "error")
	limited := dialRealtime(t, httpServer.URL, projectA.AnonKey)
	defer limited.Close()
	for i := 0; i < 20; i++ {
		topic := "realtime:limit-" + strconv.Itoa(i)
		pushRealtime(t, limited, strconv.Itoa(i), topic, "phx_join", map[string]any{"config": map[string]any{}})
		assertReply(t, limited, strconv.Itoa(i), "ok")
	}
	pushRealtime(t, limited, "overflow", "realtime:limit-overflow", "phx_join", map[string]any{"config": map[string]any{}})
	assertReply(t, limited, "overflow", "error")

	_ = b1.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var isolated any
	if err := b1.ReadJSON(&isolated); err == nil {
		t.Fatalf("project B received project A event: %#v", isolated)
	}
	_ = otherTopic.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if err := otherTopic.ReadJSON(&isolated); err == nil {
		t.Fatalf("different topic received room event: %#v", isolated)
	}
	t.Run("supabase-js realtime acceptance", func(t *testing.T) {
		runSupabaseRealtimeAcceptance(t, httpServer.URL, projectA.AnonKey)
	})
}

func runSupabaseRealtimeAcceptance(t *testing.T, baseURL, anonKey string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable; supabase-js realtime acceptance requires scripts/test-supabase-js.sh")
	}
	script, err := filepath.Abs("../../tests/realtime-js.mjs")
	if err != nil {
		t.Fatal(err)
	}
	// Node 22 exposes WebSocket globally; Node 20 in the development image
	// requires the compatibility flag for the same API.
	command := exec.Command(node, "--experimental-websocket", script)
	command.Env = append(os.Environ(), "PIBASE_TEST_URL="+baseURL, "PIBASE_TEST_ANON_KEY="+anonKey)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("supabase-js realtime acceptance failed: %v\n%s", err, output)
	} else {
		t.Log(strings.TrimSpace(string(output)))
	}
}

func dialRealtime(t *testing.T, baseURL, apiKey string) *websocket.Conn {
	t.Helper()
	endpoint := "ws" + strings.TrimPrefix(baseURL, "http") + "/realtime/v1/websocket?apikey=" + url.QueryEscape(apiKey)
	conn, _, err := websocket.DefaultDialer.Dial(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func joinFeatureChannel(t *testing.T, conn *websocket.Conn, topic, key string, self, presence bool) []byte {
	t.Helper()
	payload := map[string]any{"config": map[string]any{"broadcast": map[string]any{"self": self, "ack": true}, "presence": map[string]any{"key": key, "enabled": presence}, "private": false, "postgres_changes": []any{}}}
	pushRealtime(t, conn, "1", topic, "phx_join", payload)
	assertReply(t, conn, "1", "ok")
	if !presence {
		return nil
	}
	return assertRealtimeEvent(t, conn, "presence_state", 2*time.Second)
}

func pushRealtime(t *testing.T, conn *websocket.Conn, ref, topic, event string, payload any) {
	t.Helper()
	if err := conn.WriteJSON([]any{"1", ref, topic, event, payload}); err != nil {
		t.Fatal(err)
	}
}

func assertReply(t *testing.T, conn *websocket.Conn, ref, status string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		message := readRealtimeRaw(t, conn, time.Until(deadline))
		var frame []json.RawMessage
		if json.Unmarshal(message, &frame) != nil || len(frame) != 5 {
			continue
		}
		var gotRef, event string
		_ = json.Unmarshal(frame[1], &gotRef)
		_ = json.Unmarshal(frame[3], &event)
		if event != "phx_reply" || gotRef != ref {
			continue
		}
		var payload struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(frame[4], &payload) != nil || payload.Status != status {
			t.Fatalf("reply ref %s status=%q payload=%s", ref, payload.Status, frame[4])
		}
		return
	}
}

func assertNextReply(t *testing.T, conn *websocket.Conn, ref, status string) {
	t.Helper()
	message := readRealtimeRaw(t, conn, 2*time.Second)
	var frame []json.RawMessage
	if json.Unmarshal(message, &frame) != nil || len(frame) != 5 {
		t.Fatalf("invalid reply frame: %s", message)
	}
	var gotRef, event string
	_ = json.Unmarshal(frame[1], &gotRef)
	_ = json.Unmarshal(frame[3], &event)
	var payload struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(frame[4], &payload)
	if event != "phx_reply" || gotRef != ref || payload.Status != status {
		t.Fatalf("unexpected next frame: %s", message)
	}
}

func assertRealtimeEvent(t *testing.T, conn *websocket.Conn, event string, timeout time.Duration) []byte {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		message := readRealtimeRaw(t, conn, time.Until(deadline))
		var frame []json.RawMessage
		if json.Unmarshal(message, &frame) != nil || len(frame) != 5 {
			continue
		}
		var got string
		_ = json.Unmarshal(frame[3], &got)
		if got == event {
			return message
		}
	}
}

func readRealtimeRaw(t *testing.T, conn *websocket.Conn, timeout time.Duration) []byte {
	t.Helper()
	if timeout <= 0 {
		t.Fatal("timed out waiting for realtime frame")
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	_, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	return message
}
