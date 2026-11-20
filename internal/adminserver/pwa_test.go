package adminserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The dashboard installs as a phone or desktop app: its manifest, icons and
// service worker are served to anyone, signed in or not, and the pages allow
// them under their Content-Security-Policy.
func TestDashboardIsInstallable(t *testing.T) {
	s, err := New(Config{AdminURL: "postgres://unused", DatabaseURL: "postgres://unused", Token: "op", MasterKey: bytes.Repeat([]byte{7}, 32), BackupKey: bytes.Repeat([]byte{9}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}
	m := get("/manifest.webmanifest")
	if m.Code != 200 || m.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest: %d %s", m.Code, m.Header().Get("Content-Type"))
	}
	var manifest struct {
		StartURL string `json:"start_url"`
		Display  string `json:"display"`
		Icons    []struct{ Src, Sizes, Purpose string }
	}
	if err := json.Unmarshal(m.Body.Bytes(), &manifest); err != nil || manifest.StartURL != "/app" || manifest.Display != "standalone" {
		t.Fatalf("manifest content: %+v %v", manifest, err)
	}
	sizes := map[string]bool{}
	for _, icon := range manifest.Icons {
		r := get(icon.Src)
		if r.Code != 200 || r.Header().Get("Content-Type") != "image/png" || !bytes.HasPrefix(r.Body.Bytes(), []byte("\x89PNG")) {
			t.Fatalf("icon %s: %d", icon.Src, r.Code)
		}
		sizes[icon.Sizes+" "+icon.Purpose] = true
	}
	if !sizes["192x192 any"] || !sizes["512x512 any"] || !sizes["512x512 maskable"] {
		t.Fatalf("install needs 192 and 512 icons and a maskable one: %v", sizes)
	}
	sw := get("/sw.js")
	if sw.Code != 200 || !strings.HasPrefix(sw.Header().Get("Content-Type"), "text/javascript") || sw.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("service worker: %d %v", sw.Code, sw.Header())
	}
	// The worker keeps nothing on the device.
	if strings.Contains(sw.Body.String(), "caches.") || strings.Contains(sw.Body.String(), "indexedDB") {
		t.Fatal("the service worker must not store responses")
	}
	for _, p := range []string{"/apple-touch-icon.png", "/favicon.ico"} {
		if r := get(p); r.Code != 200 {
			t.Errorf("%s: %d", p, r.Code)
		}
	}
	for _, p := range []string{"/pwa/", "/pwa/missing.png", "/pwa/..%2fapp.html"} {
		if r := get(p); r.Code != 404 {
			t.Errorf("%s should be 404, got %d", p, r.Code)
		}
	}
	for _, p := range []string{"/", "/app"} {
		r := get(p)
		csp := r.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "manifest-src 'self'") || !strings.Contains(csp, "worker-src 'self'") {
			t.Errorf("%s CSP does not allow the manifest and worker: %s", p, csp)
		}
		body := r.Body.String()
		for _, tag := range []string{`rel="manifest" href="/manifest.webmanifest"`, `rel="apple-touch-icon"`, `name="theme-color"`, "serviceWorker.register('/sw.js')"} {
			if !strings.Contains(body, tag) {
				t.Errorf("%s misses %s", p, tag)
			}
		}
	}
}

func TestProductionDashboardSendsHSTS(t *testing.T) {
	for env, want := range map[string]string{"production": "max-age=31536000", "development": "", "test": ""} {
		s, err := New(Config{AdminURL: "postgres://unused", DatabaseURL: "postgres://unused", Token: "op", MasterKey: bytes.Repeat([]byte{7}, 32), BackupKey: bytes.Repeat([]byte{9}, 32), Env: env})
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if got := rec.Header().Get("Strict-Transport-Security"); got != want {
			t.Errorf("%s: HSTS %q, want %q", env, got, want)
		}
	}
}
