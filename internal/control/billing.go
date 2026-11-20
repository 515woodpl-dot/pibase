package control

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Billing: self-made dashboard accounts start a free trial, then pay for a
// plan through Stripe. When payment stops, a grace period follows; after it
// the account's projects are paused (the gateway refuses them, data is kept)
// until the owner pays again. Exempt accounts (the operator's, and accounts
// that existed before billing) are never charged or paused.

// Plan is one paid plan, matched to a Stripe price.
type Plan struct {
	Name     string `json:"name"`
	PriceID  string `json:"-"`
	Projects int    `json:"projects"`
	DBMB     int64  `json:"database_mb"`
	Label    string `json:"label"`
}

// BillingConfig is the operator's billing settings.
type BillingConfig struct {
	Plans         []Plan
	TrialDays     int
	TrialProjects int
	TrialDBMB     int64
	GraceDays     int
}

// ParsePlans reads PIBASE_PLANS: plans separated by ";", each
// Name:price_id:projects:database_mb:label, for example
// "Starter:price_123:1:1024:$12 a month;Pro:price_456:3:5120:$25 a month".
func ParsePlans(raw string) ([]Plan, error) {
	var plans []Plan
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		f := strings.SplitN(part, ":", 5)
		if len(f) != 5 {
			return nil, fmt.Errorf("plan %q must be Name:price_id:projects:database_mb:label", part)
		}
		name, price := strings.TrimSpace(f[0]), strings.TrimSpace(f[1])
		projects, err1 := strconv.Atoi(strings.TrimSpace(f[2]))
		mb, err2 := strconv.ParseInt(strings.TrimSpace(f[3]), 10, 64)
		if name == "" || !strings.HasPrefix(price, "price_") || err1 != nil || projects < 1 || err2 != nil || mb < 1 {
			return nil, fmt.Errorf("plan %q: need a name, a Stripe price ID (price_...), projects >= 1 and database MB >= 1", part)
		}
		if seen[strings.ToLower(name)] || seen[price] {
			return nil, fmt.Errorf("plan %q is listed twice", name)
		}
		seen[strings.ToLower(name)], seen[price] = true, true
		plans = append(plans, Plan{Name: name, PriceID: price, Projects: projects, DBMB: mb, Label: strings.TrimSpace(f[4])})
	}
	return plans, nil
}

// PlanByName finds a plan, ignoring case.
func (c BillingConfig) PlanByName(name string) (Plan, bool) {
	for _, p := range c.Plans {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Plan{}, false
}

// PlanByPrice finds the plan of a Stripe price.
func (c BillingConfig) PlanByPrice(price string) (Plan, bool) {
	for _, p := range c.Plans {
		if p.PriceID == price {
			return p, true
		}
	}
	return Plan{}, false
}

// AccountBilling is an account's billing record.
type AccountBilling struct {
	AccountID          string
	Email              string
	Exempt             bool
	CreatedAt          time.Time
	TrialEndsAt        *time.Time
	Plan               string
	CustomerID         string
	SubscriptionID     string
	SubscriptionStatus string
	CurrentPeriodEnd   *time.Time
	CancelAtPeriodEnd  bool
	LapsedAt           *time.Time
}

// Billing states.
const (
	StateExempt = "exempt"
	StateTrial  = "trial"
	StateActive = "active"
	StateGrace  = "grace"
	StatePaused = "paused"
)

// Standing is where an account is in its billing life.
type Standing struct {
	State             string     `json:"state"`
	Plan              string     `json:"plan,omitempty"`
	Status            string     `json:"subscription_status,omitempty"`
	TrialEndsAt       *time.Time `json:"trial_ends_at,omitempty"`
	GraceEndsAt       *time.Time `json:"grace_ends_at,omitempty"`
	CurrentPeriodEnd  *time.Time `json:"current_period_end,omitempty"`
	CancelAtPeriodEnd bool       `json:"cancel_at_period_end,omitempty"`
	HasCustomer       bool       `json:"has_customer"`
}

// paying are the Stripe subscription states that keep an account in good
// standing. past_due means Stripe is still retrying a failed payment.
func paying(status string) bool {
	return status == "active" || status == "trialing" || status == "past_due"
}

// StandingOf works out an account's billing state at now.
func StandingOf(b AccountBilling, cfg BillingConfig, now time.Time) Standing {
	st := Standing{Plan: b.Plan, Status: b.SubscriptionStatus, CurrentPeriodEnd: b.CurrentPeriodEnd, CancelAtPeriodEnd: b.CancelAtPeriodEnd, HasCustomer: b.CustomerID != ""}
	if b.Exempt {
		st.State = StateExempt
		return st
	}
	if b.SubscriptionID != "" && paying(b.SubscriptionStatus) {
		st.State = StateActive
		return st
	}
	trialEnd := b.CreatedAt.Add(time.Duration(cfg.TrialDays) * 24 * time.Hour)
	if b.TrialEndsAt != nil {
		trialEnd = *b.TrialEndsAt
	}
	st.TrialEndsAt = &trialEnd
	lapse := trialEnd
	if b.LapsedAt != nil && b.LapsedAt.After(lapse) {
		lapse = *b.LapsedAt
	}
	if now.Before(lapse) {
		st.State = StateTrial
		return st
	}
	grace := lapse.Add(time.Duration(cfg.GraceDays) * 24 * time.Hour)
	st.GraceEndsAt = &grace
	if now.Before(grace) {
		st.State = StateGrace
	} else {
		st.State = StatePaused
	}
	return st
}

// ProjectAllowance is how many projects an account may own and how large a
// new project's database may grow; projects 0 means it may not create any.
func ProjectAllowance(st Standing, cfg BillingConfig, defaultProjects int) (projects int, maxDBBytes int64) {
	switch st.State {
	case StateExempt:
		return defaultProjects, 0
	case StateTrial:
		return cfg.TrialProjects, cfg.TrialDBMB << 20
	case StateActive:
		if p, ok := cfg.PlanByName(st.Plan); ok {
			return p.Projects, p.DBMB << 20
		}
		return defaultProjects, 0
	}
	return 0, 0
}

const billingColumns = `id,email,billing_exempt,created_at,trial_ends_at,COALESCE(billing_plan,''),COALESCE(stripe_customer_id,''),
COALESCE(stripe_subscription_id,''),COALESCE(subscription_status,''),current_period_end,cancel_at_period_end,lapsed_at`

func scanBilling(row pgx.Row) (AccountBilling, error) {
	var b AccountBilling
	err := row.Scan(&b.AccountID, &b.Email, &b.Exempt, &b.CreatedAt, &b.TrialEndsAt, &b.Plan, &b.CustomerID,
		&b.SubscriptionID, &b.SubscriptionStatus, &b.CurrentPeriodEnd, &b.CancelAtPeriodEnd, &b.LapsedAt)
	return b, err
}

// GetAccountBilling reads one account's billing record.
func GetAccountBilling(ctx context.Context, adminURL, accountID string) (AccountBilling, error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return AccountBilling{}, err
	}
	defer conn.Close(ctx)
	b, err := scanBilling(conn.QueryRow(ctx, `SELECT `+billingColumns+` FROM control.accounts WHERE id=$1`, accountID))
	if err == pgx.ErrNoRows {
		return AccountBilling{}, ErrNotFound
	}
	return b, err
}

// AccountIDForCustomer finds the account of a Stripe customer.
func AccountIDForCustomer(ctx context.Context, adminURL, customerID string) (string, error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	var id string
	err = conn.QueryRow(ctx, `SELECT id FROM control.accounts WHERE stripe_customer_id=$1`, customerID).Scan(&id)
	if err == pgx.ErrNoRows {
		return "", ErrNotFound
	}
	return id, err
}

// SetStripeCustomer records the Stripe customer of an account.
func SetStripeCustomer(ctx context.Context, adminURL, accountID, customerID string) error {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `UPDATE control.accounts SET stripe_customer_id=$1 WHERE id=$2`, customerID, accountID)
	return err
}

// SetBillingExempt marks an account as never charged (or charged again).
func SetBillingExempt(ctx context.Context, adminURL, accountID string, exempt bool) error {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tag, err := conn.Exec(ctx, `UPDATE control.accounts SET billing_exempt=$1,
  trial_ends_at=CASE WHEN NOT $1 THEN COALESCE(trial_ends_at, now()) ELSE trial_ends_at END WHERE id=$2`, exempt, accountID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// StartTrial sets a new account's trial end.
func StartTrial(ctx context.Context, adminURL, accountID string, days int) error {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `UPDATE control.accounts SET trial_ends_at=now()+make_interval(days => $1) WHERE id=$2 AND trial_ends_at IS NULL`, days, accountID)
	return err
}

// Subscription is the part of a Stripe subscription billing needs.
type Subscription struct {
	ID                string
	CustomerID        string
	Status            string
	PriceID           string
	CurrentPeriodEnd  *time.Time
	CancelAtPeriodEnd bool
}

// SyncSubscription stores a subscription's latest state on its account. When
// it stops paying, the grace period starts now; when it pays, any lapse clears.
func SyncSubscription(ctx context.Context, adminURL, accountID string, sub Subscription, planName string) error {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	ok := paying(sub.Status)
	_, err = conn.Exec(ctx, `UPDATE control.accounts SET
  stripe_customer_id=COALESCE(NULLIF($2,''),stripe_customer_id),
  stripe_subscription_id=$3, subscription_status=$4, billing_plan=NULLIF($5,''),
  current_period_end=$6, cancel_at_period_end=$7,
  lapsed_at=CASE WHEN $8 THEN NULL ELSE COALESCE(lapsed_at, now()) END
WHERE id=$1`, accountID, sub.CustomerID, sub.ID, sub.Status, planName, sub.CurrentPeriodEnd, sub.CancelAtPeriodEnd, ok)
	return err
}

// ApplyBilling brings every owned project in line with its owner's standing:
// paused past the grace period, resumed once paid, and sized to the plan. It
// gives accounts made while billing was off a trial starting now. It returns
// how many projects it paused and resumed.
func ApplyBilling(ctx context.Context, adminURL string, cfg BillingConfig, now time.Time, onlyAccount string) (paused, resumed int64, err error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close(ctx)
	if _, err = conn.Exec(ctx, `UPDATE control.accounts SET trial_ends_at=now()+make_interval(days => $1)
WHERE trial_ends_at IS NULL AND NOT billing_exempt`, cfg.TrialDays); err != nil {
		return 0, 0, err
	}
	q := `SELECT ` + billingColumns + ` FROM control.accounts`
	args := []any{}
	if onlyAccount != "" {
		q += ` WHERE id=$1`
		args = append(args, onlyAccount)
	}
	rows, err := conn.Query(ctx, q, args...)
	if err != nil {
		return 0, 0, err
	}
	var accounts []AccountBilling
	for rows.Next() {
		b, err := scanBilling(rows)
		if err != nil {
			rows.Close()
			return 0, 0, err
		}
		accounts = append(accounts, b)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, b := range accounts {
		st := StandingOf(b, cfg, now)
		if st.State == StatePaused {
			tag, err := conn.Exec(ctx, `UPDATE control.projects SET paused_at=now() WHERE owner_id=$1 AND paused_at IS NULL`, b.AccountID)
			if err != nil {
				return paused, resumed, err
			}
			paused += tag.RowsAffected()
			continue
		}
		tag, err := conn.Exec(ctx, `UPDATE control.projects SET paused_at=NULL WHERE owner_id=$1 AND paused_at IS NOT NULL`, b.AccountID)
		if err != nil {
			return paused, resumed, err
		}
		resumed += tag.RowsAffected()
		if _, limit := ProjectAllowance(st, cfg, 0); limit > 0 {
			if _, err := conn.Exec(ctx, `UPDATE control.projects SET max_database_bytes=$2 WHERE owner_id=$1 AND max_database_bytes<>$2`, b.AccountID, limit); err != nil {
				return paused, resumed, err
			}
		}
	}
	return paused, resumed, nil
}
