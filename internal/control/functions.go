package control

import (
	"context"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// NormalizeFunctionsURL checks the address a project's /functions/v1 calls are
// forwarded to and returns it without a trailing slash. An empty value turns
// forwarding off. When private is false the host must be a public address (an
// IP literal is checked here; names are checked again each time the gateway
// connects). Private addresses, such as the site's server on the same
// machine, can only be set by the operator.
func NormalizeFunctionsURL(raw string, private bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("functions URL must be an absolute http or https address without credentials, a query or a fragment")
	}
	if len(raw) > 512 {
		return "", fmt.Errorf("functions URL is too long")
	}
	if !private {
		host := u.Hostname()
		if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
			return "", fmt.Errorf("functions URL must be a public address; ask the operator to allow a private one")
		}
		if ip, err := netip.ParseAddr(host); err == nil && !publicAddr(ip) {
			return "", fmt.Errorf("functions URL must be a public address; ask the operator to allow a private one")
		}
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsValid() && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() && !ip.IsMulticast() && !ip.IsUnspecified()
}

// SetFunctionsURL sets or clears where a project's /functions/v1 calls go.
func SetFunctionsURL(ctx context.Context, adminURL, projectID, raw string, private bool) (string, error) {
	normalized, err := NormalizeFunctionsURL(raw, private)
	if err != nil {
		return "", err
	}
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	var value *string
	if normalized != "" {
		value = &normalized
	}
	tag, err := conn.Exec(ctx, `UPDATE control.projects SET functions_url=$1, functions_private=$2 WHERE id=$3`, value, private && normalized != "", projectID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return normalized, nil
}

// FunctionsURL returns the project's forwarding address and whether the
// operator allowed it to be a private address.
func FunctionsURL(ctx context.Context, adminURL, projectID string) (string, bool, error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return "", false, err
	}
	defer conn.Close(ctx)
	var value *string
	var private bool
	if err := conn.QueryRow(ctx, `SELECT functions_url, functions_private FROM control.projects WHERE id=$1`, projectID).Scan(&value, &private); err != nil {
		return "", false, ErrNotFound
	}
	if value == nil {
		return "", false, nil
	}
	return *value, private, nil
}
