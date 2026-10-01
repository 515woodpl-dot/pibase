package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	control           *pgxpool.Pool
	projects          []*pgxpool.Pool
	databaseURL       string
	masterKey         []byte
	poolMu            sync.Mutex
	pools             map[string]*pgxpool.Pool
	databaseSlots     chan struct{}
	rateMu            sync.Mutex
	rates             map[string]rateWindow
	authRates         map[string]rateWindow
	exposeAuthTokens  bool
	storageRoot       string
	storageQuotaBytes int64
	adminDatabaseURL  string
	operatorToken     string
	startedAt         time.Time
	requestCount      atomic.Uint64
	realtime          *realtimeHub
}

func New(ctx context.Context, databaseURL string, masterKey []byte, activeProjects int, lightLoad bool) (*Server, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	config.MaxConns = 2
	config.MinConns = 1
	config.MaxConnIdleTime = 30 * time.Second
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET statement_timeout = '5s'`)
		return err
	}
	controlPool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := controlPool.Ping(ctx); err != nil {
		controlPool.Close()
		return nil, err
	}
	storageRoot := os.Getenv("PIBASE_STORAGE_ROOT")
	if storageRoot == "" {
		storageRoot = "/data/files"
	}
	if err := os.MkdirAll(storageRoot, 0o750); err != nil {
		controlPool.Close()
		return nil, fmt.Errorf("create storage root: %w", err)
	}
	storageQuotaBytes := int64(1 << 30)
	if raw := os.Getenv("PIBASE_STORAGE_MAX_BYTES"); raw != "" {
		parsed, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil || parsed <= 0 {
			controlPool.Close()
			return nil, fmt.Errorf("PIBASE_STORAGE_MAX_BYTES must be a positive integer")
		}
		storageQuotaBytes = parsed
	}
	exposeAuthTokens := os.Getenv("PIBASE_AUTH_EXPOSE_TOKENS") == "true"
	if exposeAuthTokens && os.Getenv("PIBASE_ENV") != "test" {
		controlPool.Close()
		return nil, fmt.Errorf("PIBASE_AUTH_EXPOSE_TOKENS requires PIBASE_ENV=test")
	}
	s := &Server{
		control:           controlPool,
		databaseURL:       databaseURL,
		masterKey:         append([]byte(nil), masterKey...),
		pools:             make(map[string]*pgxpool.Pool),
		databaseSlots:     make(chan struct{}, 32),
		rates:             make(map[string]rateWindow),
		authRates:         make(map[string]rateWindow),
		exposeAuthTokens:  exposeAuthTokens,
		storageRoot:       storageRoot,
		storageQuotaBytes: storageQuotaBytes,
		adminDatabaseURL:  os.Getenv("PIBASE_ADMIN_DATABASE_URL"),
		operatorToken:     os.Getenv("PIBASE_OPERATOR_TOKEN"),
		startedAt:         time.Now(),
	}
	if s.realtime, err = newRealtimeHub(s); err != nil {
		s.Close()
		return nil, err
	}
	if activeProjects > 0 {
		rows, err := controlPool.Query(ctx, `SELECT database_name FROM control.project_catalog ORDER BY id LIMIT $1`, activeProjects)
		if err != nil {
			s.Close()
			return nil, err
		}
		var databases []string
		for rows.Next() {
			var db string
			if err := rows.Scan(&db); err != nil {
				rows.Close()
				s.Close()
				return nil, err
			}
			databases = append(databases, db)
		}
		rows.Close()
		if len(databases) != activeProjects {
			s.Close()
			return nil, fmt.Errorf("requested %d active projects, found %d", activeProjects, len(databases))
		}
		for _, database := range databases {
			cfg, err := pgxpool.ParseConfig(databaseURL)
			if err != nil {
				s.Close()
				return nil, err
			}
			cfg.ConnConfig.Database = database
			cfg.MaxConns, cfg.MinConns = 1, 0
			cfg.MaxConnIdleTime = 1 * time.Second
			cfg.AfterConnect = config.AfterConnect
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				s.Close()
				return nil, err
			}
			s.projects = append(s.projects, pool)
		}
	}
	if lightLoad {
		go s.lightLoad(ctx)
	}
	return s, nil
}

func (s *Server) lightLoad(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			semaphore := make(chan struct{}, 20)
			var group sync.WaitGroup
			for _, pool := range s.projects {
				semaphore <- struct{}{}
				group.Add(1)
				go func(p *pgxpool.Pool) {
					defer group.Done()
					defer func() { <-semaphore }()
					qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
					defer cancel()
					if _, err := p.Exec(qctx, `SELECT 1`); err != nil {
						slog.Warn("measurement query failed", "error", err)
					}
					p.Reset()
				}(pool)
			}
			group.Wait()
		}
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", s.metricsHandler)
	mux.HandleFunc("GET /dashboard", s.dashboardHandler)
	mux.HandleFunc("GET /dashboard/", s.dashboardHandler)
	mux.HandleFunc("GET /_pibase/projects", s.operatorProjects)
	mux.HandleFunc("POST /_pibase/projects/{id}/sql", s.operatorSQL)
	mux.HandleFunc("GET /realtime/v1/websocket", s.realtime.websocket)
	mux.HandleFunc("POST /auth/v1/signup", s.authSignup)
	mux.HandleFunc("POST /auth/v1/token", s.authToken)
	mux.HandleFunc("POST /auth/v1/logout", s.authLogout)
	mux.HandleFunc("GET /auth/v1/verify", s.authVerify)
	mux.HandleFunc("POST /auth/v1/verify", s.authVerify)
	mux.HandleFunc("POST /auth/v1/recover", s.authRecover)
	mux.HandleFunc("GET /auth/v1/user", s.authUser)
	mux.HandleFunc("PATCH /auth/v1/user", s.authUpdateUser)
	mux.HandleFunc("GET /auth/v1/admin/users", s.authAdminList)
	mux.HandleFunc("POST /auth/v1/admin/users", s.authAdminCreate)
	mux.HandleFunc("PUT /auth/v1/admin/users/{id}", s.authAdminUpdate)
	mux.HandleFunc("DELETE /auth/v1/admin/users/{id}", s.authAdminDelete)
	mux.HandleFunc("GET /storage/v1/bucket", s.storageListBuckets)
	mux.HandleFunc("POST /storage/v1/bucket", s.storageCreateBucket)
	mux.HandleFunc("GET /storage/v1/bucket/{bucket}", s.storageGetBucket)
	mux.HandleFunc("PUT /storage/v1/bucket/{bucket}", s.storageUpdateBucket)
	mux.HandleFunc("DELETE /storage/v1/bucket/{bucket}", s.storageDeleteBucket)
	mux.HandleFunc("POST /storage/v1/object/list/{bucket}", s.storageListObjects)
	mux.HandleFunc("GET /storage/v1/object/sign/{bucket}/{path}", s.storageSignObject)
	mux.HandleFunc("GET /storage/v1/object/sign/{bucket}/{path...}", s.storageSignObject)
	mux.HandleFunc("POST /storage/v1/object/sign/{bucket}/{path...}", s.storageSignObject)
	mux.HandleFunc("GET /storage/v1/object/public/{bucket}/{path...}", s.storagePublicObject)
	mux.HandleFunc("GET /storage/v1/object/{bucket}/{path...}", s.storageDownloadObject)
	mux.HandleFunc("POST /storage/v1/object/{bucket}/{path...}", s.storageUploadObject)
	mux.HandleFunc("PUT /storage/v1/object/{bucket}/{path...}", s.storageUploadObject)
	mux.HandleFunc("DELETE /storage/v1/object/{bucket}", s.storageDeleteObjects)
	mux.HandleFunc("GET /rest/v1/{relation}", s.restHandler)
	mux.HandleFunc("HEAD /rest/v1/{relation}", s.restHandler)
	mux.HandleFunc("POST /rest/v1/{relation}", s.restHandler)
	mux.HandleFunc("PATCH /rest/v1/{relation}", s.restHandler)
	mux.HandleFunc("DELETE /rest/v1/{relation}", s.restHandler)
	mux.HandleFunc("POST /rest/v1/rpc/{function}", s.rpcHandler)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.control.Ping(ctx); err != nil {
			http.Error(w, "Postgres unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	limited := http.MaxBytesHandler(mux, 1<<20)
	return cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requestCount.Add(1)
		// Storage uploads stream directly to disk and enforce their own 1 GiB
		// object limit; all other API requests retain the 1 MiB body cap.
		if (r.Method == http.MethodPost || r.Method == http.MethodPut) && strings.HasPrefix(r.URL.Path, "/storage/v1/object/") && !strings.HasPrefix(r.URL.Path, "/storage/v1/object/list/") && !strings.HasPrefix(r.URL.Path, "/storage/v1/object/sign/") {
			mux.ServeHTTP(w, r)
			return
		}
		limited.ServeHTTP(w, r)
	}))
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "authorization, apikey, content-type, prefer, range, x-client-info")
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Range-Unit, X-Affected-Rows")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Close() {
	if s.realtime != nil {
		s.realtime.Close()
	}
	for _, pool := range s.projects {
		pool.Close()
	}
	s.poolMu.Lock()
	for _, pool := range s.pools {
		pool.Close()
	}
	s.pools = nil
	s.poolMu.Unlock()
	if s.control != nil {
		s.control.Close()
	}
}
