package gateway

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
)

// projectAuthSettings reads how a project's app users sign up and where email
// links may send them back to. It is read only on sign-up and email-link
// requests, so ordinary data requests do not pay for it.
func (s *Server) projectAuthSettings(ctx context.Context, projectID string) (control.AuthSettings, error) {
	var a control.AuthSettings
	var site *string
	err := s.control.QueryRow(ctx, `SELECT auth_confirm_email, auth_site_url, auth_redirect_urls FROM control.project_catalog WHERE id=$1`, projectID).
		Scan(&a.ConfirmEmail, &site, &a.RedirectURLs)
	if err != nil {
		return control.AuthSettings{}, fmt.Errorf("read auth settings: %w", err)
	}
	if site != nil {
		a.SiteURL = *site
	}
	return a, nil
}

// returnAddress picks where an email link sends someone once it has done its
// job: the address the app asked for when the project allows it, otherwise
// the project's site URL. Empty means the link answers with JSON instead.
func returnAddress(a control.AuthSettings, requested string) string {
	requested = strings.TrimSpace(requested)
	if requested != "" && a.RedirectAllowed(requested) {
		return requested
	}
	return a.SiteURL
}

// emailLink builds the address in a confirmation or recovery email. The link
// names the project because a browser opening it sends no apikey header.
func (s *Server) emailLink(projectID, token, kind, returnTo string) string {
	q := url.Values{"token": {token}, "type": {kind}, "project": {projectID}}
	if returnTo != "" {
		q.Set("redirect_to", returnTo)
	}
	return s.smtp.siteURL + "/auth/v1/verify?" + q.Encode()
}

// redirectWithSession sends the browser back to the app with the new session
// in the URL fragment, where the JavaScript client picks it up.
func redirectWithSession(w http.ResponseWriter, r *http.Request, target string, session map[string]any, kind string) {
	expiresIn, _ := session["expires_in"].(int)
	frag := url.Values{
		"access_token":  {fmt.Sprint(session["access_token"])},
		"refresh_token": {fmt.Sprint(session["refresh_token"])},
		"token_type":    {"bearer"},
		"expires_in":    {strconv.Itoa(expiresIn)},
		"expires_at":    {strconv.FormatInt(time.Now().Add(time.Duration(expiresIn)*time.Second).Unix(), 10)},
		"type":          {kind},
	}
	http.Redirect(w, r, withFragment(target, frag.Encode()), http.StatusSeeOther)
}

// redirectWithError sends the browser back to the app with the reason a link
// did not work, in the same fragment fields the JavaScript client reads.
func redirectWithError(w http.ResponseWriter, r *http.Request, target, code, description string) {
	frag := url.Values{"error": {"access_denied"}, "error_code": {code}, "error_description": {description}}
	http.Redirect(w, r, withFragment(target, frag.Encode()), http.StatusSeeOther)
}

func withFragment(target, fragment string) string {
	if i := strings.IndexByte(target, '#'); i >= 0 {
		target = target[:i]
	}
	return target + "#" + fragment
}
