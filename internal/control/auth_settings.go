package control

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// AuthSettings controls how a project's app users sign up and where email
// links send them back to.
type AuthSettings struct {
	// ConfirmEmail makes a new user click the link in a confirmation email
	// before they can sign in. When false, signing up signs them in at once.
	ConfirmEmail bool `json:"confirm_email"`
	// SiteURL is the app's own address, such as https://example.com. Email
	// links return people here when the app asks for no other allowed address.
	SiteURL string `json:"site_url"`
	// RedirectURLs are the other addresses an app may ask email links to
	// return to (emailRedirectTo / redirectTo). An entry ending in * allows
	// every address that starts with the rest of it.
	RedirectURLs []string `json:"redirect_urls"`
}

const maxRedirectURLs = 50

// NormalizeSiteURL checks an app address and returns it without a trailing
// slash. Empty clears it.
func NormalizeSiteURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || len(raw) > 512 {
		return "", fmt.Errorf("site URL must look like https://example.com")
	}
	return strings.TrimRight(raw, "/"), nil
}

// NormalizeRedirectURLs checks the allowed return addresses. Blank lines and
// repeats are dropped.
func NormalizeRedirectURLs(raw []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, entry := range raw {
		entry = strings.TrimSpace(entry)
		if entry == "" || seen[entry] {
			continue
		}
		check := strings.TrimSuffix(entry, "*")
		u, err := url.Parse(check)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" ||
			strings.Contains(check, "*") || len(entry) > 512 {
			return nil, fmt.Errorf("redirect URL %q must look like https://example.com/welcome, optionally ending in *", entry)
		}
		if strings.HasSuffix(entry, "*") && u.Path == "" {
			// "https://example.com*" would also match https://example.com.evil.net.
			return nil, fmt.Errorf("redirect URL %q: put a / before the *, as in https://example.com/*", entry)
		}
		seen[entry] = true
		out = append(out, entry)
	}
	if len(out) > maxRedirectURLs {
		return nil, fmt.Errorf("at most %d redirect URLs", maxRedirectURLs)
	}
	return out, nil
}

// RedirectAllowed reports whether target may be used as an email link's
// return address: the site URL (or anything under it) or a listed address.
func (a AuthSettings) RedirectAllowed(target string) bool {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return false
	}
	under := func(base string) bool {
		if target == base {
			return true
		}
		b, err := url.Parse(base)
		if err != nil || !strings.EqualFold(b.Scheme, u.Scheme) || !strings.EqualFold(b.Host, u.Host) {
			return false
		}
		path := strings.TrimRight(b.Path, "/")
		return u.Path == path || strings.HasPrefix(u.Path, path+"/")
	}
	if a.SiteURL != "" && under(a.SiteURL) {
		return true
	}
	for _, entry := range a.RedirectURLs {
		if prefix, ok := strings.CutSuffix(entry, "*"); ok {
			if strings.HasPrefix(target, prefix) {
				return true
			}
		} else if target == entry || target == strings.TrimRight(entry, "/") || strings.TrimRight(target, "/") == entry {
			return true
		}
	}
	return false
}

// SetAuthSettings saves a project's sign-up settings and returns them as stored.
func SetAuthSettings(ctx context.Context, adminURL, projectID string, in AuthSettings) (AuthSettings, error) {
	site, err := NormalizeSiteURL(in.SiteURL)
	if err != nil {
		return AuthSettings{}, err
	}
	redirects, err := NormalizeRedirectURLs(in.RedirectURLs)
	if err != nil {
		return AuthSettings{}, err
	}
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return AuthSettings{}, err
	}
	defer conn.Close(ctx)
	var siteValue *string
	if site != "" {
		siteValue = &site
	}
	tag, err := conn.Exec(ctx, `UPDATE control.projects SET auth_confirm_email=$1, auth_site_url=$2, auth_redirect_urls=$3 WHERE id=$4`,
		in.ConfirmEmail, siteValue, redirects, projectID)
	if err != nil {
		return AuthSettings{}, err
	}
	if tag.RowsAffected() == 0 {
		return AuthSettings{}, ErrNotFound
	}
	return AuthSettings{ConfirmEmail: in.ConfirmEmail, SiteURL: site, RedirectURLs: redirects}, nil
}

// GetAuthSettings returns a project's sign-up settings.
func GetAuthSettings(ctx context.Context, adminURL, projectID string) (AuthSettings, error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return AuthSettings{}, err
	}
	defer conn.Close(ctx)
	var out AuthSettings
	var site *string
	if err := conn.QueryRow(ctx, `SELECT auth_confirm_email, auth_site_url, auth_redirect_urls FROM control.projects WHERE id=$1`, projectID).
		Scan(&out.ConfirmEmail, &site, &out.RedirectURLs); err != nil {
		return AuthSettings{}, ErrNotFound
	}
	if site != nil {
		out.SiteURL = *site
	}
	if out.RedirectURLs == nil {
		out.RedirectURLs = []string{}
	}
	return out, nil
}
