package gateway

import (
	"context"
	"crypto/sha256"
	"fmt"
	"github.com/515woodpl-dot/pibase/internal/proxyip"
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
	// RemoteAddr is supplied by the HTTP server. X-Forwarded-For is caller
	// controlled, so it is read only when the peer is loopback or listed in
	// PIBASE_TRUSTED_PROXIES (the reverse proxy in front of the gateway).
	return s.authRate(who, email, proxyip.ClientIP(r, s.trustedProxies))
}

func (s *Server) enforceRefreshRate(who project, tokenDigest string) error {
	if !s.allowRequest(who.id, time.Now()) {
		return httpError(http.StatusTooManyRequests, "project request rate exceeded")
	}
	return s.authRate(who, "refresh:"+tokenDigest, "")
}

func (s *Server) authRate(who project, identity, ip string) error {
	normalized := strings.ToLower(strings.TrimSpace(identity))
	subject := fmt.Sprintf("%x", sha256.Sum256([]byte(normalized)))
	now := time.Now()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	if now.Sub(s.lastAuthRateSweep) >= time.Minute {
		for key, window := range s.authRates {
			if now.Sub(window.started) >= time.Minute {
				delete(s.authRates, key)
			}
		}
		s.lastAuthRateSweep = now
	}
	keys := []struct {
		key   string
		limit int
	}{{who.id + "|subject|" + subject, 10}}
	if ip != "" {
		keys = append(keys, struct {
			key   string
			limit int
		}{who.id + "|ip|" + ip, 120})
	}
	missing := 0
	for _, item := range keys {
		window, found := s.authRates[item.key]
		if !found {
			missing++
		}
		if !window.started.IsZero() && now.Sub(window.started) < time.Minute && window.count >= item.limit {
			return httpError(http.StatusTooManyRequests, "authentication rate limit exceeded")
		}
	}
	if len(s.authRates)+missing > 10000 {
		return httpError(http.StatusTooManyRequests, "authentication rate limit exceeded")
	}
	for _, item := range keys {
		window := s.authRates[item.key]
		if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
			window = rateWindow{started: now}
		}
		window.count++
		s.authRates[item.key] = window
	}
	return nil
}
