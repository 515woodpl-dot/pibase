package gateway

import (
	"context"
	"errors"
	"fmt"
	"github.com/515woodpl-dot/pibase/internal/proxyip"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/515woodpl-dot/pibase/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Server struct {
	control            *pgxpool.Pool
	projects           []*pgxpool.Pool
	databaseURL        string
	masterKey          []byte
	poolMu             sync.Mutex
	pools              map[string]*pgxpool.Pool
	databaseSlots      chan struct{}
	rateMu             sync.Mutex
	rates              map[string]rateWindow
	authRates          map[string]rateWindow
	lastAuthRateSweep  time.Time
	exposeAuthTokens   bool
	storageRoot        string
	storageQuotaBytes  int64
	storageUploadLocks sync.Map
	storageUploadSlots sync.Map
	startedAt          time.Time
	requestCount       atomic.Uint64
	realtime           *realtimeHub
	schedulerStop      context.CancelFunc
	schedulerWG        sync.WaitGroup
	smtp               smtpConfig
	activity           *activityRecorder
	sessions           sessionCache
	refreshReuse       time.Duration
	maxBodyBytes       int64
	trustedProxies     []*net.IPNet
	activityStop       context.CancelFunc
	activityWG         sync.WaitGroup
}

func New(ctx context.Context, databaseURL string, masterKey []byte, activeProjects int, lightLoad bool) (*Server, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes")
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	poolConfig.MaxConns = 2
	poolConfig.MinConns = 1
	poolConfig.MaxConnIdleTime = 30 * time.Second
	poolConfig.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SET statement_timeout = '5s'`)
		return err
	}
	controlPool, err := pgxpool.NewWithConfig(ctx, poolConfig)
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
	refreshReuseSeconds, err := config.Int("PIBASE_REFRESH_REUSE_SECONDS", 10)
	if err != nil || refreshReuseSeconds < 0 || refreshReuseSeconds > 60 {
		controlPool.Close()
		return nil, fmt.Errorf("PIBASE_REFRESH_REUSE_SECONDS must be between 0 and 60")
	}
	maxBodyBytes, err := config.Int("PIBASE_MAX_BODY_BYTES", defaultMaxBodyBytes)
	if err != nil || maxBodyBytes < 64<<10 || maxBodyBytes > 16<<20 {
		controlPool.Close()
		return nil, fmt.Errorf("PIBASE_MAX_BODY_BYTES must be between 65536 and 16777216")
	}
	trustedProxies, err := proxyip.Parse(os.Getenv("PIBASE_TRUSTED_PROXIES"))
	if err != nil {
		controlPool.Close()
		return nil, fmt.Errorf("PIBASE_TRUSTED_PROXIES: %w", err)
	}
	maxProjectConnections, err := config.Int("PIBASE_MAX_PROJECT_CONNECTIONS", 30)
	if err != nil || maxProjectConnections < 1 {
		controlPool.Close()
		return nil, fmt.Errorf("PIBASE_MAX_PROJECT_CONNECTIONS must be a positive integer")
	}
	s := &Server{
		control:           controlPool,
		databaseURL:       databaseURL,
		trustedProxies:    trustedProxies,
		masterKey:         append([]byte(nil), masterKey...),
		pools:             make(map[string]*pgxpool.Pool),
		databaseSlots:     make(chan struct{}, maxProjectConnections),
		rates:             make(map[string]rateWindow),
		authRates:         make(map[string]rateWindow),
		exposeAuthTokens:  exposeAuthTokens,
		storageRoot:       storageRoot,
		storageQuotaBytes: storageQuotaBytes,
		startedAt:         time.Now(),
		smtp:              smtpFromEnv(),
		activity:          newActivityRecorder(),
		refreshReuse:      time.Duration(refreshReuseSeconds) * time.Second,
		maxBodyBytes:      int64(maxBodyBytes),
	}
	if s.realtime, err = newRealtimeHub(s); err != nil {
		s.Close()
		return nil, err
	}
	activityCtx, stopActivity := context.WithCancel(context.Background())
	s.activityStop = stopActivity
	s.activityWG.Add(1)
	go func() { defer s.activityWG.Done(); s.runActivityFlusher(activityCtx) }()
	if os.Getenv("PIBASE_SCHEDULER_DISABLED") != "true" {
		workerCtx, stop := context.WithCancel(context.Background())
		s.schedulerStop = stop
		s.schedulerWG.Add(1)
		go func() { defer s.schedulerWG.Done(); s.runDeliveryWorker(workerCtx) }()
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
			cfg.AfterConnect = poolConfig.AfterConnect
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
	mux.HandleFunc("GET /client/v1/pibase-auth.js", serveAuthClient)
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		mux.HandleFunc(method+" /functions/v1/{name...}", s.functionsProxy)
	}
	mux.HandleFunc("GET /realtime/v1/websocket", s.realtime.websocket)
	mux.HandleFunc("POST /auth/v1/signup", s.authSignup)
	mux.HandleFunc("POST /auth/v1/token", s.authToken)
	mux.HandleFunc("POST /auth/v1/logout", s.authLogout)
	mux.HandleFunc("GET /auth/v1/verify", s.authVerify)
	mux.HandleFunc("POST /auth/v1/verify", s.authVerify)
	mux.HandleFunc("POST /auth/v1/recover", s.authRecover)
	mux.HandleFunc("GET /auth/v1/user", s.authUser)
	mux.HandleFunc("PATCH /auth/v1/user", s.authUpdateUser)
	mux.HandleFunc("POST /auth/v1/factors", s.authMFAEnroll)
	mux.HandleFunc("POST /auth/v1/factors/{id}/challenge", s.authMFAChallenge)
	mux.HandleFunc("POST /auth/v1/factors/{id}/verify", s.authMFAVerify)
	mux.HandleFunc("DELETE /auth/v1/factors/{id}", s.authMFAUnenroll)
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
	bodyLimit := s.maxBodyBytes
	if bodyLimit == 0 {
		bodyLimit = defaultMaxBodyBytes
	}
	limited := http.MaxBytesHandler(mux, bodyLimit)
	return cors(s.observeActivity(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.requestCount.Add(1)
		// Storage uploads stream directly to disk and enforce their own 1 GiB
		// object limit; all other API requests retain the 1 MiB body cap.
		if (r.Method == http.MethodPost || r.Method == http.MethodPut) && strings.HasPrefix(r.URL.Path, "/storage/v1/object/") && !strings.HasPrefix(r.URL.Path, "/storage/v1/object/list/") && !strings.HasPrefix(r.URL.Path, "/storage/v1/object/sign/") {
			mux.ServeHTTP(w, r)
			return
		}
		limited.ServeHTTP(w, r)
	})))
}

// defaultMaxBodyBytes caps every API request body except storage uploads,
// which stream to disk under their own limit. PIBASE_MAX_BODY_BYTES changes it.
const defaultMaxBodyBytes = 1 << 20

// bodyError reports an oversized body as 413 with the limit, and anything else
// that fails to decode as 400 with the given message.
func bodyError(err error, invalid string) error {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return httpError(http.StatusRequestEntityTooLarge, fmt.Sprintf("request body is larger than the %d-byte limit (PIBASE_MAX_BODY_BYTES)", tooLarge.Limit))
	}
	return httpError(http.StatusBadRequest, invalid)
}

// corsAllowHeaders lists every request header the Supabase JavaScript clients
// send for features pibase implements: auth (x-supabase-api-version), the
// data API (accept-profile, content-profile, prefer, range, x-retry-count) and
// storage (x-upsert, x-metadata, cache-control). A browser refuses the whole
// request when any header it sends is missing here.
const corsAllowHeaders = "authorization, apikey, content-type, prefer, range, range-unit, x-client-info, " +
	"x-supabase-api-version, accept-profile, content-profile, x-retry-count, x-upsert, x-metadata, cache-control"

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Range, Range-Unit, X-Affected-Rows")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Close() {
	if s.schedulerStop != nil {
		s.schedulerStop()
		s.schedulerWG.Wait()
	}
	if s.realtime != nil {
		s.realtime.Close()
	}
	if s.activityStop != nil {
		s.activityStop()
		s.activityWG.Wait()
		// Keep the last few seconds of activity across restarts.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		if err := s.flushActivity(ctx); err != nil {
			slog.Warn("final request activity flush", "error", err)
		}
		cancel()
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
