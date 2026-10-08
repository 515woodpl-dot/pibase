package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A browser sends a preflight before any request with non-simple headers and
// refuses the real request unless every header and the method are allowed.
// These are the headers the Supabase JavaScript clients send.
func TestCORSPreflightAllowsSupabaseClientHeaders(t *testing.T) {
	h := (&Server{}).Handler()
	cases := []struct {
		path, method string
		headers      []string
	}{
		{"/auth/v1/token?grant_type=password", "POST", []string{"apikey", "authorization", "content-type", "x-client-info", "x-supabase-api-version"}},
		{"/rest/v1/items", "POST", []string{"apikey", "authorization", "content-type", "content-profile", "prefer", "x-client-info"}},
		{"/rest/v1/items?select=*", "GET", []string{"apikey", "authorization", "accept-profile", "range", "x-retry-count"}},
		{"/storage/v1/object/files/a.txt", "POST", []string{"apikey", "authorization", "x-upsert", "cache-control", "x-metadata", "content-type"}},
		{"/storage/v1/object/files/a.txt", "PUT", []string{"apikey", "authorization", "x-upsert", "cache-control"}},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodOptions, c.path, nil)
		req.Header.Set("Origin", "https://example.com")
		req.Header.Set("Access-Control-Request-Method", c.method)
		req.Header.Set("Access-Control-Request-Headers", strings.Join(c.headers, ","))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s %s preflight: status %d", c.method, c.path, w.Code)
		}
		allowed := map[string]bool{}
		for _, name := range strings.Split(w.Header().Get("Access-Control-Allow-Headers"), ",") {
			allowed[strings.ToLower(strings.TrimSpace(name))] = true
		}
		for _, name := range c.headers {
			if !allowed[name] {
				t.Errorf("%s %s: header %q not allowed", c.method, c.path, name)
			}
		}
		if !strings.Contains(w.Header().Get("Access-Control-Allow-Methods"), c.method) {
			t.Errorf("%s %s: method not allowed: %q", c.method, c.path, w.Header().Get("Access-Control-Allow-Methods"))
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "*" || w.Header().Get("Access-Control-Max-Age") == "" {
			t.Errorf("%s %s: origin/max-age headers: %v", c.method, c.path, w.Header())
		}
	}
}
