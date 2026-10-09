package gateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxWebhookAttempts = 8

type hostResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// webhookClient resolves and dials only public addresses. Resolution happens in
// DialContext, not only during URL validation, so a DNS answer cannot be
// swapped between validation and connect. Redirects are refused entirely.
func webhookClient(resolver hostResolver) *http.Client {
	return &http.Client{Transport: publicTransport(resolver, 10*time.Second), Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("webhook redirects are not allowed")
	}}
}

// publicTransport connects only to public addresses; see webhookClient.
func publicTransport(resolver hostResolver, headerTimeout time.Duration) *http.Transport {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	return &http.Transport{
		Proxy:                 nil,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := publicIPs(ctx, resolver, host)
			if err != nil {
				return nil, err
			}
			var last error
			for _, ip := range ips {
				conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			return nil, last
		},
	}
}

func validateWebhookURL(ctx context.Context, resolver hostResolver, raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("webhook URL must be an absolute http or https URL without credentials")
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, fmt.Errorf("invalid webhook port")
		}
	}
	if _, err := publicIPs(ctx, resolver, u.Hostname()); err != nil {
		return nil, err
	}
	return u, nil
}

func publicIPs(ctx context.Context, resolver hostResolver, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !isPublicIP(ip) {
			return nil, fmt.Errorf("webhook target is not a public address")
		}
		return []netip.Addr{ip}, nil
	}
	ips, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("resolve webhook host: %w", err)
	}
	for _, ip := range ips {
		if !isPublicIP(ip) {
			return nil, fmt.Errorf("webhook target resolves to a non-public address")
		}
	}
	return ips, nil
}

func isPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

func retryDelay(attempts int) time.Duration {
	if attempts < 1 {
		return time.Minute
	}
	if attempts > 7 {
		return time.Hour
	}
	return time.Minute * time.Duration(1<<(attempts-1))
}

func safeHeader(name, value string) bool {
	return http.CanonicalHeaderKey(name) != "" && !strings.EqualFold(name, "Host") && len(name)+len(value) <= 8192
}
