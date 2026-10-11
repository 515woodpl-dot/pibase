package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/auth"
	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/jackc/pgx/v5"
)

type authRequest struct {
	Email           string         `json:"email"`
	Username        string         `json:"username"`
	Password        string         `json:"password"`
	PasswordHash    string         `json:"password_hash"`
	Data            map[string]any `json:"data"`
	RefreshToken    string         `json:"refresh_token"`
	EmailConfirm    bool           `json:"email_confirm"`
	UserMetadata    map[string]any `json:"user_metadata"`
	AppMetadata     map[string]any `json:"app_metadata"`
	BanDuration     string         `json:"ban_duration"`
	CurrentPassword string         `json:"current_password"`
	NewPassword     string         `json:"new_password"`
}

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,63}$`)

func normalizedUsername(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if !usernamePattern.MatchString(name) {
		return "", httpError(400, "username must be 3-64 characters using letters, digits, dot, underscore or hyphen")
	}
	return name, nil
}

func adminPasswordHash(req authRequest) (string, error) {
	if req.PasswordHash != "" {
		if req.Password != "" || !auth.ValidImportedPasswordHash(req.PasswordHash) {
			return "", httpError(400, "provide either password or a valid bcrypt password_hash")
		}
		return req.PasswordHash, nil
	}
	return auth.HashPassword(req.Password)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		return bodyError(err, "invalid JSON body")
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

func userJSON(id, email, username string, confirmed bool, metadata, appMetadata []byte, created, updated time.Time) map[string]any {
	var data any = map[string]any{}
	_ = json.Unmarshal(metadata, &data)
	var appData any = map[string]any{}
	_ = json.Unmarshal(appMetadata, &appData)
	return map[string]any{"id": id, "aud": "authenticated", "role": "authenticated", "email": email, "username": username,
		"email_confirmed_at": func() any {
			if confirmed {
				return true
			}
			return nil
		}(),
		"user_metadata": data, "app_metadata": appData, "created_at": created.UTC().Format(time.RFC3339Nano), "updated_at": updated.UTC().Format(time.RFC3339Nano)}
}

func accessToken(p project, id, email, session, aal, method string, appMetadata []byte) (string, error) {
	now := time.Now()
	var appData any = map[string]any{}
	_ = json.Unmarshal(appMetadata, &appData)
	return keys.SignJWT(p.secret, map[string]any{
		"iss": "pibase", "aud": "authenticated", "role": "authenticated",
		"sub": id, "email": email, "session_id": session, "aal": aal,
		"app_metadata": appData,
		"amr":          []map[string]any{{"method": method, "timestamp": now.Unix()}},
		"iat":          now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
}

// issueSession starts a new sign-in session, or adds a token to session when
// one is given, as a refresh does. The access token names the new row.
func (s *Server) issueSession(ctx context.Context, p project, tx pgx.Tx, id, email, aal, method, session string) (map[string]any, error) {
	row, err := auth.UUID()
	if err != nil {
		return nil, err
	}
	if session == "" {
		session = row
	}
	refresh, digest, err := auth.RandomToken(32)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO auth.refresh_tokens(id,user_id,token_hash,expires_at,aal,session_id) VALUES($1,$2,$3,now()+interval '30 days',$4,$5)`, row, id, digest, aal, session); err != nil {
		return nil, err
	}
	var userMeta, appMeta []byte
	var username string
	var created, updated time.Time
	if err = tx.QueryRow(ctx, `SELECT COALESCE(username,''),raw_user_meta_data,raw_app_meta_data,created_at,updated_at FROM auth.users WHERE id=$1`, id).Scan(&username, &userMeta, &appMeta, &created, &updated); err != nil {
		return nil, err
	}
	access, err := accessToken(p, id, email, row, aal, method, appMeta)
	if err != nil {
		return nil, err
	}
	return map[string]any{"access_token": access, "refresh_token": refresh, "expires_in": 3600, "token_type": "bearer", "user": userJSON(id, email, username, true, userMeta, appMeta, created, updated)}, nil
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
	username := ""
	if req.Username != "" {
		var nameErr error
		username, nameErr = normalizedUsername(req.Username)
		if nameErr != nil {
			writeAPIError(w, nameErr)
			return
		}
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
		err := tx.QueryRow(r.Context(), `INSERT INTO auth.users(id,email,username,password_hash,confirmation_token_hash,confirmation_expires_at,raw_user_meta_data) VALUES($1,$2,NULLIF($3,''),$4,$5,now()+interval '24 hours',$6) RETURNING created_at,updated_at`, id, strings.ToLower(strings.TrimSpace(req.Email)), username, password, digest, metadata).Scan(&created, &updated)
		if err != nil {
			return err
		}
		user = userJSON(id, strings.ToLower(strings.TrimSpace(req.Email)), username, false, metadata, []byte(`{}`), created, updated)
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
	s.sendConfirmationEmail(strings.ToLower(strings.TrimSpace(req.Email)), confirmation)
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
	useCookie := cookieMode(r)
	refreshGrant := r.URL.Query().Get("grant_type") == "refresh_token"
	if useCookie {
		if !cookieOriginAllowed(r) {
			writeAPIError(w, httpError(403, "cookie origin is not allowed"))
			return
		}
		if refreshGrant && req.RefreshToken == "" {
			req.RefreshToken = refreshCookie(r, p)
		}
	}
	refresh := refreshGrant || req.RefreshToken != ""
	username := ""
	identity := req.Email
	if !refresh && req.Username != "" {
		if req.Email != "" {
			writeAPIError(w, httpError(400, "provide email or username, not both"))
			return
		}
		username, err = normalizedUsername(req.Username)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		identity = "username:" + username
	}
	var rateErr error
	if refresh {
		rateErr = s.enforceRefreshRate(p, hex.EncodeToString(tokenHash(req.RefreshToken)))
	} else {
		rateErr = s.enforceAuthRate(p, r, identity)
	}
	if rateErr != nil {
		writeAPIError(w, rateErr)
		return
	}
	s.recordRequest(r.Context(), p.id)
	var out map[string]any
	var replayed bool
	var auditUser string
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		if refresh {
			return s.refreshTx(r.Context(), p, tx, req.RefreshToken, &out, &replayed)
		}
		var id, email, hash string
		var confirmed *time.Time
		var banned *time.Time
		err := tx.QueryRow(r.Context(), `SELECT id,COALESCE(email,''),password_hash,email_confirmed_at,banned_until FROM auth.users WHERE ($2='' AND email=$1) OR ($2<>'' AND username=$2)`, strings.ToLower(strings.TrimSpace(req.Email)), username).Scan(&id, &email, &hash, &confirmed, &banned)
		if err != nil {
			_ = auth.VerifyPassword(req.Password, auth.DummyPasswordHash())
			return httpError(400, "invalid login credentials")
		}
		auditUser = id
		if !auth.VerifyPassword(req.Password, hash) {
			return httpError(400, "invalid login credentials")
		}
		if banned != nil && banned.After(time.Now()) {
			return httpError(400, "user is banned")
		}
		if confirmed == nil {
			return httpError(400, "email not confirmed")
		}
		session, err := s.issueSession(r.Context(), p, tx, id, email, "aal1", "password", "")
		if err != nil {
			return err
		}
		out = session
		return nil
	})
	if !refresh {
		s.recordSignInEvent(p, r, identity, auditUser, signInReason(err))
	}
	if err != nil {
		if useCookie && refresh {
			clearRefreshCookie(w, r, p)
		}
		writeAPIError(w, err)
		return
	}
	if replayed {
		if useCookie {
			clearRefreshCookie(w, r, p)
		}
		writeAPIError(w, httpError(400, "invalid refresh token"))
		return
	}
	if useCookie {
		setRefreshCookie(w, r, p, out["refresh_token"].(string))
		delete(out, "refresh_token")
	}
	writeJSON(w, 200, out)
}

func (s *Server) refreshTx(ctx context.Context, p project, tx pgx.Tx, raw string, out *map[string]any, replayed *bool) error {
	var id, user, aal, session string
	var expires time.Time
	var revoked *time.Time
	var recentlyRotated bool
	var email string
	err := tx.QueryRow(ctx, `SELECT id,user_id,expires_at,revoked_at,aal,COALESCE(session_id,id)::text,
  replaced_by IS NOT NULL AND revoked_at > now() - make_interval(secs => $2)
FROM auth.refresh_tokens WHERE token_hash=$1 FOR UPDATE`, tokenHash(raw), s.refreshReuse.Seconds()).Scan(&id, &user, &expires, &revoked, &aal, &session, &recentlyRotated)
	if err != nil || expires.Before(time.Now()) {
		return httpError(400, "invalid refresh token")
	}
	if revoked != nil {
		var live bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.refresh_tokens WHERE COALESCE(session_id,id)=$1::uuid AND revoked_at IS NULL AND expires_at>now())`, session).Scan(&live); err != nil {
			return err
		}
		if !live {
			// The session already ended (signed out): nothing left to protect.
			return httpError(400, "invalid refresh token")
		}
		if !recentlyRotated {
			// A rotated token used again long after rotation is treated as
			// stolen: end every session of the user. Commit the revocation
			// before reporting a replay to the caller.
			_, err = tx.Exec(ctx, `UPDATE auth.refresh_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE user_id=$1`, user)
			if err != nil {
				return err
			}
			s.endSessions(p.id, user, nil)
			*replayed = true
			return nil
		}
		// Another tab refreshed with the same token moments ago. Give this
		// caller its own token in the same session rather than ending it.
	}
	var userBanned bool
	if err = tx.QueryRow(ctx, `SELECT COALESCE(email,''), coalesce(banned_until > now(), false) FROM auth.users WHERE id=$1`, user).Scan(&email, &userBanned); err != nil {
		return err
	}
	if userBanned {
		return httpError(400, "user is banned")
	}
	method := "password"
	if aal == "aal2" {
		var verifiedFactor bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.mfa_factors WHERE user_id=$1 AND status='verified')`, user).Scan(&verifiedFactor); err != nil {
			return err
		}
		if verifiedFactor {
			method = "totp"
		} else {
			aal = "aal1"
		}
	}
	newSession, err := s.issueSession(ctx, p, tx, user, email, aal, method, session)
	if err != nil {
		return err
	}
	if revoked == nil {
		newID := newSession["refresh_token"].(string)
		if _, err = tx.Exec(ctx, `UPDATE auth.refresh_tokens SET revoked_at=now(),replaced_by=(SELECT id FROM auth.refresh_tokens WHERE token_hash=$1) WHERE id=$2`, tokenHash(newID), id); err != nil {
			return err
		}
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
		useCookie := cookieMode(r)
		if useCookie && !cookieOriginAllowed(r) {
			writeAPIError(w, httpError(http.StatusForbidden, "origin is not allowed for cookie sessions"))
			return
		}
		var id, email string
		var out map[string]any
		err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
			if err := tx.QueryRow(r.Context(), `SELECT id,COALESCE(email,'') FROM auth.users WHERE reset_token_hash=$1 AND reset_expires_at>now()`, tokenHash(token)).Scan(&id, &email); err != nil {
				return err
			}
			var e error
			out, e = s.issueSession(r.Context(), p, tx, id, email, "aal1", "recovery", "")
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
		if useCookie {
			setRefreshCookie(w, r, p, out["refresh_token"].(string))
			delete(out, "refresh_token")
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
	s.sendRecoveryEmail(strings.ToLower(strings.TrimSpace(req.Email)), raw)
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
	if err := s.requireActiveSession(r.Context(), p, cSessionID(c), c.Subject); err != nil {
		return project{}, keys.Claims{}, err
	}
	return p, c, nil
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	p, c, err := s.bearerUser(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		scope = "local"
	}
	if scope != "local" && scope != "others" && scope != "global" {
		writeAPIError(w, httpError(400, "invalid logout scope"))
		return
	}
	var ended []string
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		if cSessionID(c) == "" {
			return httpError(http.StatusUnauthorized, "invalid JWT session")
		}
		var e error
		if scope == "local" {
			ended, e = revokeSession(r.Context(), tx, cSessionID(c), c.Subject)
		} else {
			ended, e = revokeUserSessions(r.Context(), tx, c.Subject, cSessionID(c), scope == "others")
		}
		return e
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	s.endSessions(p.id, c.Subject, ended)
	if cookieMode(r) {
		clearRefreshCookie(w, r, p)
	}
	w.WriteHeader(http.StatusNoContent)
}

// authChangePassword verifies the caller's current password in the same
// transaction that replaces it and ends every other sign-in.
func (s *Server) authChangePassword(w http.ResponseWriter, r *http.Request) {
	p, c, err := s.bearerUser(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if cSessionID(c) == "" {
		writeAPIError(w, httpError(http.StatusUnauthorized, "a sign-in session is required"))
		return
	}
	var req authRequest
	if err = decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeAPIError(w, httpError(400, "current_password and new_password are required"))
		return
	}
	h, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		writeAPIError(w, httpError(400, err.Error()))
		return
	}
	var ended []string
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		var oldHash string
		if e := tx.QueryRow(r.Context(), `SELECT password_hash FROM auth.users WHERE id=$1 FOR UPDATE`, c.Subject).Scan(&oldHash); e != nil {
			return e
		}
		if !auth.VerifyPassword(req.CurrentPassword, oldHash) {
			return httpError(400, "invalid current password")
		}
		if _, e := tx.Exec(r.Context(), `UPDATE auth.users SET password_hash=$1,updated_at=now() WHERE id=$2`, h, c.Subject); e != nil {
			return e
		}
		var e error
		ended, e = revokeUserSessions(r.Context(), tx, c.Subject, cSessionID(c), true)
		return e
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	s.endSessions(p.id, c.Subject, ended)
	w.WriteHeader(http.StatusNoContent)
}

// authAdminLogout ends a target user's sessions without preventing new sign-ins.
func (s *Server) authAdminLogout(w http.ResponseWriter, r *http.Request) {
	p, err := s.serviceProject(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	id := r.PathValue("id")
	if !uuidPattern.MatchString(id) {
		writeAPIError(w, httpError(400, "invalid user id"))
		return
	}
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		_, e := revokeUserSessions(r.Context(), tx, id, "", false)
		return e
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	s.endSessions(p.id, id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func cSessionID(c keys.Claims) string {
	var claims struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(c.JSON, &claims)
	return claims.SessionID
}
func isRecoverySession(c keys.Claims) bool {
	var claims struct {
		AMR []struct {
			Method string `json:"method"`
		} `json:"amr"`
	}
	_ = json.Unmarshal(c.JSON, &claims)
	for _, method := range claims.AMR {
		if method.Method == "recovery" {
			return true
		}
	}
	return false
}
func (s *Server) authUser(w http.ResponseWriter, r *http.Request) {
	p, c, err := s.bearerUser(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var id, email, username string
	var confirmed *time.Time
	var meta, appMeta []byte
	var created, updated time.Time
	var factors []mfaFactor
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		if err := tx.QueryRow(r.Context(), `SELECT id,COALESCE(email,''),COALESCE(username,''),email_confirmed_at,raw_user_meta_data,raw_app_meta_data,created_at,updated_at FROM auth.users WHERE id=$1`, c.Subject).Scan(&id, &email, &username, &confirmed, &meta, &appMeta, &created, &updated); err != nil {
			return err
		}
		var err error
		factors, err = loadMFAFactors(r.Context(), tx, c.Subject)
		return err
	})
	if err != nil {
		writeAPIError(w, httpError(404, "user not found"))
		return
	}
	user := userJSON(id, email, username, confirmed != nil, meta, appMeta, created, updated)
	user["factors"] = factors
	writeJSON(w, 200, user)
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
	var ended []string
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		if req.Password != "" {
			if cSessionID(c) == "" {
				return httpError(401, "a sign-in session is required")
			}
			var currentHash string
			if e := tx.QueryRow(r.Context(), `SELECT password_hash FROM auth.users WHERE id=$1 FOR UPDATE`, c.Subject).Scan(&currentHash); e != nil {
				return e
			}
			if !isRecoverySession(c) && (req.CurrentPassword == "" || !auth.VerifyPassword(req.CurrentPassword, currentHash)) {
				return httpError(400, "invalid current password")
			}
			h, e := auth.HashPassword(req.Password)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(r.Context(), `UPDATE auth.users SET password_hash=$1,updated_at=now() WHERE id=$2`, h, c.Subject); e != nil {
				return e
			}
			ended, e = revokeUserSessions(r.Context(), tx, c.Subject, cSessionID(c), true)
			if e != nil {
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
	if req.Password != "" {
		s.endSessions(p.id, c.Subject, ended)
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
		rows, e := tx.Query(r.Context(), `SELECT id,COALESCE(email,''),COALESCE(username,''),email_confirmed_at,raw_user_meta_data,raw_app_meta_data,created_at,updated_at FROM auth.users ORDER BY created_at`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id, email, username string
			var c *time.Time
			var m, appMeta []byte
			var a, b time.Time
			if e = rows.Scan(&id, &email, &username, &c, &m, &appMeta, &a, &b); e != nil {
				return e
			}
			users = append(users, userJSON(id, email, username, c != nil, m, appMeta, a, b))
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
	h, err := adminPasswordHash(req)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	username := ""
	if req.Username != "" {
		username, err = normalizedUsername(req.Username)
		if err != nil {
			writeAPIError(w, err)
			return
		}
	}
	if strings.TrimSpace(req.Email) == "" && username == "" {
		writeAPIError(w, httpError(400, "email or username is required"))
		return
	}
	id, _ := auth.UUID()
	m, _ := json.Marshal(req.UserMetadata)
	if req.UserMetadata == nil {
		m = []byte(`{}`)
	}
	appMeta, _ := json.Marshal(req.AppMetadata)
	if req.AppMetadata == nil {
		appMeta = []byte(`{}`)
	}
	var u map[string]any
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		var a, b time.Time
		c := time.Now()
		if req.EmailConfirm {
			c = time.Now()
		}
		email := strings.ToLower(strings.TrimSpace(req.Email))
		e := tx.QueryRow(r.Context(), `INSERT INTO auth.users(id,email,username,password_hash,email_confirmed_at,raw_user_meta_data,raw_app_meta_data) VALUES($1,NULLIF($2,''),NULLIF($3,''),$4,$5,$6,$7) RETURNING created_at,updated_at`, id, email, username, h, func() any {
			if req.EmailConfirm || email == "" {
				return c
			}
			return nil
		}(), m, appMeta).Scan(&a, &b)
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
			u = userJSON(id, email, username, req.EmailConfirm || email == "", m, appMeta, a, b)
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
	banned := false
	passwordChanged := false
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		if req.Password != "" || req.PasswordHash != "" {
			h, e := adminPasswordHash(req)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(r.Context(), `UPDATE auth.users SET password_hash=$1,updated_at=now() WHERE id=$2`, h, id); e != nil {
				return e
			}
			if _, e = revokeUserSessions(r.Context(), tx, id, "", false); e != nil {
				return e
			}
			passwordChanged = true
		}
		if req.Email != "" {
			if _, e := tx.Exec(r.Context(), `UPDATE auth.users SET email=$1,updated_at=now() WHERE id=$2`, strings.ToLower(strings.TrimSpace(req.Email)), id); e != nil {
				return e
			}
		}
		if req.Username != "" {
			name, e := normalizedUsername(req.Username)
			if e != nil {
				return e
			}
			if _, e := tx.Exec(r.Context(), `UPDATE auth.users SET username=$1,updated_at=now() WHERE id=$2`, name, id); e != nil {
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
			if until != nil && until.After(time.Now()) {
				// A ban ends the user's current sessions, not only future sign-ins.
				if _, e := tx.Exec(r.Context(), `UPDATE auth.refresh_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE user_id=$1`, id); e != nil {
					return e
				}
				banned = true
			}
		}
		if req.UserMetadata != nil {
			m, _ := json.Marshal(req.UserMetadata)
			if _, e := tx.Exec(r.Context(), `UPDATE auth.users SET raw_user_meta_data=$1,updated_at=now() WHERE id=$2`, m, id); e != nil {
				return e
			}
		}
		if req.AppMetadata != nil {
			m, _ := json.Marshal(req.AppMetadata)
			if _, e := tx.Exec(r.Context(), `UPDATE auth.users SET raw_app_meta_data=$1,updated_at=now() WHERE id=$2`, m, id); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if banned || passwordChanged {
		s.endSessions(p.id, id, nil)
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
	s.endSessions(p.id, id, nil)
	w.WriteHeader(http.StatusNoContent)
}
