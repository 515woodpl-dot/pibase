package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // TOTP interoperability requires HMAC-SHA-1 by default.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/auth"
	"github.com/515woodpl-dot/pibase/internal/secretbox"
	"github.com/jackc/pgx/v5"
	qrcode "github.com/skip2/go-qrcode"
)

type mfaRequest struct {
	FactorType  string `json:"factor_type"`
	Friendly    string `json:"friendly_name"`
	Issuer      string `json:"issuer"`
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
}

type mfaFactor struct {
	ID             string     `json:"id"`
	FriendlyName   string     `json:"friendly_name,omitempty"`
	FactorType     string     `json:"factor_type"`
	Status         string     `json:"status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastChallenged *time.Time `json:"last_challenged_at,omitempty"`
}

func (s *Server) authMFAEnroll(w http.ResponseWriter, r *http.Request) {
	p, claims, err := s.bearerUser(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var req mfaRequest
	if err = decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	if req.FactorType != "totp" {
		writeAPIError(w, httpError(http.StatusBadRequest, "only totp MFA factors are supported"))
		return
	}
	req.Friendly = strings.TrimSpace(req.Friendly)
	if len(req.Friendly) > 128 || strings.ContainsAny(req.Friendly, "\r\n\x00") {
		writeAPIError(w, httpError(http.StatusBadRequest, "invalid friendly_name"))
		return
	}
	issuer := strings.TrimSpace(req.Issuer)
	if issuer == "" {
		issuer = "pibase"
	}
	if len(issuer) > 64 || strings.ContainsAny(issuer, "\r\n\x00") {
		writeAPIError(w, httpError(http.StatusBadRequest, "invalid issuer"))
		return
	}
	secretBytes := make([]byte, 20)
	if _, err = rand.Read(secretBytes); err != nil {
		writeAPIError(w, err)
		return
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secretBytes)
	encrypted, err := secretbox.Encrypt(s.masterKey, []byte(secret))
	if err != nil {
		writeAPIError(w, err)
		return
	}
	id, err := auth.UUID()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	var email string
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		var count int
		if err := tx.QueryRow(r.Context(), `SELECT email FROM auth.users WHERE id=$1`, claims.Subject).Scan(&email); err != nil {
			return err
		}
		if err := tx.QueryRow(r.Context(), `SELECT count(*) FROM auth.mfa_factors WHERE user_id=$1`, claims.Subject).Scan(&count); err != nil {
			return err
		}
		if count >= 10 {
			return httpError(http.StatusUnprocessableEntity, "too many MFA factors")
		}
		_, err := tx.Exec(r.Context(), `INSERT INTO auth.mfa_factors(id,user_id,factor_type,friendly_name,secret_encrypted) VALUES($1,$2,'totp',NULLIF($3,''),$4)`, id, claims.Subject, req.Friendly, encrypted)
		if err != nil && strings.Contains(err.Error(), "unique") {
			return httpError(http.StatusUnprocessableEntity, "friendly_name is already used")
		}
		return err
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	uri := "otpauth://totp/" + url.PathEscape(issuer+":"+email) + "?secret=" + url.QueryEscape(secret) + "&issuer=" + url.QueryEscape(issuer)
	qr, err := qrcode.New(uri, qrcode.Medium)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "type": "totp", "friendly_name": req.Friendly,
		"totp": map[string]string{
			"secret": secret, "uri": uri,
			// auth-js prepends its own data:image/svg+xml URI prefix.
			"qr_code": qrSVG(qr.Bitmap()),
		},
	})
}

func qrSVG(bitmap [][]bool) string {
	size := len(bitmap)
	var svg strings.Builder
	svg.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 `)
	svg.WriteString(strconv.Itoa(size))
	svg.WriteByte(' ')
	svg.WriteString(strconv.Itoa(size))
	svg.WriteString(`" shape-rendering="crispEdges"><rect width="100%" height="100%" fill="white"/><path fill="black" d="`)
	for y, row := range bitmap {
		for x, set := range row {
			if !set {
				continue
			}
			svg.WriteByte('M')
			svg.WriteString(strconv.Itoa(x))
			svg.WriteByte(' ')
			svg.WriteString(strconv.Itoa(y))
			svg.WriteString("h1v1h-1z")
		}
	}
	svg.WriteString(`"/></svg>`)
	return svg.String()
}

func (s *Server) authMFAChallenge(w http.ResponseWriter, r *http.Request) {
	p, claims, err := s.bearerUser(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if err = s.enforceAuthRate(p, r, claims.Subject); err != nil {
		writeAPIError(w, err)
		return
	}
	factorID := r.PathValue("id")
	challengeID, err := auth.UUID()
	if err != nil {
		writeAPIError(w, err)
		return
	}
	expires := time.Now().Add(5 * time.Minute)
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM auth.mfa_factors WHERE id=$1 AND user_id=$2)`, factorID, claims.Subject).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return httpError(http.StatusNotFound, "MFA factor not found")
		}
		// TOTP challenges carry no server nonce, so retaining more than the
		// newest challenge adds no value and permits needless table growth.
		if _, err := tx.Exec(r.Context(), `DELETE FROM auth.mfa_challenges WHERE factor_id=$1`, factorID); err != nil {
			return err
		}
		if _, err := tx.Exec(r.Context(), `UPDATE auth.mfa_factors SET last_challenged_at=now(),updated_at=now() WHERE id=$1`, factorID); err != nil {
			return err
		}
		_, err := tx.Exec(r.Context(), `INSERT INTO auth.mfa_challenges(id,factor_id,user_id,expires_at) VALUES($1,$2,$3,$4)`, challengeID, factorID, claims.Subject, expires)
		return err
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": challengeID, "expires_at": expires.UTC().Format(time.RFC3339Nano)})
}

func (s *Server) authMFAVerify(w http.ResponseWriter, r *http.Request) {
	p, claims, err := s.bearerUser(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if err = s.enforceAuthRate(p, r, claims.Subject); err != nil {
		writeAPIError(w, err)
		return
	}
	var req mfaRequest
	if err = decodeJSON(r, &req); err != nil {
		writeAPIError(w, err)
		return
	}
	if req.ChallengeID == "" || len(req.Code) != 6 {
		writeAPIError(w, httpError(http.StatusBadRequest, "challenge_id and six-digit code are required"))
		return
	}
	factorID := r.PathValue("id")
	var session map[string]any
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		var encrypted []byte
		var expires time.Time
		var verified *time.Time
		var lastCounter *int64
		var email string
		err := tx.QueryRow(r.Context(), `SELECT f.secret_encrypted,c.expires_at,c.verified_at,f.last_used_counter,u.email
FROM auth.mfa_challenges c
JOIN auth.mfa_factors f ON f.id=c.factor_id
JOIN auth.users u ON u.id=f.user_id
WHERE c.id=$1 AND c.factor_id=$2 AND c.user_id=$3
FOR UPDATE OF c,f`, req.ChallengeID, factorID, claims.Subject).Scan(&encrypted, &expires, &verified, &lastCounter, &email)
		if err != nil || verified != nil || !expires.After(time.Now()) {
			return httpError(http.StatusBadRequest, "invalid or expired MFA challenge")
		}
		secret, err := secretbox.Decrypt(s.masterKey, encrypted)
		if err != nil {
			return err
		}
		counter, ok := verifyTOTP(string(secret), req.Code, time.Now())
		if !ok || (lastCounter != nil && counter <= *lastCounter) {
			return httpError(http.StatusBadRequest, "invalid TOTP code")
		}
		if _, err = tx.Exec(r.Context(), `UPDATE auth.mfa_challenges SET verified_at=now() WHERE id=$1`, req.ChallengeID); err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `UPDATE auth.mfa_factors SET status='verified',last_used_counter=$1,updated_at=now() WHERE id=$2`, counter, factorID); err != nil {
			return err
		}
		session, err = s.issueSession(r.Context(), p, tx, claims.Subject, email, "aal2", "totp")
		if err != nil {
			return err
		}
		newRefresh := session["refresh_token"].(string)
		_, err = tx.Exec(r.Context(), `UPDATE auth.refresh_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE user_id=$1 AND token_hash<>$2`, claims.Subject, tokenHash(newRefresh))
		return err
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, session)
}

func (s *Server) authMFAUnenroll(w http.ResponseWriter, r *http.Request) {
	p, claims, err := s.bearerUser(r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	factorID := r.PathValue("id")
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(r.Context(), `SELECT status FROM auth.mfa_factors WHERE id=$1 AND user_id=$2`, factorID, claims.Subject).Scan(&status); err != nil {
			return httpError(http.StatusNotFound, "MFA factor not found")
		}
		if status == "verified" && claims.AAL != "aal2" {
			return httpError(http.StatusUnprocessableEntity, "aal2 session required to remove a verified factor")
		}
		_, err := tx.Exec(r.Context(), `DELETE FROM auth.mfa_factors WHERE id=$1 AND user_id=$2`, factorID, claims.Subject)
		return err
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": factorID})
}

func loadMFAFactors(ctx context.Context, tx pgx.Tx, userID string) ([]mfaFactor, error) {
	rows, err := tx.Query(ctx, `SELECT id,COALESCE(friendly_name,''),factor_type,status,created_at,updated_at,last_challenged_at FROM auth.mfa_factors WHERE user_id=$1 ORDER BY created_at,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	factors := make([]mfaFactor, 0)
	for rows.Next() {
		var factor mfaFactor
		if err := rows.Scan(&factor.ID, &factor.FriendlyName, &factor.FactorType, &factor.Status, &factor.CreatedAt, &factor.UpdatedAt, &factor.LastChallenged); err != nil {
			return nil, err
		}
		factors = append(factors, factor)
	}
	return factors, rows.Err()
}

func verifyTOTP(secret, code string, now time.Time) (int64, bool) {
	current := now.Unix() / 30
	for offset := int64(-1); offset <= 1; offset++ {
		counter := current + offset
		if counter < 0 {
			continue
		}
		expected, err := totpCode(secret, counter)
		if err == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return counter, true
		}
	}
	return 0, false
}

func totpCode(secret string, counter int64) (string, error) {
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil || len(decoded) < 16 || counter < 0 {
		return "", fmt.Errorf("invalid TOTP secret or counter")
	}
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(counter))
	mac := hmac.New(sha1.New, decoded)
	_, _ = mac.Write(message[:])
	digest := mac.Sum(nil)
	index := digest[len(digest)-1] & 0x0f
	value := (uint32(digest[index])&0x7f)<<24 | uint32(digest[index+1])<<16 | uint32(digest[index+2])<<8 | uint32(digest[index+3])
	return fmt.Sprintf("%06d", value%1_000_000), nil
}
