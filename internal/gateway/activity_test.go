package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestObserveActivityCountsStatusClasses(t *testing.T) {
	s := &Server{activity: newActivityRecorder()}
	h := s.observeActivity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noteActivityProject(r.Context(), "abcd1234")
		switch r.URL.Path {
		case "/rest/v1/boom":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/storage/v1/bad":
			w.WriteHeader(http.StatusBadRequest)
			w.WriteHeader(http.StatusOK) // later calls must not change the class
		default:
			_, _ = w.Write([]byte("ok")) // implicit 200
		}
	}))
	for _, path := range []string{"/rest/v1/boom", "/storage/v1/bad", "/auth/v1/user", "/healthz"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	got := s.activity.take()
	minute := time.Now().Unix() / 60 * 60
	want := map[activityKey]int64{
		{"abcd1234", minute, "database", 5}: 1,
		{"abcd1234", minute, "storage", 4}:  1,
		{"abcd1234", minute, "auth", 2}:     1,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, n := range want {
		if got[k] != n {
			t.Fatalf("%+v: got %d, want %d (all: %v)", k, got[k], n, got)
		}
	}
}

func TestActivityRestoreKeepsCountsForRetry(t *testing.T) {
	a := newActivityRecorder()
	now := time.Now()
	a.add("abcd1234", "database", 200, now)
	pending := a.take()
	a.add("abcd1234", "database", 200, now)
	a.restore(pending)
	for _, n := range a.take() {
		if n != 2 {
			t.Fatalf("got %d, want 2", n)
		}
	}
	a.add("", "database", 200, now)
	a.add("abcd1234", "", 200, now)
	a.add("abcd1234", "database", 0, now)
	if len(a.take()) != 0 {
		t.Fatal("unattributed requests were counted")
	}
}
