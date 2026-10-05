package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicGatewayDoesNotServeOperatorRoutes(t *testing.T) {
	h := (&Server{}).Handler()
	for _, path := range []string{"/dashboard", "/_pibase/projects"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: got %d want 404", path, w.Code)
		}
	}
}
