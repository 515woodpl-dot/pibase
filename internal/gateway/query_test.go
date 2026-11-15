package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A raw ";" makes Go drop that query segment, which for the data API would
// remove a filter and widen a read, update or delete. Such requests must be
// refused before any handler sees them; an encoded ";" stays a plain value.
func TestMalformedQueryStringIsRefused(t *testing.T) {
	h := (&Server{}).Handler()
	for _, target := range []string{
		"/rest/v1/clients?name=eq.nothing;x",
		"/rest/v1/clients?select=name&user_id=eq.abc;x",
		"/rest/v1/clients?id=eq.%zz",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest(method, target, strings.NewReader("{}")))
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "query string is malformed") {
				t.Fatalf("%s %s: status %d body %s, want 400 malformed query", method, target, w.Code, w.Body.String())
			}
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/rest/v1/clients?name=eq.nothing%3Bx", nil))
	if w.Code == http.StatusBadRequest && strings.Contains(w.Body.String(), "query string is malformed") {
		t.Fatalf("an encoded ';' was refused: %s", w.Body.String())
	}
}
