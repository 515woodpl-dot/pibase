// Package proxyip finds a request's client address behind trusted reverse
// proxies, for rate limits keyed by address.
package proxyip

import (
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Parse reads a comma-separated list of IPs and CIDRs.
func Parse(v string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, f := range strings.Split(v, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.Contains(f, "/") {
			ip := net.ParseIP(f)
			if ip == nil {
				return nil, fmt.Errorf("invalid trusted proxy %q", f)
			}
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(f)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q", f)
		}
		out = append(out, n)
	}
	return out, nil
}

// Trusted reports whether ip is loopback or inside one of the networks.
func Trusted(ip net.IP, networks []*net.IPNet) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range networks {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the request's client address. X-Forwarded-For is honoured
// only when the direct peer is trusted, and then read from the nearest hop:
// the first address that is not one of our own proxies is the client.
// Anything further left was written by the client and is ignored.
func ClientIP(r *http.Request, networks []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !Trusted(net.ParseIP(host), networks) {
		return host
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			break
		}
		host = ip.String()
		if !Trusted(ip, networks) {
			break
		}
	}
	return host
}
