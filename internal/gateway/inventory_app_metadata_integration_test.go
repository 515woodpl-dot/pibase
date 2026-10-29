package gateway

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestServerOnlyAppMetadata(t *testing.T) {
	e := newAuditEnv(t, nil)
	id := e.createUser("app-meta@example.test")
	requestAuthJSON(t, e.h.Client(), http.MethodPut, e.h.URL+"/auth/v1/admin/users/"+id,
		e.p.ServiceKey, `{"app_metadata":{"role":"office"}}`, 200)
	tokens := e.login("app-meta@example.test")
	user := requestAuthJSONBearer(t, e.h.Client(), http.MethodGet, e.h.URL+"/auth/v1/user", e.p.AnonKey, tokens.Access, "", 200)
	if !strings.Contains(string(user), `"role":"office"`) {
		t.Fatalf("admin metadata missing: %s", user)
	}
	requestAuthJSONBearer(t, e.h.Client(), http.MethodPatch, e.h.URL+"/auth/v1/user", e.p.AnonKey,
		tokens.Access, `{"app_metadata":{"role":"admin"},"user_metadata":{"name":"Crew"}}`, 200)
	user = requestAuthJSONBearer(t, e.h.Client(), http.MethodGet, e.h.URL+"/auth/v1/user", e.p.AnonKey, tokens.Access, "", 200)
	if !strings.Contains(string(user), `"role":"office"`) || strings.Contains(string(user), `"role":"admin"`) {
		t.Fatalf("client modified app metadata: %s", user)
	}
	parts := strings.Split(tokens.Access, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["app_metadata"].(map[string]any)["role"] != "office" {
		t.Fatalf("JWT app metadata: %s", payload)
	}
	// Demoting a role must invalidate a JWT that still says "office".
	requestAuthJSON(t, e.h.Client(), http.MethodPut, e.h.URL+"/auth/v1/admin/users/"+id,
		e.p.ServiceKey, `{"app_metadata":{"role":"viewer"}}`, 200)
	requestAuthJSONBearer(t, e.h.Client(), http.MethodGet, e.h.URL+"/auth/v1/user", e.p.AnonKey,
		tokens.Access, "", http.StatusUnauthorized)
	fresh := e.login("app-meta@example.test")
	user = requestAuthJSONBearer(t, e.h.Client(), http.MethodGet, e.h.URL+"/auth/v1/user", e.p.AnonKey,
		fresh.Access, "", 200)
	if !strings.Contains(string(user), `"role":"viewer"`) {
		t.Fatalf("new session did not receive updated role: %s", user)
	}
}
