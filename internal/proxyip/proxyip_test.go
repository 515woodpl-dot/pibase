package proxyip

import (
	"net/http/httptest"
	"testing"
)

func TestClientIP(t *testing.T) {
	trusted, err := Parse("172.18.0.0/16, 10.0.0.5")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, remote, xff, want string
	}{
		{"untrusted peer ignores the header", "203.0.113.9:5000", "198.51.100.1", "203.0.113.9"},
		{"loopback proxy", "127.0.0.1:5000", "198.51.100.1", "198.51.100.1"},
		{"docker bridge proxy", "172.18.0.1:5000", "198.51.100.1", "198.51.100.1"},
		{"client-written entries on the left are ignored", "172.18.0.1:5000", "6.6.6.6, 198.51.100.1", "198.51.100.1"},
		{"chain of our own proxies", "172.18.0.1:5000", "198.51.100.1, 10.0.0.5", "198.51.100.1"},
		{"trusted peer without header", "172.18.0.1:5000", "", "172.18.0.1"},
		{"garbage header", "172.18.0.1:5000", "not-an-ip", "172.18.0.1"},
		{"ipv6 client", "[::1]:5000", "2001:db8::1", "2001:db8::1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.remote
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := ClientIP(r, trusted); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
	for _, bad := range []string{"nope", "10.0.0.0/99", "1.2.3"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}
