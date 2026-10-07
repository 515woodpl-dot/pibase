package gateway

import "testing"

func TestStorageBucketNameAcceptsOrdinaryNames(t *testing.T) {
	for _, name := range []string{"box", "2026-files", "x", "0", "avatars", "my_bucket.v2", "Exports-100"} {
		if got, err := storageBucketName(name); err != nil || got != name {
			t.Errorf("storageBucketName(%q) = %q, %v; want accepted", name, got, err)
		}
	}
}

func TestStorageBucketNameRejectsUnsafeNames(t *testing.T) {
	for _, raw := range []string{"", ".", "..", "%2e%2e", "%2E.", "a/b", "a%2fb", `a\b`, "a%5cb", "a%00b", "a b", "%252e%252e"} {
		if got, err := storageBucketName(raw); err == nil {
			t.Errorf("storageBucketName(%q) = %q; want rejected", raw, got)
		}
	}
}
