package control

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/auth"
	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a project or account does not exist.
var ErrNotFound = errors.New("not found")

// ErrForbidden is returned when an account does not own the requested project.
var ErrForbidden = errors.New("forbidden")

// ErrDuplicate is returned when a unique constraint would be violated (e.g. duplicate email).
var ErrDuplicate = errors.New("already exists")

// Account is a dashboard operator account.
type Account struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

// sessionTTL is the lifetime of a dashboard session cookie.
const sessionTTL = 24 * time.Hour

// CreateAccount creates a new dashboard account with the given email and password.
func CreateAccount(ctx context.Context, adminURL, email, password string) (Account, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return Account{}, fmt.Errorf("email is required")
	}
	if len(password) < 8 || len(password) > 1024 {
		return Account{}, fmt.Errorf("password must be between 8 and 1024 characters")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return Account{}, err
	}
	id, err := auth.UUID()
	if err != nil {
		return Account{}, err
	}
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return Account{}, err
	}
	defer conn.Close(ctx)
	var a Account
	err = conn.QueryRow(ctx,
		`INSERT INTO control.accounts(id,email,password_hash) VALUES($1,$2,$3) RETURNING id,email,created_at`,
		id, email, hash,
	).Scan(&a.ID, &a.Email, &a.CreatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return Account{}, ErrDuplicate
		}
		return Account{}, err
	}
	return a, nil
}

// AuthenticateAccount verifies email + password and returns the account on success.
// Always runs argon2id at equal cost whether the email exists or not.
func AuthenticateAccount(ctx context.Context, adminURL, email, password string) (Account, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		// Connection failure: still run dummy verify for timing parity.
		auth.VerifyPassword(password, auth.DummyPasswordHash())
		return Account{}, fmt.Errorf("invalid credentials")
	}
	defer conn.Close(ctx)
	var a Account
	var hash string
	err = conn.QueryRow(ctx,
		`SELECT id,email,password_hash,created_at FROM control.accounts WHERE email=$1`,
		email,
	).Scan(&a.ID, &a.Email, &hash, &a.CreatedAt)
	if err == pgx.ErrNoRows {
		auth.VerifyPassword(password, auth.DummyPasswordHash())
		return Account{}, fmt.Errorf("invalid credentials")
	}
	if err != nil {
		auth.VerifyPassword(password, auth.DummyPasswordHash())
		return Account{}, fmt.Errorf("invalid credentials")
	}
	if !auth.VerifyPassword(password, hash) {
		return Account{}, fmt.Errorf("invalid credentials")
	}
	return a, nil
}

// IssueSession creates a new session for the given account and returns the opaque token.
// The token is a base64url-encoded 32-byte random value; only its SHA-256 hash is stored.
func IssueSession(ctx context.Context, adminURL, accountID string) (string, error) {
	token, digest, err := auth.RandomToken(32)
	if err != nil {
		return "", err
	}
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	expiresAt := time.Now().UTC().Add(sessionTTL)
	_, err = conn.Exec(ctx,
		`INSERT INTO control.sessions(token_hash,account_id,expires_at) VALUES($1,$2,$3)`,
		digest, accountID, expiresAt,
	)
	if err != nil {
		return "", err
	}
	return token, nil
}

// ResolveSession looks up the account for a non-expired session token.
func ResolveSession(ctx context.Context, adminURL, token string) (Account, error) {
	digest := sha256.Sum256([]byte(token))
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return Account{}, fmt.Errorf("invalid session")
	}
	defer conn.Close(ctx)
	var a Account
	err = conn.QueryRow(ctx,
		`SELECT a.id,a.email,a.created_at
		 FROM control.sessions s
		 JOIN control.accounts a ON a.id=s.account_id
		 WHERE s.token_hash=$1 AND s.expires_at > now()`,
		digest[:],
	).Scan(&a.ID, &a.Email, &a.CreatedAt)
	if err != nil {
		return Account{}, fmt.Errorf("invalid session")
	}
	return a, nil
}

// RevokeSession deletes the given session token.
func RevokeSession(ctx context.Context, adminURL, token string) error {
	digest := sha256.Sum256([]byte(token))
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `DELETE FROM control.sessions WHERE token_hash=$1`, digest[:])
	return err
}

// AccountOwnsProject returns nil when accountID owns projectID, ErrNotFound when
// the project does not exist, and ErrForbidden when it exists but is owned by someone else.
func AccountOwnsProject(ctx context.Context, adminURL, projectID, accountID string) error {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("internal error")
	}
	defer conn.Close(ctx)
	var ownerID *string
	err = conn.QueryRow(ctx,
		`SELECT owner_id::text FROM control.projects WHERE id=$1`,
		projectID,
	).Scan(&ownerID)
	if err == pgx.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("internal error")
	}
	if ownerID == nil || *ownerID != accountID {
		return ErrForbidden
	}
	return nil
}

// SetProjectOwner sets the owner_id for an existing project.
func SetProjectOwner(ctx context.Context, adminURL, projectID, accountID string) error {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tag, err := conn.Exec(ctx,
		`UPDATE control.projects SET owner_id=$1 WHERE id=$2`,
		accountID, projectID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ChangePassword verifies currentPassword then replaces the stored hash.
func ChangePassword(ctx context.Context, adminURL, accountID, currentPassword, newPassword string) error {
	if len(newPassword) < 8 || len(newPassword) > 1024 {
		return fmt.Errorf("password must be between 8 and 1024 characters")
	}
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return fmt.Errorf("invalid credentials")
	}
	defer conn.Close(ctx)
	var stored string
	err = conn.QueryRow(ctx, `SELECT password_hash FROM control.accounts WHERE id=$1`, accountID).Scan(&stored)
	if err != nil {
		auth.VerifyPassword(currentPassword, auth.DummyPasswordHash())
		return fmt.Errorf("invalid credentials")
	}
	if !auth.VerifyPassword(currentPassword, stored) {
		return fmt.Errorf("invalid credentials")
	}
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	_, err = conn.Exec(ctx, `UPDATE control.accounts SET password_hash=$1 WHERE id=$2`, hash, accountID)
	return err
}

// DeleteAccount deletes all projects owned by accountID then deletes the account.
// Sessions are removed via ON DELETE CASCADE on control.sessions.
func DeleteAccount(ctx context.Context, adminURL, accountID string) error {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT id FROM control.projects WHERE owner_id=$1`, accountID)
	if err != nil {
		return err
	}
	var projectIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		projectIDs = append(projectIDs, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range projectIDs {
		if err := DeleteProject(ctx, adminURL, id); err != nil {
			return fmt.Errorf("delete project %s: %w", id, err)
		}
	}
	_, err = conn.Exec(ctx, `DELETE FROM control.accounts WHERE id=$1`, accountID)
	return err
}

// controlDBConn opens a connection to the _control database.
func controlDBConn(ctx context.Context, adminURL string) (*pgx.Conn, error) {
	u, err := withDatabase(adminURL, "_control")
	if err != nil {
		return nil, fmt.Errorf("parse admin URL: %w", err)
	}
	return pgx.Connect(ctx, u)
}
