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
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "pibase operator") {
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
	for _, required := range []string{"--ink:#333", "-webkit-tap-highlight-color:transparent", ":focus-visible", "chain.scrollLeft=chain.scrollWidth", "textContent"} {
		if !strings.Contains(page, required) {
			t.Fatalf("dashboard misses %q", required)
		}
	}
}
