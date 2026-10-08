package gateway

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAuthClientIsServed(t *testing.T) {
	h := (&Server{}).Handler()
	req := httptest.NewRequest(http.MethodGet, "/client/v1/pibase-auth.js", nil)
	req.Header.Set("Origin", "https://site.example")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !bytes.Equal(w.Body.Bytes(), authClientJS) || !bytes.Contains(authClientJS, []byte("createClient")) {
		t.Fatalf("status %d, %d bytes", w.Code, w.Body.Len())
	}
	for name, want := range map[string]string{
		"Content-Type":                "text/javascript; charset=utf-8",
		"X-Content-Type-Options":      "nosniff",
		"Access-Control-Allow-Origin": "*",
	} {
		if got := w.Header().Get(name); got != want {
			t.Errorf("%s: %q", name, got)
		}
	}
}

// TestBrowserAuthClient runs tests/auth-client-browser.cjs in Chromium against
// a real gateway, with the page served from a different origin. Set
// PIBASE_TEST_BROWSER=1 and make the playwright package resolvable (NODE_PATH).
func TestBrowserAuthClient(t *testing.T) {
	if os.Getenv("PIBASE_TEST_BROWSER") == "" {
		t.Skip("PIBASE_TEST_BROWSER is not set")
	}
	e := newAuditEnv(t, nil)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>auth client</title>
<script src="%s/client/v1/pibase-auth.js"></script>
<script>
const q = new URLSearchParams(location.search);
window.storageKey = "pibase.auth:" + q.get("url");
window.auth = PibaseAuth.createClient({ url: q.get("url"), anonKey: q.get("key"), locks: q.get("locks") === "1" });
window.usesLocks = q.get("locks") === "1" && !!navigator.locks;
window.events = [];
window.auth.onAuthStateChange((event) => window.events.push(event));
</script>`, e.h.URL)
	}))
	defer page.Close()
	script, err := filepath.Abs("../../tests/auth-client-browser.cjs")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("node", script)
	cmd.Env = append(os.Environ(),
		"PIBASE_TEST_URL="+e.h.URL,
		"PIBASE_TEST_PAGE_URL="+page.URL,
		"PIBASE_TEST_ANON_KEY="+e.p.AnonKey,
		"PIBASE_TEST_SERVICE_KEY="+e.p.ServiceKey,
		fmt.Sprintf("PIBASE_TEST_EMAIL_PREFIX=client-%d", time.Now().UnixNano()),
		"PIBASE_TEST_PASSWORD=audit user password",
	)
	out, err := cmd.CombinedOutput()
	t.Logf("%s", out)
	if err != nil {
		t.Fatal(err)
	}
}
