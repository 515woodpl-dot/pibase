package billing

import (
	"strings"
	"testing"
	"time"
)

func TestVerifyWebhook(t *testing.T) {
	now := time.Now()
	body := []byte(`{"id":"evt_1","type":"customer.subscription.updated","data":{"object":{"id":"sub_1"}}}`)
	ev, err := VerifyWebhook(body, SignWebhook(body, "whsec_test", now), "whsec_test", now)
	if err != nil || ev.Type != "customer.subscription.updated" || !strings.Contains(string(ev.Data.Object), "sub_1") {
		t.Fatalf("signed event: %+v %v", ev, err)
	}
	// Stripe may send several v1 signatures while a secret is being rolled.
	two := "t=" + strings.Split(SignWebhook(body, "whsec_test", now), ",")[0][2:] + ",v1=00ff," + strings.Split(SignWebhook(body, "whsec_test", now), ",")[1]
	if _, err := VerifyWebhook(body, two, "whsec_test", now); err != nil {
		t.Fatalf("second signature should match: %v", err)
	}
	for name, c := range map[string]struct {
		body   []byte
		header string
		secret string
		now    time.Time
	}{
		"wrong secret":      {body, SignWebhook(body, "whsec_other", now), "whsec_test", now},
		"changed body":      {[]byte(strings.Replace(string(body), "sub_1", "sub_2", 1)), SignWebhook(body, "whsec_test", now), "whsec_test", now},
		"replayed later":    {body, SignWebhook(body, "whsec_test", now.Add(-10*time.Minute)), "whsec_test", now},
		"no header":         {body, "", "whsec_test", now},
		"no secret set":     {body, SignWebhook(body, "", now), "", now},
		"signature not hex": {body, "t=1,v1=zz", "whsec_test", now},
	} {
		if _, err := VerifyWebhook(c.body, c.header, c.secret, c.now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSubscriptionPeriodEnd(t *testing.T) {
	var s Subscription
	if s.PeriodEnd() != nil {
		t.Fatal("no period end should be nil")
	}
	s.Items.Data = append(s.Items.Data, struct {
		CurrentPeriodEnd int64 `json:"current_period_end"`
		Price            struct {
			ID string `json:"id"`
		} `json:"price"`
	}{CurrentPeriodEnd: 1800000000})
	if s.PeriodEnd() == nil || s.PeriodEnd().Unix() != 1800000000 {
		t.Fatal("newer API versions put the period end on the item")
	}
	s.CurrentPeriodEnd = 1700000000
	if s.PeriodEnd().Unix() != 1700000000 {
		t.Fatal("older API versions put it on the subscription")
	}
}
