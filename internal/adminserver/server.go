// Package adminserver provides the loopback-only operator dashboard.  It is
// deliberately separate from the public gateway so its process is the only
// HTTP process that receives operator credentials.
package adminserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/515woodpl-dot/pibase/internal/adminsql"
	"github.com/515woodpl-dot/pibase/internal/auth"
	"github.com/515woodpl-dot/pibase/internal/backup"
	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
)

type Config struct {
	AdminURL, DatabaseURL, StorageRoot, BackupRoot, Token string
	MasterKey, BackupKey                                  []byte
	// Env is "test" in test environments; disables Secure cookie flag and rate limiting.
	// "development" also disables the Secure cookie flag so a plain-HTTP dashboard
	// (localhost or Tailscale) can keep a session. Any other value, including
	// empty, keeps cookies Secure.
	Env string
	// PublicURL is the base URL of the public gateway (e.g. "https://api.example.com").
	// When set, every project stat includes api_url = PublicURL. The gateway
	// selects the project from the apikey header, so the URL has no project path.
	PublicURL string
	// MaxProjectsPerAccount caps projects an account session may own; 0 means 2.
	MaxProjectsPerAccount int
	// TrustedProxies are peers (besides loopback) whose X-Forwarded-For header
	// is believed when working out the client address for rate limiting.
	TrustedProxies []*net.IPNet
}

type Server struct {
	Config
	loginLimit    *rateLimiter
	signupLimit   *rateLimiter
	passwordLimit *rateLimiter
	loginFailures *rateLimiter
	// accountProjectsMu serialises account-owned project creation and account
	// deletion so the per-account cap cannot be raced and a delete cannot
	// leave behind a project created while it ran.
	accountProjectsMu sync.Mutex
}

func New(c Config) (*Server, error) {
	if c.AdminURL == "" || c.DatabaseURL == "" || c.Token == "" || len(c.MasterKey) != 32 || len(c.BackupKey) != 32 {
		return nil, fmt.Errorf("admin-serve requires admin/database URLs, operator token, master key, and backup key")
	}
	if c.StorageRoot == "" {
		c.StorageRoot = "/data/files"
	}
	if c.BackupRoot == "" {
		c.BackupRoot = "/data/backups"
	}
	if c.MaxProjectsPerAccount <= 0 {
		c.MaxProjectsPerAccount = 2
	}
	return &Server{Config: c, loginLimit: newRateLimiter(), signupLimit: newRateLimiter(), passwordLimit: newRateLimiter(), loginFailures: newRateLimiter()}, nil
}

// identityKind distinguishes operator-token auth from account-session auth.
type identityKind int

const (
	opKind      identityKind = iota // PIBASE_OPERATOR_TOKEN bearer
	accountKind                     // pibase_session cookie
)

type identity struct {
	kind      identityKind
	accountID string
	email     string
}

type contextKey int

const identKey contextKey = 0

func withIdentity(ctx context.Context, id identity) context.Context {
	return context.WithValue(ctx, identKey, id)
}
func getIdentity(ctx context.Context) (identity, bool) {
	id, ok := ctx.Value(identKey).(identity)
	return id, ok
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_pibase/projects", s.projects)
	mux.HandleFunc("POST /_pibase/projects", s.createProject)
	mux.HandleFunc("DELETE /_pibase/projects/{id}", s.deleteProject)
	mux.HandleFunc("POST /_pibase/projects/{id}/sql", s.sql)
	mux.HandleFunc("POST /_pibase/projects/{id}/migrations", s.migration)
	mux.HandleFunc("POST /_pibase/projects/{id}/extensions/{extension}", s.extension)
	mux.HandleFunc("POST /_pibase/projects/{id}/keys/rotate", s.rotateKeys)
	mux.HandleFunc("PATCH /_pibase/projects/{id}", s.renameProject)
	mux.HandleFunc("POST /_pibase/projects/{id}/backup", s.backup)
	mux.HandleFunc("GET /_pibase/projects/{id}/backups", s.backups)
	mux.HandleFunc("GET /_pibase/projects/{id}/tables", s.tables)
	mux.HandleFunc("GET /_pibase/projects/{id}/tables/{table}/rows", s.rows)
	mux.HandleFunc("GET /_pibase/projects/{id}/migrations", s.migrations)
	mux.HandleFunc("GET /_pibase/projects/{id}/users", s.users)
	mux.HandleFunc("GET /_pibase/projects/{id}/usage", s.usage)
	mux.HandleFunc("GET /_pibase/projects/{id}/activity", s.activity)
	mux.HandleFunc("GET /_pibase/projects/{id}/keys", s.projectKeys)
	mux.HandleFunc("PATCH /_pibase/projects/{id}/limits", s.setLimits)
	mux.HandleFunc("POST /_pibase/projects/{id}/users", s.createUser)
	mux.HandleFunc("DELETE /_pibase/projects/{id}/users/{user}", s.deleteUser)
	// Auth endpoints — no token required.
	mux.HandleFunc("POST /_pibase/auth/signup", s.authSignup)
	mux.HandleFunc("POST /_pibase/auth/login", s.authLogin)
	mux.HandleFunc("POST /_pibase/auth/logout", s.authLogout)
	mux.HandleFunc("GET /_pibase/auth/me", s.authMe)
	mux.HandleFunc("POST /_pibase/auth/password", s.authPassword)
	mux.HandleFunc("DELETE /_pibase/auth/account", s.authDeleteAccount)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Page and auth endpoints need no operator credential.
		if r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/dashboard") {
			s.dashboard(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/_pibase/auth/") {
			mux.ServeHTTP(w, r)
			return
		}
		// All other endpoints require operator token or account session.
		id, ok := s.authorizeRequest(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), id)))
	})
}

// authorizeRequest returns the caller's identity from either a Bearer token
// or a valid pibase_session cookie. Returns false when neither is present/valid.
func (s *Server) authorizeRequest(r *http.Request) (identity, bool) {
	if s.operatorAuthorized(r) {
		return identity{kind: opKind}, true
	}
	c, err := r.Cookie("pibase_session")
	if err != nil {
		return identity{}, false
	}
	account, err := control.ResolveSession(r.Context(), s.AdminURL, c.Value)
	if err != nil {
		return identity{}, false
	}
	return identity{kind: accountKind, accountID: account.ID, email: account.Email}, true
}

func (s *Server) operatorAuthorized(r *http.Request) bool {
	const p = "Bearer "
	v := r.Header.Get("Authorization")
	return strings.HasPrefix(v, p) && len(v[len(p):]) == len(s.Token) && subtle.ConstantTimeCompare([]byte(v[len(p):]), []byte(s.Token)) == 1
}

// checkOwner writes 404/403 and returns false when the caller does not own projectID.
// Operators bypass the check.
func (s *Server) checkOwner(w http.ResponseWriter, r *http.Request, projectID string) bool {
	id, ok := getIdentity(r.Context())
	if !ok || id.kind == opKind {
		return true
	}
	err := control.AccountOwnsProject(r.Context(), s.AdminURL, projectID, id.accountID)
	if err == nil {
		return true
	}
	if errors.Is(err, control.ErrNotFound) {
		http.NotFound(w, r)
	} else {
		http.Error(w, "forbidden", http.StatusForbidden)
	}
	return false
}

// sessionCookieValue extracts the pibase_session cookie value, or "".
func sessionCookieValue(r *http.Request) string {
	c, err := r.Cookie("pibase_session")
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	c := &http.Cookie{
		Name:     "pibase_session",
		Value:    token,
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.Env != "test" && s.Env != "development",
	}
	http.SetCookie(w, c)
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "pibase_session",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// --- Auth endpoints ---

func (s *Server) authSignup(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.Env != "test" && !s.signupLimit.Allow(ip, 5, time.Minute) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	account, err := control.CreateAccount(r.Context(), s.AdminURL, b.Email, b.Password)
	if err != nil {
		if errors.Is(err, control.ErrDuplicate) {
			http.Error(w, "email already exists", http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	token, err := control.IssueSession(r.Context(), s.AdminURL, account.ID)
	if err != nil {
		fail(w, err)
		return
	}
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusCreated, account)
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if s.Env != "test" && !s.loginLimit.Allow(ip, 10, 10*time.Minute) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var b struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	// Per-email lockout catches guessing spread across many addresses. It is
	// short so a stranger cannot keep someone else's account locked for long.
	emailKey := strings.ToLower(strings.TrimSpace(b.Email))
	if s.loginFailures.Count(emailKey, loginFailureWindow) >= maxLoginFailures {
		http.Error(w, "too many failed sign-in attempts for this account; try again in 15 minutes", http.StatusTooManyRequests)
		return
	}
	account, err := control.AuthenticateAccount(r.Context(), s.AdminURL, b.Email, b.Password)
	if err != nil {
		s.loginFailures.Allow(emailKey, maxLoginFailures, loginFailureWindow)
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	s.loginFailures.Reset(emailKey)
	token, err := control.IssueSession(r.Context(), s.AdminURL, account.ID)
	if err != nil {
		fail(w, err)
		return
	}
	s.setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	token := sessionCookieValue(r)
	if token == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	_ = control.RevokeSession(r.Context(), s.AdminURL, token)
	clearSessionCookie(w)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) authMe(w http.ResponseWriter, r *http.Request) {
	token := sessionCookieValue(r)
	if token == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	account, err := control.ResolveSession(r.Context(), s.AdminURL, token)
	if err != nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, account)
}

func (s *Server) authPassword(w http.ResponseWriter, r *http.Request) {
	token := sessionCookieValue(r)
	if token == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	account, err := control.ResolveSession(r.Context(), s.AdminURL, token)
	if err != nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	if s.Env != "test" && !s.passwordLimit.Allow(account.ID, 5, 10*time.Minute) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	var b struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	if err := control.ChangePassword(r.Context(), s.AdminURL, account.ID, token, b.CurrentPassword, b.NewPassword); err != nil {
		if strings.Contains(err.Error(), "invalid credentials") {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authDeleteAccount(w http.ResponseWriter, r *http.Request) {
	token := sessionCookieValue(r)
	if token == "" {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	account, err := control.ResolveSession(r.Context(), s.AdminURL, token)
	if err != nil {
		http.Error(w, "not authenticated", http.StatusUnauthorized)
		return
	}
	var b struct {
		Confirm string `json:"confirm"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	if b.Confirm != account.Email {
		http.Error(w, "type your email address to confirm deletion", http.StatusBadRequest)
		return
	}
	s.accountProjectsMu.Lock()
	defer s.accountProjectsMu.Unlock()
	// Finish the delete even if the client disconnects part way through.
	if err := control.DeleteAccount(context.WithoutCancel(r.Context()), s.AdminURL, account.ID); err != nil {
		fail(w, err)
		return
	}
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// --- Project endpoints ---

type tableInfo struct {
	Name         string   `json:"name"`
	Count        int64    `json:"count"`
	RLS          bool     `json:"rls"`
	AnonReadable bool     `json:"anon_readable"`
	PrimaryKey   []string `json:"primary_key"`
	Columns      []string `json:"columns"`
}

func (s *Server) tables(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	tables, err := adminsql.ListTables(r.Context(), s.DatabaseURL, id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, tables)
}

func (s *Server) rows(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	t, err := s.table(r)
	if err != nil {
		fail(w, err)
		return
	}
	offset := 0
	fmt.Sscanf(r.URL.Query().Get("offset"), "%d", &offset)
	if offset < 0 {
		offset = 0
	}
	x, err := adminsql.BrowseRows(r.Context(), s.DatabaseURL, id, t.Name, t.Columns, t.PrimaryKey, offset)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, x)
}

func (s *Server) table(r *http.Request) (tableInfo, error) {
	x, err := adminsql.RunParams(r.Context(), s.DatabaseURL, r.PathValue("id"), `SELECT c.relname AS name, COALESCE((SELECT array_agg(a.attname ORDER BY k.ord) FROM pg_index i JOIN unnest(i.indkey) WITH ORDINALITY k(attnum,ord) ON true JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.attnum WHERE i.indrelid=c.oid AND i.indisprimary), ARRAY[]::text[]) AS primary_key, (SELECT array_agg(a.attname ORDER BY a.attnum) FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped) AS columns FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname=$1`, r.PathValue("table"))
	if err != nil {
		return tableInfo{}, err
	}
	if len(x.Rows) != 1 {
		return tableInfo{}, fmt.Errorf("table not found")
	}
	return tableInfo{Name: r.PathValue("table"), PrimaryKey: stringsOf(x.Rows[0]["primary_key"]), Columns: stringsOf(x.Rows[0]["columns"])}, nil
}
func quoteIdent(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func stringsOf(v any) []string {
	a, ok := v.([]string)
	if ok {
		return a
	}
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, x := range raw {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
func number(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

// User management uses the administrator connection because the auth schema is
// operator-owned and not accessible to project owner SQL.
func (s *Server) projectDB(ctx context.Context, id string) (*pgx.Conn, error) {
	controlURL, err := url.Parse(s.AdminURL)
	if err != nil {
		return nil, err
	}
	controlURL.Path = "/_control"
	c, err := pgx.Connect(ctx, controlURL.String())
	if err != nil {
		return nil, err
	}
	defer c.Close(ctx)
	var database string
	if err = c.QueryRow(ctx, `SELECT database_name FROM control.project_catalog WHERE id=$1`, id).Scan(&database); err != nil {
		return nil, fmt.Errorf("project not found: %w", err)
	}
	u, err := url.Parse(s.AdminURL)
	if err != nil {
		return nil, err
	}
	u.Path = "/" + database
	return pgx.Connect(ctx, u.String())
}
func (s *Server) users(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	c, err := s.projectDB(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	defer c.Close(r.Context())
	rows, err := c.Query(r.Context(), `SELECT id,email,email_confirmed_at,banned_until,created_at FROM auth.users ORDER BY created_at`)
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id, email string
		var confirmed, banned *time.Time
		var created time.Time
		if err := rows.Scan(&id, &email, &confirmed, &banned, &created); err != nil {
			fail(w, err)
			return
		}
		out = append(out, map[string]any{"id": id, "email": email, "email_confirmed_at": confirmed, "banned_until": banned, "created_at": created})
	}
	if err := rows.Err(); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	var b struct {
		Email        string `json:"email"`
		Password     string `json:"password"`
		EmailConfirm bool   `json:"email_confirm"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	b.Email = strings.ToLower(strings.TrimSpace(b.Email))
	if b.Email == "" || len(b.Password) < 8 {
		fail(w, fmt.Errorf("email and a password of at least 8 characters are required"))
		return
	}
	hash, err := auth.HashPassword(b.Password)
	if err != nil {
		fail(w, err)
		return
	}
	c, err := s.projectDB(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	defer c.Close(r.Context())
	uid, err := newUUID()
	if err != nil {
		fail(w, err)
		return
	}
	var confirmed any
	if b.EmailConfirm {
		confirmed = time.Now().UTC()
	}
	_, err = c.Exec(r.Context(), `INSERT INTO auth.users(id,email,password_hash,email_confirmed_at) VALUES($1,$2,$3,$4)`, uid, b.Email, hash, confirmed)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]string{"id": uid, "email": b.Email})
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return hex.EncodeToString(b[0:4]) + "-" + hex.EncodeToString(b[4:6]) + "-" + hex.EncodeToString(b[6:8]) + "-" + hex.EncodeToString(b[8:10]) + "-" + hex.EncodeToString(b[10:16]), nil
}
func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.checkOwner(w, r, projectID) {
		return
	}
	var b struct {
		Confirm string `json:"confirm"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	userID := r.PathValue("user")
	if b.Confirm != userID {
		fail(w, fmt.Errorf("type the user id to delete"))
		return
	}
	c, err := s.projectDB(r.Context(), projectID)
	if err != nil {
		fail(w, err)
		return
	}
	defer c.Close(r.Context())
	tag, err := c.Exec(r.Context(), `DELETE FROM auth.users WHERE id=$1`, userID)
	if err != nil {
		fail(w, err)
		return
	}
	if tag.RowsAffected() != 1 {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	id, _ := getIdentity(r.Context())
	var (
		stats []control.ProjectStat
		err   error
	)
	if id.kind == accountKind {
		stats, err = control.ProjectStatsByOwner(r.Context(), s.AdminURL, s.StorageRoot, id.accountID)
	} else {
		stats, err = control.ProjectStats(r.Context(), s.AdminURL, s.StorageRoot)
	}
	if err != nil {
		fail(w, err)
		return
	}
	if s.PublicURL != "" {
		for i := range stats {
			stats[i].APIURL = strings.TrimRight(s.PublicURL, "/")
		}
	}
	if stats == nil {
		// An account with no projects gets [], not null, so clients can append.
		stats = []control.ProjectStat{}
	}
	writeJSON(w, 200, stats)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Name string `json:"name"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	id, _ := getIdentity(r.Context())
	if id.kind == accountKind {
		s.accountProjectsMu.Lock()
		defer s.accountProjectsMu.Unlock()
		n, err := control.CountOwnedProjects(r.Context(), s.AdminURL, id.accountID)
		if err != nil {
			fail(w, err)
			return
		}
		if n >= s.MaxProjectsPerAccount {
			http.Error(w, fmt.Sprintf("project limit reached (%d per account)", s.MaxProjectsPerAccount), http.StatusForbidden)
			return
		}
	}
	p, err := control.CreateProject(r.Context(), s.AdminURL, b.Name, s.MasterKey)
	if err != nil {
		fail(w, err)
		return
	}
	if id.kind == accountKind {
		if err := control.SetProjectOwner(r.Context(), s.AdminURL, p.ID, id.accountID); err != nil {
			// An ownerless project would be invisible to its creator; remove it.
			_ = control.DeleteProject(context.Background(), s.AdminURL, p.ID)
			fail(w, err)
			return
		}
	}
	writeJSON(w, 201, p)
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.checkOwner(w, r, projectID) {
		return
	}
	var b struct {
		Confirm string `json:"confirm"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	if b.Confirm != projectID {
		http.Error(w, "type the project id to delete", 400)
		return
	}
	if err := control.DeleteProject(r.Context(), s.AdminURL, projectID); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) rotateKeys(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.checkOwner(w, r, projectID) {
		return
	}
	anon, svc, err := control.RotateKeys(r.Context(), s.AdminURL, projectID, s.MasterKey)
	if err != nil {
		if errors.Is(err, control.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"anon_key": anon, "service_key": svc})
}

func (s *Server) renameProject(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("id")
	if !s.checkOwner(w, r, projectID) {
		return
	}
	var b struct {
		Name string `json:"name"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	if err := control.RenameProject(r.Context(), s.AdminURL, projectID, b.Name); err != nil {
		if errors.Is(err, control.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	rows, err := control.Usage(r.Context(), s.AdminURL, id, time.Time{}, time.Time{})
	if err != nil {
		if errors.Is(err, control.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		fail(w, err)
		return
	}
	if rows == nil {
		rows = []control.ProjectUsage{}
	}
	writeJSON(w, 200, rows)
}

// activityWindows are the dashboard's activity ranges and their bucket sizes.
var activityWindows = map[string][2]time.Duration{
	"60m": {time.Hour, time.Minute},
	"24h": {24 * time.Hour, 30 * time.Minute},
	"7d":  {7 * 24 * time.Hour, 6 * time.Hour},
}

func (s *Server) activity(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	name := r.URL.Query().Get("window")
	if name == "" {
		name = "60m"
	}
	win, ok := activityWindows[name]
	if !ok {
		http.Error(w, "window must be 60m, 24h or 7d", http.StatusBadRequest)
		return
	}
	a, err := control.ProjectActivity(r.Context(), s.AdminURL, id, win[0], win[1], time.Now())
	if err != nil {
		if errors.Is(err, control.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		fail(w, err)
		return
	}
	writeJSON(w, 200, a)
}

// projectKeys returns the project's API URL, anon and service keys and JWT
// secret to its owner (or the operator), like a hosted project's API settings.
func (s *Server) projectKeys(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	k, err := control.GetProjectKeys(r.Context(), s.AdminURL, id, s.MasterKey)
	if err != nil {
		if errors.Is(err, control.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		fail(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, struct {
		APIURL string `json:"api_url,omitempty"`
		control.ProjectKeys
	}{strings.TrimRight(s.PublicURL, "/"), k})
}

func (s *Server) setLimits(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if ident, ok := getIdentity(r.Context()); !ok || ident.kind != opKind {
		http.Error(w, "operator token required to change limits", http.StatusForbidden)
		return
	}
	var b struct {
		MaxDatabaseBytes int64 `json:"max_database_bytes"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	if err := control.SetLimits(r.Context(), s.AdminURL, id, b.MaxDatabaseBytes); err != nil {
		if errors.Is(err, control.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) sql(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	var b struct {
		SQL string `json:"sql"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	x, err := adminsql.Run(r.Context(), s.DatabaseURL, id, b.SQL)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, x)
}

func (s *Server) migration(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	var b struct {
		Name string `json:"name"`
		SQL  string `json:"sql"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	if err := adminsql.ApplyMigration(r.Context(), s.DatabaseURL, id, b.Name, b.SQL); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) migrations(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	x, err := adminsql.RunParams(r.Context(), s.DatabaseURL, id, `SELECT name, left(checksum,8) AS checksum, applied_at FROM public.pibase_migrations ORDER BY applied_at`)
	if err != nil {
		writeJSON(w, 200, []any{})
		return
	}
	writeJSON(w, 200, x.Rows)
}

func (s *Server) extension(w http.ResponseWriter, r *http.Request) {
	// Extensions modify the shared cluster; operator only.
	ident, ok := getIdentity(r.Context())
	if !ok || ident.kind != opKind {
		http.Error(w, "operator token required for extension management", http.StatusForbidden)
		return
	}
	var err error
	switch r.PathValue("extension") {
	case "postgis":
		err = control.EnablePostGIS(r.Context(), s.AdminURL, r.PathValue("id"))
	case "pgvector":
		err = control.EnablePGVector(r.Context(), s.AdminURL, r.PathValue("id"))
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) backup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	var k [32]byte
	copy(k[:], s.BackupKey)
	file, err := backup.BackupTo(r.Context(), s.AdminURL, id, s.StorageRoot, s.BackupRoot, k)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, map[string]string{"file": filepath.Base(file)})
}

func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	entries, err := os.ReadDir(s.BackupRoot)
	if err != nil && !os.IsNotExist(err) {
		fail(w, err)
		return
	}
	out := make([]map[string]any, 0)
	for _, e := range entries {
		name := e.Name()
		ours := strings.HasPrefix(name, "pibase-"+id+"-") || strings.HasPrefix(name, "pre-restore-"+id+"-")
		if e.IsDir() || !ours || !strings.HasSuffix(name, ".pib") {
			continue
		}
		i, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, map[string]any{"name": name, "size": i.Size(), "time": i.ModTime()})
	}
	writeJSON(w, 200, out)
}

func (s *Server) dashboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}

// --- Helpers ---

func jsonBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(v); err != nil {
		http.Error(w, "invalid JSON", 400)
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadRequest) }

// clientIP keys rate limits. X-Forwarded-For is honoured only from a loopback
// peer (the local reverse proxy), and only its last entry, which that proxy
// wrote; earlier entries are client-controlled.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !s.trustedPeer(net.ParseIP(host)) {
		return host
	}
	// Walk the chain from the nearest hop; the first address that is not one of
	// our own proxies is the client. Anything further left is client-supplied.
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			break
		}
		host = ip.String()
		if !s.trustedPeer(ip) {
			break
		}
	}
	return host
}

func (s *Server) trustedPeer(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, n := range s.TrustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ParseTrustedProxies parses a comma-separated list of IPs and CIDRs.
func ParseTrustedProxies(v string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, f := range strings.Split(v, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if !strings.Contains(f, "/") {
			ip := net.ParseIP(f)
			if ip == nil {
				return nil, fmt.Errorf("invalid trusted proxy %q", f)
			}
			bits := 128
			if ip.To4() != nil {
				ip, bits = ip.To4(), 32
			}
			out = append(out, &net.IPNet{IP: ip, Mask: net.CIDRMask(bits, bits)})
			continue
		}
		_, n, err := net.ParseCIDR(f)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q", f)
		}
		out = append(out, n)
	}
	return out, nil
}

const (
	maxLoginFailures   = 10
	loginFailureWindow = 15 * time.Minute
)

// rateLimiter is a simple in-memory sliding-window rate limiter.
type rateLimiter struct {
	mu        sync.Mutex
	buckets   map[string][]time.Time
	lastSweep time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: make(map[string][]time.Time)}
}

// Allow returns true if the key has made fewer than maxN requests in window.
func (r *rateLimiter) Allow(key string, maxN int, window time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-window)
	if now.Sub(r.lastSweep) > window {
		for k, ts := range r.buckets {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(r.buckets, k)
			}
		}
		r.lastSweep = now
	}
	prev := r.buckets[key]
	valid := prev[:0]
	for _, t := range prev {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	valid = append(valid, now)
	r.buckets[key] = valid
	return len(valid) <= maxN
}

// Count returns how many events key has recorded within window, without
// recording a new one.
func (r *rateLimiter) Count(key string, window time.Duration) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := time.Now().Add(-window)
	n := 0
	for _, t := range r.buckets[key] {
		if t.After(cutoff) {
			n++
		}
	}
	return n
}

// Reset forgets every event recorded for key.
func (r *rateLimiter) Reset(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.buckets, key)
}

const page = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>pibase</title><link rel="icon" href="data:,"><style>
:root{--ink:#333;--muted:#777;--line:#e8e8e8;--tint:#f3f6f8;font:14px system-ui,sans-serif;color:var(--ink);background:#fff}*{box-sizing:border-box}body{margin:0}button,a,.menu{font:inherit;color:inherit;-webkit-tap-highlight-color:transparent;user-select:none}button{background:#fff;border:1px solid var(--line);padding:9px 12px;cursor:pointer}button:focus{outline:none}button:focus-visible,a:focus-visible,.menu:focus-visible{outline:none;background:var(--tint);text-decoration:underline}input,textarea{font:inherit;color:var(--ink);border:1px solid var(--line);padding:9px;width:100%}textarea{min-height:150px}.side{position:fixed;inset:0 auto 0 0;width:210px;border-right:1px solid var(--line);padding:32px 20px;background:#fff;z-index:1}.brand{font-size:18px;margin-bottom:34px}.menu{display:block;border:0;background:transparent;padding:9px 0;text-align:left;width:100%;cursor:pointer}.menu.secondary{color:var(--muted);padding-left:12px}.muted{color:var(--muted)}.app{margin-left:210px}.topbar{border-bottom:1px solid var(--line);padding:14px 24px;display:flex;gap:12px;align-items:center;min-height:52px}.topbar input{max-width:360px}.chain{display:flex;min-height:calc(100vh - 52px);overflow-x:auto}.pane{flex:0 0 390px;min-width:0;border-right:1px solid var(--line);padding:32px 24px;min-height:100%}.pane.wide{flex-basis:760px}.pane p{overflow-wrap:anywhere}.actions{display:flex;flex-wrap:wrap;gap:8px;margin:0 0 28px}button:disabled{color:#bbb;cursor:default}.pane h1{font-size:18px;font-weight:600;margin:0 0 28px}.group{margin:0 0 28px}.group>*+*{margin-top:10px}.item{display:block;width:100%;text-align:left;border-width:0 0 1px;padding:13px 0}.item span{display:block;color:var(--muted);font-size:12px;margin-top:4px}.row{display:flex;gap:10px;align-items:center}.row>*{min-width:0}.row>*+*{margin-top:0}.row input{flex:1}.danger{color:#8b3333}.data{overflow:auto;border-top:1px solid var(--line);border-bottom:1px solid var(--line)}table{border-collapse:collapse;width:100%;font-size:12px}th,td{text-align:left;vertical-align:top;border-bottom:1px solid var(--line);padding:9px;white-space:nowrap}th{font-weight:600}pre{white-space:pre-wrap;overflow:auto;background:#fafafa;padding:12px}.hidden{display:none}@media(max-width:600px){.side{width:120px;padding:24px 12px}.app{margin-left:120px}.pane,.pane.wide{flex-basis:calc(100vw - 120px);padding:28px 18px}.topbar{padding:12px 18px;display:block}.topbar button{margin-top:10px}}
</style><style>
:root{--ink:#172033;--muted:#68738a;--line:#dce3ef;--tint:#edf4ff;--surface:#fff;--accent:#2864dc;--shadow:0 10px 28px rgba(30,52,88,.09)}body{background:linear-gradient(135deg,#f6f9ff,#eef3fb 52%,#f8faff)}button{border-radius:10px;border-color:#cbd6e7;box-shadow:0 2px 5px rgba(30,52,88,.04)}button:hover{border-color:#95b4ec;box-shadow:0 7px 15px rgba(30,52,88,.12)}input,textarea{border-radius:10px;border-color:#cbd6e7;background:#fff;box-shadow:inset 0 1px 2px rgba(30,52,88,.04)}input:focus,textarea:focus{outline:3px solid #cfe0ff;border-color:#5285e5}.side{width:230px;padding:28px 16px;background:rgba(255,255,255,.92);box-shadow:4px 0 18px rgba(30,52,88,.05)}.brand{padding:0 10px;font-weight:750;letter-spacing:-.04em}.menu{border-radius:9px;padding:10px;text-align:left}.menu:hover,.menu:focus-visible{background:var(--tint);text-decoration:none}.menu::before{content:'◇';display:inline-block;width:22px;color:var(--accent);font-size:16px}.menu.secondary{padding-left:18px}.menu.secondary::before{content:'○';font-size:12px}.app{margin-left:230px}.topbar{margin:16px 18px 0;border:1px solid var(--line);border-radius:14px;background:rgba(255,255,255,.86);box-shadow:var(--shadow);padding:12px 16px}.chain{padding:18px;gap:18px}.pane{border:1px solid var(--line);border-radius:16px;background:var(--surface);box-shadow:var(--shadow);min-height:calc(100vh - 106px);padding:26px 22px}.pane h1{letter-spacing:-.025em}.item{border:1px solid transparent;border-radius:10px;padding:12px;margin:6px 0;background:#fff}.item:hover{background:var(--tint);border-color:#d8e5fb}.data{border:1px solid var(--line);border-radius:10px}.icon{width:16px;height:16px;fill:none;stroke:currentColor;stroke-width:1.8;stroke-linecap:round;stroke-linejoin:round;vertical-align:-3px;margin-right:7px}.stat-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;margin:0 0 20px}.stat{padding:12px;border:1px solid var(--line);border-radius:12px;background:#f9fbff}.stat b{display:block;font-size:18px;margin-top:4px}.chart-card{padding:14px;border:1px solid var(--line);border-radius:12px;background:linear-gradient(180deg,#fff,#f7faff);margin:0 0 20px}.chart-card svg{width:100%;height:118px;display:block}.chart-empty{padding:24px 0;color:var(--muted)}@media(max-width:600px){.side{width:132px}.app{margin-left:132px}.topbar{margin:10px}.chain{padding:10px;gap:10px}.pane,.pane.wide{border-radius:12px}.stat-grid{grid-template-columns:1fr}}
.act-pick{margin:0 0 14px}.act-pick button.selected{border-color:var(--accent);color:var(--accent)}.act-summary{display:flex;align-items:baseline;gap:8px;flex-wrap:wrap;margin:0 0 16px}.act-summary b{font-size:22px}.act-summary span{color:var(--muted);margin-right:18px}.act-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(250px,1fr));gap:12px;margin:0 0 14px}.act-card{padding:14px;border:1px solid var(--line);border-radius:12px;background:#fff}.act-head{display:flex;gap:14px;align-items:flex-start;margin:0 0 8px}.act-name{flex:1}.act-name span,.act-count span{display:block;font-size:11px;letter-spacing:.08em;text-transform:uppercase;color:var(--muted)}.act-name b{font-size:20px}.act-count{text-align:right}.act-count.warn span{color:#a8740f}.act-count.err span{color:#b83232}.act-card svg{width:100%;height:110px;display:block}.act-axis{display:flex;justify-content:space-between;color:var(--muted);font-size:11px;margin-top:6px}</style><style>.back{display:none}.menu.current{background:var(--tint);color:var(--accent)}body .menu.current{border-color:var(--accent)}@media(max-width:760px){body .side{position:sticky;top:0;inset:auto;width:auto;height:auto;display:flex;align-items:center;gap:6px;overflow-x:auto;padding:10px 12px;border-right:0;border-bottom:1px solid var(--line);box-shadow:0 2px 10px rgba(30,52,88,.06);z-index:5;scrollbar-width:none}body .side::-webkit-scrollbar{display:none}body .brand{flex:0 0 auto;font-size:15px;letter-spacing:.12em;margin:0 4px 0 0;padding:0;text-align:left}body .active-project{flex:0 0 auto;max-width:130px;margin:0;padding:6px 10px;border-radius:999px;font-size:12px;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}body #projectMenus,body .menu-group{display:contents}body #projectMenus.hidden{display:none}body .nav-label{display:none}body .menu,body .menu-group .menu,body .menu.secondary{flex:0 0 auto;width:auto;margin:0;padding:7px 12px;border:1px solid var(--line);border-radius:999px;background:#fff;white-space:nowrap;font-size:13px;color:var(--ink)}body .menu::before,body .menu.secondary::before{display:none}body .app{margin-left:0}body .topbar{display:flex;flex-wrap:wrap;align-items:center;gap:8px;margin:10px 10px 0;padding:8px 12px;min-height:0}body .topbar>span:first-child{flex:1 1 0;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-size:13px}body .topbar button{margin:0;padding:6px 10px;font-size:13px}body .topbar #status{flex:1 0 100%;font-size:12px;overflow-wrap:anywhere}body .topbar #status:empty{display:none}body .topbar input{max-width:none;flex:1 1 100%}body .topbar strong{flex:1 0 100%}body .chain{display:block;padding:10px;min-height:0;overflow:visible}body .chain>.pane:not(:last-child){display:none}body .pane,body .pane.wide{width:auto;min-height:0;padding:18px 16px;border-radius:14px}body .pane h1{font-size:17px;margin-bottom:18px}body .back{display:inline-block;margin:10px 10px 0;padding:6px 12px;font-size:13px}body .back.hidden{display:none}body .act-grid{grid-template-columns:1fr}body .act-pick{flex-wrap:wrap;gap:6px}body .act-pick button{white-space:nowrap;padding:6px 10px;font-size:13px}body .act-summary span{margin-right:10px}body input,body textarea{font-size:16px}}</style></head><body><aside class="side"><div class="brand">PIBASE</div><button class="menu" data-menu="projects">Projects</button><div id="projectMenus"><button class="menu secondary" data-menu="tables">Tables</button><button class="menu secondary" data-menu="users">Users</button><button class="menu secondary" data-menu="sql">SQL</button><button class="menu secondary" data-menu="usage">Activity</button><button class="menu secondary" data-menu="api">API keys</button><div class="nav-label">Manage</div><div class="menu-group"><button class="menu secondary" data-menu="migrations">Migrations</button><button class="menu secondary" data-menu="backups">Backups</button><button class="menu secondary" data-menu="extensions">Extensions</button></div></div></aside><main class="app"><div class="topbar" id="topbar"></div><div id="chain" class="chain"></div></main><script>
const q=id=>document.getElementById(id), chain=q('chain');let projects=[],selected=null,account=null;
function el(tag,text){const e=document.createElement(tag);if(text!==undefined)e.textContent=text;return e}function pane(title){const p=el('section');p.className='pane';p.append(el('h1',title));return p}function clearFrom(n){while(chain.children.length>n)chain.lastChild.remove()}function add(p){chain.append(p);alignChain()}function alignChain(){chain.style.paddingRight='0px';const max=chain.scrollWidth-chain.clientWidth;let x=max;for(const c of chain.children){const left=c.offsetLeft-chain.offsetLeft;if(left>=max){x=left;break}}if(x>max)chain.style.paddingRight=(x-max)+'px';chain.scrollLeft=x}function cell(v){if(v===null)return 'NULL';return typeof v==='object'?JSON.stringify(v):String(v)}function dataTable(columns,rows){const d=el('div');d.className='data';const table=el('table'),head=el('tr');for(const c of columns)head.append(el('th',c));table.append(head);for(const row of rows){const tr=el('tr');for(const c of columns){const td=el('td',cell(row[c]));if(row[c]===null)td.className='muted';tr.append(td)}table.append(tr)}d.append(table);return d}
function apiHeaders(){return account?{'Content-Type':'application/json'}:{Authorization:'Bearer '+opToken(),'Content-Type':'application/json'}}function opToken(){const i=q('opToken');return i?i.value:''}
async function api(path,opt={}){opt.headers=apiHeaders();const r=await fetch(path,opt),t=await r.text();if(!r.ok)throw Error(t||r.status);return t?JSON.parse(t):null}function body(v,method='POST'){return{method,body:JSON.stringify(v)}}
function apiBase(x){return (x&&x.api_url)||(location.protocol+'//'+location.hostname+':18080')}function copyField(label,value){const group=el('div');group.className='group';const row=el('div');row.className='key-row';const input=el('input');input.value=value;input.readOnly=true;input.setAttribute('aria-label',label);const status=el('span');status.className='muted';row.append(input,button('Copy',()=>copySecret(value,status)));group.append(el('div',label),row,status);return group}function connectPanel(x){const box=el('div');box.append(el('h1','Connect'),copyField('API URL',apiBase(x)),el('p','Send the project key in the apikey header. Data: /rest/v1/<table>. Auth: /auth/v1. Storage: /storage/v1. Realtime: /realtime/v1/websocket. Functions: /rest/v1/rpc/<name>.'),copyField('Example',"curl '"+apiBase(x)+"/rest/v1/YOUR_TABLE?select=*' -H 'apikey: YOUR_ANON_KEY'"));if(!x.api_url){const n=el('p','Guessed from this page address. Set PIBASE_PUBLIC_URL to fix it.');n.className='muted';box.append(n)}return box}
function projectPath(){return '/_pibase/projects/'+encodeURIComponent(selected.id)}
function say(m){const s=q('status');if(s)s.textContent=m}


async function init(){renderTopbar();try{const me=await fetch('/_pibase/auth/me');if(me.ok){account=await me.json();renderTopbar();load();return}}catch(_){}if(account){load();return}showLogin()}







function simple(title,content,wide){clearFrom(2);const p=pane(title);if(wide)p.classList.add('wide');p.append(content);add(p)}function openSQL(){const a=el('textarea');const out=el('div');const box=el('div');box.append(a,button('Run SQL',async()=>{try{const r=await api(projectPath()+'/sql',body({sql:a.value}));const n=(r.rows||[]).length;out.replaceChildren(el('p',r.command||(n+' rows')));if(r.columns&&r.columns.length)out.append(dataTable(r.columns,r.rows||[]))}catch(e){const m=el('pre',e.message);out.replaceChildren(m)}}),out);box.className='group';simple('SQL',box,true)}async function openMigrations(note){clearFrom(2);const p=pane('Migrations');const nameIn=el('input');nameIn.placeholder='Migration name';const sqlIn=el('textarea');sqlIn.placeholder='SQL';const status=el('p',note||'');status.className='muted';const g=el('div');g.className='group';g.append(el('div','Apply migration'),nameIn,sqlIn,button('Apply',async()=>{try{await api(projectPath()+'/migrations',body({name:nameIn.value,sql:sqlIn.value}));openMigrations('Applied: '+nameIn.value)}catch(e){status.textContent=e.message}}),status);p.append(g);p.append(el('h1','Applied'));try{const ms=await api(projectPath()+'/migrations');for(const m of ms){const d=el('div');d.className='item';d.textContent=m.name;d.append(el('span',m.checksum+' · '+new Date(m.applied_at).toLocaleString()));p.append(d)}if(!ms.length)p.append(el('p','No migrations applied.'))}catch(e){p.append(el('p',e.message))}add(p)}async function openBackups(note){clearFrom(2);const p=pane('Backups');const status=el('p',note||'');status.className='muted';p.append(button('Back up now',async()=>{try{const r=await api(projectPath()+'/backup',{method:'POST'});openBackups('Created: '+r.file)}catch(e){status.textContent=e.message}}),status);try{const bs=await api(projectPath()+'/backups');for(const b of bs){const d=el('div');d.className='item';d.textContent=b.name;d.append(el('span',Math.round(b.size/1024)+' KB'));p.append(d)}if(!bs.length)p.append(el('p','No backups yet.'))}catch(e){p.append(el('p',e.message))}add(p)}
// Dashboard additions are kept in this document so the admin server remains a
// single server-rendered page.  They deliberately use textContent and DOM APIs
// instead of injecting response data as HTML.
function bytes(n){n=Number(n)||0;const u=['B','KB','MB','GB','TB'];let i=0;while(n>=1024&&i<u.length-1){n/=1024;i++}return (i?n.toFixed(n<10?1:0):n)+' '+u[i]}


function openAccountSettings(){clearFrom(0);const p=pane('Account');const current=el('input');current.type='password';current.placeholder='Current password';const next=el('input');next.type='password';next.placeholder='New password (8+ characters)';const status=el('p');status.className='muted';const password=el('div');password.className='group';password.append(el('div','Change password'),current,next,button('Save password',async()=>{try{await api('/_pibase/auth/password',body({current_password:current.value,new_password:next.value}));current.value='';next.value='';status.textContent='Password updated. Sign in again with the new password.'}catch(e){status.textContent=e.message}}),status);const danger=el('div');danger.className='group danger';const confirmEmail=el('input');confirmEmail.placeholder='Type '+account.email+' to confirm';danger.append(el('div','Delete account and its projects'),confirmEmail,button('Delete account',async()=>{try{await api('/_pibase/auth/account',body({confirm:confirmEmail.value},'DELETE'));account=null;init()}catch(e){status.textContent=e.message}},'danger'));p.append(password,danger);add(p)}


function refreshProject(x){return api('/_pibase/projects').then(all=>{projects=all||[];const fresh=projects.find(p=>p.id===x.id)||x;selected=fresh;return fresh})}


function openLimits(){clearFrom(2);const p=pane('Database limit');const input=el('input');input.type='number';input.min='1';input.value=selected.max_database_bytes||'';input.placeholder='Bytes';const status=el('p');status.className='muted';p.append(el('p','Only the operator token can change this project limit.'),input,button('Save limit',async()=>{try{await api(projectPath()+'/limits',body({max_database_bytes:Number(input.value)},'PATCH'));await refreshProject(selected);status.textContent='Limit saved: '+bytes(selected.max_database_bytes)}catch(e){status.textContent=e.message}}),status);add(p)}
async function openTables(){clearFrom(2);const p=pane('Tables');p.append(el('p','Loading…'));add(p);try{const ts=await api(projectPath()+'/tables');p.replaceChildren(el('h1','Tables'));if(!ts.length){p.append(el('p','No tables yet. Use SQL or apply a migration to create your first table.'));const a=el('div');a.className='actions';a.append(button('Open SQL',openSQL),button('Open migrations',()=>openMigrations()));p.append(a);return}for(const t of ts){const b=button(t.name,()=>openTable(t),'item');b.append(el('span',t.count+' rows · RLS '+(t.rls?'on':'off')+' · anon '+(t.anon_readable?'yes':'no')));p.append(b)}}catch(e){p.replaceChildren(el('h1','Tables'),el('p',e.message))}}
function openTable(t){clearFrom(3);const p=pane(t.name);p.append(el('p',t.count+' rows · RLS '+(t.rls?'on':'off')+' · anon readable '+(t.anon_readable?'yes':'no')));if(t.columns&&t.columns.length)p.append(el('p','Columns: '+t.columns.join(', ')));const url=apiBase(selected)+'/rest/v1/'+encodeURIComponent(t.name)+'?select=*';p.append(copyField('Read this table',url),copyField('Example',"curl '"+url+"' -H 'apikey: YOUR_ANON_KEY'"));if(!t.anon_readable)p.append(el('p','The anon key cannot read this table yet. Grant SELECT to the anon role or add an RLS policy, or use the service key from a server.'));p.append(button('Browse rows',()=>openRows(t)));add(p)}
async function openRows(t,offset=0){clearFrom(4);const p=pane(t.name+' rows');p.classList.add('wide');p.append(el('p','Loading…'));add(p);try{const data=await api(projectPath()+'/tables/'+encodeURIComponent(t.name)+'/rows?offset='+offset);p.replaceChildren(el('h1',t.name+' rows'));if(!data.rows.length){p.append(el('p','This table has no rows yet.'));return}p.append(dataTable(data.columns,data.rows));const nav=el('div');nav.className='row group';const prev=button('Previous',()=>openRows(t,Math.max(0,offset-50)));if(offset===0)prev.disabled=true;const next=button('Next',()=>openRows(t,offset+50));if(!data.has_more)next.disabled=true;nav.append(prev,next,el('span','Rows '+(offset+1)+'–'+(offset+data.rows.length)));p.append(nav)}catch(e){p.replaceChildren(el('h1',t.name+' rows'),el('p',e.message))}}
async function openUsers(){clearFrom(2);const p=pane('Users');p.append(el('p','Loading…'));add(p);try{const users=await api(projectPath()+'/users');p.replaceChildren(el('h1','Users'));const email=el('input');email.placeholder='Email';const pass=el('input');pass.type='password';pass.placeholder='Password (8+ characters)';const cg=el('div');cg.className='group';cg.append(el('div','Create user'),email,pass,button('Create user',async()=>{try{await api(projectPath()+'/users',body({email:email.value,password:pass.value,email_confirm:true}));openUsers()}catch(e){say(e.message)}}));p.append(cg);if(!users.length)p.append(el('p','No users yet. Create the first user above.'));for(const u of users){const g=el('div');g.className='group';g.append(el('div',u.email),el('div','ID: '+u.id),el('div','Created: '+new Date(u.created_at).toLocaleString()),el('div','Email confirmed: '+(u.email_confirmed_at?'yes':'no'),));const c=el('input');c.placeholder='Type id to confirm';g.append(c,button('Delete user',async()=>{try{await api(projectPath()+'/users/'+encodeURIComponent(u.id),body({confirm:c.value},'DELETE'));openUsers()}catch(e){say(e.message)}},'danger'));p.append(g)}}catch(e){p.replaceChildren(el('h1','Users'),el('p',e.message))}}
function openExtensions(){if(account){simple('Extensions',el('p','Extensions can only be enabled by an operator token.'));return}const x=el('div');const status=el('p');status.className='muted';x.append(el('p','Extensions modify the shared database cluster.'),button('Enable PostGIS',()=>api(projectPath()+'/extensions/postgis',{method:'POST'}).then(()=>{status.textContent='PostGIS enabled.'}).catch(e=>{status.textContent=e.message})),button('Enable pgvector',()=>api(projectPath()+'/extensions/pgvector',{method:'POST'}).then(()=>{status.textContent='pgvector enabled.'}).catch(e=>{status.textContent=e.message})),status);simple('Extensions',x)}
function icon(name){const paths={Projects:'M3 3h5v5H3z M12 3h5v5h-5z M3 12h5v5H3z M12 12h5v5h-5z',Tables:'M3 5h14M3 10h14M3 15h14M5 3v14',Users:'M16 18v-1a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v1M9 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6',SQL:'M7 6 3 10l4 4M13 14l4-4-4-4M11 4 9 16',Migrations:'M4 6h12M4 12h12M4 18h12M3 6l1 1 2-2',Backups:'M4 5h12v12H4zM7 3v4M13 3v4M7 12h6',Usage:'M4 17V9M10 17V5M16 17v-7',Account:'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8M4 20a8 8 0 0 1 16 0',Delete:'M4 6h12M8 6V4h4v2M6 6l1 12h6l1-12',Extensions:'M12 2v5M12 17v5M4.9 4.9l3.5 3.5M15.6 15.6l3.5 3.5M2 12h5M17 12h5M4.9 19.1l3.5-3.5M15.6 8.4l3.5-3.5',Create:'M12 5v14M5 12h14'};const svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.setAttribute('viewBox','0 0 24 24');svg.setAttribute('aria-hidden','true');svg.classList.add('icon');const path=document.createElementNS('http://www.w3.org/2000/svg','path');path.setAttribute('d',paths[name]||'M5 12h14');svg.append(path);return svg}
function button(text,fn,cls){const b=document.createElement('button');if(cls)b.className=cls;const named={Create:'Create', 'Create project':'Create',Tables:'Tables',Users:'Users',SQL:'SQL',Migrations:'Migrations',Backups:'Backups',Usage:'Usage',Extensions:'Extensions','Rotate keys':'Extensions','API keys':'Extensions',Account:'Account','Delete project':'Delete','Delete user':'Delete'}[text];if(named)b.append(icon(named));b.append(document.createTextNode(text));b.onclick=fn;return b}
function requestChart(rows){const card=el('div');card.className='chart-card';card.append(el('div','Request activity · last 30 days'));if(!rows.length){const empty=el('div','No requests recorded yet. Open Activity for live per-service charts.');empty.className='chart-empty';card.append(empty);return card}const values=rows.map(r=>Number(r.requests)||0),max=Math.max(...values,1),svg=document.createElementNS('http://www.w3.org/2000/svg','svg');svg.setAttribute('viewBox','0 0 300 118');const base=document.createElementNS('http://www.w3.org/2000/svg','path');base.setAttribute('d','M0 102H300');base.setAttribute('stroke','#dce3ef');base.setAttribute('fill','none');svg.append(base);const path=document.createElementNS('http://www.w3.org/2000/svg','path');path.setAttribute('fill','none');path.setAttribute('stroke','#2864dc');path.setAttribute('stroke-width','3');path.setAttribute('stroke-linecap','round');path.setAttribute('stroke-linejoin','round');path.setAttribute('d',values.map((v,i)=>(i?'L':'M')+(values.length===1?150:i/(values.length-1)*300)+' '+(102-v/max*80)).join(' '));svg.append(path);card.append(svg,el('p',values.reduce((a,b)=>a+b,0)+' requests recorded. Open Activity for success rate and per-service charts.'));return card}
async function addPulse(p){try{const rows=await api(projectPath()+'/usage');p.insertBefore(requestChart(rows),p.querySelector('.actions'))}catch(e){const note=el('p','Usage chart unavailable: '+e.message);note.className='muted';p.insertBefore(note,p.querySelector('.actions'))}}

function openProject(x){selected=x;q('projectMenus').classList.remove('hidden');clearFrom(1);const p=pane(x.name);p.append(el('p','Project ID: '+x.id));const stats=el('div');stats.className='stat-grid';for(const [label,value] of [['Database',bytes(x.database_bytes)],['Storage',x.storage_error?'Unavailable':bytes(x.storage_bytes)],['Requests, 30 days',x.requests_30d||0],['Database limit',x.max_database_bytes?bytes(x.max_database_bytes):'Not set']]){const stat=el('div');stat.className='stat';stat.append(el('span',label),el('b',String(value)));stats.append(stat)}p.append(stats);if(x.storage_error){const w=el('p','Storage: '+x.storage_error);w.className='danger';p.append(w)}p.append(connectPanel(x));const acts=el('div');acts.className='actions';acts.append(button('Tables',openTables),button('Users',openUsers),button('SQL',openSQL),button('Migrations',()=>openMigrations()),button('Backups',()=>openBackups()),button('Activity',openUsage),button('API keys',openKeys));if(!account){acts.append(button('Set limit',openLimits),button('Extensions',openExtensions))}p.append(acts);const rename=el('div');rename.className='group';const name=el('input');name.value=x.name;rename.append(el('div','Rename project'),name,button('Save name',async()=>{try{await api(projectPath(),body({name:name.value},'PATCH'));await refreshProject(x);openProject(selected)}catch(e){say(e.message)}}));p.append(rename);const danger=el('div');danger.className='group danger';danger.append(el('div','Delete project — type its id to confirm'));const i=el('input');danger.append(i,button('Delete project',async()=>{try{await api(projectPath(),body({confirm:i.value},'DELETE'));selected=null;await load()}catch(e){say(e.message)}}));p.append(danger);add(p);addPulse(p)}

const dashboardStyle=document.createElement('style');dashboardStyle.textContent='.brand{font-size:30px;line-height:1;text-align:center;text-transform:uppercase;letter-spacing:.12em;margin:6px 0 38px;padding:0;color:#123d91}.nav-label{margin:23px 10px 7px;color:#8a96a9;font-size:10px;font-weight:800;letter-spacing:.13em;text-transform:uppercase}.menu-group{padding:7px 0;border-radius:12px;background:#f4f7fc;border:1px solid #e1e8f3}.menu-group .menu{margin:1px 6px;width:calc(100% - 12px)}.active-project{margin:0 8px 18px;padding:11px;border:1px solid #b9cff7;border-radius:11px;background:#eaf2ff;color:#1649a1;font-size:12px;font-weight:700;overflow-wrap:anywhere}.item.active{background:#eaf2ff;border-color:#a9c6f7}.key-row{display:flex;gap:8px;align-items:center;margin:8px 0}.key-row input{min-width:0;flex:1;font-family:ui-monospace,SFMono-Regular,Menlo,monospace}.key-row button{white-space:nowrap}';document.head.append(dashboardStyle)
const activeProject=el('div');activeProject.id='activeProject';activeProject.className='active-project';activeProject.textContent='Choose a project';q('projectMenus').before(activeProject)
function setActiveProject(project){activeProject.textContent=project?'Working in: '+project.name:'Choose a project'}
async function copySecret(value,status){try{if(navigator.clipboard&&window.isSecureContext){await navigator.clipboard.writeText(value)}else{const temp=document.createElement('textarea');temp.value=value;document.body.append(temp);temp.select();document.execCommand('copy');temp.remove()}status.textContent='Copied'}catch(e){status.textContent='Copy failed: '+e.message}}
function secretField(label,value){const group=el('div');group.className='group';const row=el('div');row.className='key-row';const input=el('input');input.type='password';input.value=value;input.readOnly=true;input.setAttribute('aria-label',label);const status=el('span');status.className='muted';const reveal=button('Reveal',()=>{input.type=input.type==='password'?'text':'password';reveal.lastChild.nodeValue=input.type==='password'?'Reveal':'Hide'});const copy=button('Copy',()=>copySecret(value,status));row.append(input,reveal,copy);group.append(el('div',label),row,status);return group}
function projectKeyPane(x,label){const p=pane(x.name+' keys');p.append(el('p',label),el('p','Project ID: '+x.id),secretField('Anon key',x.anon_key),secretField('Service key',x.service_key));add(p)}
function renderProjects(){const list=q('projectList');if(!list)return;list.replaceChildren();if(!projects.length){const empty=el('p','No projects yet. Create one above to get started.');empty.className='muted';list.append(empty);return}for(const x of projects){const b=button(x.name,()=>{setActiveProject(x);openProject(x)},'item'+(selected&&selected.id===x.id?' active':''));const detail=[x.id,x.api_url?x.api_url:'',x.database_bytes!==undefined?bytes(x.database_bytes):''].filter(Boolean).join(' · ');b.append(el('span',detail));list.append(b)}}
function projectsPane(){const p=pane('Projects');const create=el('div');create.className='group';create.append(el('div','Create your first project'),el('p','A project gives you an isolated database, API URL, and keys.'));const input=el('input');input.placeholder='Project name, for example: inventory';const createProject=async()=>{try{const x=await api('/_pibase/projects',body({name:input.value}));projects.push(x);selected=x;setActiveProject(x);renderProjects();q('projectMenus').classList.remove('hidden');say(projects.length+(projects.length===1?' project':' projects'));openProject(x);projectKeyPane(x,'Created. Copy the keys now, or find them later under API keys.')}catch(e){say(e.message)}};input.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();e.stopPropagation();createProject()}});create.append(input,button('Create project',createProject));p.append(create);const list=el('div');list.id='projectList';p.append(list);return p}
async function load(){try{projects=(await api('/_pibase/projects'))||[];if(selected)selected=projects.find(x=>x.id===selected.id)||null;const wanted=location.hash.replace('#project=','');if(!selected&&wanted)selected=projects.find(x=>x.id===wanted)||null;setActiveProject(selected);clearFrom(0);add(projectsPane());renderProjects();q('projectMenus').classList.remove('hidden');say(projects.length+(projects.length===1?' project':' projects'));if(selected)openProject(selected)}catch(e){say(e.message)}}
function clearDashboard(){selected=null;projects=[];clearFrom(0);setActiveProject(null);q('projectMenus').classList.add('hidden')}
function renderTopbar(){const tb=q('topbar');tb.replaceChildren();const st=el('span');st.id='status';st.className='muted';if(account){const s=el('span',account.email);s.className='muted';tb.append(s,button('Account',openAccountSettings),button('Log out',async()=>{await fetch('/_pibase/auth/logout',{method:'POST'});account=null;showLogin()}),st)}else{const title=el('strong','Sign in to PIBASE');const inp=el('input');inp.id='opToken';inp.type='password';inp.placeholder='Operator token';inp.autocomplete='off';tb.append(title,inp,button('Load',load),st)}}
function showLogin(){clearDashboard();const tb=q('topbar');tb.replaceChildren();const title=el('strong','Sign in to PIBASE');const email=el('input');email.type='email';email.placeholder='Email address';email.autocomplete='email';const pass=el('input');pass.type='password';pass.placeholder='Password';pass.autocomplete='current-password';const msg=el('span');msg.className='muted';const doLogin=async()=>{try{const r=await fetch('/_pibase/auth/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({email:email.value,password:pass.value})});if(!r.ok){msg.textContent=await r.text();return}account=await r.json();renderTopbar();load()}catch(e){msg.textContent=e.message}};const signup=async()=>{try{const r=await fetch('/_pibase/auth/signup',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({email:email.value,password:pass.value})});if(!r.ok){msg.textContent=await r.text();return}account=await r.json();renderTopbar();load()}catch(e){msg.textContent=e.message}};pass.addEventListener('keydown',e=>{if(e.key==='Enter'){e.preventDefault();doLogin()}});tb.append(title,email,pass,button('Sign in',doLogin),button('Create account',signup),msg)}
document.addEventListener('keydown',e=>{if(e.key!=='Enter'||e.target.tagName!=='INPUT')return;const group=e.target.closest('.group');const action=group&&group.querySelector('button');if(action&&e.target.placeholder!=='Password (8+ characters)'){e.preventDefault();action.click()}})
const ACT_LABEL={database:'Database',auth:'Auth',storage:'Storage',realtime:'Realtime'};
function svgEl(tag,attrs){const e=document.createElementNS('http://www.w3.org/2000/svg',tag);for(const k in attrs)e.setAttribute(k,attrs[k]);return e}
function shortTime(t,step){const d=new Date(t),hm=d.toLocaleTimeString(undefined,{hour:'numeric',minute:'2-digit'});return step>=3600?d.toLocaleDateString(undefined,{month:'short',day:'numeric'})+', '+hm:hm}
function activityBars(series,step){const w=300,h=110,n=series.length||1,bw=w/n,max=Math.max(1,...series.map(x=>x.requests));const svg=svgEl('svg',{viewBox:'0 0 '+w+' '+h,role:'img'});svg.append(svgEl('path',{d:'M0 '+(h-0.5)+'H'+w,stroke:'#dce3ef',fill:'none'}));series.forEach((x,i)=>{if(!x.requests)return;const left=i*bw+bw*0.15,width=Math.max(1,bw*0.7);let y=h;for(const [v,color] of [[x.requests-x.warnings-x.errors,'#2fb47c'],[x.warnings,'#d99a1e'],[x.errors,'#d64545']]){if(v<=0)continue;const bh=Math.max(1.5,v/max*(h-8));y-=bh;const r=svgEl('rect',{x:left,y:y,width:width,height:bh,fill:color});const tip=svgEl('title',{});tip.textContent=shortTime(x.t,step)+': '+x.requests+' requests, '+x.warnings+' warnings, '+x.errors+' errors';r.append(tip);svg.append(r)}});return svg}
function activityCard(title,s,from,to,step){const c=el('div');c.className='act-card';const head=el('div');head.className='act-head';const name=el('div');name.className='act-name';name.append(el('span',title),el('b',String(s.requests)));const warn=el('div');warn.className='act-count warn';warn.append(el('span','Warnings'),el('b',String(s.warnings)));const err=el('div');err.className='act-count err';err.append(el('span','Errors'),el('b',String(s.errors)));head.append(name,warn,err);const axis=el('div');axis.className='act-axis';axis.append(el('span',shortTime(from,step)),el('span',shortTime(to,step)));c.append(head,activityBars(s.series,step),axis);return c}
async function openUsage(win){win=typeof win==='string'?win:'60m';clearFrom(2);const p=pane('Activity');p.classList.add('wide');p.append(el('p','Loading activity…'));add(p);const project=selected;const render=async()=>{if(!p.isConnected||selected!==project)return;let a;try{a=await api(projectPath()+'/activity?window='+win)}catch(e){p.replaceChildren(el('h1','Activity'),el('p',e.message));return}if(!p.isConnected)return;const pick=el('div');pick.className='row act-pick';for(const [k,label] of [['60m','Last 60 minutes'],['24h','Last 24 hours'],['7d','Last 7 days']]){const b=button(label,()=>openUsage(k));if(k===win)b.className='selected';pick.append(b)}const sum=el('div');sum.className='act-summary';sum.append(el('b',String(a.requests)),el('span','Total requests'),el('b',a.success_rate===null?'—':(a.success_rate*100).toFixed(1)+'%'),el('span','Success rate'));const grid=el('div');grid.className='act-grid';grid.append(activityCard('API gateway',{requests:a.requests,warnings:a.warnings,errors:a.errors,series:a.total},a.from,a.to,a.bucket_seconds));for(const s of a.services)grid.append(activityCard(ACT_LABEL[s.service]||s.service,s,a.from,a.to,a.bucket_seconds));const note=el('p','Warnings are 4xx responses and errors are 5xx. Requests with a missing or invalid key are not counted. New requests show up within about 10 seconds; this page refreshes every 30 seconds.');note.className='muted';p.replaceChildren(el('h1','Activity'),pick,sum,grid,note);if(!a.requests)p.insertBefore(noteEl('No requests yet in this period. Call the project API (API keys has an example) and they will appear here.'),grid);try{const days=await api(projectPath()+'/usage');if(p.isConnected&&days.length)p.append(el('h1','Daily requests, last 30 days'),dataTable(['day','requests'],days))}catch(e){}setTimeout(render,30000)};render()}
function noteEl(t){const n=el('p',t);n.className='muted';return n}
function rotateButton(){const b=button('Rotate keys',async()=>{if(!b.dataset.armed){b.dataset.armed='1';b.lastChild.nodeValue='Click again: old keys stop working';return}try{await api(projectPath()+'/keys/rotate',{method:'POST'});await openKeys();say('Keys rotated. Update every app that used the old keys.')}catch(e){say(e.message)}});return b}
async function openKeys(){clearFrom(2);const p=pane('API keys');p.classList.add('wide');p.append(el('p','Loading…'));add(p);const project=selected;try{const k=await api(projectPath()+'/keys');if(!p.isConnected)return;p.replaceChildren(el('h1','API keys'));const url=copyField('Project URL',k.api_url||apiBase(project));url.append(noteEl('Use this as the URL in supabase-js or any HTTP client.'));p.append(url);if(k.anon_key){const anon=copyField('anon · public',k.anon_key);anon.append(noteEl('Safe in a browser or mobile app as long as your tables use Row Level Security. Send it in the apikey header.'));p.append(anon);const svc=secretField('service_role · secret',k.service_key);svc.append(noteEl('Bypasses Row Level Security. Only use it on a server you control; never put it in an app or web page.'));p.append(svc)}else{const g=el('div');g.className='group';g.append(el('div','anon and service_role keys'),noteEl('These keys were issued before pibase kept a copy, so they cannot be shown again. Rotate once to get keys you can view here at any time.'));p.append(g)}const jwt=secretField('JWT secret',k.jwt_secret);jwt.append(noteEl('HS256 secret that signs the keys above and your users\' access tokens. It is base64: decode it to bytes before verifying tokens on your own server. Rotating keys replaces it and signs everyone out.'));p.append(jwt);const ex=copyField('Try it',"curl '"+(k.api_url||apiBase(project))+"/rest/v1/YOUR_TABLE?select=*' -H 'apikey: "+(k.anon_key||'YOUR_ANON_KEY')+"'");p.append(ex);const acts=el('div');acts.className='actions';acts.append(rotateButton());p.append(acts)}catch(e){p.replaceChildren(el('h1','API keys'),el('p',e.message))}}
const backBar=button('‹ Back',()=>{if(chain.children.length>1){chain.lastChild.remove();alignChain()}},'back hidden');chain.before(backBar);new MutationObserver(()=>backBar.classList.toggle('hidden',chain.children.length<2)).observe(chain,{childList:true});
document.querySelectorAll('[data-menu]').forEach(b=>b.onclick=()=>{document.querySelectorAll('[data-menu]').forEach(x=>x.classList.toggle('current',x===b));if(b.dataset.menu==='projects')load();else if(selected)({tables:openTables,users:openUsers,sql:openSQL,usage:openUsage,api:openKeys,migrations:()=>openMigrations(),backups:()=>openBackups(),extensions:openExtensions}[b.dataset.menu])()});init();
</script></body></html>`
