package gateway

import "testing"

func TestRealtimeRejectsDeleteSubscription(t *testing.T) {
	for _, event := range []string{"DELETE", "*", ""} {
		client := &realtimeClient{}
		var join realtimeJoin
		join.Config.PostgresChanges = append(join.Config.PostgresChanges, struct {
		ID     string `json:"id"`
		Event  string `json:"event"`
		Schema string `json:"schema"`
		Table  string `json:"table"`
		Filter string `json:"filter"`
		}{ID: "unsupported", Event: event, Schema: "public", Table: "todos"})
		if _, err := client.join("realtime:unsupported", join); err == nil {
			t.Fatalf("%q subscription was accepted", event)
		}
	}
}
