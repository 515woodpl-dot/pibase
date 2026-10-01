package gateway

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/adminsql"
	"github.com/515woodpl-dot/pibase/internal/control"
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
	if s.adminDatabaseURL != "" {
		if projects, err := control.ProjectStats(ctx, s.adminDatabaseURL, s.storageRoot); err == nil {
			fmt.Fprintln(w, "# HELP pibase_project_database_bytes PostgreSQL database size by project.\n# TYPE pibase_project_database_bytes gauge")
			for _, p := range projects {
				fmt.Fprintf(w, "pibase_project_database_bytes{project_id=%q} %d\n", p.ID, p.DatabaseBytes)
			}
			fmt.Fprintln(w, "# HELP pibase_project_storage_bytes Object file size by project.\n# TYPE pibase_project_storage_bytes gauge")
			for _, p := range projects {
				fmt.Fprintf(w, "pibase_project_storage_bytes{project_id=%q} %d\n", p.ID, p.StorageBytes)
			}
			fmt.Fprintln(w, "# HELP pibase_project_requests_30d Requests counted during the last 30 days.\n# TYPE pibase_project_requests_30d gauge")
			for _, p := range projects {
				fmt.Fprintf(w, "pibase_project_requests_30d{project_id=%q} %d\n", p.ID, p.Requests30d)
			}
		}
	}
}

func (s *Server) openPoolCount() int {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	return len(s.pools) + len(s.projects)
}

func contextWithTimeout(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), timeout)
}

func (s *Server) operatorAuthorized(r *http.Request) bool {
	if s.operatorToken == "" || s.adminDatabaseURL == "" {
		return false
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
		return false
	}
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return len(provided) == len(s.operatorToken) && subtle.ConstantTimeCompare([]byte(provided), []byte(s.operatorToken)) == 1
}

func (s *Server) requireOperator(w http.ResponseWriter, r *http.Request) bool {
	if !s.operatorAuthorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "operator authentication required", http.StatusUnauthorized)
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	return true
}

func (s *Server) dashboardHandler(w http.ResponseWriter, r *http.Request) {
	if s.operatorToken == "" || s.adminDatabaseURL == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(dashboardHTML))
}

func (s *Server) operatorProjects(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	projects, err := control.ProjectStats(r.Context(), s.adminDatabaseURL, s.storageRoot)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) operatorSQL(w http.ResponseWriter, r *http.Request) {
	if !s.requireOperator(w, r) {
		return
	}
	var body struct {
		SQL string `json:"sql"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := decoder.Decode(&body); err != nil {
		writeAPIError(w, httpError(http.StatusBadRequest, "invalid SQL request"))
		return
	}
	result, err := adminsql.Run(r.Context(), s.adminDatabaseURL, r.PathValue("id"), body.SQL)
	if err != nil {
		writeAPIError(w, httpError(http.StatusBadRequest, err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, result)
}
