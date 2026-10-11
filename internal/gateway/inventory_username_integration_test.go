package gateway

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestUsernameOnlySignIn(t *testing.T) {
	e := newAuditEnv(t, nil)
	created := requestAuthJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/admin/users", e.p.ServiceKey,
		`{"username":"Crew.Lead","password":"username password"}`, 201)
	var result struct {
		User struct {
			ID, Email, Username string
			EmailConfirmedAt    any `json:"email_confirmed_at"`
		} `json:"user"`
	}
	if err := json.Unmarshal(created, &result); err != nil {
		t.Fatal(err)
	}
	if result.User.ID == "" || result.User.Email != "" || result.User.Username != "crew.lead" {
		t.Fatalf("username user: %s", created)
	}
	login := requestAuthJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/token?grant_type=password", e.p.AnonKey,
		`{"username":"Crew.Lead","password":"username password"}`, 200)
	tokens := parseTokens(t, login)
	user := requestAuthJSONBearer(t, e.h.Client(), http.MethodGet, e.h.URL+"/auth/v1/user", e.p.AnonKey, tokens.Access, "", 200)
	if err := json.Unmarshal(user, &result.User); err != nil {
		t.Fatal(err)
	}
	if result.User.Username != "crew.lead" {
		t.Fatalf("username missing from user: %s", user)
	}
}
