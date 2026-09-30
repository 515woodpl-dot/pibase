package gateway

import (
	"context"
	"net/http"
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
