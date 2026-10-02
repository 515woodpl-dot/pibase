package gateway

import (
	"encoding/json"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

const (
	maxRealtimeNameBytes    = 128
	maxRealtimePayloadBytes = 16 << 10
	maxRealtimeEventsSecond = 60
)

type realtimeTopic struct {
	topic                            string
	broadcastSelf, broadcastAck      bool
	presenceEnabled, presenceTracked bool
	presenceKey, presenceRef         string
	presenceMeta                     map[string]any
}

type realtimePresence struct {
	topic, key, ref string
	meta            map[string]any
}

type realtimeEventPayload struct {
	Type    string          `json:"type"`
	Event   string          `json:"event"`
	Payload json.RawMessage `json:"payload"`
}

func decodeRealtimeFrame(kind int, message []byte) (realtimeFrame, error) {
	if kind == websocket.TextMessage {
		var raw []json.RawMessage
		if json.Unmarshal(message, &raw) != nil || len(raw) != 5 {
			return realtimeFrame{}, fmt.Errorf("invalid JSON frame")
		}
		var frame realtimeFrame
		if json.Unmarshal(raw[0], &frame.joinRef) != nil || json.Unmarshal(raw[1], &frame.ref) != nil || json.Unmarshal(raw[2], &frame.topic) != nil || json.Unmarshal(raw[3], &frame.event) != nil {
			return realtimeFrame{}, fmt.Errorf("invalid frame fields")
		}
		frame.payload = raw[4]
		return frame, nil
	}
	if kind != websocket.BinaryMessage || len(message) < 7 || message[0] != 3 {
		return realtimeFrame{}, fmt.Errorf("unsupported binary frame")
	}
	joinSize, refSize, topicSize := int(message[1]), int(message[2]), int(message[3])
	eventSize, metadataSize, encoding := int(message[4]), int(message[5]), message[6]
	offset := 7
	end := offset + joinSize + refSize + topicSize + eventSize + metadataSize
	if end > len(message) || encoding != 1 {
		return realtimeFrame{}, fmt.Errorf("only JSON broadcast binary envelopes are supported")
	}
	read := func(size int) string {
		value := string(message[offset : offset+size])
		offset += size
		return value
	}
	frame := realtimeFrame{joinRef: read(joinSize), ref: read(refSize), topic: read(topicSize), event: "broadcast"}
	userEvent := read(eventSize)
	metadata := read(metadataSize)
	payload := json.RawMessage(message[end:])
	if !json.Valid(payload) || len(userEvent) == 0 {
		return realtimeFrame{}, fmt.Errorf("invalid binary broadcast payload")
	}
	wrapper := map[string]any{"type": "broadcast", "event": userEvent, "payload": payload}
	if metadata != "" {
		var values map[string]any
		if json.Unmarshal([]byte(metadata), &values) != nil {
			return realtimeFrame{}, fmt.Errorf("invalid binary broadcast metadata")
		}
		for key, value := range values {
			if key == "type" || key == "event" || key == "payload" {
				continue
			}
			wrapper[key] = value
		}
	}
	frame.payload, _ = json.Marshal(wrapper)
	return frame, nil
}

func validateRealtimeTopic(topic string) error {
	const prefix = "realtime:"
	if !utf8.ValidString(topic) || len(topic) <= len(prefix) || len(topic) > len(prefix)+maxRealtimeNameBytes || topic[:len(prefix)] != prefix {
		return fmt.Errorf("realtime topic must be 'realtime:' plus 1 to 128 bytes")
	}
	return nil
}

func (c *realtimeClient) allowRealtimeEvent() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.rateWindow.IsZero() || now.Sub(c.rateWindow) >= time.Second {
		c.rateWindow, c.rateEvents = now, 0
	}
	if c.rateEvents >= maxRealtimeEventsSecond {
		return false
	}
	c.rateEvents++
	return true
}

func (c *realtimeClient) handleBroadcast(frame realtimeFrame) error {
	if !c.allowRealtimeEvent() {
		return fmt.Errorf("realtime event rate limit reached")
	}
	var payload realtimeEventPayload
	if json.Unmarshal(frame.payload, &payload) != nil || payload.Type != "broadcast" || payload.Event == "" || !utf8.ValidString(payload.Event) || len(payload.Event) > maxRealtimeNameBytes || len(payload.Payload) > maxRealtimePayloadBytes || !json.Valid(payload.Payload) {
		return fmt.Errorf("invalid broadcast payload")
	}
	c.mu.Lock()
	channel := c.channels[frame.topic]
	c.mu.Unlock()
	if channel == nil {
		return fmt.Errorf("channel is not joined")
	}
	c.hub.broadcast(c, frame.topic, payload)
	if channel.broadcastAck {
		_ = c.reply(frame.joinRef, frame.ref, frame.topic, "ok", map[string]any{})
	}
	return nil
}

func (h *realtimeHub) broadcast(sender *realtimeClient, topic string, payload realtimeEventPayload) {
	h.mu.Lock()
	clients := make([]*realtimeClient, 0, len(h.clients))
	for client := range h.clients {
		if client.projectID == sender.projectID {
			clients = append(clients, client)
		}
	}
	h.mu.Unlock()
	for _, client := range clients {
		client.mu.Lock()
		joined := client.channels[topic] != nil
		self := client == sender && client.channels[topic] != nil && client.channels[topic].broadcastSelf
		client.mu.Unlock()
		if !joined || client == sender && !self {
			continue
		}
		_ = client.write([]any{nil, nil, topic, "broadcast", payload})
	}
}

func (c *realtimeClient) handlePresence(frame realtimeFrame) error {
	if !c.allowRealtimeEvent() {
		return fmt.Errorf("realtime event rate limit reached")
	}
	var payload realtimeEventPayload
	if json.Unmarshal(frame.payload, &payload) != nil || payload.Type != "presence" {
		return fmt.Errorf("invalid presence payload")
	}
	c.mu.Lock()
	channel := c.channels[frame.topic]
	c.mu.Unlock()
	if channel == nil {
		return fmt.Errorf("channel is not joined")
	}
	switch payload.Event {
	case "track":
		if len(payload.Payload) > maxRealtimePayloadBytes {
			return fmt.Errorf("presence payload exceeds 16 KiB")
		}
		var meta map[string]any
		if json.Unmarshal(payload.Payload, &meta) != nil || meta == nil {
			return fmt.Errorf("presence payload must be a JSON object")
		}
		previous, wasTracked := c.takePresence(frame.topic)
		presence := realtimePresence{topic: frame.topic, key: channel.presenceKey, ref: realtimeID(), meta: meta}
		c.mu.Lock()
		if current := c.channels[frame.topic]; current != nil {
			current.presenceTracked, current.presenceRef, current.presenceMeta = true, presence.ref, meta
		}
		c.mu.Unlock()
		if wasTracked {
			c.hub.presenceDiff(c.projectID, frame.topic, nil, &previous)
		}
		c.hub.presenceDiff(c.projectID, frame.topic, &presence, nil)
	case "untrack":
		presence, ok := c.takePresence(frame.topic)
		if ok {
			c.hub.presenceDiff(c.projectID, frame.topic, nil, &presence)
		}
	default:
		return fmt.Errorf("presence event must be track or untrack")
	}
	_ = c.reply(frame.joinRef, frame.ref, frame.topic, "ok", map[string]any{})
	return nil
}

func presenceMeta(p realtimePresence) map[string]any {
	meta := make(map[string]any, len(p.meta)+1)
	for key, value := range p.meta {
		meta[key] = value
	}
	meta["phx_ref"] = p.ref
	return meta
}

func (h *realtimeHub) presenceState(projectID, topic string) map[string]any {
	h.mu.Lock()
	clients := make([]*realtimeClient, 0, len(h.clients))
	for client := range h.clients {
		if client.projectID == projectID {
			clients = append(clients, client)
		}
	}
	h.mu.Unlock()
	state := make(map[string]any)
	for _, client := range clients {
		client.mu.Lock()
		channel := client.channels[topic]
		if channel != nil && channel.presenceTracked {
			key := channel.presenceKey
			entry, _ := state[key].(map[string]any)
			if entry == nil {
				entry = map[string]any{"metas": []map[string]any{}}
			}
			entry["metas"] = append(entry["metas"].([]map[string]any), presenceMeta(realtimePresence{key: key, ref: channel.presenceRef, meta: channel.presenceMeta}))
			state[key] = entry
		}
		client.mu.Unlock()
	}
	return state
}

func (h *realtimeHub) presenceDiff(projectID, topic string, join, leave *realtimePresence) {
	joins, leaves := map[string]any{}, map[string]any{}
	if join != nil {
		joins[join.key] = map[string]any{"metas": []map[string]any{presenceMeta(*join)}}
	}
	if leave != nil {
		leaves[leave.key] = map[string]any{"metas": []map[string]any{presenceMeta(*leave)}}
	}
	h.mu.Lock()
	clients := make([]*realtimeClient, 0, len(h.clients))
	for client := range h.clients {
		if client.projectID == projectID {
			clients = append(clients, client)
		}
	}
	h.mu.Unlock()
	for _, client := range clients {
		client.mu.Lock()
		channel := client.channels[topic]
		enabled := channel != nil && channel.presenceEnabled
		client.mu.Unlock()
		if enabled {
			_ = client.write([]any{nil, nil, topic, "presence_diff", map[string]any{"joins": joins, "leaves": leaves}})
		}
	}
}

func (c *realtimeClient) takePresence(topic string) (realtimePresence, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	channel := c.channels[topic]
	if channel == nil || !channel.presenceTracked {
		return realtimePresence{}, false
	}
	presence := realtimePresence{topic: topic, key: channel.presenceKey, ref: channel.presenceRef, meta: channel.presenceMeta}
	channel.presenceTracked, channel.presenceRef, channel.presenceMeta = false, "", nil
	return presence, true
}

func (c *realtimeClient) trackedPresences() []realtimePresence {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result []realtimePresence
	for _, channel := range c.channels {
		if channel.presenceTracked {
			result = append(result, realtimePresence{topic: channel.topic, key: channel.presenceKey, ref: channel.presenceRef, meta: channel.presenceMeta})
		}
	}
	return result
}

func (h *realtimeHub) leaveChannel(client *realtimeClient, topic string) {
	presence, tracked := client.takePresence(topic)
	client.mu.Lock()
	delete(client.channels, topic)
	kept := client.subscriptions[:0]
	for _, subscription := range client.subscriptions {
		if subscription.topic != topic {
			kept = append(kept, subscription)
		}
	}
	client.subscriptions = kept
	client.mu.Unlock()
	if tracked {
		h.presenceDiff(client.projectID, topic, nil, &presence)
	}
}
