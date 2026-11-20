package adminserver

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
)

// Spam protection for public dashboard sign-up, in layers:
//
//  1. The sign-up form carries a signed timestamp from GET signup-form. A
//     submission needs a genuine one at least minFormSeconds old, so a script
//     must load the form and wait like a person would.
//  2. A hidden "leave this empty" field that people never see; bots fill it.
//  3. Throwaway email domains are refused.
//  4. Each address may create a few accounts a day, and the whole server a
//     limited number an hour, so a burst cannot fill a small machine.
//  5. With email set up, a new account must open the link in its welcome
//     email before it can create projects; unconfirmed accounts without
//     projects are removed after two days.
//  6. The operator can list accounts and delete spam ones.

const (
	minFormSeconds   = 3
	maxFormAge       = 2 * time.Hour
	signupsPerIPDay  = 3
	defaultHourlyCap = 30
)

// throwawayDomains are common disposable-inbox services. PIBASE_BLOCKED_EMAIL_DOMAINS
// adds more.
var throwawayDomains = []string{
	"mailinator.com", "guerrillamail.com", "guerrillamail.net", "sharklasers.com", "grr.la",
	"10minutemail.com", "10minutemail.net", "temp-mail.org", "tempmail.com", "tempmail.net",
	"yopmail.com", "yopmail.net", "trashmail.com", "trashmail.de", "getnada.com", "nada.email",
	"dispostable.com", "maildrop.cc", "throwawaymail.com", "mailnesia.com", "mintemail.com",
	"fakeinbox.com", "emailondeck.com", "mohmal.com", "tempr.email", "discard.email",
	"spamgourmet.com", "mailcatch.com", "moakt.com", "tempmailo.com", "burnermail.io",
	"inboxkitten.com", "mailpoof.com", "33mail.com", "tempinbox.com", "spambox.us",
}

// signupState holds what the guard remembers between requests.
type signupState struct {
	daily       *rateLimiter
	hourly      *rateLimiter
	resend      *rateLimiter
	mu          sync.Mutex
	lastCleanup time.Time
}

func newSignupState() *signupState {
	return &signupState{daily: newRateLimiter(), hourly: newRateLimiter(), resend: newRateLimiter()}
}

// verifyEmailRequired reports whether new accounts must confirm their email.
// It needs email to be set up and the dashboard's own address for the link.
func (s *Server) verifyEmailRequired() bool { return s.resetEmailAvailable() }

func (s *Server) signupHourlyCap() int {
	if s.SignupHourlyLimit > 0 {
		return s.SignupHourlyLimit
	}
	return defaultHourlyCap
}

func (s *Server) formTokenAt(t time.Time) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	mac := hmac.New(sha256.New, s.MasterKey)
	mac.Write([]byte("pibase-signup-form:" + ts))
	return ts + "." + hex.EncodeToString(mac.Sum(nil))
}

// checkFormToken accepts a token this server signed between minFormSeconds
// and maxFormAge ago.
func (s *Server) checkFormToken(token string, now time.Time) error {
	ts, _, ok := strings.Cut(token, ".")
	unix, err := strconv.ParseInt(ts, 10, 64)
	if !ok || err != nil || !hmac.Equal([]byte(token), []byte(s.formTokenAt(time.Unix(unix, 0)))) {
		return errors.New("The sign-up form expired. Reload the page and try again.")
	}
	age := now.Sub(time.Unix(unix, 0))
	if age < minFormSeconds*time.Second {
		return errors.New("That was quick. Wait a moment, then press Create account again.")
	}
	if age > maxFormAge {
		return errors.New("The sign-up form expired. Reload the page and try again.")
	}
	return nil
}

func (s *Server) blockedEmailDomain(email string) bool {
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	domain := strings.ToLower(strings.TrimSpace(email[at+1:]))
	list := append([]string{}, throwawayDomains...)
	list = append(list, s.BlockedEmailDomains...)
	for _, d := range list {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && (domain == d || strings.HasSuffix(domain, "."+d)) {
			return true
		}
	}
	return false
}

// signupForm gives the landing page a signed form token and tells it whether
// sign-up is open and whether new accounts must confirm their email.
func (s *Server) signupForm(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"open": !s.SignupClosed, "token": s.formTokenAt(time.Now()), "verify_email": s.verifyEmailRequired()})
}

// checkSignup runs the spam checks before an account is created. It returns
// the HTTP status and message to refuse with, or 0.
func (s *Server) checkSignup(r *http.Request, ip, email, formToken, trap string) (int, string) {
	if s.SignupClosed {
		return http.StatusForbidden, "Sign-up is closed on this server. Ask the operator for an account."
	}
	if trap != "" {
		// Only bots fill the hidden field. Give nothing away.
		return http.StatusBadRequest, "The account could not be created."
	}
	if s.Env == "test" {
		return 0, ""
	}
	if err := s.checkFormToken(formToken, time.Now()); err != nil {
		return http.StatusBadRequest, err.Error()
	}
	if s.blockedEmailDomain(email) {
		return http.StatusBadRequest, "Use a permanent email address, not a throwaway inbox."
	}
	if s.signup.daily.Count("ip:"+ip, 24*time.Hour) >= signupsPerIPDay {
		return http.StatusTooManyRequests, "Too many accounts were created from your network today. Try again tomorrow."
	}
	if s.signup.hourly.Count("all", time.Hour) >= s.signupHourlyCap() {
		slog.Warn("dashboard sign-up paused: hourly limit reached", "limit", s.signupHourlyCap())
		return http.StatusTooManyRequests, "Sign-up is busy right now. Try again in an hour."
	}
	return 0, ""
}

// noteSignup counts a created account against the limits.
func (s *Server) noteSignup(ip string) {
	s.signup.daily.Allow("ip:"+ip, 1<<30, 24*time.Hour)
	s.signup.hourly.Allow("all", 1<<30, time.Hour)
}

// cleanupUnverified removes abandoned unconfirmed accounts, at most hourly.
func (s *Server) cleanupUnverified() {
	s.signup.mu.Lock()
	if time.Since(s.signup.lastCleanup) < time.Hour {
		s.signup.mu.Unlock()
		return
	}
	s.signup.lastCleanup = time.Now()
	s.signup.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if n, err := control.RemoveStaleUnverifiedAccounts(ctx, s.AdminURL); err != nil {
			slog.Warn("remove unconfirmed accounts", "error", err)
		} else if n > 0 {
			slog.Info("removed unconfirmed dashboard accounts", "count", n)
		}
	}()
}

// sendVerification emails a new confirmation link to the account.
func (s *Server) sendVerification(ctx context.Context, accountID, email string) error {
	token, err := control.CreateEmailVerification(ctx, s.AdminURL, accountID)
	if err != nil || token == "" {
		return err
	}
	link := strings.TrimRight(s.DashboardURL, "/") + "/#verify=" + url.QueryEscape(token)
	body := "Welcome to pibase.\n\nConfirm your email address to start creating projects (the link works once, for two days):\n\n" + link +
		"\n\nIf you didn't sign up, ignore this email; the account is removed after two days.\n"
	go func() {
		if err := s.SendMail(email, "Confirm your pibase account", body); err != nil {
			slog.Warn("confirmation email failed", "error", err)
		}
	}()
	return nil
}

// authVerify confirms an account with the token from its welcome email.
func (s *Server) authVerify(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.Env != "test" && !s.recoverLimit.Allow("verify:"+ip, 20, 15*time.Minute) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var b struct {
		Token string `json:"token"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	email, err := control.VerifyEmail(r.Context(), s.AdminURL, b.Token)
	if err != nil {
		if errors.Is(err, control.ErrInvalidVerifyToken) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"email": email})
}

// authResendVerification emails a fresh link to the signed-in account.
func (s *Server) authResendVerification(w http.ResponseWriter, r *http.Request) {
	token := sessionCookieValue(r)
	account, err := control.ResolveSession(r.Context(), s.AdminURL, token)
	if token == "" || err != nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	if account.EmailVerified {
		writeJSON(w, http.StatusOK, map[string]bool{"email_sent": false, "verified": true})
		return
	}
	if !s.verifyEmailRequired() {
		http.Error(w, "this server cannot send email; ask the operator to confirm your account", http.StatusConflict)
		return
	}
	if s.Env != "test" && !s.signup.resend.Allow(account.ID, 3, time.Hour) {
		http.Error(w, "a link was sent a few times already; check your spam folder or try again in an hour", http.StatusTooManyRequests)
		return
	}
	if err := s.sendVerification(r.Context(), account.ID, account.Email); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"email_sent": true})
}

// requireVerified refuses unconfirmed accounts. Operators always pass.
func (s *Server) requireVerified(w http.ResponseWriter, r *http.Request) bool {
	id, ok := getIdentity(r.Context())
	if !ok || id.kind != accountKind {
		return true
	}
	verified, err := control.AccountVerified(r.Context(), s.AdminURL, id.accountID)
	if err != nil {
		fail(w, err)
		return false
	}
	if !verified {
		http.Error(w, control.ErrEmailNotVerified.Error(), http.StatusForbidden)
		return false
	}
	return true
}

// operatorAccounts lists dashboard accounts for spotting spam.
func (s *Server) operatorAccounts(w http.ResponseWriter, r *http.Request) {
	if ident, ok := getIdentity(r.Context()); !ok || ident.kind != opKind {
		http.Error(w, "operator token required", http.StatusForbidden)
		return
	}
	list, err := control.ListAccounts(r.Context(), s.AdminURL)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// operatorDeleteAccount deletes an account and every project it owns.
func (s *Server) operatorDeleteAccount(w http.ResponseWriter, r *http.Request) {
	if ident, ok := getIdentity(r.Context()); !ok || ident.kind != opKind {
		http.Error(w, "operator token required", http.StatusForbidden)
		return
	}
	var b struct {
		Confirm string `json:"confirm"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	id := r.PathValue("id")
	list, err := control.ListAccounts(r.Context(), s.AdminURL)
	if err != nil {
		fail(w, err)
		return
	}
	var email string
	for _, a := range list {
		if a.ID == id {
			email = a.Email
		}
	}
	if email == "" {
		http.NotFound(w, r)
		return
	}
	if b.Confirm != email {
		http.Error(w, "type the account's email address to confirm deletion", http.StatusBadRequest)
		return
	}
	s.accountProjectsMu.Lock()
	defer s.accountProjectsMu.Unlock()
	if err := control.DeleteAccount(context.WithoutCancel(r.Context()), s.AdminURL, id); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
