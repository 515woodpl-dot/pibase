package adminserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/billing"
	"github.com/515woodpl-dot/pibase/internal/control"
)

// Billing for dashboard accounts through Stripe. It is on when the operator
// sets a Stripe secret key and at least one plan; otherwise every account
// works as before, limited only by MaxProjectsPerAccount.

func (s *Server) billingEnabled() bool { return s.Stripe != nil && len(s.Billing.Plans) > 0 }

// standing reads an account's billing standing.
func (s *Server) standing(ctx context.Context, accountID string) (control.Standing, control.AccountBilling, error) {
	b, err := control.GetAccountBilling(ctx, s.AdminURL, accountID)
	if err != nil {
		return control.Standing{}, b, err
	}
	return control.StandingOf(b, s.Billing, time.Now()), b, nil
}

// projectAllowance is how many projects the account may own and the database
// size of a new one (0: the server default). When it may create none, msg
// says why.
func (s *Server) projectAllowance(ctx context.Context, accountID string) (limit int, dbBytes int64, msg string, err error) {
	if !s.billingEnabled() {
		return s.MaxProjectsPerAccount, 0, "", nil
	}
	st, _, err := s.standing(ctx, accountID)
	if err != nil {
		return 0, 0, "", err
	}
	limit, dbBytes = control.ProjectAllowance(st, s.Billing, s.MaxProjectsPerAccount)
	switch st.State {
	case control.StateGrace:
		msg = "Your trial or subscription has ended. Choose a plan under Billing to create projects."
	case control.StatePaused:
		msg = "Your projects are paused because billing has lapsed. Choose a plan under Billing to resume them."
	}
	return limit, dbBytes, msg, nil
}

// checkProjectRoom writes the refusal and returns false when an account may
// not add another project. Callers hold accountProjectsMu.
func (s *Server) checkProjectRoom(w http.ResponseWriter, r *http.Request, accountID string) (dbBytes int64, ok bool) {
	limit, dbBytes, msg, err := s.projectAllowance(r.Context(), accountID)
	if err != nil {
		fail(w, err)
		return 0, false
	}
	if msg != "" {
		http.Error(w, msg, http.StatusPaymentRequired)
		return 0, false
	}
	n, err := control.CountOwnedProjects(r.Context(), s.AdminURL, accountID)
	if err != nil {
		fail(w, err)
		return 0, false
	}
	if n >= limit {
		hint := "delete a project or ask the operator"
		if s.billingEnabled() {
			hint = "delete a project or choose a bigger plan under Billing"
		}
		http.Error(w, fmt.Sprintf("project limit reached (%d on your plan); %s", limit, hint), http.StatusForbidden)
		return 0, false
	}
	return dbBytes, true
}

// billingInfo tells the dashboard the account's plan, trial and the plans
// it can choose.
func (s *Server) billingInfo(w http.ResponseWriter, r *http.Request) {
	id, ok := getIdentity(r.Context())
	if !ok || id.kind != accountKind {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	if !s.billingEnabled() {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
		return
	}
	st, _, err := s.standing(r.Context(), id.accountID)
	if err != nil {
		fail(w, err)
		return
	}
	n, err := control.CountOwnedProjects(r.Context(), s.AdminURL, id.accountID)
	if err != nil {
		fail(w, err)
		return
	}
	limit, _ := control.ProjectAllowance(st, s.Billing, s.MaxProjectsPerAccount)
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": true, "standing": st, "plans": s.Billing.Plans, "projects": n, "project_limit": limit,
		"trial_days": s.Billing.TrialDays, "trial_projects": s.Billing.TrialProjects, "trial_database_mb": s.Billing.TrialDBMB, "grace_days": s.Billing.GraceDays,
	})
}

func (s *Server) billingReturnURL(r *http.Request) string {
	base := strings.TrimRight(s.DashboardURL, "/")
	if base == "" {
		scheme := "https"
		if r.TLS == nil && (s.Env == "development" || s.Env == "test") {
			scheme = "http"
		}
		base = scheme + "://" + r.Host
	}
	return base + "/app"
}

// billingCheckout starts Stripe Checkout for a plan.
func (s *Server) billingCheckout(w http.ResponseWriter, r *http.Request) {
	id, ok := getIdentity(r.Context())
	if !ok || id.kind != accountKind || !s.billingEnabled() {
		http.Error(w, "billing is not available", http.StatusNotFound)
		return
	}
	var b struct {
		Plan string `json:"plan"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	plan, ok := s.Billing.PlanByName(b.Plan)
	if !ok {
		http.Error(w, "unknown plan", http.StatusBadRequest)
		return
	}
	if !s.requireVerified(w, r) {
		return
	}
	st, acct, err := s.standing(r.Context(), id.accountID)
	if err != nil {
		fail(w, err)
		return
	}
	// One subscription per account: someone already paying changes plan in
	// the portal, so they are never charged twice.
	if st.State == control.StateActive && acct.CustomerID != "" {
		url, err := s.Stripe.PortalURL(r.Context(), acct.CustomerID, s.billingReturnURL(r)+"#billing")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"url": url})
		return
	}
	customer := acct.CustomerID
	if customer == "" {
		account, err := control.ResolveSession(r.Context(), s.AdminURL, sessionCookieValue(r))
		if err != nil {
			http.Error(w, "not authenticated", http.StatusUnauthorized)
			return
		}
		if customer, err = s.Stripe.CreateCustomer(r.Context(), id.accountID, account.Email, account.Organization); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := control.SetStripeCustomer(r.Context(), s.AdminURL, id.accountID, customer); err != nil {
			fail(w, err)
			return
		}
	}
	back := s.billingReturnURL(r)
	url, err := s.Stripe.CheckoutURL(r.Context(), customer, id.accountID, plan.PriceID, back+"#billing=done", back+"#billing")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

// billingPortal opens Stripe's customer portal: card, invoices, plan, cancel.
func (s *Server) billingPortal(w http.ResponseWriter, r *http.Request) {
	id, ok := getIdentity(r.Context())
	if !ok || id.kind != accountKind || !s.billingEnabled() {
		http.Error(w, "billing is not available", http.StatusNotFound)
		return
	}
	_, acct, err := s.standing(r.Context(), id.accountID)
	if err != nil {
		fail(w, err)
		return
	}
	if acct.CustomerID == "" {
		http.Error(w, "choose a plan first", http.StatusConflict)
		return
	}
	url, err := s.Stripe.PortalURL(r.Context(), acct.CustomerID, s.billingReturnURL(r)+"#billing")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

// billingWebhook receives Stripe's signed events and stores the
// subscription's current state, read back from Stripe.
func (s *Server) billingWebhook(w http.ResponseWriter, r *http.Request) {
	if !s.billingEnabled() || s.StripeWebhookSecret == "" {
		http.Error(w, "billing is not set up", http.StatusServiceUnavailable)
		return
	}
	payload, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	ev, err := billing.VerifyWebhook(payload, r.Header.Get("Stripe-Signature"), s.StripeWebhookSecret, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var obj struct {
		ID                string            `json:"id"`
		Object            string            `json:"object"`
		Subscription      string            `json:"subscription"`
		ClientReferenceID string            `json:"client_reference_id"`
		Metadata          map[string]string `json:"metadata"`
	}
	_ = json.Unmarshal(ev.Data.Object, &obj)
	subID := ""
	switch ev.Type {
	case "checkout.session.completed":
		subID = obj.Subscription
	case "customer.subscription.created", "customer.subscription.updated", "customer.subscription.deleted",
		"customer.subscription.paused", "customer.subscription.resumed":
		subID = obj.ID
	case "invoice.paid", "invoice.payment_failed":
		subID = obj.Subscription
	}
	if subID == "" {
		w.WriteHeader(http.StatusOK) // not an event billing needs
		return
	}
	if err := s.syncSubscription(r.Context(), subID); err != nil {
		slog.Warn("stripe webhook", "event", ev.ID, "type", ev.Type, "error", err)
		// A 5xx makes Stripe send the event again later.
		http.Error(w, "could not apply the event", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// syncSubscription reads a subscription from Stripe, stores it on its
// account and pauses or resumes that account's projects to match.
func (s *Server) syncSubscription(ctx context.Context, subID string) error {
	sub, err := s.Stripe.GetSubscription(ctx, subID)
	if err != nil {
		return err
	}
	accountID := sub.Metadata["account_id"]
	if accountID == "" {
		if accountID, err = control.AccountIDForCustomer(ctx, s.AdminURL, sub.Customer); err != nil {
			if errors.Is(err, control.ErrNotFound) {
				slog.Info("stripe subscription for an unknown customer ignored", "subscription", sub.ID)
				return nil
			}
			return err
		}
	}
	if _, err := control.GetAccountBilling(ctx, s.AdminURL, accountID); err != nil {
		if errors.Is(err, control.ErrNotFound) {
			slog.Info("stripe subscription for a deleted account ignored", "subscription", sub.ID)
			return nil
		}
		return err
	}
	planName := ""
	if p, ok := s.Billing.PlanByPrice(sub.PriceID()); ok {
		planName = p.Name
	}
	if err := control.SyncSubscription(ctx, s.AdminURL, accountID, control.Subscription{
		ID: sub.ID, CustomerID: sub.Customer, Status: sub.Status, PriceID: sub.PriceID(),
		CurrentPeriodEnd: sub.PeriodEnd(), CancelAtPeriodEnd: sub.CancelAtPeriodEnd,
	}, planName); err != nil {
		return err
	}
	_, _, err = control.ApplyBilling(ctx, s.AdminURL, s.Billing, time.Now(), accountID)
	return err
}

// cancelSubscriptionOf stops charging an account that is being deleted.
func (s *Server) cancelSubscriptionOf(ctx context.Context, accountID string) error {
	if s.Stripe == nil {
		return nil
	}
	b, err := control.GetAccountBilling(ctx, s.AdminURL, accountID)
	if err != nil || b.SubscriptionID == "" || b.SubscriptionStatus == "canceled" || b.SubscriptionStatus == "incomplete_expired" {
		return nil
	}
	return s.Stripe.CancelSubscription(ctx, b.SubscriptionID)
}

// operatorSetBilling exempts an account from billing, or charges it again.
func (s *Server) operatorSetBilling(w http.ResponseWriter, r *http.Request) {
	if ident, ok := getIdentity(r.Context()); !ok || ident.kind != opKind {
		http.Error(w, "operator token required", http.StatusForbidden)
		return
	}
	var b struct {
		Exempt *bool `json:"exempt"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	if b.Exempt == nil {
		http.Error(w, "exempt is required", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	if err := control.SetBillingExempt(r.Context(), s.AdminURL, id, *b.Exempt); err != nil {
		if errors.Is(err, control.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		fail(w, err)
		return
	}
	if s.billingEnabled() {
		if _, _, err := control.ApplyBilling(r.Context(), s.AdminURL, s.Billing, time.Now(), id); err != nil {
			fail(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// RunBilling pauses and resumes projects as trials and grace periods run
// out, every interval until ctx ends. Webhooks handle changes at once; this
// catches the ones that only time brings.
func (s *Server) RunBilling(ctx context.Context, interval time.Duration) {
	if !s.billingEnabled() {
		return
	}
	tick := func() {
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		paused, resumed, err := control.ApplyBilling(c, s.AdminURL, s.Billing, time.Now(), "")
		if err != nil {
			slog.Warn("billing sweep", "error", err)
		} else if paused > 0 || resumed > 0 {
			slog.Info("billing sweep", "paused_projects", paused, "resumed_projects", resumed)
		}
	}
	tick()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}
