package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"
)

type rateWindow struct {
	started time.Time
	count   int
}

func (s *Server) allowRequest(projectID string, now time.Time) bool {
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	window := s.rates[projectID]
	if window.started.IsZero() || now.Sub(window.started) >= time.Second {
		window = rateWindow{started: now}
	}
	if window.count >= 100 {
		return false
	}
	window.count++
	s.rates[projectID] = window
	return true
}

func (s *Server) enforceRate(who principal) error {
	if !s.allowRequest(who.project.id, time.Now()) {
		return httpError(http.StatusTooManyRequests, "project request rate exceeded")
	}
	return nil
}

func (s *Server) recordRequest(ctx context.Context, projectID string) {
	_, _ = s.control.Exec(ctx, `INSERT INTO control.project_usage(project_id,day,requests)
VALUES($1,current_date,1)
ON CONFLICT(project_id,day) DO UPDATE SET requests=control.project_usage.requests+1`, projectID)
}

func (s *Server) enforceAuthRate(who project, r *http.Request, email string) error {
	ip := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if comma := strings.IndexByte(ip, ','); comma >= 0 {
		ip = ip[:comma]
	}
	if ip == "" {
		ip = r.RemoteAddr
	}
	normalized := strings.ToLower(strings.TrimSpace(email))
	now := time.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	for _, key := range []string{who.id + "|ip|" + ip, who.id + "|email|" + normalized} {
		window := s.authRates[key]
		if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
			window = rateWindow{started: now}
		}
		if window.count >= 10 {
			return httpError(http.StatusTooManyRequests, "authentication rate limit exceeded")
		}
		window.count++
		s.authRates[key] = window
	}
	return nil
}
