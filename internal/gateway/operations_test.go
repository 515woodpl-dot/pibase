package gateway

import (
	"net/http/httptest"
	"testing"
)

func TestOperatorAuthorizationRequiresBothConfigurationAndExactBearer(t *testing.T) {
	s := &Server{adminDatabaseURL: "postgres://example", operatorToken: "correct-token"}
	for _, test := range []struct {
		header string
		want   bool
	}{{"Bearer correct-token", true}, {"correct-token", false}, {"Bearer wrong-token", false}, {"", false}} {
		r := httptest.NewRequest("GET", "/_pibase/projects", nil)
		r.Header.Set("Authorization", test.header)
		if got := s.operatorAuthorized(r); got != test.want {
			t.Fatalf("header %q: got %v want %v", test.header, got, test.want)
		}
	}
	s.adminDatabaseURL = ""
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer correct-token")
	if s.operatorAuthorized(r) {
		t.Fatal("authorization enabled without admin database")
	}
}

func TestDashboardDisabledWithoutOperatorConfiguration(t *testing.T) {
	s := &Server{}
	request := httptest.NewRequest("GET", "/dashboard", nil)
	response := httptest.NewRecorder()
	s.dashboardHandler(response, request)
	if response.Code != 404 {
		t.Fatalf("got %d want 404", response.Code)
	}
}
