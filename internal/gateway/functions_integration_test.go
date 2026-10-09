package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
)

type seenRequest struct {
	method, path, query, body string
	header                    http.Header
}

// upstream records what the site's server receives.
type upstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []seenRequest
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.seen = append(u.seen, seenRequest{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, string(body), r.Header.Clone()})
		u.mu.Unlock()
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://127.0.0.1:1/elsewhere", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Custom", "kept")
		w.Header().Set("Set-Cookie", "site=1")
		w.Header().Set("Access-Control-Allow-Origin", "https://evil.example")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"sent":true}`))
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *upstream) count() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.seen)
}

func (u *upstream) last() seenRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.seen[len(u.seen)-1]
}

func (e *auditEnv) ctl(statement string, args ...any) {
	e.t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL(e.t, e.adminURL, "_control", parsedUsername(e.t, e.adminURL), urlPassword(e.t, e.adminURL)))
	if err != nil {
		e.t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, statement, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *auditEnv) call(method, path string, headers map[string]string, body string) (*http.Response, string) {
	e.t.Helper()
	req, err := http.NewRequest(method, e.h.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.h.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

func TestFunctionsProxyForwardsWithVerifiedIdentity(t *testing.T) {
	e := newAuditEnv(t, nil)
	site := newUpstream(t)
	if _, err := control.SetFunctionsURL(context.Background(), e.adminURL, e.p.ID, site.URL+"/api/", true); err != nil {
		t.Fatal(err)
	}
	userID := e.createUser("fn@example.test")
	tokens := e.login("fn@example.test")

	// A signed-out caller (anon key only) reaches the route as anon.
	resp, body := e.call("POST", "/functions/v1/send-quote?x=1&y=a%20b", map[string]string{
		"apikey": e.p.AnonKey, "Authorization": "Bearer " + e.p.AnonKey, "Content-Type": "application/json",
		// A caller can't claim an identity or hand the site the project key.
		"X-Pibase-User-Id": "00000000-0000-0000-0000-000000000000", "X-Pibase-Role": "service_role", "X-Forwarded-For": "6.6.6.6", "Cookie": "gateway=1",
	}, `{"to":"a@b.c"}`)
	if resp.StatusCode != http.StatusCreated || body != `{"sent":true}` {
		t.Fatalf("anon call: %d %s", resp.StatusCode, body)
	}
	got := site.last()
	if got.method != "POST" || got.path != "/api/send-quote" || got.query != "x=1&y=a%20b" || got.body != `{"to":"a@b.c"}` {
		t.Fatalf("forwarded request: %+v", got)
	}
	if got.header.Get("X-Pibase-Role") != "anon" || got.header.Get("X-Pibase-User-Id") != "" || got.header.Get("X-Pibase-Project-Id") != e.p.ID {
		t.Fatalf("identity headers: %v", got.header)
	}
	if got.header.Get("Apikey") != "" || got.header.Get("Cookie") != "" || got.header.Get("X-Forwarded-For") != "" {
		t.Fatalf("headers that should not be forwarded: %v", got.header)
	}
	if got.header.Get("Authorization") != "Bearer "+e.p.AnonKey || got.header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers that should be forwarded: %v", got.header)
	}
	if resp.Header.Get("X-Custom") != "kept" || resp.Header.Get("Set-Cookie") != "" || resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("response headers: %v", resp.Header)
	}

	// A signed-in caller is identified from the verified token, and the route
	// gets the token so it can act as the user.
	resp, _ = e.call("GET", "/functions/v1/nested/route", map[string]string{"apikey": e.p.AnonKey, "Authorization": "Bearer " + tokens.Access}, "")
	got = site.last()
	if resp.StatusCode != http.StatusCreated || got.path != "/api/nested/route" || got.header.Get("X-Pibase-Role") != "authenticated" || got.header.Get("X-Pibase-User-Id") != userID || got.header.Get("Authorization") != "Bearer "+tokens.Access {
		t.Fatalf("signed-in call: %d %+v", resp.StatusCode, got)
	}

	// After sign-out the token is refused before the site is called.
	e.status("POST", "/auth/v1/logout", tokens.Access, "{}")
	before := site.count()
	resp, _ = e.call("POST", "/functions/v1/send-quote", map[string]string{"apikey": e.p.AnonKey, "Authorization": "Bearer " + tokens.Access}, "{}")
	if resp.StatusCode != http.StatusUnauthorized || site.count() != before {
		t.Fatalf("signed-out call: %d, site calls %d -> %d", resp.StatusCode, before, site.count())
	}

	// No key, no call.
	resp, _ = e.call("POST", "/functions/v1/send-quote", nil, "{}")
	if resp.StatusCode != http.StatusUnauthorized || site.count() != before {
		t.Fatalf("keyless call: %d", resp.StatusCode)
	}

	// A browser preflight is answered by the gateway.
	resp, _ = e.call("OPTIONS", "/functions/v1/send-quote", map[string]string{"Origin": "https://site.example", "Access-Control-Request-Method": "POST", "Access-Control-Request-Headers": "authorization,apikey,content-type,x-client-info"}, "")
	if resp.StatusCode != http.StatusNoContent || site.count() != before {
		t.Fatalf("preflight: %d", resp.StatusCode)
	}
}

func TestFunctionsProxyRefusesUnsafeTargetsAndNames(t *testing.T) {
	e := newAuditEnv(t, nil)
	site := newUpstream(t)
	headers := map[string]string{"apikey": e.p.AnonKey, "Authorization": "Bearer " + e.p.AnonKey}

	// Nothing configured.
	if resp, _ := e.call("POST", "/functions/v1/x", headers, "{}"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unset: %d", resp.StatusCode)
	}

	// A project owner can't aim the gateway at a private address...
	if _, err := control.SetFunctionsURL(context.Background(), e.adminURL, e.p.ID, site.URL, false); err == nil {
		t.Fatal("owner was allowed to set a private functions address")
	}
	// ...and even if one is stored without the operator's approval, the
	// gateway refuses to connect to it.
	e.ctl(`UPDATE control.projects SET functions_url=$1, functions_private=false WHERE id=$2`, site.URL, e.p.ID)
	resp, body := e.call("POST", "/functions/v1/x", headers, "{}")
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(body, "not public") || site.count() != 0 {
		t.Fatalf("unapproved private target: %d %s (site calls %d)", resp.StatusCode, body, site.count())
	}

	// With the operator's approval it works; redirects are not followed.
	if _, err := control.SetFunctionsURL(context.Background(), e.adminURL, e.p.ID, site.URL, true); err != nil {
		t.Fatal(err)
	}
	redirectClient := *e.h.Client()
	redirectClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, _ := http.NewRequest("GET", e.h.URL+"/functions/v1/redirect", nil)
	req.Header.Set("apikey", e.p.AnonKey)
	redirected, err := redirectClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	redirected.Body.Close()
	if redirected.StatusCode != http.StatusFound || site.count() != 1 {
		t.Fatalf("redirect: status %d, site calls %d (the gateway must hand it back, not follow it)", redirected.StatusCode, site.count())
	}
	site.mu.Lock()
	site.seen = nil
	site.mu.Unlock()

	// Names can't climb out of the functions address.
	// (The server's own path cleaning turns a/../b and a//b into plain requests
	// for b and a/b, which are ordinary names; those can't leave the address.)
	for _, name := range []string{"..", "%2e%2e/admin", "a%2f..%2fb", "a%2f%2fb", ".hidden", "a b", "a%00b", strings.Repeat("a", 129)} {
		resp, _ := e.call("GET", "/functions/v1/"+name, headers, "")
		if resp.StatusCode == http.StatusCreated || site.count() != 0 {
			t.Fatalf("name %q reached the site (status %d)", name, resp.StatusCode)
		}
	}

	// Turning it off.
	if _, err := control.SetFunctionsURL(context.Background(), e.adminURL, e.p.ID, "", false); err != nil {
		t.Fatal(err)
	}
	if resp, _ := e.call("POST", "/functions/v1/x", headers, "{}"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("after clearing: %d", resp.StatusCode)
	}
}

func TestNormalizeFunctionsURL(t *testing.T) {
	for _, tc := range []struct {
		in      string
		private bool
		want    string
		ok      bool
	}{
		{"https://app.example.com/api/", false, "https://app.example.com/api", true},
		{" https://app.example.com ", false, "https://app.example.com", true},
		{"", false, "", true},
		{"http://localhost:3000/api", false, "", false},
		{"http://127.0.0.1:3000", false, "", false},
		{"http://192.168.1.5:3000", false, "", false},
		{"http://[::1]:3000", false, "", false},
		{"http://127.0.0.1:3000/api", true, "http://127.0.0.1:3000/api", true},
		{"ftp://x.example", false, "", false},
		{"https://u:p@x.example", false, "", false},
		{"https://x.example/a?k=v", false, "", false},
		{"/relative", false, "", false},
	} {
		got, err := control.NormalizeFunctionsURL(tc.in, tc.private)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("%q private=%v: got %q, %v", tc.in, tc.private, got, err)
		}
	}
}

// supabase-js's functions.invoke works unchanged against /functions/v1.
// Needs node and the @supabase/supabase-js package (npm install).
func TestSupabaseFunctionsInvoke(t *testing.T) {
	script, err := filepath.Abs("../../tests/functions-invoke.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(script), "..", "node_modules", "@supabase", "supabase-js")); err != nil {
		t.Skip("@supabase/supabase-js is not installed")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	e := newAuditEnv(t, nil)
	site := newUpstream(t)
	if _, err := control.SetFunctionsURL(context.Background(), e.adminURL, e.p.ID, site.URL+"/api", true); err != nil {
		t.Fatal(err)
	}
	e.createUser("invoke@example.test")
	cmd := exec.Command("node", script)
	cmd.Env = append(os.Environ(), "PIBASE_TEST_URL="+e.h.URL, "PIBASE_TEST_ANON_KEY="+e.p.AnonKey,
		"PIBASE_TEST_EMAIL=invoke@example.test", "PIBASE_TEST_PASSWORD=audit user password")
	out, err := cmd.CombinedOutput()
	t.Logf("%s", out)
	if err != nil {
		t.Fatal(err)
	}
	if site.count() != 2 || site.last().path != "/api/nested/route" || site.last().header.Get("X-Pibase-Role") != "authenticated" {
		t.Fatalf("site saw %d calls, last %+v", site.count(), site.last())
	}
}
