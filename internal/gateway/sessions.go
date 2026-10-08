package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

// An access token names the sign-in session it belongs to in its session_id
// claim. The session keeps its ID when refresh tokens rotate, and logging out
// revokes every refresh token of the session. From then on the session's
// access tokens are refused even though their signature and expiry are still
// valid. Tokens without a session_id (minted by a project's own server with
// the JWT secret) are not tied to a session and are not checked.

const (
	sessionCacheTTL = 15 * time.Second
	sessionCacheMax = 10000
)

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type sessionKey struct{ project, session, user string }

type sessionCache struct {
	mu     sync.Mutex
	active map[sessionKey]time.Time // confirmed active until
}

func (c *sessionCache) fresh(k sessionKey, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.active[k]
	return ok && now.Before(until)
}

func (c *sessionCache) remember(k sessionKey, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active == nil || len(c.active) >= sessionCacheMax {
		c.active = make(map[sessionKey]time.Time)
	}
	c.active[k] = now.Add(sessionCacheTTL)
}

// forget drops cached sessions of one user, or one session when session is set.
func (c *sessionCache) forget(project, session, user string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.active {
		if k.project == project && k.user == user && (session == "" || k.session == session) {
			delete(c.active, k)
		}
	}
}

func sessionIDFromClaims(raw []byte) string {
	var claims struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(raw, &claims)
	return claims.SessionID
}

// requireActiveSession refuses an access token whose sign-in session has ended.
func (s *Server) requireActiveSession(ctx context.Context, p project, sessionID, userID string) error {
	if sessionID == "" {
		return nil
	}
	if !uuidPattern.MatchString(sessionID) || !uuidPattern.MatchString(userID) {
		return httpError(http.StatusUnauthorized, "invalid JWT session")
	}
	key := sessionKey{project: p.id, session: sessionID, user: userID}
	now := time.Now()
	if s.sessions.fresh(key, now) {
		return nil
	}
	var active bool
	err := s.adminTx(ctx, p, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.refresh_tokens
WHERE COALESCE(session_id,id)=$1::uuid AND user_id=$2::uuid AND revoked_at IS NULL AND expires_at>now())`, sessionID, userID).Scan(&active)
	})
	if err != nil {
		return err
	}
	if !active {
		return httpError(http.StatusUnauthorized, "session has ended; sign in again")
	}
	s.sessions.remember(key, now)
	return nil
}

// endSessions takes effect immediately in this gateway: cached approvals are
// dropped and live realtime connections of the session (or of every session
// of the user when sessionID is empty) are closed.
func (s *Server) endSessions(projectID, userID, sessionID string) {
	s.sessions.forget(projectID, sessionID, userID)
	if s.realtime != nil {
		s.realtime.closeSessions(projectID, userID, sessionID)
	}
}
