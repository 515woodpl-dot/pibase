package gateway

import (
	"testing"
	"time"
)

func TestPerProjectRateWindow(t *testing.T) {
	server := &Server{rates: make(map[string]rateWindow)}
	now := time.Unix(100, 0)
	for i := 0; i < 100; i++ {
		if !server.allowRequest("project", now) {
			t.Fatalf("request %d was rejected", i+1)
		}
	}
	if server.allowRequest("project", now) {
		t.Fatal("101st request in one second was accepted")
	}
	if !server.allowRequest("project", now.Add(time.Second)) {
		t.Fatal("new rate window did not open")
	}
}
