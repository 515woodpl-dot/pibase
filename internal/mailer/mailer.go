// Package mailer sends plain-text email through the SMTP server configured
// with PIBASE_SMTP_HOST, _PORT, _USER, _PASS and _FROM.
package mailer

import (
	"fmt"
	"net/smtp"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Host, User, Pass, From string
	Port                   int
}

// FromEnv reads the SMTP settings; Port defaults to 587.
func FromEnv() Config {
	port, _ := strconv.Atoi(os.Getenv("PIBASE_SMTP_PORT"))
	if port == 0 {
		port = 587
	}
	return Config{
		Host: os.Getenv("PIBASE_SMTP_HOST"),
		Port: port,
		User: os.Getenv("PIBASE_SMTP_USER"),
		Pass: os.Getenv("PIBASE_SMTP_PASS"),
		From: os.Getenv("PIBASE_SMTP_FROM"),
	}
}

// Configured reports whether a host and sender are set.
func (c Config) Configured() bool { return c.Host != "" && c.From != "" }

// Send delivers one message. Addresses and subject must not contain line
// breaks; callers pass validated values only.
func (c Config) Send(to, subject, body string) error {
	for _, v := range []string{to, subject, c.From} {
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("invalid email header")
		}
	}
	msg := []byte("From: " + c.From + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: " + subject + "\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n" +
		"\r\n" + body)
	var a smtp.Auth
	if c.User != "" {
		a = smtp.PlainAuth("", c.User, c.Pass, c.Host)
	}
	return smtp.SendMail(fmt.Sprintf("%s:%d", c.Host, c.Port), a, c.From, []string{to}, msg)
}
