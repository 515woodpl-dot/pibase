package adminserver

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
)

// Forgotten passwords for dashboard accounts.
//
// With email set up (PIBASE_SMTP_* and PIBASE_DASHBOARD_URL), "Forgot password?"
// sends a link valid for an hour to <dashboard>/#reset=<token>. The token sits
// in the URL fragment, so browsers never send it to servers or in Referer
// headers. The answer is the same whether or not the account exists. Without
// email, the page tells the person to ask the operator, who can set a new
// password from the operator view or with `pibase account-password-reset`.

// resetEmailAvailable reports whether reset links can be emailed.
func (s *Server) resetEmailAvailable() bool {
	return s.SendMail != nil && s.DashboardURL != ""
}

func (s *Server) authRecover(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.Env != "test" && !s.recoverLimit.Allow("ip:"+ip, 5, 15*time.Minute) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var b struct {
		Email string `json:"email"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(b.Email))
	if at := strings.IndexByte(email, '@'); at < 1 || at == len(email)-1 || len(email) > 254 || strings.ContainsAny(email, " \t\r\n") {
		http.Error(w, "enter a valid email address", http.StatusBadRequest)
		return
	}
	if !s.resetEmailAvailable() {
		writeJSON(w, http.StatusOK, map[string]any{"email_sent": false})
		return
	}
	// At most 3 links per address per hour, so nobody can flood an inbox.
	if s.Env == "test" || s.recoverLimit.Allow("email:"+email, 3, time.Hour) {
		token, found, err := control.CreatePasswordReset(r.Context(), s.AdminURL, email)
		if err != nil {
			fail(w, err)
			return
		}
		if found {
			link := strings.TrimRight(s.DashboardURL, "/") + "/#reset=" + url.QueryEscape(token)
			body := "Someone asked to reset the password of your pibase dashboard account.\n\n" +
				"Choose a new password here (the link works once, for one hour):\n\n" + link +
				"\n\nIf it wasn't you, ignore this email; your password stays the same.\n"
			go func() {
				if err := s.SendMail(email, "Reset your pibase password", body); err != nil {
					slog.Warn("password reset email failed", "error", err)
				}
			}()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"email_sent": true})
}

func (s *Server) authReset(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.Env != "test" && !s.recoverLimit.Allow("reset:"+ip, 10, 15*time.Minute) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var b struct {
		Token           string `json:"token"`
		Password        string `json:"password"`
		PasswordConfirm string `json:"password_confirm"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	if b.Password != b.PasswordConfirm {
		http.Error(w, "the two passwords do not match", http.StatusBadRequest)
		return
	}
	if b.Token == "" || len(b.Token) > 128 {
		http.Error(w, control.ErrInvalidResetToken.Error(), http.StatusBadRequest)
		return
	}
	email, err := control.ResetPassword(r.Context(), s.AdminURL, b.Token, b.Password)
	if err != nil {
		if errors.Is(err, control.ErrInvalidResetToken) || strings.Contains(err.Error(), "password must be") {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fail(w, err)
		return
	}
	s.loginFailures.Reset(email)
	writeJSON(w, http.StatusOK, map[string]string{"email": email})
}

// operatorSetPassword lets the operator give an account a new password, for
// people who can't receive a reset email.
func (s *Server) operatorSetPassword(w http.ResponseWriter, r *http.Request) {
	if ident, ok := getIdentity(r.Context()); !ok || ident.kind != opKind {
		http.Error(w, "operator token required", http.StatusForbidden)
		return
	}
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	if err := control.SetAccountPassword(r.Context(), s.AdminURL, b.Email, b.Password); err != nil {
		if errors.Is(err, control.ErrNotFound) {
			http.Error(w, "no account with that email", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.loginFailures.Reset(strings.ToLower(strings.TrimSpace(b.Email)))
	w.WriteHeader(http.StatusNoContent)
}

// authResetInfo tells the landing page whether "Forgot password?" can email a
// link or must send people to the operator.
func (s *Server) authResetInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"email": s.resetEmailAvailable()})
}
