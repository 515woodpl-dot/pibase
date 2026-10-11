package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"
)

// An access token's session_id claim names the refresh-token row it was issued
// with, as in earlier releases, so project SQL that checks that row keeps
// working. Every row of one sign-in shares a session (the session_id column),
// which survives refresh-token rotation. Signing out revokes the whole
// session, and from then on all of its access tokens are refused even though
// their signature and expiry are still valid. Tokens without a session_id
// (minted by a project's own server with the JWT secret) are not checked.

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

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
	// Check PostgreSQL on every request. Project SQL may revoke sessions
	// directly through auth.admin_end_sessions or admin_set_password, and an
	// in-process positive cache would keep those tokens alive after commit.
	var active bool
	err := s.adminTx(ctx, p, func(tx pgx.Tx) error {
		// A banned user's sessions count as ended, including bans written
		// straight into auth.users.
		return tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.refresh_tokens f
WHERE f.user_id=$2::uuid AND f.revoked_at IS NULL AND f.expires_at>now()
  AND COALESCE(f.session_id,f.id) = (SELECT COALESCE(r.session_id,r.id) FROM auth.refresh_tokens r WHERE r.id=$1::uuid AND r.user_id=$2::uuid))
  AND NOT EXISTS(SELECT 1 FROM auth.users u WHERE u.id=$2::uuid AND u.banned_until > now())`, sessionID, userID).Scan(&active)
	})
	if err != nil {
		return err
	}
	if !active {
		return httpError(http.StatusUnauthorized, "session has ended; sign in again")
	}
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

// revokeUserSessions revokes all of a user's sign-ins, or all except the
// sign-in containing keepRow. It returns token-row IDs for realtime closure.
func revokeUserSessions(ctx context.Context, tx pgx.Tx, userID, keepRow string, keep bool) ([]string, error) {
	if !uuidPattern.MatchString(userID) || (keep && !uuidPattern.MatchString(keepRow)) {
		return nil, httpError(http.StatusBadRequest, "invalid user or session id")
	}
	query := `UPDATE auth.refresh_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE user_id=$1::uuid`
	args := []any{userID}
	if keep {
		query += ` AND COALESCE(session_id,id) <> (SELECT COALESCE(session_id,id) FROM auth.refresh_tokens WHERE id=$2::uuid AND user_id=$1::uuid)`
		args = append(args, keepRow)
	}
	query += ` RETURNING id::text`
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ended []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ended = append(ended, id)
	}
	return ended, rows.Err()
}

// endSessions closes live realtime connections. HTTP checks query PostgreSQL.
func (s *Server) endSessions(projectID, userID string, rows []string) {
	if s.realtime != nil {
		s.realtime.closeSessions(projectID, userID, rows)
	}
}
