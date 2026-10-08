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

// An access token's session_id claim names the refresh-token row it was issued
// with, as in earlier releases, so project SQL that checks that row keeps
// working. Every row of one sign-in shares a session (the session_id column),
// which survives refresh-token rotation. Signing out revokes the whole
// session, and from then on all of its access tokens are refused even though
// their signature and expiry are still valid. Tokens without a session_id
// (minted by a project's own server with the JWT secret) are not checked.

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

// forget drops cached approvals of the given token rows, or of every token of
// the user when rows is nil.
func (c *sessionCache) forget(project, user string, rows []string) {
	ended := make(map[string]bool, len(rows))
	for _, row := range rows {
		ended[row] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.active {
		if k.project == project && k.user == user && (rows == nil || ended[k.session]) {
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
// sessionID is the token's claim: the refresh-token row it was issued with.
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
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.refresh_tokens f
WHERE f.user_id=$2::uuid AND f.revoked_at IS NULL AND f.expires_at>now()
  AND COALESCE(f.session_id,f.id) = (SELECT COALESCE(r.session_id,r.id) FROM auth.refresh_tokens r WHERE r.id=$1::uuid AND r.user_id=$2::uuid))`, sessionID, userID).Scan(&active)
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

// revokeSession revokes every refresh token of the sign-in that tokenRow
// belongs to and returns their IDs, which are also the session_id claims of
// the sign-in's access tokens.
func revokeSession(ctx context.Context, tx pgx.Tx, tokenRow, userID string) ([]string, error) {
	rows, err := tx.Query(ctx, `UPDATE auth.refresh_tokens SET revoked_at=COALESCE(revoked_at,now())
WHERE user_id=$2 AND COALESCE(session_id,id) = (SELECT COALESCE(session_id,id) FROM auth.refresh_tokens WHERE id=$1::uuid AND user_id=$2)
RETURNING id::text`, tokenRow, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ended := []string{tokenRow}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ended = append(ended, id)
	}
	return ended, rows.Err()
}

// endSessions takes effect immediately in this gateway: cached approvals are
// dropped and live realtime connections are closed, for the given token rows
// or for every session of the user when rows is nil.
func (s *Server) endSessions(projectID, userID string, rows []string) {
	s.sessions.forget(projectID, userID, rows)
	if s.realtime != nil {
		s.realtime.closeSessions(projectID, userID, rows)
	}
}
