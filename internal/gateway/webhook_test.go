package gateway

import (
	"context"
	"net/netip"
	"testing"
	"time"
)

type fixedResolver map[string][]netip.Addr

func (r fixedResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	return r[host], nil
}

func TestWebhookURLRejectsNonPublicTargets(t *testing.T) {
	resolver := fixedResolver{"public.test": {netip.MustParseAddr("1.1.1.1")}, "private.test": {netip.MustParseAddr("10.0.0.1")}, "metadata.test": {netip.MustParseAddr("169.254.169.254")}}
	if _, err := validateWebhookURL(context.Background(), resolver, "https://public.test/hook"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"http://127.0.0.1/x", "http://[::1]/x", "http://private.test/x", "http://metadata.test/x"} {
		if _, err := validateWebhookURL(context.Background(), resolver, raw); err == nil {
			t.Fatalf("accepted unsafe URL %q", raw)
		}
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	if got := retryDelay(99); got != time.Hour {
		t.Fatalf("delay = %s, want 1h", got)
	}
}
