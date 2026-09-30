package gateway

import (
	"net/http/httptest"
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

func TestAuthRateWindow(t *testing.T) {
	server := &Server{authRates: make(map[string]rateWindow)}
	who := project{id: "project"}
	r := httptest.NewRequest("POST", "/auth/v1/token", nil)
	r.RemoteAddr = "192.0.2.10:1234"
	for i := 0; i < 10; i++ {
		if err := server.enforceAuthRate(who, r, "user@example.test"); err != nil {
			t.Fatalf("request %d was rejected: %v", i+1, err)
		}
	}
	if err := server.enforceAuthRate(who, r, "user@example.test"); err == nil {
		t.Fatal("11th authentication request was accepted")
	}
}
