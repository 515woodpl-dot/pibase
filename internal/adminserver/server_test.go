package adminserver

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"
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

func TestPagesLoadWithoutTokenAndHoldNoSecrets(t *testing.T) {
	s := &Server{Config: Config{Token: "operator-secret-token"}}
	for _, path := range []string{"/", "/app", "/dashboard"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "pibase") {
			t.Fatalf("%s: got %d", path, w.Code)
		}
		if strings.Contains(w.Body.String(), "operator-secret-token") || strings.Contains(w.Body.String(), "innerHTML") {
			t.Fatalf("%s: page leaks the token or uses innerHTML", path)
		}
		if !strings.Contains(w.Header().Get("Content-Security-Policy"), "default-src 'none'") || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: missing security headers: %v", path, w.Header())
		}
	}
	// Uptime checkers send HEAD; it must not need a credential either.
	for _, path := range []string{"/", "/app"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodHead, path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("HEAD %s: got %d", path, w.Code)
		}
	}
	// Writes never bypass the token, even on the page paths.
	for _, path := range []string{"/", "/app", "/dashboard"} {
		for _, method := range []string{http.MethodPost, http.MethodDelete} {
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, httptest.NewRequest(method, path, nil))
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s: got %d", method, path, w.Code)
			}
		}
	}
}

func TestLandingPageIsALandingPageWithLoginAndSignup(t *testing.T) {
	s := &Server{Config: Config{Token: "operator"}}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	body := w.Body.String()
	for _, required := range []string{
		"<h1>Your own Postgres backend.",
		`id="form-login"`, `id="form-signup"`,
		`name="organization"`, `name="password_confirm"`,
		"The two passwords do not match.",
		"/_pibase/auth/login", "/_pibase/auth/signup", "password_confirm: again",
		`href="/app#operator"`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("landing page misses %q", required)
		}
	}
	if strings.Contains(body, "id=\"chain\"") {
		t.Error("landing page must not contain the dashboard")
	}
	for _, external := range []string{`src="http`, `href="http`, "//cdn", "googleapis"} {
		if strings.Contains(body, external) {
			t.Errorf("landing page loads something from another site: %q", external)
		}
	}
	// The app and the landing page are different documents.
	app := httptest.NewRecorder()
	s.Handler().ServeHTTP(app, httptest.NewRequest(http.MethodGet, "/app", nil))
	if !strings.Contains(app.Body.String(), `id="chain"`) || strings.Contains(app.Body.String(), `id="form-signup"`) {
		t.Error("/app must serve the dashboard, not the landing page")
	}
}

func TestPagesUIHardRules(t *testing.T) {
	for name, doc := range map[string]string{"dashboard": page, "landing": landingPage} {
		for _, forbidden := range []string{"alert(", "confirm(", "prompt(", "<dialog", "transition", "animation", "scroll-behavior: smooth", "innerHTML", "localStorage", "sessionStorage"} {
			if strings.Contains(doc, forbidden) {
				t.Fatalf("%s contains forbidden %q", name, forbidden)
			}
		}
		for _, required := range []string{"--ink:", "-webkit-tap-highlight-color:transparent", ":focus-visible", "textContent", `name="viewport"`, "@media(max-width:"} {
			if !strings.Contains(doc, required) {
				t.Fatalf("%s misses %q", name, required)
			}
		}
	}
	for _, required := range []string{"function alignChain()", "chain.scrollLeft=x", "function dataTable("} {
		if !strings.Contains(page, required) {
			t.Fatalf("dashboard misses %q", required)
		}
	}
}

// Every pane explains itself: pane(title, description) is never called with a
// title alone.
func TestEveryDashboardPaneHasADescription(t *testing.T) {
	for _, m := range regexp.MustCompile(`pane\((?:'(?:[^'\\]|\\.)*'|[^,()]+)\)`).FindAllString(page, -1) {
		t.Errorf("pane without a description: %s", m)
	}
	if n := strings.Count(page, "pane('") + strings.Count(page, "pane(x.name,") + strings.Count(page, "pane(t.name+"); n < 12 {
		t.Errorf("only %d described panes found; a pane may have been dropped", n)
	}
}

func TestDashboardWiresEveryEndpoint(t *testing.T) {
	for _, required := range []string{
		"Your project is ready. Copy these keys into your app, or come back to this page any time under API keys.",
		"'Project ID: '+x.id",
		"secretField('service_role · secret',k.service_key",
		"'/_pibase/projects'",
		"'/tables'", "'/tables/'+encodeURIComponent(t.name)+'/rows?offset='",
		"'/users'", "'/sql'", "'/migrations'", "'/backup'", "'/backups'", "'/extensions/postgis'", "'/extensions/pgvector'",
		"'/keys/rotate'", "'/usage'", "'/activity?window='", "'/keys'", "'/limits'", "'/_pibase/auth/password'",
		"'/_pibase/auth/account'", "'/_pibase/auth/organization'", "'/_pibase/auth/me'", "'/_pibase/auth/logout'",
	} {
		if !strings.Contains(page, required) {
			t.Errorf("dashboard page misses %q", required)
		}
	}
	// Collapsed by default: labels only show when the menu is open.
	for _, required := range []string{".nav-item .lbl{display:none}", ".nav.open .lbl{display:inline}", "function navOpen("} {
		if !strings.Contains(page, required) {
			t.Errorf("dashboard menu misses %q", required)
		}
	}
}

func TestClientIPIgnoresSpoofedForwardedFor(t *testing.T) {
	cases := []struct{ remote, xff, want string }{
		{"203.0.113.9:4000", "1.2.3.4", "203.0.113.9"},
		{"127.0.0.1:4000", "1.2.3.4, 198.51.100.7", "198.51.100.7"},
		{"[::1]:4000", "198.51.100.8", "198.51.100.8"},
		{"127.0.0.1:4000", "", "127.0.0.1"},
		{"127.0.0.1:4000", "garbage", "127.0.0.1"},
		// Docker-bridge proxy is trusted only once configured.
		{"172.18.0.5:4000", "198.51.100.9", "198.51.100.9"},
		{"172.18.0.5:4000", "6.6.6.6, 198.51.100.9, 172.18.0.7", "198.51.100.9"},
		{"172.19.0.5:4000", "198.51.100.9", "172.19.0.5"},
	}
	trusted, err := ParseTrustedProxies("172.18.0.0/16, 10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Config: Config{TrustedProxies: trusted}}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = c.remote
		if c.xff != "" {
			r.Header.Set("X-Forwarded-For", c.xff)
		}
		if got := s.clientIP(r); got != c.want {
			t.Errorf("clientIP(%q, %q) = %q, want %q", c.remote, c.xff, got, c.want)
		}
	}
}

func TestParseTrustedProxiesRejectsJunk(t *testing.T) {
	for _, v := range []string{"nope", "10.0.0.0/99", "1.2.3"} {
		if _, err := ParseTrustedProxies(v); err == nil {
			t.Errorf("ParseTrustedProxies(%q) accepted", v)
		}
	}
	if n, err := ParseTrustedProxies(""); err != nil || len(n) != 0 {
		t.Errorf("empty list: %v %v", n, err)
	}
}

func TestLoginFailureCounter(t *testing.T) {
	l := newRateLimiter()
	for i := 0; i < 3; i++ {
		l.Allow("a@x.test", maxLoginFailures, time.Minute)
	}
	if n := l.Count("a@x.test", time.Minute); n != 3 {
		t.Fatalf("count=%d", n)
	}
	if n := l.Count("a@x.test", time.Minute); n != 3 {
		t.Fatalf("Count must not record: %d", n)
	}
	l.Reset("a@x.test")
	if n := l.Count("a@x.test", time.Minute); n != 0 {
		t.Fatalf("after reset count=%d", n)
	}
}
