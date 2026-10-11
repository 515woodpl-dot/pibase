package gateway

import (
	"encoding/json"
	"golang.org/x/crypto/bcrypt"
	"net/http"
	"testing"
)

func TestInventorySessionScopesAndPasswordChange(t *testing.T) {
	e := newAuditEnv(t, nil)
	user := e.createUser("inventory-sessions@example.test")
	a := e.login("inventory-sessions@example.test")
	b := e.login("inventory-sessions@example.test")
	c := e.login("inventory-sessions@example.test")
	if got := e.status("POST", "/auth/v1/logout?scope=invalid", a.Access, `{}`); got != 400 {
		t.Fatalf("invalid scope = %d", got)
	}
	if got := e.status("POST", "/auth/v1/logout?scope=others", a.Access, `{}`); got != 204 {
		t.Fatalf("others logout = %d", got)
	}
	for _, token := range []string{b.Access, c.Access} {
		if got := e.status("GET", "/auth/v1/user", token, ""); got != 401 {
			t.Fatalf("other session survived: %d", got)
		}
	}
	if got := e.status("GET", "/auth/v1/user", a.Access, ""); got != 200 {
		t.Fatalf("kept session ended: %d", got)
	}
	b = e.login("inventory-sessions@example.test")
	if got := e.status("PATCH", "/auth/v1/user", a.Access, `{"password":"unverified change"}`); got != 400 {
		t.Fatalf("password update without current password = %d", got)
	}
	if got := e.status("POST", "/auth/v1/user/password", a.Access, `{"current_password":"wrong","new_password":"new inventory password"}`); got != 400 {
		t.Fatalf("wrong current password = %d", got)
	}
	if got := e.status("POST", "/auth/v1/user/password", a.Access, `{"current_password":"audit user password","new_password":"new inventory password"}`); got != 204 {
		t.Fatalf("change password = %d", got)
	}
	if got := e.status("GET", "/auth/v1/user", b.Access, ""); got != 401 {
		t.Fatalf("other session survived password change: %d", got)
	}
	if got := e.status("GET", "/auth/v1/user", a.Access, ""); got != 200 {
		t.Fatalf("current session ended: %d", got)
	}
	if got := e.status("POST", "/auth/v1/token?grant_type=password", "", `{"email":"inventory-sessions@example.test","password":"audit user password"}`); got != 400 {
		t.Fatalf("old password still works: %d", got)
	}
	if got := e.status("POST", "/auth/v1/token?grant_type=password", "", `{"email":"inventory-sessions@example.test","password":"new inventory password"}`); got != 200 {
		t.Fatalf("new password refused: %d", got)
	}
	requestAuthJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/admin/users/"+user+"/logout", e.p.ServiceKey, `{}`, 204)
	if got := e.status("GET", "/auth/v1/user", a.Access, ""); got != 401 {
		t.Fatalf("admin logout left session alive: %d", got)
	}
	// Global logout should end both the caller and another newly signed-in device.
	newA := requestAuthJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/token?grant_type=password", e.p.AnonKey, `{"email":"inventory-sessions@example.test","password":"new inventory password"}`, 200)
	newB := requestAuthJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/token?grant_type=password", e.p.AnonKey, `{"email":"inventory-sessions@example.test","password":"new inventory password"}`, 200)
	first, second := parseTokens(t, newA), parseTokens(t, newB)
	if got := e.status("POST", "/auth/v1/logout?scope=global", first.Access, `{}`); got != 204 {
		t.Fatalf("global logout = %d", got)
	}
	if got := e.status("GET", "/auth/v1/user", second.Access, ""); got != 401 {
		t.Fatalf("global logout left second session alive: %d", got)
	}
}

func TestInventoryAdminPasswordImportEndsSessions(t *testing.T) {
	e := newAuditEnv(t, nil)
	id := e.createUser("inventory-import@example.test")
	before := e.login("inventory-import@example.test")
	hash, err := bcrypt.GenerateFromPassword([]byte("imported password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"password_hash": string(hash)})
	requestAuthJSON(t, e.h.Client(), http.MethodPut, e.h.URL+"/auth/v1/admin/users/"+id, e.p.ServiceKey, string(body), 200)
	if got := e.status("GET", "/auth/v1/user", before.Access, ""); got != 401 {
		t.Fatalf("admin reset left session alive: %d", got)
	}
	if got := e.status("POST", "/auth/v1/token?grant_type=password", "", `{"email":"inventory-import@example.test","password":"imported password"}`); got != 200 {
		t.Fatalf("imported bcrypt login = %d", got)
	}
}
