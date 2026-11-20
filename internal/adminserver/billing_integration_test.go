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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/billing"
	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
)

// fakeStripe stands in for the parts of the Stripe API pibase calls.
type fakeStripe struct {
	mu        sync.Mutex
	customers map[string]string // id -> account id
	subs      map[string]map[string]any
	checkouts []url.Values
	canceled  []string
	n         int
}

func newFakeStripe() *fakeStripe {
	return &fakeStripe{customers: map[string]string{}, subs: map[string]map[string]any{}}
}

func (f *fakeStripe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if user, _, ok := r.BasicAuth(); !ok || user != "sk_test_fake" {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
		return
	}
	_ = r.ParseForm()
	f.n++
	switch {
	case r.Method == "POST" && r.URL.Path == "/v1/customers":
		id := fmt.Sprintf("cus_test%d_%d", time.Now().UnixNano(), f.n) // unique across runs
		f.customers[id] = r.PostForm.Get("metadata[account_id]")
		writeJSON(w, 200, map[string]string{"id": id})
	case r.Method == "POST" && r.URL.Path == "/v1/checkout/sessions":
		f.checkouts = append(f.checkouts, r.PostForm)
		writeJSON(w, 200, map[string]string{"url": "https://checkout.stripe.test/c/" + fmt.Sprint(f.n)})
	case r.Method == "POST" && r.URL.Path == "/v1/billing_portal/sessions":
		writeJSON(w, 200, map[string]string{"url": "https://billing.stripe.test/p/" + r.PostForm.Get("customer")})
	case strings.HasPrefix(r.URL.Path, "/v1/subscriptions/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/subscriptions/")
		sub, ok := f.subs[id]
		if !ok {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"message":"no such subscription"}}`))
			return
		}
		if r.Method == "DELETE" {
			sub["status"] = "canceled"
			f.canceled = append(f.canceled, id)
		}
		writeJSON(w, 200, sub)
	default:
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"message":"unknown path"}}`))
	}
}

// setSub creates or changes a subscription the way a customer's actions in
// Checkout or the portal would.
func (f *fakeStripe) setSub(id, customer, accountID, price, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subs[id] = map[string]any{
		"id": id, "customer": customer, "status": status, "cancel_at_period_end": false,
		"metadata": map[string]string{"account_id": accountID},
		"items":    map[string]any{"data": []any{map[string]any{"current_period_end": time.Now().Add(30 * 24 * time.Hour).Unix(), "price": map[string]string{"id": price}}}},
	}
}

func TestBillingLifecycle(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	if err := control.Bootstrap(ctx, adminURL, "billing-pw"); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(adminURL)
	parsed.User = url.UserPassword("authenticator", "billing-pw")
	parsed.Path = "/_control"
	fake := newFakeStripe()
	stripeSrv := httptest.NewServer(fake)
	defer stripeSrv.Close()
	stripe := billing.New("sk_test_fake")
	stripe.API = stripeSrv.URL
	plans, _ := control.ParsePlans("Starter:price_starter:1:100:$12 a month;Pro:price_pro:3:300:$25 a month")
	cfg := control.BillingConfig{Plans: plans, TrialDays: 14, TrialProjects: 1, TrialDBMB: 50, GraceDays: 7}
	root := t.TempDir()
	s, err := New(Config{AdminURL: adminURL, DatabaseURL: parsed.String(), Token: "billing-op",
		StorageRoot: filepath.Join(root, "f"), BackupRoot: filepath.Join(root, "b"),
		MasterKey: bytes.Repeat([]byte{7}, 32), BackupKey: bytes.Repeat([]byte{9}, 32), Env: "test",
		DashboardURL: "https://app.example.test", Stripe: stripe, StripeWebhookSecret: "whsec_fake", Billing: cfg})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	controlURL, _ := url.Parse(adminURL)
	controlURL.Path = "/_control"
	db, err := pgx.Connect(ctx, controlURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)

	var cookie *http.Cookie
	call := func(method, path, body string, want int) []byte {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		for _, c := range resp.Cookies() {
			if c.Name == "pibase_session" {
				cookie = c
			}
		}
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, want, data)
		}
		return data
	}
	webhook := func(eventType string, object map[string]any, secret string, want int) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"id": "evt_" + eventType, "type": eventType, "data": map[string]any{"object": object}})
		req, _ := http.NewRequest("POST", hs.URL+"/_pibase/billing/webhook", bytes.NewReader(payload))
		req.Header.Set("Stripe-Signature", billing.SignWebhook(payload, secret, time.Now()))
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("webhook %s: %d, want %d", eventType, resp.StatusCode, want)
		}
	}
	type info struct {
		Standing     control.Standing `json:"standing"`
		ProjectLimit int              `json:"project_limit"`
	}
	state := func() info {
		t.Helper()
		var i info
		_ = json.Unmarshal(call("GET", "/_pibase/billing", "", 200), &i)
		return i
	}
	paused := func(projectID string) bool {
		t.Helper()
		var p bool
		if err := db.QueryRow(ctx, `SELECT paused_at IS NOT NULL FROM control.projects WHERE id=$1`, projectID).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	sweep := func() {
		t.Helper()
		if _, _, err := control.ApplyBilling(ctx, adminURL, cfg, time.Now(), ""); err != nil {
			t.Fatal(err)
		}
	}

	// 1. A new account starts a 14-day trial with one small project.
	email := fmt.Sprintf("billing%d@example.test", time.Now().UnixNano())
	signup, _ := json.Marshal(map[string]string{"email": email, "password": "long enough pw", "password_confirm": "long enough pw", "organization": "Billing Org"})
	var account control.Account
	_ = json.Unmarshal(call("POST", "/_pibase/auth/signup", string(signup), 201), &account)
	defer func() { _ = control.DeleteAccount(ctx, adminURL, account.ID) }()
	if i := state(); i.Standing.State != control.StateTrial || i.ProjectLimit != 1 || i.Standing.TrialEndsAt == nil || time.Until(*i.Standing.TrialEndsAt) < 13*24*time.Hour {
		t.Fatalf("new account: %+v", i)
	}
	var project control.Project
	_ = json.Unmarshal(call("POST", "/_pibase/projects", `{"name":"trial site"}`, 201), &project)
	var limit int64
	_ = db.QueryRow(ctx, `SELECT max_database_bytes FROM control.projects WHERE id=$1`, project.ID).Scan(&limit)
	if limit != 50<<20 {
		t.Fatalf("trial project should hold 50 MB, got %d", limit)
	}
	call("POST", "/_pibase/projects", `{"name":"second"}`, 403)

	// 2. The trial ends: grace, no new projects, the site still works.
	if _, err := db.Exec(ctx, `UPDATE control.accounts SET trial_ends_at=now()-interval '1 day' WHERE id=$1`, account.ID); err != nil {
		t.Fatal(err)
	}
	sweep()
	if i := state(); i.Standing.State != control.StateGrace || paused(project.ID) {
		t.Fatalf("after trial: %+v paused=%v", i, paused(project.ID))
	}
	call("POST", "/_pibase/projects", `{"name":"in grace"}`, 402)

	// 3. Grace runs out: the project pauses.
	if _, err := db.Exec(ctx, `UPDATE control.accounts SET trial_ends_at=now()-interval '8 days' WHERE id=$1`, account.ID); err != nil {
		t.Fatal(err)
	}
	sweep()
	if i := state(); i.Standing.State != control.StatePaused || !paused(project.ID) {
		t.Fatalf("after grace: %+v paused=%v", i, paused(project.ID))
	}

	// 4. They choose Pro: Checkout for the right price and customer...
	var out map[string]string
	_ = json.Unmarshal(call("POST", "/_pibase/billing/checkout", `{"plan":"pro"}`, 200), &out)
	if !strings.HasPrefix(out["url"], "https://checkout.stripe.test/") || len(fake.checkouts) != 1 {
		t.Fatalf("checkout: %v", out)
	}
	co := fake.checkouts[0]
	customer := co.Get("customer")
	if co.Get("line_items[0][price]") != "price_pro" || co.Get("mode") != "subscription" || fake.customers[customer] != account.ID ||
		co.Get("success_url") != "https://app.example.test/app#billing=done" || co.Get("subscription_data[metadata][account_id]") != account.ID {
		t.Fatalf("checkout form: %v", co)
	}
	call("POST", "/_pibase/billing/checkout", `{"plan":"nope"}`, 400)
	// ...and Stripe's signed webhook makes it active and resumes the project.
	fake.setSub("sub_1", customer, account.ID, "price_pro", "active")
	webhook("checkout.session.completed", map[string]any{"object": "checkout.session", "subscription": "sub_1"}, "whsec_wrong", 400)
	if paused(project.ID) == false {
		t.Fatal("a forged webhook must change nothing")
	}
	webhook("checkout.session.completed", map[string]any{"object": "checkout.session", "subscription": "sub_1"}, "whsec_fake", 200)
	i := state()
	_ = db.QueryRow(ctx, `SELECT max_database_bytes FROM control.projects WHERE id=$1`, project.ID).Scan(&limit)
	if i.Standing.State != control.StateActive || i.Standing.Plan != "Pro" || i.ProjectLimit != 3 || paused(project.ID) || limit != 300<<20 {
		t.Fatalf("after paying: %+v paused=%v limit=%d", i, paused(project.ID), limit)
	}
	call("POST", "/_pibase/projects", `{"name":"second site"}`, 201)
	// Paying customers change plan in the portal, never a second Checkout.
	_ = json.Unmarshal(call("POST", "/_pibase/billing/checkout", `{"plan":"starter"}`, 200), &out)
	if !strings.HasPrefix(out["url"], "https://billing.stripe.test/p/") || len(fake.checkouts) != 1 {
		t.Fatalf("plan change should open the portal: %v", out)
	}
	_ = json.Unmarshal(call("POST", "/_pibase/billing/portal", ``, 200), &out)
	if out["url"] != "https://billing.stripe.test/p/"+customer {
		t.Fatalf("portal: %v", out)
	}

	// 5. A failed card (past_due) keeps things running while Stripe retries.
	fake.setSub("sub_1", customer, account.ID, "price_pro", "past_due")
	webhook("invoice.payment_failed", map[string]any{"object": "invoice", "subscription": "sub_1"}, "whsec_fake", 200)
	if i := state(); i.Standing.State != control.StateActive || paused(project.ID) {
		t.Fatalf("past due: %+v", i)
	}

	// 6. They cancel: a grace period, then a pause; the operator can exempt them.
	fake.setSub("sub_1", customer, account.ID, "price_pro", "canceled")
	webhook("customer.subscription.deleted", map[string]any{"object": "subscription", "id": "sub_1"}, "whsec_fake", 200)
	if i := state(); i.Standing.State != control.StateGrace || paused(project.ID) {
		t.Fatalf("after cancel: %+v", i)
	}
	if _, err := db.Exec(ctx, `UPDATE control.accounts SET lapsed_at=now()-interval '8 days' WHERE id=$1`, account.ID); err != nil {
		t.Fatal(err)
	}
	sweep()
	if !paused(project.ID) {
		t.Fatal("eight days after cancelling the project should be paused")
	}
	opReq := func(method, path, body string, want int) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer billing-op")
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d want %d", method, path, resp.StatusCode, want)
		}
	}
	opReq("PATCH", "/_pibase/operator/accounts/"+account.ID+"/billing", `{"exempt":true}`, 204)
	if i := state(); i.Standing.State != control.StateExempt || paused(project.ID) {
		t.Fatalf("exempt: %+v paused=%v", i, paused(project.ID))
	}
	opReq("PATCH", "/_pibase/operator/accounts/"+account.ID+"/billing", `{"exempt":false}`, 204)

	// 7. Deleting the account cancels its live subscription first.
	fake.setSub("sub_1", customer, account.ID, "price_pro", "active")
	webhook("customer.subscription.updated", map[string]any{"object": "subscription", "id": "sub_1"}, "whsec_fake", 200)
	call("DELETE", "/_pibase/auth/account", `{"confirm":"`+email+`"}`, 204)
	if len(fake.canceled) != 1 || fake.canceled[0] != "sub_1" {
		t.Fatalf("subscription should be cancelled on delete: %v", fake.canceled)
	}
	// Events for a deleted account are acknowledged, not retried forever.
	webhook("customer.subscription.deleted", map[string]any{"object": "subscription", "id": "sub_1"}, "whsec_fake", 200)
	// Events billing does not use are acknowledged too.
	webhook("customer.created", map[string]any{"object": "customer", "id": customer}, "whsec_fake", 200)
}
