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
	"github.com/jackc/pgx/v5"
)

func guardConfig(c Config) Config {
	c.AdminURL, c.DatabaseURL, c.Token, c.BackupKey = "postgres://unused", "postgres://unused", "op", bytes.Repeat([]byte{9}, 32)
	if c.MasterKey == nil {
		c.MasterKey = bytes.Repeat([]byte{7}, 32)
	}
	return c
}

func guardServer() *Server {
	s, err := New(guardConfig(Config{Env: "production", SignupHourlyLimit: 4, BlockedEmailDomains: []string{"spam.example"}}))
	if err != nil {
		panic(err)
	}
	return s
}

func TestSignupFormToken(t *testing.T) {
	s := guardServer()
	now := time.Now()
	if err := s.checkFormToken(s.formTokenAt(now.Add(-10*time.Second)), now); err != nil {
		t.Fatalf("a 10 second old token should pass: %v", err)
	}
	if err := s.checkFormToken(s.formTokenAt(now.Add(-time.Second)), now); err == nil {
		t.Fatal("a form sent after one second should be refused")
	}
	if err := s.checkFormToken(s.formTokenAt(now.Add(-3*time.Hour)), now); err == nil {
		t.Fatal("a three hour old form should be refused")
	}
	old := s.formTokenAt(now.Add(-time.Minute))
	ts, _, _ := strings.Cut(old, ".")
	for _, forged := range []string{"", "abc", ts, ts + ".00", fmt.Sprint(now.Add(-time.Minute).Unix()) + "." + strings.Repeat("a", 64)} {
		if err := s.checkFormToken(forged, now); err == nil {
			t.Errorf("forged token %q accepted", forged)
		}
	}
	other, _ := New(guardConfig(Config{MasterKey: bytes.Repeat([]byte{8}, 32)}))
	if err := s.checkFormToken(other.formTokenAt(now.Add(-time.Minute)), now); err == nil {
		t.Fatal("a token signed with another key should be refused")
	}
}

func TestSignupSpamChecks(t *testing.T) {
	s := guardServer()
	r := httptest.NewRequest(http.MethodPost, "/_pibase/auth/signup", nil)
	good := s.formTokenAt(time.Now().Add(-10 * time.Second))
	if status, msg := s.checkSignup(r, "1.1.1.1", "ann@example.com", good, ""); status != 0 {
		t.Fatalf("a normal sign-up was refused: %d %s", status, msg)
	}
	if status, _ := s.checkSignup(r, "1.1.1.1", "ann@example.com", good, "http://spam"); status != http.StatusBadRequest {
		t.Fatal("the hidden field should catch bots")
	}
	if status, _ := s.checkSignup(r, "1.1.1.1", "ann@example.com", "", ""); status != http.StatusBadRequest {
		t.Fatal("a sign-up without the form token should be refused")
	}
	for _, email := range []string{"x@mailinator.com", "x@MAILINATOR.COM", "x@eu.yopmail.com", "x@spam.example"} {
		if status, _ := s.checkSignup(r, "1.1.1.1", email, good, ""); status != http.StatusBadRequest {
			t.Errorf("throwaway address %s accepted", email)
		}
	}
	if s.blockedEmailDomain("x@notmailinator.com") {
		t.Error("a domain that only ends in the same letters must not be blocked")
	}
	// Three accounts a day per address.
	for i := 0; i < signupsPerIPDay; i++ {
		s.noteSignup("2.2.2.2")
	}
	if status, _ := s.checkSignup(r, "2.2.2.2", "b@example.com", good, ""); status != http.StatusTooManyRequests {
		t.Fatal("a fourth account from one address in a day should wait")
	}
	// The hourly cap (4 here) covers every address together.
	s.noteSignup("3.3.3.3")
	if status, _ := s.checkSignup(r, "4.4.4.4", "c@example.com", good, ""); status != http.StatusTooManyRequests {
		t.Fatal("the server-wide hourly cap should stop a burst from many addresses")
	}
	closed, _ := New(guardConfig(Config{SignupClosed: true, Env: "test"}))
	if status, _ := closed.checkSignup(r, "5.5.5.5", "d@example.com", good, ""); status != http.StatusForbidden {
		t.Fatal("closed sign-up should refuse")
	}
}

func TestSignupConfirmsEmailBeforeProjects(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "verify-pw"
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(adminURL)
	parsed.User = url.UserPassword("authenticator", password)
	parsed.Path = "/_control"
	root := t.TempDir()
	var mu sync.Mutex
	var mails []string
	s, err := New(Config{AdminURL: adminURL, DatabaseURL: parsed.String(), Token: "verify-op",
		StorageRoot: filepath.Join(root, "f"), BackupRoot: filepath.Join(root, "b"),
		MasterKey: bytes.Repeat([]byte{7}, 32), BackupKey: bytes.Repeat([]byte{9}, 32), Env: "test",
		DashboardURL: "https://app.example.test",
		SendMail: func(to, subject, body string) error {
			mu.Lock()
			defer mu.Unlock()
			mails = append(mails, to+"\n"+body)
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	call := func(method, path, body string, cookie *http.Cookie, op bool, want int) (*http.Response, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		if op {
			req.Header.Set("Authorization", "Bearer verify-op")
		}
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, want, data)
		}
		return resp, data
	}
	email := fmt.Sprintf("verify%d@example.test", time.Now().UnixNano())
	signup, _ := json.Marshal(map[string]string{"email": email, "password": "long enough pw", "password_confirm": "long enough pw", "organization": "Verify Org"})
	resp, body := call("POST", "/_pibase/auth/signup", string(signup), nil, false, 201)
	var account control.Account
	_ = json.Unmarshal(body, &account)
	defer func() { _ = control.DeleteAccount(ctx, adminURL, account.ID) }()
	if account.EmailVerified {
		t.Fatal("a self-made account should start unconfirmed when email is set up")
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "pibase_session" {
			cookie = c
		}
	}
	call("POST", "/_pibase/projects", `{"name":"too early"}`, cookie, false, 403)
	call("POST", "/_pibase/projects/import", "", cookie, false, 403)

	// The welcome email carries the confirmation link to the dashboard.
	var link string
	for i := 0; i < 50 && link == ""; i++ {
		mu.Lock()
		for _, m := range mails {
			if strings.HasPrefix(m, email+"\n") {
				link = regexp.MustCompile(`https://app\.example\.test/#verify=(\S+)`).FindStringSubmatch(m)[1]
			}
		}
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	if link == "" {
		t.Fatal("no confirmation email was sent")
	}
	token, _ := url.QueryUnescape(link)
	call("POST", "/_pibase/auth/verify", `{"token":"wrong"}`, nil, false, 400)
	_, out := call("POST", "/_pibase/auth/verify", `{"token":"`+token+`"}`, nil, false, 200)
	if !strings.Contains(string(out), email) {
		t.Fatalf("verify answered %s", out)
	}
	call("POST", "/_pibase/auth/verify", `{"token":"`+token+`"}`, nil, false, 400) // single use
	_, me := call("GET", "/_pibase/auth/me", "", cookie, false, 200)
	if !strings.Contains(string(me), `"email_verified":true`) {
		t.Fatalf("me after confirming: %s", me)
	}
	_, created := call("POST", "/_pibase/projects", `{"name":"now allowed"}`, cookie, false, 201)
	if !strings.Contains(string(created), "anon_key") {
		t.Fatalf("project: %s", created)
	}

	// The operator sees the account and can delete it with its projects.
	call("GET", "/_pibase/operator/accounts", "", cookie, false, 403)
	_, list := call("GET", "/_pibase/operator/accounts", "", nil, true, 200)
	if !strings.Contains(string(list), email) || !strings.Contains(string(list), `"projects":1`) {
		t.Fatalf("operator list: %s", list)
	}
	call("DELETE", "/_pibase/operator/accounts/"+account.ID, `{"confirm":"wrong@example.test"}`, nil, true, 400)
	call("DELETE", "/_pibase/operator/accounts/"+account.ID, `{"confirm":"`+email+`"}`, nil, true, 204)
	call("GET", "/_pibase/auth/me", "", cookie, false, 401)
}

func TestStaleUnconfirmedAccountsAreRemoved(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	if err := control.Bootstrap(ctx, adminURL, "stale-pw"); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().UnixNano()
	old, err := control.CreateAccount(ctx, adminURL, fmt.Sprintf("stale%d@example.test", stamp), "long enough pw", "Old")
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := control.CreateAccount(ctx, adminURL, fmt.Sprintf("fresh%d@example.test", stamp), "long enough pw", "New")
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := control.CreateAccount(ctx, adminURL, fmt.Sprintf("kept%d@example.test", stamp), "long enough pw", "Kept")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, id := range []string{old.ID, fresh.ID, confirmed.ID} {
			_ = control.DeleteAccount(ctx, adminURL, id)
		}
	}()
	if err := control.MarkAccountVerified(ctx, adminURL, confirmed.ID); err != nil {
		t.Fatal(err)
	}
	controlURL, _ := url.Parse(adminURL)
	controlURL.Path = "/_control"
	conn, err := pgx.Connect(ctx, controlURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `UPDATE control.accounts SET created_at=now()-interval '3 days' WHERE id = ANY($1::uuid[])`, []string{old.ID, confirmed.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := control.RemoveStaleUnverifiedAccounts(ctx, adminURL); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{old.ID: false, fresh.ID: true, confirmed.ID: true} {
		_, err := control.AccountVerified(ctx, adminURL, id)
		if exists := err == nil; exists != want {
			t.Errorf("account %s exists=%v, want %v", id, exists, want)
		}
	}
}
