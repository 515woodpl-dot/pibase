package control

import (
	"testing"
	"time"
)

func TestParsePlans(t *testing.T) {
	plans, err := ParsePlans("Starter:price_A:1:1024:$12 a month; Pro:price_B:3:5120:$25 a month: billed monthly")
	if err != nil || len(plans) != 2 || plans[1].Projects != 3 || plans[1].DBMB != 5120 || plans[1].Label != "$25 a month: billed monthly" {
		t.Fatalf("plans = %+v, %v", plans, err)
	}
	for _, bad := range []string{"Starter:prod_A:1:1024:x", "Starter:price_A:0:1024:x", "Starter:price_A:1:x:y", "Starter:price_A:1", "A:price_A:1:1:x;a:price_B:1:1:x", "A:price_A:1:1:x;B:price_A:1:1:x"} {
		if _, err := ParsePlans(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	if plans, err := ParsePlans(""); err != nil || len(plans) != 0 {
		t.Fatalf("empty: %v %v", plans, err)
	}
}

func TestStandingOf(t *testing.T) {
	cfg := BillingConfig{Plans: []Plan{{Name: "Pro", PriceID: "price_P", Projects: 3, DBMB: 5120}}, TrialDays: 14, TrialProjects: 1, TrialDBMB: 512, GraceDays: 7}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	at := func(d time.Duration) *time.Time { t := start.Add(d); return &t }
	base := AccountBilling{CreatedAt: start, TrialEndsAt: at(14 * day)}
	cases := []struct {
		name string
		b    AccountBilling
		now  time.Time
		want string
	}{
		{"exempt never pays", AccountBilling{Exempt: true, CreatedAt: start}, start.Add(400 * day), StateExempt},
		{"day 3 of trial", base, start.Add(3 * day), StateTrial},
		{"trial ended, in grace", base, start.Add(15 * day), StateGrace},
		{"trial and grace over", base, start.Add(22 * day), StatePaused},
		{"paying", func() AccountBilling {
			b := base
			b.SubscriptionID, b.SubscriptionStatus, b.Plan = "sub_1", "active", "Pro"
			return b
		}(), start.Add(300 * day), StateActive},
		{"card failing, Stripe retrying", func() AccountBilling {
			b := base
			b.SubscriptionID, b.SubscriptionStatus = "sub_1", "past_due"
			return b
		}(), start.Add(300 * day), StateActive},
		{"canceled 3 days ago", func() AccountBilling {
			b := base
			b.SubscriptionID, b.SubscriptionStatus, b.LapsedAt = "sub_1", "canceled", at(100*day)
			return b
		}(), start.Add(103 * day), StateGrace},
		{"canceled 8 days ago", func() AccountBilling {
			b := base
			b.SubscriptionID, b.SubscriptionStatus, b.LapsedAt = "sub_1", "canceled", at(100*day)
			return b
		}(), start.Add(108 * day), StatePaused},
		{"canceled during trial keeps the trial", func() AccountBilling {
			b := base
			b.SubscriptionID, b.SubscriptionStatus, b.LapsedAt = "sub_1", "canceled", at(2*day)
			return b
		}(), start.Add(10 * day), StateTrial},
		{"no trial end stored uses sign-up date", AccountBilling{CreatedAt: start}, start.Add(13 * day), StateTrial},
		{"first payment still pending is not paid", func() AccountBilling {
			b := base
			b.SubscriptionID, b.SubscriptionStatus = "sub_1", "incomplete"
			return b
		}(), start.Add(30 * day), StatePaused},
	}
	for _, c := range cases {
		if got := StandingOf(c.b, cfg, c.now); got.State != c.want {
			t.Errorf("%s: state %s, want %s", c.name, got.State, c.want)
		}
	}
	grace := StandingOf(base, cfg, start.Add(15*day))
	if grace.GraceEndsAt == nil || !grace.GraceEndsAt.Equal(start.Add(21*day)) {
		t.Errorf("grace should end 7 days after the trial: %v", grace.GraceEndsAt)
	}
	for _, c := range []struct {
		st       Standing
		projects int
		bytes    int64
	}{
		{Standing{State: StateExempt}, 2, 0},
		{Standing{State: StateTrial}, 1, 512 << 20},
		{Standing{State: StateActive, Plan: "pro"}, 3, 5120 << 20},
		{Standing{State: StateActive, Plan: "Removed"}, 2, 0},
		{Standing{State: StateGrace}, 0, 0},
		{Standing{State: StatePaused}, 0, 0},
	} {
		if p, b := ProjectAllowance(c.st, cfg, 2); p != c.projects || b != c.bytes {
			t.Errorf("%+v: allowance %d/%d, want %d/%d", c.st, p, b, c.projects, c.bytes)
		}
	}
}
