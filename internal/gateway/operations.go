package gateway

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"time"
)

func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 2*time.Second)
	defer cancel()
	up := 1
	if err := s.control.Ping(ctx); err != nil {
		up = 0
	}
	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintf(w, "# HELP pibase_up Whether the gateway can reach PostgreSQL.\n# TYPE pibase_up gauge\npibase_up %d\n", up)
	fmt.Fprintf(w, "# HELP pibase_uptime_seconds Gateway process uptime.\n# TYPE pibase_uptime_seconds gauge\npibase_uptime_seconds %.0f\n", time.Since(s.startedAt).Seconds())
	fmt.Fprintf(w, "# HELP pibase_http_requests_total Requests accepted since process start.\n# TYPE pibase_http_requests_total counter\npibase_http_requests_total %d\n", s.requestCount.Load())
	fmt.Fprintf(w, "# HELP pibase_go_heap_bytes Current Go heap allocation.\n# TYPE pibase_go_heap_bytes gauge\npibase_go_heap_bytes %d\n", memory.Alloc)
	fmt.Fprintf(w, "# HELP pibase_project_pools_open Cached project database pools.\n# TYPE pibase_project_pools_open gauge\npibase_project_pools_open %d\n", s.openPoolCount())
}

func (s *Server) openPoolCount() int {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	return len(s.pools) + len(s.projects)
}

func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}
