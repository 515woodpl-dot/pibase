package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/auth"
	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/jackc/pgx/v5"
)

type authRequest struct {
	Email        string         `json:"email"`
	Password     string         `json:"password"`
	Data         map[string]any `json:"data"`
	RefreshToken string         `json:"refresh_token"`
	EmailConfirm bool           `json:"email_confirm"`
	UserMetadata map[string]any `json:"user_metadata"`
	BanDuration  string         `json:"ban_duration"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return httpError(http.StatusBadRequest, "invalid JSON body")
	}
	return nil
}

func tokenHash(token string) []byte { sum := sha256.Sum256([]byte(token)); return sum[:] }

func (s *Server) serviceProject(ctx context.Context, r *http.Request) (project, error) {
	key := strings.TrimSpace(r.Header.Get("apikey"))
	if key == "" {
		return project{}, httpError(http.StatusUnauthorized, "missing apikey")
	}
	p, _, serviceHash, err := s.resolveProject(ctx, key)
	if err != nil {
		return project{}, err
	}
	if !keys.EqualHash(key, serviceHash) {
		return project{}, httpError(http.StatusForbidden, "service apikey required")
	}
	return p, nil
}

func (s *Server) adminTx(ctx context.Context, p project, fn func(pgx.Tx) error) error {
	select {
	case s.databaseSlots <- struct{}{}:
		defer func() { <-s.databaseSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	pool, err := s.projectPool(ctx, &p)
	if err != nil {
		return err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{p.adminRole}.Sanitize()); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='5s'`); err != nil {
		return err
	}
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func userJSON(id, email string, confirmed bool, metadata []byte, created, updated time.Time) map[string]any {
	var data any = map[string]any{}
	_ = json.Unmarshal(metadata, &data)
	return map[string]any{"id": id, "aud": "authenticated", "role": "authenticated", "email": email,
		"email_confirmed_at": func() any {
			if confirmed {
				return true
			}
			return nil
		}(),
		"user_metadata": data, "created_at": created.UTC().Format(time.RFC3339Nano), "updated_at": updated.UTC().Format(time.RFC3339Nano)}
}

func accessToken(p project, id, email, session string) (string, error) {
	return keys.SignJWT(p.secret, map[string]any{"iss": "pibase", "aud": "authenticated", "role": "authenticated", "sub": id, "email": email, "session_id": session, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
}

func (s *Server) issueSession(ctx context.Context, p project, tx pgx.Tx, id, email string) (map[string]any, error) {
	session, err := auth.UUID()
	if err != nil {
		return nil, err
	}
	refresh, digest, err := auth.RandomToken(32)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth.refresh_tokens(id,user_id,token_hash,expires_at) VALUES($1,$2,$3,now()+interval '30 days')`, session, id, digest); err != nil {
		return nil, err
	}
	access, err := accessToken(p, id, email, session)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	return map[string]any{"access_token": access, "refresh_token": refresh, "expires_in": 3600, "token_type": "bearer", "user": userJSON(id, email, true, []byte(`{}`), now, now)}, nil
}

func (s *Server) authSignup(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	if req.Email == "" || req.Password == "" {
		writeAPIError(w, httpError(400, "email and password are required"))
		return
	}
	p, err := s.resolveAuthProject(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if err := s.enforceAuthRate(p, r, req.Email); err != nil {
		writeAPIError(w, err)
		return
	}
	s.recordRequest(r.Context(), p.id)
	password, err := auth.HashPassword(req.Password)
	if err != nil {
		writeAPIError(w, httpError(400, err.Error()))
		return
	}
	id, err := auth.UUID()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	confirmation, digest, err := auth.RandomToken(32)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	metadata, _ := json.Marshal(req.Data)
	if req.Data == nil {
		metadata = []byte(`{}`)
	}
	var user map[string]any
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		var created, updated time.Time
		err := tx.QueryRow(r.Context(), `INSERT INTO auth.users(id,email,password_hash,confirmation_token_hash,confirmation_expires_at,raw_user_meta_data) VALUES($1,$2,$3,$4,now()+interval '24 hours',$5) RETURNING created_at,updated_at`, id, strings.ToLower(strings.TrimSpace(req.Email)), password, digest, metadata).Scan(&created, &updated)
		if err != nil {
			return err
		}
		user = userJSON(id, strings.ToLower(strings.TrimSpace(req.Email)), false, metadata, created, updated)
		return nil
	})
	if err != nil {
		if strings.Contains(err.Error(), "duplicate key") {
			// Do not disclose whether an account exists. The response shape
			// matches an unconfirmed signup with no session.
			writeJSON(w, http.StatusOK, map[string]any{"user": nil, "session": nil})
		} else {
			writeAPIError(w, err)
		}
		return
	}
	result := map[string]any{"user": user, "session": nil}
	if s.exposeAuthTokens {
		result["confirmation_token"] = confirmation
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) resolveAuthProject(r *http.Request) (project, error) {
	return s.resolveProjectOnly(r.Context(), strings.TrimSpace(r.Header.Get("apikey")))
}

// resolveProject is intentionally wrapped to keep auth handlers from depending on key hashes.
func (s *Server) resolveProjectOnly(ctx context.Context, key string) (project, error) {
	p, _, _, err := s.resolveProject(ctx, key)
	return p, err
}

func (s *Server) authToken(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	p, err := s.resolveAuthProject(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if err := s.enforceAuthRate(p, r, req.Email); err != nil {
		writeAPIError(w, err)
		return
	}
	s.recordRequest(r.Context(), p.id)
	grant := r.URL.Query().Get("grant_type")
	var out map[string]any
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		if grant == "refresh_token" || req.RefreshToken != "" {
			return s.refreshTx(r.Context(), p, tx, req.RefreshToken, &out)
		}
		var id, email, hash string
		var confirmed *time.Time
		var banned *time.Time
		err := tx.QueryRow(r.Context(), `SELECT id,email,password_hash,email_confirmed_at,banned_until FROM auth.users WHERE email=$1`, strings.ToLower(strings.TrimSpace(req.Email))).Scan(&id, &email, &hash, &confirmed, &banned)
		if err != nil {
			_ = auth.VerifyPassword(req.Password, auth.DummyPasswordHash())
			return httpError(400, "invalid login credentials")
		}
		if !auth.VerifyPassword(req.Password, hash) {
			return httpError(400, "invalid login credentials")
		}
		if banned != nil && banned.After(time.Now()) {
			return httpError(400, "user is banned")
		}
		if confirmed == nil {
			return httpError(400, "email not confirmed")
		}
		session, err := s.issueSession(r.Context(), p, tx, id, email)
		if err != nil {
			return err
		}
		out = session
		return nil
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) refreshTx(ctx context.Context, p project, tx pgx.Tx, raw string, out *map[string]any) error {
	var id, user string
	var expires time.Time
	var revoked *time.Time
	var email string
	err := tx.QueryRow(ctx, `SELECT id,user_id,expires_at,revoked_at FROM auth.refresh_tokens WHERE token_hash=$1`, tokenHash(raw)).Scan(&id, &user, &expires, &revoked)
	if err != nil || expires.Before(time.Now()) {
		return httpError(400, "invalid refresh token")
	}
	if revoked != nil {
		_, _ = tx.Exec(ctx, `UPDATE auth.refresh_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE user_id=$1`, user)
		return httpError(400, "invalid refresh token")
	}
	if err = tx.QueryRow(ctx, `SELECT email FROM auth.users WHERE id=$1`, user).Scan(&email); err != nil {
		return err
	}
	newSession, err := s.issueSession(ctx, p, tx, user, email)
	if err != nil {
		return err
	}
	newID := newSession["refresh_token"].(string)
	if _, err = tx.Exec(ctx, `UPDATE auth.refresh_tokens SET revoked_at=now(),replaced_by=(SELECT id FROM auth.refresh_tokens WHERE token_hash=$1) WHERE id=$2`, tokenHash(newID), id); err != nil {
		return err
	}
	*out = newSession
	return nil
}

func (s *Server) authVerify(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" {
		writeAPIError(w, httpError(400, "token is required"))
		return
	}
	p, err := s.resolveAuthProject(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if r.URL.Query().Get("type") == "recovery" {
		var id, email string
		var out map[string]any
		err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
			if err := tx.QueryRow(r.Context(), `SELECT id,email FROM auth.users WHERE reset_token_hash=$1 AND reset_expires_at>now()`, tokenHash(token)).Scan(&id, &email); err != nil {
				return err
			}
			var e error
			out, e = s.issueSession(r.Context(), p, tx, id, email)
			if e != nil {
				return e
			}
			_, e = tx.Exec(r.Context(), `UPDATE auth.users SET reset_token_hash=NULL,reset_expires_at=NULL,updated_at=now() WHERE id=$1`, id)
			return e
		})
		if err != nil {
			writeAPIError(w, httpError(400, "invalid or expired recovery token"))
			return
		}
		writeJSON(w, 200, out)
		return
	}
	var id, email string
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `UPDATE auth.users SET email_confirmed_at=COALESCE(email_confirmed_at,now()),confirmation_token_hash=NULL,confirmation_expires_at=NULL WHERE confirmation_token_hash=$1 AND confirmation_expires_at>now() RETURNING id,email`, tokenHash(token)).Scan(&id, &email)
	})
	if err != nil {
		writeAPIError(w, httpError(400, "invalid or expired verification token"))
		return
	}
	writeJSON(w, 200, map[string]any{"user": map[string]any{"id": id, "email": email}, "type": r.URL.Query().Get("type")})
}

func (s *Server) authRecover(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	p, err := s.resolveAuthProject(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if err := s.enforceAuthRate(p, r, req.Email); err != nil {
		writeAPIError(w, err)
		return
	}
	s.recordRequest(r.Context(), p.id)
	raw, digest, err := auth.RandomToken(32)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(), `UPDATE auth.users SET reset_token_hash=$1,reset_expires_at=now()+interval '1 hour' WHERE email=$2`, digest, strings.ToLower(strings.TrimSpace(req.Email)))
		return e
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	out := map[string]any{}
	if s.exposeAuthTokens {
		out["reset_token"] = raw
	}
	writeJSON(w, 200, out)
}

func (s *Server) bearerUser(r *http.Request) (project, keys.Claims, error) {
	p, err := s.resolveAuthProject(r)
	if err != nil {
		return project{}, keys.Claims{}, err
	}
	h := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if h == "" {
		return project{}, keys.Claims{}, httpError(401, "missing bearer token")
	}
	c, err := keys.VerifyJWT(h, p.secret, time.Now())
	if err != nil || c.Subject == "" || c.Audience != "authenticated" || c.Role != "authenticated" {
		return project{}, keys.Claims{}, httpError(401, "invalid JWT")
	}
	return p, c, nil
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	p, c, err := s.bearerUser(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		if cSessionID(c) == "" {
			return httpError(http.StatusUnauthorized, "invalid JWT session")
		}
		_, e := tx.Exec(r.Context(), `UPDATE auth.refresh_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1 AND user_id=$2`, cSessionID(c), c.Subject)
		return e
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func cSessionID(c keys.Claims) string {
	var claims struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(c.JSON, &claims)
	return claims.SessionID
}
func (s *Server) authUser(w http.ResponseWriter, r *http.Request) {
	p, c, err := s.bearerUser(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var id, email string
	var confirmed *time.Time
	var meta []byte
	var created, updated time.Time
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `SELECT id,email,email_confirmed_at,raw_user_meta_data,created_at,updated_at FROM auth.users WHERE id=$1`, c.Subject).Scan(&id, &email, &confirmed, &meta, &created, &updated)
	})
	if err != nil {
		writeAPIError(w, httpError(404, "user not found"))
		return
	}
	writeJSON(w, 200, userJSON(id, email, confirmed != nil, meta, created, updated))
}

func (s *Server) authUpdateUser(w http.ResponseWriter, r *http.Request) {
	p, c, err := s.bearerUser(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var req authRequest
	if err = decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		if req.Password != "" {
			h, e := auth.HashPassword(req.Password)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(r.Context(), `UPDATE auth.users SET password_hash=$1,updated_at=now() WHERE id=$2`, h, c.Subject); e != nil {
				return e
			}
		}
		if req.UserMetadata != nil {
			m, _ := json.Marshal(req.UserMetadata)
			_, err = tx.Exec(r.Context(), `UPDATE auth.users SET raw_user_meta_data=$1,updated_at=now() WHERE id=$2`, m, c.Subject)
		}
		return err
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	s.authUser(w, r)
}

func (s *Server) authAdminList(w http.ResponseWriter, r *http.Request) {
	p, err := s.serviceProject(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var users []map[string]any
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		rows, e := tx.Query(r.Context(), `SELECT id,email,email_confirmed_at,raw_user_meta_data,created_at,updated_at FROM auth.users ORDER BY created_at`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id, email string
			var c *time.Time
			var m []byte
			var a, b time.Time
			if e = rows.Scan(&id, &email, &c, &m, &a, &b); e != nil {
				return e
			}
			users = append(users, userJSON(id, email, c != nil, m, a, b))
		}
		return rows.Err()
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"users": users, "aud": "authenticated"})
}

func (s *Server) authAdminCreate(w http.ResponseWriter, r *http.Request) {
	p, err := s.serviceProject(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var req authRequest
	if err = decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	h, err := auth.HashPassword(req.Password)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	id, _ := auth.UUID()
	m, _ := json.Marshal(req.UserMetadata)
	if req.UserMetadata == nil {
		m = []byte(`{}`)
	}
	var u map[string]any
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		var a, b time.Time
		c := time.Now()
		if req.EmailConfirm {
			c = time.Now()
		}
		email := strings.ToLower(strings.TrimSpace(req.Email))
		e := tx.QueryRow(r.Context(), `INSERT INTO auth.users(id,email,password_hash,email_confirmed_at,raw_user_meta_data) VALUES($1,$2,$3,$4,$5) RETURNING created_at,updated_at`, id, email, h, func() any {
			if req.EmailConfirm {
				return c
			}
			return nil
		}(), m).Scan(&a, &b)
		if e == nil {
			if req.BanDuration != "" {
				var until *time.Time
				if req.BanDuration != "none" {
					d, banErr := time.ParseDuration(req.BanDuration)
					if banErr != nil || d < 0 {
						return httpError(400, "invalid ban_duration")
					}
					t := time.Now().Add(d)
					until = &t
				}
				if _, banErr := tx.Exec(r.Context(), `UPDATE auth.users SET banned_until=$1,updated_at=now() WHERE id=$2`, until, id); banErr != nil {
					return banErr
				}
			}
			u = userJSON(id, email, req.EmailConfirm, m, a, b)
		}
		return e
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"user": u})
}

func (s *Server) authAdminUpdate(w http.ResponseWriter, r *http.Request) {
	p, err := s.serviceProject(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var req authRequest
	if err = decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	id := r.PathValue("id")
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		if req.Password != "" {
			h, e := auth.HashPassword(req.Password)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(r.Context(), `UPDATE auth.users SET password_hash=$1,updated_at=now() WHERE id=$2`, h, id); e != nil {
				return e
			}
		}
		if req.Email != "" {
			if _, e := tx.Exec(r.Context(), `UPDATE auth.users SET email=$1,updated_at=now() WHERE id=$2`, strings.ToLower(strings.TrimSpace(req.Email)), id); e != nil {
				return e
			}
		}
		if req.EmailConfirm {
			if _, e := tx.Exec(r.Context(), `UPDATE auth.users SET email_confirmed_at=COALESCE(email_confirmed_at,now()),updated_at=now() WHERE id=$1`, id); e != nil {
				return e
			}
		}
		if req.BanDuration != "" {
			var until *time.Time
			if req.BanDuration != "none" {
				d, e := time.ParseDuration(req.BanDuration)
				if e != nil || d < 0 {
					return httpError(400, "invalid ban_duration")
				}
				t := time.Now().Add(d)
				until = &t
			}
			if _, e := tx.Exec(r.Context(), `UPDATE auth.users SET banned_until=$1,updated_at=now() WHERE id=$2`, until, id); e != nil {
				return e
			}
		}
		if req.UserMetadata != nil {
			m, _ := json.Marshal(req.UserMetadata)
			if _, e := tx.Exec(r.Context(), `UPDATE auth.users SET raw_user_meta_data=$1,updated_at=now() WHERE id=$2`, m, id); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}
func (s *Server) authAdminDelete(w http.ResponseWriter, r *http.Request) {
	p, err := s.serviceProject(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	id := r.PathValue("id")
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(), `DELETE FROM auth.users WHERE id=$1`, id)
		return e
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
