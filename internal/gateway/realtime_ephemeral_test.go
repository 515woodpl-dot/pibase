package gateway

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestDecodeRealtimeBinaryBroadcast(t *testing.T) {
	joinRef, ref, topic, event := "1", "2", "realtime:room", "cursor"
	payload := []byte(`{"x":12}`)
	header := []byte{3, byte(len(joinRef)), byte(len(ref)), byte(len(topic)), byte(len(event)), 0, 1}
	message := append(header, []byte(joinRef+ref+topic+event)...)
	message = append(message, payload...)
	frame, err := decodeRealtimeFrame(websocket.BinaryMessage, message)
	if err != nil {
		t.Fatal(err)
	}
	if frame.joinRef != joinRef || frame.ref != ref || frame.topic != topic || frame.event != "broadcast" {
		t.Fatalf("unexpected decoded frame: %#v", frame)
	}
	var decoded realtimeEventPayload
	if err := json.Unmarshal(frame.payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Event != event || string(decoded.Payload) != string(payload) {
		t.Fatalf("unexpected decoded payload: %#v", decoded)
	}
}

func TestDecodeRealtimeRejectsArbitraryBinaryPayload(t *testing.T) {
	message := []byte{3, 0, 0, 1, 1, 0, 0, 't', 'e', 0xff}
	if _, err := decodeRealtimeFrame(websocket.BinaryMessage, message); err == nil {
		t.Fatal("arbitrary binary broadcast payload was accepted")
	}
}

func TestRealtimeEventRateLimit(t *testing.T) {
	client := &realtimeClient{}
	for i := 0; i < maxRealtimeEventsSecond; i++ {
		if !client.allowRealtimeEvent() {
			t.Fatalf("event %d was rejected before limit", i)
		}
	}
	if client.allowRealtimeEvent() {
		t.Fatal("event above per-second limit was accepted")
	}
}

func TestRealtimeTopicValidation(t *testing.T) {
	for _, topic := range []string{"", "room", "realtime:", "realtime:" + strings.Repeat("x", 129)} {
		if validateRealtimeTopic(topic) == nil {
			t.Fatalf("invalid topic %q was accepted", topic)
		}
	}
	if err := validateRealtimeTopic("realtime:room"); err != nil {
		t.Fatal(err)
	}
}
