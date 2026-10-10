package adminserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
)

type sentMail struct{ to, subject, body string }

func resetTestServer(t *testing.T, withMail bool) (*httptest.Server, func() []sentMail, string) {
	t.Helper()
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	const password = "reset-test-pw"
	if err := control.Bootstrap(context.Background(), adminURL, password); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(adminURL)
	parsed.User = url.UserPassword("authenticator", password)
	parsed.Path = "/_control"
	root := t.TempDir()
	var mu sync.Mutex
	var sent []sentMail
	cfg := Config{AdminURL: adminURL, DatabaseURL: parsed.String(), Token: "reset-op",
		StorageRoot: filepath.Join(root, "f"), BackupRoot: filepath.Join(root, "b"),
		MasterKey: bytes.Repeat([]byte{7}, 32), BackupKey: bytes.Repeat([]byte{9}, 32), Env: "test"}
	if withMail {
		cfg.DashboardURL = "https://dash.example.com/"
		cfg.SendMail = func(to, subject, body string) error {
			mu.Lock()
			defer mu.Unlock()
			sent = append(sent, sentMail{to, subject, body})
			return nil
		}
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(hs.Close)
	mails := func() []sentMail {
		// Mail is sent in the background; wait briefly for it.
		for i := 0; i < 50; i++ {
			mu.Lock()
			n := len(sent)
			mu.Unlock()
			if n > 0 {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		return append([]sentMail(nil), sent...)
	}
	return hs, mails, adminURL
}

func resetCall(t *testing.T, hs *httptest.Server, method, path, body string, header map[string]string, cookie *http.Cookie) (int, []byte, *http.Response) {
	t.Helper()
	req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range header {
		req.Header.Set(k, v)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := hs.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, resp
}

func sessionCookie(resp *http.Response) *http.Cookie {
	for _, c := range resp.Cookies() {
		if c.Name == "pibase_session" {
			return c
		}
	}
	return nil
}

func TestForgotPasswordByEmail(t *testing.T) {
	hs, mails, adminURL := resetTestServer(t, true)
	email := fmt.Sprintf("forgot%d@example.test", time.Now().UnixNano())
	j := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	status, _, resp := resetCall(t, hs, "POST", "/_pibase/auth/signup", j(map[string]string{"email": email, "password": "old-password-1", "password_confirm": "old-password-1", "organization": "Org"}), nil, nil)
	if status != http.StatusCreated {
		t.Fatalf("signup: %d", status)
	}
	oldSession := sessionCookie(resp)
	t.Cleanup(func() {
		_, me, _ := resetCall(t, hs, "POST", "/_pibase/auth/login", j(map[string]string{"email": email, "password": "new-password-2"}), nil, nil)
		var a struct{ ID string }
		_ = json.Unmarshal(me, &a)
		if a.ID != "" {
			_ = control.DeleteAccount(context.Background(), adminURL, a.ID)
		}
	})

	if _, info, _ := resetCall(t, hs, "GET", "/_pibase/auth/reset-info", "", nil, nil); !strings.Contains(string(info), `"email":true`) {
		t.Fatalf("reset-info: %s", info)
	}
	// Unknown and known addresses get the same answer; only the known one gets mail.
	_, unknown, _ := resetCall(t, hs, "POST", "/_pibase/auth/recover", j(map[string]string{"email": "nobody-" + email}), nil, nil)
	_, known, _ := resetCall(t, hs, "POST", "/_pibase/auth/recover", j(map[string]string{"email": strings.ToUpper(email)}), nil, nil)
	if string(unknown) != string(known) || !strings.Contains(string(known), `"email_sent":true`) {
		t.Fatalf("answers differ or wrong: %s vs %s", unknown, known)
	}
	sent := mails()
	if len(sent) != 1 || sent[0].to != email {
		t.Fatalf("mail sent: %+v", sent)
	}
	link := regexp.MustCompile(`https://dash\.example\.com/#reset=([A-Za-z0-9_%-]+)`).FindStringSubmatch(sent[0].body)
	if link == nil {
		t.Fatalf("no reset link in: %s", sent[0].body)
	}
	token, _ := url.QueryUnescape(link[1])

	// Mismatch, short password and a wrong token are refused.
	for _, bad := range []map[string]string{
		{"token": token, "password": "new-password-2", "password_confirm": "different-2"},
		{"token": token, "password": "short", "password_confirm": "short"},
		{"token": token + "x", "password": "new-password-2", "password_confirm": "new-password-2"},
	} {
		if status, body, _ := resetCall(t, hs, "POST", "/_pibase/auth/reset", j(bad), nil, nil); status != http.StatusBadRequest {
			t.Fatalf("bad reset %v: %d %s", bad, status, body)
		}
	}
	status, body, _ := resetCall(t, hs, "POST", "/_pibase/auth/reset", j(map[string]string{"token": token, "password": "new-password-2", "password_confirm": "new-password-2"}), nil, nil)
	if status != http.StatusOK || !strings.Contains(string(body), email) {
		t.Fatalf("reset: %d %s", status, body)
	}
	// The link works once, the old session is gone, the old password fails, the new one works.
	if status, _, _ := resetCall(t, hs, "POST", "/_pibase/auth/reset", j(map[string]string{"token": token, "password": "third-password-3", "password_confirm": "third-password-3"}), nil, nil); status != http.StatusBadRequest {
		t.Fatalf("reused link: %d", status)
	}
	if status, _, _ := resetCall(t, hs, "GET", "/_pibase/auth/me", "", nil, oldSession); status != http.StatusUnauthorized {
		t.Fatalf("old session still works: %d", status)
	}
	if status, _, _ := resetCall(t, hs, "POST", "/_pibase/auth/login", j(map[string]string{"email": email, "password": "old-password-1"}), nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("old password still works: %d", status)
	}
	if status, _, _ := resetCall(t, hs, "POST", "/_pibase/auth/login", j(map[string]string{"email": email, "password": "new-password-2"}), nil, nil); status != http.StatusOK {
		t.Fatalf("new password: %d", status)
	}
	// A newer link replaces an older one.
	resetCall(t, hs, "POST", "/_pibase/auth/recover", j(map[string]string{"email": email}), nil, nil)
	first, _, _ := control.CreatePasswordReset(context.Background(), adminURL, email)
	second, _, _ := control.CreatePasswordReset(context.Background(), adminURL, email)
	if _, err := control.ResetPassword(context.Background(), adminURL, first, "fourth-password-4"); err == nil {
		t.Fatal("an older link still worked after a newer one was issued")
	}
	if _, err := control.ResetPassword(context.Background(), adminURL, second, "new-password-2"); err != nil {
		t.Fatalf("newest link: %v", err)
	}
}

func TestForgotPasswordWithoutEmailAndOperatorReset(t *testing.T) {
	hs, mails, adminURL := resetTestServer(t, false)
	email := fmt.Sprintf("nomail%d@local.test", time.Now().UnixNano())
	j := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	status, _, resp := resetCall(t, hs, "POST", "/_pibase/auth/signup", j(map[string]string{"email": email, "password": "old-password-1", "password_confirm": "old-password-1", "organization": "Org"}), nil, nil)
	if status != http.StatusCreated {
		t.Fatalf("signup: %d", status)
	}
	oldSession := sessionCookie(resp)
	t.Cleanup(func() {
		_, me, _ := resetCall(t, hs, "POST", "/_pibase/auth/login", j(map[string]string{"email": email, "password": "operator-set-2"}), nil, nil)
		var a struct{ ID string }
		_ = json.Unmarshal(me, &a)
		if a.ID != "" {
			_ = control.DeleteAccount(context.Background(), adminURL, a.ID)
		}
	})
	if _, info, _ := resetCall(t, hs, "GET", "/_pibase/auth/reset-info", "", nil, nil); !strings.Contains(string(info), `"email":false`) {
		t.Fatalf("reset-info: %s", info)
	}
	if _, body, _ := resetCall(t, hs, "POST", "/_pibase/auth/recover", j(map[string]string{"email": email}), nil, nil); !strings.Contains(string(body), `"email_sent":false`) {
		t.Fatalf("recover without mail: %s", body)
	}
	if len(mails()) != 0 {
		t.Fatal("mail sent although none is configured")
	}
	op := map[string]string{"Authorization": "Bearer reset-op"}
	// Only the operator may set a password.
	if status, _, _ := resetCall(t, hs, "POST", "/_pibase/operator/account-password", j(map[string]string{"email": email, "password": "hijacked-pass"}), nil, oldSession); status != http.StatusForbidden {
		t.Fatalf("account session set a password: %d", status)
	}
	if status, _, _ := resetCall(t, hs, "POST", "/_pibase/operator/account-password", j(map[string]string{"email": email, "password": "hijacked-pass"}), nil, nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous set a password: %d", status)
	}
	if status, _, _ := resetCall(t, hs, "POST", "/_pibase/operator/account-password", j(map[string]string{"email": "missing-" + email, "password": "operator-set-2"}), op, nil); status != http.StatusNotFound {
		t.Fatalf("unknown account: %d", status)
	}
	if status, _, _ := resetCall(t, hs, "POST", "/_pibase/operator/account-password", j(map[string]string{"email": email, "password": "short"}), op, nil); status != http.StatusBadRequest {
		t.Fatalf("short password: %d", status)
	}
	if status, body, _ := resetCall(t, hs, "POST", "/_pibase/operator/account-password", j(map[string]string{"email": strings.ToUpper(email), "password": "operator-set-2"}), op, nil); status != http.StatusNoContent {
		t.Fatalf("operator reset: %d %s", status, body)
	}
	if status, _, _ := resetCall(t, hs, "GET", "/_pibase/auth/me", "", nil, oldSession); status != http.StatusUnauthorized {
		t.Fatalf("old session survived the operator reset: %d", status)
	}
	if status, _, _ := resetCall(t, hs, "POST", "/_pibase/auth/login", j(map[string]string{"email": email, "password": "operator-set-2"}), nil, nil); status != http.StatusOK {
		t.Fatalf("login with operator-set password: %d", status)
	}
}
