package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/515woodpl-dot/pibase/internal/secretbox"
	"github.com/jackc/pgx/v5"
)

type project struct {
	id                string
	database          string
	anonRole          string
	authenticatedRole string
	serviceRole       string
	adminRole         string
	secret            []byte
	maxDatabaseBytes  int64
}

type principal struct {
	project *project
	role    string
	claims  []byte
}

func (s *Server) authenticate(ctx context.Context, r *http.Request) (principal, error) {
	apiKey := strings.TrimSpace(r.Header.Get("apikey"))
	if apiKey == "" {
		return principal{}, httpError(http.StatusUnauthorized, "missing apikey")
	}
	hash := keys.Hash(apiKey)
	var p project
	var encrypted, anonHash, serviceHash []byte
	err := s.control.QueryRow(ctx, `SELECT id,database_name,anon_role,authenticated_role,service_role,admin_role,jwt_secret_encrypted,anon_key_hash,service_key_hash,max_database_bytes
FROM control.project_catalog WHERE anon_key_hash=$1 OR service_key_hash=$1`, hash).Scan(
		&p.id, &p.database, &p.anonRole, &p.authenticatedRole, &p.serviceRole, &p.adminRole, &encrypted, &anonHash, &serviceHash, &p.maxDatabaseBytes)
	if err != nil {
		if err == pgx.ErrNoRows {
			return principal{}, httpError(http.StatusUnauthorized, "invalid apikey")
		}
		return principal{}, fmt.Errorf("lookup project: %w", err)
	}
	if !keys.EqualHash(apiKey, anonHash) && !keys.EqualHash(apiKey, serviceHash) {
		return principal{}, httpError(http.StatusUnauthorized, "invalid apikey")
	}
	p.secret, err = secretbox.Decrypt(s.masterKey, encrypted)
	if err != nil {
		return principal{}, fmt.Errorf("decrypt project secret: %w", err)
	}
	token := apiKey
	if authorization := r.Header.Get("Authorization"); authorization != "" {
		const prefix = "Bearer "
		if !strings.HasPrefix(authorization, prefix) || len(authorization) == len(prefix) {
			return principal{}, httpError(http.StatusUnauthorized, "invalid Authorization header")
		}
		token = strings.TrimSpace(strings.TrimPrefix(authorization, prefix))
	}
	claims, err := keys.VerifyJWT(token, p.secret, time.Now())
	if err != nil {
		return principal{}, httpError(http.StatusUnauthorized, "invalid JWT")
	}
	var databaseRole string
	switch claims.Role {
	case "anon":
		databaseRole = p.anonRole
	case "authenticated":
		databaseRole = p.authenticatedRole
	case "service_role":
		if !keys.EqualHash(apiKey, serviceHash) {
			return principal{}, httpError(http.StatusForbidden, "service role requires service apikey")
		}
		databaseRole = p.serviceRole
	default:
		return principal{}, httpError(http.StatusForbidden, "JWT role is not allowed")
	}
	if len(claims.JSON) == 0 {
		claims.JSON, _ = json.Marshal(claims)
	}
	return principal{project: &p, role: databaseRole, claims: claims.JSON}, nil
}

type requestError struct {
	status  int
	message string
}

func (e requestError) Error() string { return e.message }

func httpError(status int, message string) error {
	return requestError{status: status, message: message}
}
