package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
)

const (
	realtimeChannel = "pibase_changes"
	realtimeIdle    = 5 * time.Minute
)

// realtimeHub implements the narrow, auditable postgres_changes subset of the
// Phoenix protocol used by supabase-js. It intentionally has no broadcast or
// presence support: every event must pass a database RLS check.
type realtimeHub struct {
	server        *Server
	maxPerProject int
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	clients       map[*realtimeClient]struct{}
	listeners     map[string]struct{}
	closed        bool
}

type realtimeClient struct {
	hub           *realtimeHub
	conn          *websocket.Conn
	principal     principal
	apiKey        string
	mu            sync.Mutex
	subscriptions []realtimeSubscription
}

type realtimeSubscription struct {
	id, topic, schema, table, event string
}

type realtimeJoin struct {
	Config struct {
		PostgresChanges []struct {
			ID     string `json:"id"`
			Event  string `json:"event"`
			Schema string `json:"schema"`
			Table  string `json:"table"`
			Filter string `json:"filter"`
		} `json:"postgres_changes"`
	} `json:"config"`
	AccessToken string `json:"access_token"`
}

func newRealtimeHub(server *Server) (*realtimeHub, error) {
	maximum := 32
	if raw := os.Getenv("PIBASE_REALTIME_MAX_CONNECTIONS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 512 {
			return nil, fmt.Errorf("PIBASE_REALTIME_MAX_CONNECTIONS must be between 1 and 512")
		}
		maximum = parsed
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &realtimeHub{server: server, maxPerProject: maximum, ctx: ctx, cancel: cancel, clients: make(map[*realtimeClient]struct{}), listeners: make(map[string]struct{})}, nil
}

func (h *realtimeHub) websocket(w http.ResponseWriter, r *http.Request) {
	// supabase-js supplies the project key in the WebSocket query string.
	if r.Header.Get("apikey") == "" && r.URL.Query().Get("apikey") != "" {
		r.Header.Set("apikey", r.URL.Query().Get("apikey"))
	}
	who, err := h.server.authenticate(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	h.mu.Lock()
	count := 0
	for client := range h.clients {
		if client.principal.project.id == who.project.id {
			count++
		}
	}
	if h.closed || count >= h.maxPerProject {
		h.mu.Unlock()
		writeAPIError(w, httpError(http.StatusTooManyRequests, "realtime connection limit reached"))
		return
	}
	h.mu.Unlock()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, ReadBufferSize: 4096, WriteBufferSize: 4096}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &realtimeClient{hub: h, conn: conn, principal: who, apiKey: r.Header.Get("apikey")}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = conn.Close()
		return
	}
	h.clients[client] = struct{}{}
	h.mu.Unlock()
	defer h.remove(client)
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(realtimeIdle))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(realtimeIdle)) })
	go client.ping()
	client.read()
}

func (c *realtimeClient) read() {
	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		var frame []json.RawMessage
		if json.Unmarshal(message, &frame) != nil || len(frame) != 5 {
			_ = c.write([]any{nil, nil, "phoenix", "phx_error", map[string]string{"reason": "invalid realtime frame"}})
			continue
		}
		var joinRef, ref, topic, event string
		_ = json.Unmarshal(frame[0], &joinRef)
		_ = json.Unmarshal(frame[1], &ref)
		_ = json.Unmarshal(frame[2], &topic)
		_ = json.Unmarshal(frame[3], &event)
		if event == "heartbeat" {
			_ = c.reply(joinRef, ref, topic, "ok", map[string]any{})
			continue
		}
		if event != "phx_join" {
			_ = c.reply(joinRef, ref, topic, "error", map[string]string{"reason": "only postgres_changes joins are supported"})
			continue
		}
		var join realtimeJoin
		if err := json.Unmarshal(frame[4], &join); err != nil {
			_ = c.reply(joinRef, ref, topic, "error", map[string]string{"reason": "invalid join payload"})
			continue
		}
		if join.AccessToken != "" {
			// Access tokens are supplied in the channel join by supabase-js.
			req := realtimeTokenRequest(c.apiKey, join.AccessToken)
			who, err := c.hub.server.authenticate(context.Background(), req)
			if err != nil || who.project.id != c.principal.project.id {
				_ = c.reply(joinRef, ref, topic, "error", map[string]string{"reason": "invalid access token"})
				continue
			}
			c.principal = who
		}
		subs, err := c.join(topic, join)
		if err != nil {
			_ = c.reply(joinRef, ref, topic, "error", map[string]string{"reason": err.Error()})
			continue
		}
		ids := make([]map[string]string, 0, len(subs))
		for _, sub := range subs {
			ids = append(ids, map[string]string{"id": sub.id})
		}
		_ = c.reply(joinRef, ref, topic, "ok", map[string]any{"postgres_changes": ids})
	}
}

func realtimeTokenRequest(apiKey, token string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "http://realtime.invalid/", nil)
	r.Header.Set("apikey", apiKey)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func (c *realtimeClient) join(topic string, join realtimeJoin) ([]realtimeSubscription, error) {
	if len(join.Config.PostgresChanges) == 0 || len(join.Config.PostgresChanges) > 20 {
		return nil, fmt.Errorf("postgres_changes requires 1 to 20 subscriptions")
	}
	subs := make([]realtimeSubscription, 0, len(join.Config.PostgresChanges))
	for i, change := range join.Config.PostgresChanges {
		if change.Schema != "public" || change.Table == "" || change.Filter != "" {
			return nil, fmt.Errorf("only unfiltered public-table changes are supported")
		}
		event := change.Event
		if event == "" {
			event = "*"
		}
		if event != "*" && event != "INSERT" && event != "UPDATE" && event != "DELETE" {
			return nil, fmt.Errorf("invalid postgres_changes event")
		}
		id := change.ID
		if id == "" {
			id = strconv.Itoa(i + 1)
		}
		subs = append(subs, realtimeSubscription{id: id, topic: topic, schema: change.Schema, table: change.Table, event: event})
	}
	c.mu.Lock()
	c.subscriptions = append(c.subscriptions, subs...)
	c.mu.Unlock()
	c.hub.ensureListener(c.principal.project)
	return subs, nil
}

func (c *realtimeClient) reply(joinRef, ref, topic, status string, response any) error {
	return c.write([]any{joinRef, ref, topic, "phx_reply", map[string]any{"status": status, "response": response}})
}

func (c *realtimeClient) write(frame any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(frame)
}

func (c *realtimeClient) ping() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		if c.writeControl(websocket.PingMessage, []byte("pibase"), time.Now().Add(5*time.Second)) != nil {
			return
		}
	}
}

func (c *realtimeClient) writeControl(kind int, data []byte, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteControl(kind, data, deadline)
}

func (h *realtimeHub) remove(client *realtimeClient) {
	h.mu.Lock()
	delete(h.clients, client)
	h.mu.Unlock()
	_ = client.conn.Close()
}

func (h *realtimeHub) ensureListener(p *project) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	if _, ok := h.listeners[p.id]; ok {
		h.mu.Unlock()
		return
	}
	h.listeners[p.id] = struct{}{}
	h.mu.Unlock()
	go h.listen(p)
}

func (h *realtimeHub) listen(p *project) {
	defer func() {
		h.mu.Lock()
		delete(h.listeners, p.id)
		h.mu.Unlock()
	}()
	config, err := pgx.ParseConfig(h.server.databaseURL)
	if err != nil {
		return
	}
	config.Database = p.database
	ctx := h.ctx
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, "LISTEN "+pgx.Identifier{realtimeChannel}.Sanitize()); err != nil {
		return
	}
	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return
		}
		var change struct{ Table, Op, ID string }
		if json.Unmarshal([]byte(notification.Payload), &change) != nil || change.Table == "" || change.ID == "" {
			continue
		}
		go h.deliver(p, change.Table, change.Op, change.ID)
	}
}

func (h *realtimeHub) deliver(p *project, table, operation, id string) {
	if operation == "DELETE" { // no row exists to RLS-check without leaking OLD.
		return
	}
	h.mu.Lock()
	var clients []*realtimeClient
	for client := range h.clients {
		if client.principal.project.id == p.id {
			clients = append(clients, client)
		}
	}
	h.mu.Unlock()
	for _, client := range clients {
		client.mu.Lock()
		subs := append([]realtimeSubscription(nil), client.subscriptions...)
		who := client.principal
		client.mu.Unlock()
		for _, sub := range subs {
			if sub.table != table || (sub.event != "*" && sub.event != operation) || !h.visible(who, table, id) {
				continue
			}
			_ = client.write([]any{nil, nil, sub.topic, "postgres_changes", map[string]any{"ids": []string{sub.id}, "data": map[string]any{"schema": "public", "table": table, "type": operation, "record": map[string]string{"id": id}}}})
		}
	}
}

func (h *realtimeHub) visible(who principal, relation, id string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	visible := false
	err := h.server.transaction(ctx, who, func(tx pgx.Tx) error {
		table, err := loadTable(ctx, tx, "public", relation)
		if err != nil {
			return err
		}
		column, err := table.column("id")
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table.identifier()+" WHERE "+pgx.Identifier{column.name}.Sanitize()+"="+column.cast("$1")+")", id).Scan(&visible)
	})
	return err == nil && visible
}

func (h *realtimeHub) Close() {
	h.cancel()
	h.mu.Lock()
	h.closed = true
	for client := range h.clients {
		_ = client.conn.Close()
	}
	h.clients = make(map[*realtimeClient]struct{})
	h.mu.Unlock()
}
