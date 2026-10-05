package adminserver

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEveryAdminEndpointRejectsMissingToken(t *testing.T) {
	s := &Server{Config: Config{Token: "operator"}}
	for _, path := range []string{"/dashboard", "/_pibase/projects", "/_pibase/projects/x/sql", "/_pibase/projects/x/backup"} {
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: got %d", path, w.Code)
		}
	}
}
