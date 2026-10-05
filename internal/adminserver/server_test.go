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
