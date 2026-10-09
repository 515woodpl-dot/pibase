// Package adminserver provides the loopback-only operator dashboard.  It is
// deliberately separate from the public gateway so its process is the only
// HTTP process that receives operator credentials.
package adminserver

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
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
	mux.HandleFunc("PATCH /_pibase/projects/{id}/functions", s.setFunctions)
	mux.HandleFunc("POST /_pibase/projects/{id}/users", s.createUser)
	mux.HandleFunc("DELETE /_pibase/projects/{id}/users/{user}", s.deleteUser)
	// Auth endpoints — no token required.
	mux.HandleFunc("POST /_pibase/auth/signup", s.authSignup)
	mux.HandleFunc("POST /_pibase/auth/login", s.authLogin)
	mux.HandleFunc("POST /_pibase/auth/logout", s.authLogout)
	mux.HandleFunc("GET /_pibase/auth/me", s.authMe)
	mux.HandleFunc("POST /_pibase/auth/password", s.authPassword)
	mux.HandleFunc("DELETE /_pibase/auth/account", s.authDeleteAccount)
	mux.HandleFunc("PATCH /_pibase/auth/organization", s.authOrganization)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// Page and auth endpoints need no operator credential.
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			switch r.URL.Path {
			case "/":
				s.servePage(w, landingPage)
				return
			case "/app", "/dashboard":
				s.servePage(w, page)
				return
			}
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
		Organization    string `json:"organization"`
		Email           string `json:"email"`
		Password        string `json:"password"`
		PasswordConfirm string `json:"password_confirm"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	// The password is typed twice when an account is created, so a typo cannot
	// lock someone out of a brand-new account.
	if b.Password != b.PasswordConfirm {
		http.Error(w, "the two passwords do not match", http.StatusBadRequest)
		return
	}
	account, err := control.CreateAccount(r.Context(), s.AdminURL, b.Email, b.Password, b.Organization)
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

// setFunctions sets where the project's /functions/v1 calls are forwarded. An
// owner may use a public address; a private one (the site's server on the same
// machine or network) needs the operator token.
func (s *Server) setFunctions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.checkOwner(w, r, id) {
		return
	}
	var b struct {
		URL string `json:"url"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	ident, ok := getIdentity(r.Context())
	private := ok && ident.kind == opKind
	url, err := control.SetFunctionsURL(r.Context(), s.AdminURL, id, b.URL, private)
	if err != nil {
		if errors.Is(err, control.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, 200, map[string]string{"url": url})
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

func (s *Server) servePage(w http.ResponseWriter, html string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The pages hold no secrets and load nothing from other sites.
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(html))
}

// authOrganization renames the signed-in account's organization.
func (s *Server) authOrganization(w http.ResponseWriter, r *http.Request) {
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
		Organization string `json:"organization"`
	}
	if !jsonBody(w, r, &b) {
		return
	}
	name, err := control.SetOrganization(r.Context(), s.AdminURL, account.ID, b.Organization)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	account.Organization = name
	writeJSON(w, http.StatusOK, account)
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

// The two pages are plain HTML files compiled into the binary.
//
//go:embed web/app.html
var page string

//go:embed web/landing.html
var landingPage string
