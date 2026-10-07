package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// realtimeHub implements the bounded Phoenix protocol subset used by
// supabase-js. Database changes pass an RLS re-check; public broadcast and
// presence are explicitly untrusted, ephemeral application messages.
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
	projectID     string
	id            string
	apiKey        string
	mu            sync.Mutex
	subscriptions []realtimeSubscription
	channels      map[string]*realtimeTopic
	rateWindow    time.Time
	rateEvents    int
}

type realtimeSubscription struct {
	id, topic, schema, table, event string
}

type realtimeJoin struct {
	Config struct {
		Broadcast struct {
			Self bool `json:"self"`
			Ack  bool `json:"ack"`
		} `json:"broadcast"`
		Presence struct {
			Key     string `json:"key"`
			Enabled bool   `json:"enabled"`
		} `json:"presence"`
		Private         bool `json:"private"`
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

type realtimeFrame struct {
	joinRef, ref, topic, event string
	payload                    json.RawMessage
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
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }, ReadBufferSize: 4096, WriteBufferSize: 4096}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &realtimeClient{hub: h, conn: conn, principal: who, projectID: who.project.id, id: realtimeID(), apiKey: r.Header.Get("apikey"), channels: make(map[string]*realtimeTopic)}
	h.mu.Lock()
	count := 0
	for existing := range h.clients {
		if existing.projectID == who.project.id {
			count++
		}
	}
	if h.closed || count >= h.maxPerProject {
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
		kind, message, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		frame, err := decodeRealtimeFrame(kind, message)
		if err != nil {
			_ = c.write([]any{nil, nil, "phoenix", "phx_error", map[string]string{"reason": "invalid realtime frame"}})
			continue
		}
		if frame.event == "heartbeat" {
			_ = c.reply(frame.joinRef, frame.ref, frame.topic, "ok", map[string]any{})
			continue
		}
		if frame.event == "phx_leave" {
			c.hub.leaveChannel(c, frame.topic)
			_ = c.reply(frame.joinRef, frame.ref, frame.topic, "ok", map[string]any{})
			continue
		}
		if frame.event == "broadcast" {
			if err := c.handleBroadcast(frame); err != nil {
				_ = c.reply(frame.joinRef, frame.ref, frame.topic, "error", map[string]string{"reason": err.Error()})
			}
			continue
		}
		if frame.event == "presence" {
			if err := c.handlePresence(frame); err != nil {
				_ = c.reply(frame.joinRef, frame.ref, frame.topic, "error", map[string]string{"reason": err.Error()})
			}
			continue
		}
		if frame.event != "phx_join" {
			_ = c.reply(frame.joinRef, frame.ref, frame.topic, "error", map[string]string{"reason": "unsupported realtime event"})
			continue
		}
		var join realtimeJoin
		if err := json.Unmarshal(frame.payload, &join); err != nil {
			_ = c.reply(frame.joinRef, frame.ref, frame.topic, "error", map[string]string{"reason": "invalid join payload"})
			continue
		}
		if join.AccessToken != "" {
			// Access tokens are supplied in the channel join by supabase-js.
			req := realtimeTokenRequest(c.apiKey, join.AccessToken)
			who, err := c.hub.server.authenticate(context.Background(), req)
			if err != nil || who.project.id != c.projectID {
				_ = c.reply(frame.joinRef, frame.ref, frame.topic, "error", map[string]string{"reason": "invalid access token"})
				continue
			}
			c.mu.Lock()
			c.principal = who
			c.mu.Unlock()
		}
		subs, state, err := c.join(frame.topic, join)
		if err != nil {
			_ = c.reply(frame.joinRef, frame.ref, frame.topic, "error", map[string]string{"reason": err.Error()})
			continue
		}
		ids := make([]map[string]string, 0, len(subs))
		for _, sub := range subs {
			ids = append(ids, map[string]string{"id": sub.id})
		}
		_ = c.reply(frame.joinRef, frame.ref, frame.topic, "ok", map[string]any{"postgres_changes": ids})
		if state != nil {
			_ = c.write([]any{frame.joinRef, nil, frame.topic, "presence_state", state})
		}
	}
}

func realtimeTokenRequest(apiKey, token string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "http://realtime.invalid/", nil)
	r.Header.Set("apikey", apiKey)
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

func (c *realtimeClient) join(topic string, join realtimeJoin) ([]realtimeSubscription, map[string]any, error) {
	if err := validateRealtimeTopic(topic); err != nil {
		return nil, nil, err
	}
	if join.Config.Private {
		return nil, nil, fmt.Errorf("private realtime channels are not supported")
	}
	if len(join.Config.PostgresChanges) > 20 {
		return nil, nil, fmt.Errorf("postgres_changes allows at most 20 subscriptions")
	}
	if len(join.Config.Presence.Key) > 128 {
		return nil, nil, fmt.Errorf("presence key exceeds 128 bytes")
	}
	subs := make([]realtimeSubscription, 0, len(join.Config.PostgresChanges))
	for i, change := range join.Config.PostgresChanges {
		if change.Schema != "public" || change.Table == "" || change.Filter != "" {
			return nil, nil, fmt.Errorf("only unfiltered public-table changes are supported")
		}
		event := change.Event
		// DELETE cannot be safely delivered from a NOTIFY-only feed: once the
		// transaction commits there is no row left on which to re-run RLS. Keep
		// that limitation explicit instead of accepting a subscription that can
		// never receive an event.
		if event == "DELETE" || event == "*" || event == "" {
			return nil, nil, fmt.Errorf("only INSERT and UPDATE postgres_changes are supported; DELETE and wildcard subscriptions are unavailable")
		}
		if event != "INSERT" && event != "UPDATE" {
			return nil, nil, fmt.Errorf("invalid postgres_changes event")
		}
		id := change.ID
		if id == "" {
			id = strconv.Itoa(i + 1)
		}
		subs = append(subs, realtimeSubscription{id: id, topic: topic, schema: change.Schema, table: change.Table, event: event})
	}
	c.mu.Lock()
	if _, exists := c.channels[topic]; !exists && len(c.channels) >= 20 {
		c.mu.Unlock()
		return nil, nil, fmt.Errorf("realtime topic limit reached")
	}
	c.mu.Unlock()
	previousPresence, wasTracked := c.takePresence(topic)
	c.mu.Lock()
	// A Phoenix topic can be rejoined after a reconnect. Replace, rather than
	// append, its subscriptions so a client cannot receive duplicate events.
	kept := c.subscriptions[:0]
	for _, existing := range c.subscriptions {
		if existing.topic != topic {
			kept = append(kept, existing)
		}
	}
	c.subscriptions = append(kept, subs...)
	presenceKey := join.Config.Presence.Key
	if presenceKey == "" {
		presenceKey = c.id
	}
	c.channels[topic] = &realtimeTopic{topic: topic, broadcastSelf: join.Config.Broadcast.Self, broadcastAck: join.Config.Broadcast.Ack, presenceEnabled: join.Config.Presence.Enabled, presenceKey: presenceKey}
	project := c.principal.project
	c.mu.Unlock()
	if wasTracked {
		c.hub.presenceDiff(c.projectID, topic, nil, &previousPresence)
	}
	if len(subs) > 0 {
		c.hub.ensureListener(project)
	}
	if join.Config.Presence.Enabled {
		return subs, c.hub.presenceState(c.projectID, topic), nil
	}
	return subs, nil, nil
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
	leaves := client.trackedPresences()
	h.mu.Lock()
	delete(h.clients, client)
	h.mu.Unlock()
	_ = client.conn.Close()
	for _, leave := range leaves {
		h.presenceDiff(client.projectID, leave.topic, nil, &leave)
	}
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
	for {
		if h.ctx.Err() != nil {
			return
		}
		config, err := pgx.ParseConfig(h.server.databaseURL)
		if err == nil {
			config.Database = p.database
			conn, connectErr := pgx.ConnectConfig(h.ctx, config)
			if connectErr == nil {
				err = func() error {
					defer conn.Close(h.ctx)
					if _, err := conn.Exec(h.ctx, "LISTEN "+pgx.Identifier{realtimeChannel}.Sanitize()); err != nil {
						return err
					}
					for {
						notification, err := conn.WaitForNotification(h.ctx)
						if err != nil {
							return err
						}
						var change struct{ Table, Op, ID string }
						if json.Unmarshal([]byte(notification.Payload), &change) == nil && change.Table != "" && change.ID != "" {
							go h.deliver(p, change.Table, change.Op, change.ID)
						}
					}
				}()
			}
		}
		if h.ctx.Err() != nil {
			return
		}
		// A transient database restart must not leave existing subscriptions
		// permanently inert.
		select {
		case <-h.ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func (h *realtimeHub) deliver(p *project, table, operation, id string) {
	if operation == "DELETE" { // no row exists to RLS-check without leaking OLD.
		return
	}
	h.mu.Lock()
	var clients []*realtimeClient
	for client := range h.clients {
		if client.projectID == p.id {
			clients = append(clients, client)
		}
	}
	h.mu.Unlock()
	for _, client := range clients {
		client.mu.Lock()
		subs := append([]realtimeSubscription(nil), client.subscriptions...)
		who := client.principal
		client.mu.Unlock()
		record, ok := h.visibleRecord(who, table, id)
		for _, sub := range subs {
			if sub.table != table || (sub.event != "*" && sub.event != operation) || !ok {
				continue
			}
			_ = client.write([]any{nil, nil, sub.topic, "postgres_changes", map[string]any{"ids": []string{sub.id}, "data": map[string]any{"schema": "public", "table": table, "type": operation, "record": record}}})
		}
	}
}

func realtimeID() string {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return fmt.Sprintf("socket-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value)
}

// visibleRecord fetches the row inside the subscriber's database transaction.
// Selecting it is the RLS check; never deliver data merely because a trigger
// fired. Deletes deliberately do not reach this point because their row can no
// longer be rechecked without retaining a potentially stale copy.
func (h *realtimeHub) visibleRecord(who principal, relation, id string) (json.RawMessage, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var record json.RawMessage
	err := h.server.transaction(ctx, who, func(tx pgx.Tx) error {
		table, err := loadTable(ctx, tx, "public", relation)
		if err != nil {
			return err
		}
		column, err := table.column("id")
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, "SELECT row_to_json(t)::jsonb FROM "+table.identifier()+" AS t WHERE "+pgx.Identifier{column.name}.Sanitize()+"="+column.cast("$1"), id).Scan(&record)
	})
	return record, err == nil
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
