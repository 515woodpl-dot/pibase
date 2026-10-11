package gateway

import (
	"fmt"
	"log/slog"
	"net/smtp"
	"os"
	"strconv"
	"strings"
)

type smtpConfig struct {
	host    string
	port    int
	user    string
	pass    string
	from    string
	siteURL string
}

func smtpFromEnv() smtpConfig {
	port, _ := strconv.Atoi(os.Getenv("PIBASE_SMTP_PORT"))
	if port == 0 {
		port = 587
	}
	siteURL := strings.TrimRight(os.Getenv("PIBASE_SITE_URL"), "/")
	if siteURL == "" {
		// The droplet setup and pi-funnel.sh record the gateway's public
		// address here; without either, email links would have no host.
		siteURL = strings.TrimRight(os.Getenv("PIBASE_PUBLIC_URL"), "/")
	}
	return smtpConfig{
		host:    os.Getenv("PIBASE_SMTP_HOST"),
		port:    port,
		user:    os.Getenv("PIBASE_SMTP_USER"),
		pass:    os.Getenv("PIBASE_SMTP_PASS"),
		from:    os.Getenv("PIBASE_SMTP_FROM"),
		siteURL: siteURL,
	}
}

func (c smtpConfig) configured() bool { return c.host != "" && c.from != "" }

func (c smtpConfig) send(to, subject, body string) error {
	addr := fmt.Sprintf("%s:%d", c.host, c.port)
	msg := []byte(
		"From: " + c.from + "\r\n" +
			"To: " + to + "\r\n" +
			"Subject: " + subject + "\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n" +
			"\r\n" +
			body)
	var a smtp.Auth
	if c.user != "" {
		a = smtp.PlainAuth("", c.user, c.pass, c.host)
	}
	return smtp.SendMail(addr, a, c.from, []string{to}, msg)
}

// sendConfirmationEmail sends a signup confirmation email if SMTP is configured.
// It runs asynchronously; a failed send is logged but does not fail the signup.
func (s *Server) sendConfirmationEmail(to, token string) {
	if !s.smtp.configured() {
		return
	}
	link := s.smtp.siteURL + "/auth/v1/verify?token=" + token + "&type=confirmation"
	body := "Confirm your email address:\n\n" + link + "\n\nThis link expires in 24 hours."
	go func() {
		if err := s.smtp.send(to, "Confirm your email", body); err != nil {
			slog.Warn("confirmation email failed", "error", err)
		}
	}()
}

// sendRecoveryEmail sends a password-reset email if SMTP is configured.
// It runs asynchronously; a failed send is logged but does not fail the recover call.
func (s *Server) sendRecoveryEmail(to, token string) {
	if !s.smtp.configured() {
		return
	}
	link := s.smtp.siteURL + "/auth/v1/verify?token=" + token + "&type=recovery"
	body := "Reset your password:\n\n" + link + "\n\nThis link expires in 1 hour.\n\nIf you did not request a reset, ignore this email."
	go func() {
		if err := s.smtp.send(to, "Reset your password", body); err != nil {
			slog.Warn("recovery email failed", "error", err)
		}
	}()
}
