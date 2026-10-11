package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHttpOnlyCookieSession(t *testing.T) {
	t.Setenv("PIBASE_COOKIE_ORIGINS", "https://app.example.test")
	e := newAuditEnv(t, nil)
	e.createUser("cookie-user@example.test")
	jar, _ := cookiejar.New(nil)
	client := e.h.Client()
	client.Jar = jar
	call := func(path, body, bearer string, want int) ([]byte, *http.Response) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, e.h.URL+path, strings.NewReader(body))
		req.Header.Set("apikey", e.p.AnonKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Pibase-Session-Mode", "cookie")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		content, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s: %d %s", path, res.StatusCode, content)
		}
		return content, res
	}
	login, response := call("/auth/v1/token?grant_type=password", `{"email":"cookie-user@example.test","password":"audit user password"}`, "", 200)
	if strings.Contains(string(login), "refresh_token") || !strings.Contains(response.Header.Get("Set-Cookie"), "HttpOnly") {
		t.Fatalf("refresh token exposed or cookie not HttpOnly: %s %s", login, response.Header.Get("Set-Cookie"))
	}
	e.createUser("cookie-second@example.test")
	second, _ := call("/auth/v1/token?grant_type=password", `{"email":"cookie-second@example.test","password":"audit user password"}`, "", 200)
	if !strings.Contains(string(second), "cookie-second@example.test") {
		t.Fatalf("existing cookie overrode password sign-in: %s", second)
	}
	var session struct {
		Access string `json:"access_token"`
	}
	if err := json.Unmarshal(login, &session); err != nil {
		t.Fatal(err)
	}
	refreshed, _ := call("/auth/v1/token?grant_type=refresh_token", `{}`, "", 200)
	if err := json.Unmarshal(refreshed, &session); err != nil {
		t.Fatal(err)
	}
	call("/auth/v1/logout", `{}`, session.Access, 204)
	call("/auth/v1/token?grant_type=refresh_token", `{}`, "", 400)
	preflight, _ := http.NewRequest(http.MethodOptions, e.h.URL+"/auth/v1/token", nil)
	preflight.Header.Set("Origin", "https://app.example.test")
	preflight.Header.Set("Access-Control-Request-Headers", "apikey,x-pibase-session-mode,content-type")
	preflightResponse, err := client.Do(preflight)
	if err != nil {
		t.Fatal(err)
	}
	preflightResponse.Body.Close()
	if preflightResponse.Header.Get("Access-Control-Allow-Origin") != "https://app.example.test" || preflightResponse.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("cookie CORS: %v", preflightResponse.Header)
	}
	badOrigin, _ := http.NewRequest(http.MethodPost, e.h.URL+"/auth/v1/token?grant_type=password", strings.NewReader(`{"email":"cookie-user@example.test","password":"audit user password"}`))
	badOrigin.Header.Set("apikey", e.p.AnonKey)
	badOrigin.Header.Set("X-Pibase-Session-Mode", "cookie")
	badOrigin.Header.Set("Origin", "https://untrusted.example")
	badResponse, err := client.Do(badOrigin)
	if err != nil {
		t.Fatal(err)
	}
	badResponse.Body.Close()
	if badResponse.StatusCode != 403 {
		t.Fatalf("untrusted cookie origin = %d", badResponse.StatusCode)
	}
}

func TestCookieBrowserClientRestoresWithoutLocalStorage(t *testing.T) {
	e := newAuditEnv(t, nil)
	e.createUser("cookie-client@example.test")
	script, err := filepath.Abs("../../tests/cookie-client.mjs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", script)
	cmd.Env = append(os.Environ(), "PIBASE_TEST_URL="+e.h.URL, "PIBASE_TEST_ANON_KEY="+e.p.AnonKey,
		"PIBASE_TEST_EMAIL=cookie-client@example.test", "PIBASE_TEST_PASSWORD=audit user password")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cookie client: %v\n%s", err, out)
	}
}

func TestCookieRecoverySession(t *testing.T) {
	t.Setenv("PIBASE_ENV", "test")
	t.Setenv("PIBASE_AUTH_EXPOSE_TOKENS", "true")
	e := newAuditEnv(t, nil)
	e.createUser("cookie-recovery@example.test")
	issued := requestAuthJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/recover", e.p.AnonKey,
		`{"email":"cookie-recovery@example.test"}`, http.StatusOK)
	var recovery struct {
		Token string `json:"reset_token"`
	}
	if err := json.Unmarshal(issued, &recovery); err != nil || recovery.Token == "" {
		t.Fatalf("recovery token: %s (%v)", issued, err)
	}
	url := e.h.URL + "/auth/v1/verify?type=recovery&token=" + recovery.Token
	request, _ := http.NewRequest(http.MethodGet, url, nil)
	request.Header.Set("apikey", e.p.AnonKey)
	request.Header.Set("X-Pibase-Session-Mode", "cookie")
	response, err := e.h.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	content, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || strings.Contains(string(content), "refresh_token") || !strings.Contains(response.Header.Get("Set-Cookie"), "HttpOnly") {
		t.Fatalf("cookie recovery: status=%d body=%s cookie=%s", response.StatusCode, content, response.Header.Get("Set-Cookie"))
	}
}
