package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/515woodpl-dot/pibase/internal/control"
)

// authSettingsServer starts a gateway on a fresh project and returns it with
// the project. Tokens are exposed so the test can play the email's link.
func authSettingsServer(t *testing.T, name string) (*httptest.Server, control.Project, string) {
	t.Helper()
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "integration-auth-password"
	masterKey := bytes.Repeat([]byte{7}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, adminURL, name, masterKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupGatewayProject(t, adminURL, p) })
	t.Setenv("PIBASE_AUTH_EXPOSE_TOKENS", "true")
	t.Setenv("PIBASE_ENV", "test")
	server, err := New(ctx, databaseURL(t, adminURL, "_control", "authenticator", password), masterKey, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	httpServer := httptest.NewServer(server.Handler())
	t.Cleanup(httpServer.Close)
	return httpServer, p, adminURL
}

func TestAuthSettingsDefaultRequiresConfirmation(t *testing.T) {
	srv, p, adminURL := authSettingsServer(t, "auth-settings-default")
	a, err := control.GetAuthSettings(context.Background(), adminURL, p.ID)
	if err != nil || !a.ConfirmEmail || a.SiteURL != "" || len(a.RedirectURLs) != 0 {
		t.Fatalf("new project settings = %+v, %v; want confirm on and nothing else", a, err)
	}
	email := "default-" + p.ID + "@example.test"
	out := requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/signup", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)
	if strings.Contains(string(out), "access_token") {
		t.Fatalf("signup with confirm on returned a session: %s", out)
	}
	requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/token?grant_type=password", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusBadRequest)
}

func TestAuthSettingsConfirmOffSignsInAtOnce(t *testing.T) {
	srv, p, adminURL := authSettingsServer(t, "auth-settings-off")
	if _, err := control.SetAuthSettings(context.Background(), adminURL, p.ID, control.AuthSettings{ConfirmEmail: false}); err != nil {
		t.Fatal(err)
	}
	email := "off-" + p.ID + "@example.test"
	out := requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/signup", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)
	var session struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		User         struct {
			Email     string `json:"email"`
			Confirmed any    `json:"email_confirmed_at"`
		} `json:"user"`
	}
	if err := json.Unmarshal(out, &session); err != nil || session.AccessToken == "" || session.RefreshToken == "" || session.User.Email != email || session.User.Confirmed == nil {
		t.Fatalf("signup with confirm off should sign in at once: %s", out)
	}
	requestAuthenticated(t, srv.Client(), srv.URL+"/auth/v1/user", p.AnonKey, session.AccessToken)
	requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/token?grant_type=password", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)
	// Signing up again with the same email reveals nothing.
	again := requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/signup", p.AnonKey, `{"email":"`+email+`","password":"another password here"}`, http.StatusOK)
	if strings.Contains(string(again), "access_token") {
		t.Fatalf("duplicate signup returned a session: %s", again)
	}
}

// noRedirects is a client that reports a redirect instead of following it.
func noRedirects(c *http.Client) *http.Client {
	copy := *c
	copy.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &copy
}

// openEmailLink opens a verify link the way a browser does: GET, no apikey.
func openEmailLink(t *testing.T, srv *httptest.Server, query url.Values) *http.Response {
	t.Helper()
	resp, err := noRedirects(srv.Client()).Get(srv.URL + "/auth/v1/verify?" + query.Encode())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func fragmentOf(t *testing.T, resp *http.Response) (string, url.Values) {
	t.Helper()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	base, frag, _ := strings.Cut(loc, "#")
	values, err := url.ParseQuery(frag)
	if err != nil {
		t.Fatalf("bad fragment in %q", loc)
	}
	return base, values
}

func TestAuthSettingsEmailLinksReturnToApp(t *testing.T) {
	srv, p, adminURL := authSettingsServer(t, "auth-settings-links")
	if _, err := control.SetAuthSettings(context.Background(), adminURL, p.ID, control.AuthSettings{
		ConfirmEmail: true, SiteURL: "https://app.example.com", RedirectURLs: []string{"http://localhost:3000/*"},
	}); err != nil {
		t.Fatal(err)
	}
	email := "links-" + p.ID + "@example.test"
	out := requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/signup?redirect_to="+url.QueryEscape("http://localhost:3000/welcome"), p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)
	var created struct {
		Token string `json:"confirmation_token"`
	}
	if err := json.Unmarshal(out, &created); err != nil || created.Token == "" {
		t.Fatalf("signup: %s", out)
	}

	// The link names the project, so it works without an apikey header, and
	// returns to the allowed page the app asked for, signed in.
	base, frag := fragmentOf(t, openEmailLink(t, srv, url.Values{"token": {created.Token}, "type": {"signup"}, "project": {p.ID}, "redirect_to": {"http://localhost:3000/welcome"}}))
	if base != "http://localhost:3000/welcome" || frag.Get("access_token") == "" || frag.Get("refresh_token") == "" || frag.Get("type") != "signup" || frag.Get("token_type") != "bearer" {
		t.Fatalf("confirm redirect = %s # %v", base, frag)
	}
	requestAuthenticated(t, srv.Client(), srv.URL+"/auth/v1/user", p.AnonKey, frag.Get("access_token"))
	requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/token?grant_type=password", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)

	// A used link goes back with an error the client can show.
	base, frag = fragmentOf(t, openEmailLink(t, srv, url.Values{"token": {created.Token}, "type": {"signup"}, "project": {p.ID}}))
	if base != "https://app.example.com" || frag.Get("error_code") != "otp_expired" || frag.Get("access_token") != "" {
		t.Fatalf("used link redirect = %s # %v", base, frag)
	}

	// Password reset: an address that is not allowed falls back to the site URL.
	out = requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/recover", p.AnonKey, `{"email":"`+email+`"}`, http.StatusOK)
	var recovered struct {
		Token string `json:"reset_token"`
	}
	if err := json.Unmarshal(out, &recovered); err != nil || recovered.Token == "" {
		t.Fatalf("recover: %s", out)
	}
	base, frag = fragmentOf(t, openEmailLink(t, srv, url.Values{"token": {recovered.Token}, "type": {"recovery"}, "project": {p.ID}, "redirect_to": {"https://evil.example.net/steal"}}))
	if base != "https://app.example.com" || frag.Get("type") != "recovery" || frag.Get("access_token") == "" {
		t.Fatalf("recovery redirect = %s # %v", base, frag)
	}
	requestAuthenticated(t, srv.Client(), srv.URL+"/auth/v1/user", p.AnonKey, frag.Get("access_token"))
}

func TestAuthSettingsLinksWithoutSiteURLAnswerJSON(t *testing.T) {
	srv, p, _ := authSettingsServer(t, "auth-settings-json")
	email := "json-" + p.ID + "@example.test"
	out := requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/signup?redirect_to="+url.QueryEscape("https://evil.example.net/"), p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)
	var created struct {
		Token string `json:"confirmation_token"`
	}
	if err := json.Unmarshal(out, &created); err != nil || created.Token == "" {
		t.Fatalf("signup: %s", out)
	}
	resp := openEmailLink(t, srv, url.Values{"token": {created.Token}, "type": {"signup"}, "project": {p.ID}, "redirect_to": {"https://evil.example.net/"}})
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Location") != "" {
		t.Fatalf("with no site URL the link should answer JSON, got %d to %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/token?grant_type=password", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)
}

func TestEmailLinkNamesProject(t *testing.T) {
	s := &Server{smtp: smtpConfig{siteURL: "https://api.example.com"}}
	link, err := url.Parse(s.emailLink("ab12cd34", "tok", "signup", "https://app.example.com/welcome?x=1"))
	if err != nil {
		t.Fatal(err)
	}
	q := link.Query()
	if link.Host != "api.example.com" || link.Path != "/auth/v1/verify" || q.Get("project") != "ab12cd34" || q.Get("token") != "tok" || q.Get("type") != "signup" || q.Get("redirect_to") != "https://app.example.com/welcome?x=1" {
		t.Fatalf("link = %s", link)
	}
	if link, _ := url.Parse(s.emailLink("ab12cd34", "tok", "recovery", "")); link.Query().Has("redirect_to") {
		t.Fatalf("no return address should leave redirect_to out: %s", link)
	}
}
