package control

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// NormalizePublicURL checks a project's API address, such as
// https://pibase.client.com, and returns it without a trailing slash. Empty
// clears it. The address only changes what the dashboard shows; the gateway
// finds the project from its key, whatever name the request used.
func NormalizePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || len(raw) > 255 {
		return "", fmt.Errorf("API address must look like https://pibase.example.com, with no path")
	}
	return u.Scheme + "://" + u.Host, nil
}

// SetPublicURL sets or clears the project's API address.
func SetPublicURL(ctx context.Context, adminURL, projectID, raw string) (string, error) {
	normalized, err := NormalizePublicURL(raw)
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
	tag, err := conn.Exec(ctx, `UPDATE control.projects SET public_url=$1 WHERE id=$2`, value, projectID)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return normalized, nil
}

// PublicURL returns the project's own API address, or "" when unset.
func PublicURL(ctx context.Context, adminURL, projectID string) (string, error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	var value *string
	if err := conn.QueryRow(ctx, `SELECT public_url FROM control.projects WHERE id=$1`, projectID).Scan(&value); err != nil {
		return "", ErrNotFound
	}
	if value == nil {
		return "", nil
	}
	return *value, nil
}
