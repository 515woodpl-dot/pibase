package adminserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEveryAdminEndpointRejectsMissingToken(t *testing.T) {
	s := &Server{Config: Config{Token: "operator"}}
	for _, path := range []string{"/_pibase/projects", "/_pibase/projects/x/backups", "/_pibase/anything", "/other"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: got %d", path, w.Code)
		}
	}
}

func TestDashboardPageLoadsWithoutTokenAndHoldsNoSecrets(t *testing.T) {
	s := &Server{Config: Config{Token: "operator-secret-token"}}
	for _, path := range []string{"/", "/dashboard"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "pibase") {
			t.Fatalf("%s: got %d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "operator-secret-token") || strings.Contains(w.Body.String(), "innerHTML") {
			t.Fatalf("%s: page leaks the token or uses innerHTML", path)
		}
	}
	// Writes never bypass the token, even on the page path.
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(method, "/dashboard", nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s /dashboard: got %d", method, w.Code)
		}
	}
}

func TestDashboardUIHardRules(t *testing.T) {
	for _, forbidden := range []string{"alert(", "confirm(", "prompt(", "<dialog", "transition", "animation", "scroll-behavior: smooth", "innerHTML"} {
		if strings.Contains(page, forbidden) {
			t.Fatalf("dashboard contains forbidden %q", forbidden)
		}
	}
	for _, required := range []string{"--ink:#333", "-webkit-tap-highlight-color:transparent", ":focus-visible", "function alignChain()", "chain.scrollLeft=x", "function dataTable(", "textContent"} {
		if !strings.Contains(page, required) {
			t.Fatalf("dashboard misses %q", required)
		}
	}
}

func TestDashboardShowsKeysOnceAndWiresEveryEndpoint(t *testing.T) {
	for _, required := range []string{
		"Shown once; copy them now.",
		"'project id: '+x.id",
		"ak.textContent='anon key: '+x.anon_key",
		"sk.textContent='service key: '+x.service_key",
		"'/_pibase/projects'",
		"'/tables'", "'/tables/'+encodeURIComponent(t.name)+'/rows?offset='",
		"'/users'", "'/sql'", "'/migrations'", "'/backup'", "'/backups'", "'/extensions/postgis'", "'/extensions/pgvector'",
	} {
		if !strings.Contains(page, required) {
			t.Errorf("dashboard page misses %q", required)
		}
	}
	if strings.Contains(page, "localStorage") || strings.Contains(page, "sessionStorage") {
		t.Error("dashboard must not store keys or the token in browser storage")
	}
}

func TestClientIPIgnoresSpoofedForwardedFor(t *testing.T) {
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.9:4000", "1.2.3.4", "203.0.113.9"},
		{"127.0.0.1:4000", "1.2.3.4, 198.51.100.7", "198.51.100.7"},
		{"[::1]:4000", "198.51.100.8", "198.51.100.8"},
		{"127.0.0.1:4000", "", "127.0.0.1"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("clientIP(%q, %q) = %q, want %q", c.remote, c.xff, got, c.want)
		}
	}
}
