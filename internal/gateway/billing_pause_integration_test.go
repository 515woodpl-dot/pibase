package gateway

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
)

// A project whose owner stopped paying is paused: every part of the API
// answers 402 with a plain reason, and nothing changes in its data. Clearing
// the pause brings it straight back.
func TestPausedProjectIsRefused(t *testing.T) {
	srv, p, adminURL := authSettingsServer(t, "billing-paused")
	ctx := context.Background()
	controlURL, _ := url.Parse(adminURL)
	controlURL.Path = "/_control"
	conn, err := pgx.Connect(ctx, controlURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	setPaused := func(on bool) {
		t.Helper()
		q := `UPDATE control.projects SET paused_at=NULL WHERE id=$1`
		if on {
			q = `UPDATE control.projects SET paused_at=now() WHERE id=$1`
		}
		if _, err := conn.Exec(ctx, q, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	// Sign-up without email confirmation, so the user can sign in right away.
	if _, err := control.SetAuthSettings(ctx, adminURL, p.ID, control.AuthSettings{ConfirmEmail: false}); err != nil {
		t.Fatal(err)
	}
	email := "paused-" + p.ID + "@example.test"
	requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/signup", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)

	setPaused(true)
	body := requestAuthJSON(t, srv.Client(), http.MethodGet, srv.URL+"/rest/v1/missing_table", p.AnonKey, "", http.StatusPaymentRequired)
	if !strings.Contains(string(body), "paused because its billing has lapsed") {
		t.Fatalf("paused answer should explain itself: %s", body)
	}
	requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/token?grant_type=password", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusPaymentRequired)
	requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/signup", p.ServiceKey, `{"email":"x`+email+`","password":"correct horse battery staple"}`, http.StatusPaymentRequired)
	requestAuthJSON(t, srv.Client(), http.MethodGet, srv.URL+"/storage/v1/bucket", p.ServiceKey, "", http.StatusPaymentRequired)
	// Email links name the project instead of sending a key; they stop too.
	resp, err := srv.Client().Get(srv.URL + "/auth/v1/verify?token=x&type=signup&project=" + p.ID)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("email link on a paused project: %d", resp.StatusCode)
	}
	// A wrong key still gets 401: a pause tells nothing to strangers.
	requestAuthJSON(t, srv.Client(), http.MethodGet, srv.URL+"/rest/v1/missing_table", "not-a-key", "", http.StatusUnauthorized)

	setPaused(false)
	requestAuthJSON(t, srv.Client(), http.MethodPost, srv.URL+"/auth/v1/token?grant_type=password", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)
}
