package gateway

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestSignInAuditRecordsSuccessAndFailure(t *testing.T) {
	e := newAuditEnv(t, nil)
	e.createUser("audit-login@example.test")
	requestAuthJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/token?grant_type=password", e.p.AnonKey,
		`{"email":"audit-login@example.test","password":"wrong"}`, 400)
	e.login("audit-login@example.test")
	if got := e.status("GET", "/auth/v1/admin/signin-events", "", ""); got != 403 {
		t.Fatalf("anon audit read = %d", got)
	}
	body := requestAuthJSON(t, e.h.Client(), http.MethodGet, e.h.URL+"/auth/v1/admin/signin-events?limit=2", e.p.ServiceKey, "", 200)
	var result struct {
		Events []struct {
			Success bool   `json:"success"`
			Reason  string `json:"reason"`
			Email   string `json:"email"`
		} `json:"events"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 2 || !result.Events[0].Success || result.Events[0].Reason != "success" || result.Events[1].Reason != "invalid_credentials" {
		t.Fatalf("audit events: %s", body)
	}
}
