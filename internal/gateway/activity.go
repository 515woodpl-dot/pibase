package gateway

import (
	"bufio"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Request activity feeds the dashboard's per-service charts. Each request is
// counted in memory by (project, minute, service, status class) and flushed to
// control.project_activity in one statement every activityFlushInterval, so the
// hot path never waits on Postgres.
const (
	activityFlushInterval = 10 * time.Second
	activityRetention     = 8 * 24 * time.Hour
	// activityMaxKeys bounds memory if Postgres is unreachable for a long time.
	activityMaxKeys = 50000
)

type activityKey struct {
	project string
	minute  int64 // unix seconds, truncated to the minute
	service string
	class   int16 // 1..5 for 1xx..5xx
}

type activityRecorder struct {
	mu        sync.Mutex
	counts    map[activityKey]int64
	dropped   int64
	lastPrune time.Time
}

func newActivityRecorder() *activityRecorder {
	return &activityRecorder{counts: make(map[activityKey]int64)}
}

func (a *activityRecorder) add(project, service string, status int, now time.Time) {
	class := int16(status / 100)
	if a == nil || project == "" || service == "" || class < 1 || class > 5 {
		return
	}
	k := activityKey{project: project, minute: now.Unix() / 60 * 60, service: service, class: class}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.counts[k]; !ok && len(a.counts) >= activityMaxKeys {
		a.dropped++
		return
	}
	a.counts[k]++
}

// take swaps out the pending counts.
func (a *activityRecorder) take() map[activityKey]int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending := a.counts
	a.counts = make(map[activityKey]int64)
	return pending
}

// restore puts counts back after a failed flush so they are retried.
func (a *activityRecorder) restore(pending map[activityKey]int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k, n := range pending {
		if _, ok := a.counts[k]; !ok && len(a.counts) >= activityMaxKeys {
			a.dropped += n
			continue
		}
		a.counts[k] += n
	}
}

// activityService maps a gateway path to the dashboard service it belongs to.
func activityService(path string) string {
	switch {
	case strings.HasPrefix(path, "/rest/v1/"):
		return "database"
	case strings.HasPrefix(path, "/auth/v1/"):
		return "auth"
	case strings.HasPrefix(path, "/storage/v1/"):
		return "storage"
	case strings.HasPrefix(path, "/realtime/v1/"):
		return "realtime"
	}
	return ""
}

// flushActivity writes pending counts. Rows for projects deleted since the
// requests were counted are skipped rather than failing the whole batch.
func (s *Server) flushActivity(ctx context.Context) error {
	pending := s.activity.take()
	if len(pending) == 0 {
		return s.pruneActivity(ctx)
	}
	projects := make([]string, 0, len(pending))
	minutes := make([]time.Time, 0, len(pending))
	services := make([]string, 0, len(pending))
	classes := make([]int16, 0, len(pending))
	counts := make([]int64, 0, len(pending))
	for k, n := range pending {
		projects = append(projects, k.project)
		minutes = append(minutes, time.Unix(k.minute, 0).UTC())
		services = append(services, k.service)
		classes = append(classes, k.class)
		counts = append(counts, n)
	}
	_, err := s.control.Exec(ctx, `INSERT INTO control.project_activity(project_id,minute,service,status_class,requests)
SELECT u.project_id,u.minute,u.service,u.status_class,u.requests
FROM unnest($1::text[],$2::timestamptz[],$3::text[],$4::smallint[],$5::bigint[]) AS u(project_id,minute,service,status_class,requests)
WHERE EXISTS (SELECT 1 FROM control.project_catalog c WHERE c.id=u.project_id)
ON CONFLICT (project_id,minute,service,status_class)
DO UPDATE SET requests=control.project_activity.requests+EXCLUDED.requests`,
		projects, minutes, services, classes, counts)
	if err != nil {
		s.activity.restore(pending)
		return err
	}
	return s.pruneActivity(ctx)
}

func (s *Server) pruneActivity(ctx context.Context) error {
	s.activity.mu.Lock()
	due := time.Since(s.activity.lastPrune) >= time.Hour
	if due {
		s.activity.lastPrune = time.Now()
	}
	s.activity.mu.Unlock()
	if !due {
		return nil
	}
	_, err := s.control.Exec(ctx, `DELETE FROM control.project_activity WHERE minute < $1`, time.Now().Add(-activityRetention))
	return err
}

func (s *Server) runActivityFlusher(ctx context.Context) {
	ticker := time.NewTicker(activityFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := s.flushActivity(fctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("flush request activity", "error", err)
			}
			cancel()
		}
	}
}

// requestActivity is attached to each request's context. Project resolution
// fills in the project so failed requests are attributed too.
type requestActivity struct {
	mu      sync.Mutex
	project string
}

type activityContextKey struct{}

func noteActivityProject(ctx context.Context, projectID string) {
	if a, ok := ctx.Value(activityContextKey{}).(*requestActivity); ok {
		a.mu.Lock()
		a.project = projectID
		a.mu.Unlock()
	}
}

// observeActivity wraps the gateway handler and counts every request that
// resolved to a project.
func (s *Server) observeActivity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service := activityService(r.URL.Path)
		if service == "" {
			next.ServeHTTP(w, r)
			return
		}
		info := &requestActivity{}
		sw := &statusWriter{ResponseWriter: w}
		sw.onHijack = func() {
			// A WebSocket upgrade is counted when it succeeds, not when the
			// connection eventually closes.
			info.mu.Lock()
			project := info.project
			info.mu.Unlock()
			s.activity.add(project, service, http.StatusSwitchingProtocols, time.Now())
		}
		next.ServeHTTP(sw, r.WithContext(context.WithValue(r.Context(), activityContextKey{}, info)))
		if sw.hijacked {
			return
		}
		info.mu.Lock()
		project := info.project
		info.mu.Unlock()
		status := sw.status
		if status == 0 {
			status = http.StatusOK
		}
		s.activity.add(project, service, status, time.Now())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status   int
	hijacked bool
	onHijack func()
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	conn, rw, err := h.Hijack()
	if err == nil {
		w.hijacked = true
		if w.onHijack != nil {
			w.onHijack()
		}
	}
	return conn, rw, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
