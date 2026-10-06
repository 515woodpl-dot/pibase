package gateway

import (
	"fmt"
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

func TestAuthRateRefreshSubjectsDoNotShareLimit(t *testing.T) {
	server := &Server{rates: make(map[string]rateWindow), authRates: make(map[string]rateWindow)}
	who := project{id: "project"}
	for i := 0; i < 20; i++ {
		if err := server.enforceRefreshRate(who, fmt.Sprintf("token-%d", i)); err != nil {
			t.Fatalf("distinct refresh subject %d was rejected: %v", i, err)
		}
	}
	if len(server.authRates) != 20 {
		t.Fatalf("refresh requests unexpectedly share a source bucket: %d keys", len(server.authRates))
	}
}

func TestAuthRateIgnoresUntrustedForwardedForAndBoundsState(t *testing.T) {
	server := &Server{authRates: make(map[string]rateWindow)}
	who := project{id: "project"}
	r := httptest.NewRequest("POST", "/auth/v1/token", nil)
	r.RemoteAddr = "192.0.2.10:1234"
	for i := 0; i < 120; i++ {
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		if err := server.enforceAuthRate(who, r, fmt.Sprintf("user-%d@example.test", i)); err != nil {
			t.Fatalf("request %d was rejected: %v", i, err)
		}
	}
	if err := server.enforceAuthRate(who, r, "another@example.test"); err == nil {
		t.Fatal("spoofed X-Forwarded-For bypassed the source limit")
	}
	if len(server.authRates) != 121 {
		t.Fatalf("unexpected auth rate state: %d", len(server.authRates))
	}
	for key, window := range server.authRates {
		window.started = time.Now().Add(-2 * time.Minute)
		server.authRates[key] = window
	}
	server.lastAuthRateSweep = time.Now().Add(-2 * time.Minute)
	if err := server.enforceAuthRate(who, r, "fresh@example.test"); err != nil {
		t.Fatal(err)
	}
	if len(server.authRates) != 2 {
		t.Fatalf("expired auth rate state was not swept: %d", len(server.authRates))
	}
}
