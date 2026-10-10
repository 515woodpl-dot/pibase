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

// PasswordResetTTL is how long a reset link works.
const PasswordResetTTL = time.Hour

// ErrInvalidResetToken means the link is unknown, used or expired.
var ErrInvalidResetToken = errors.New("this reset link is invalid or has expired; request a new one")

// CreatePasswordReset returns a single-use reset token for the account with
// this email. found is false (and token empty) when no such account exists,
// so callers can answer the same way either way. Older links of the account
// stop working.
func CreatePasswordReset(ctx context.Context, adminURL, email string) (token string, found bool, err error) {
	email = strings.ToLower(strings.TrimSpace(email))
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return "", false, err
	}
	defer conn.Close(ctx)
	var accountID string
	err = conn.QueryRow(ctx, `SELECT id FROM control.accounts WHERE email=$1`, email).Scan(&accountID)
	if err == pgx.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	token, digest, err := auth.RandomToken(32)
	if err != nil {
		return "", false, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `DELETE FROM control.password_resets WHERE account_id=$1 OR expires_at <= now()`, accountID); err != nil {
		return "", false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO control.password_resets(token_hash,account_id,expires_at) VALUES($1,$2,$3)`,
		digest, accountID, time.Now().UTC().Add(PasswordResetTTL)); err != nil {
		return "", false, err
	}
	return token, true, tx.Commit(ctx)
}

// ResetPassword sets a new password with a reset token, uses the token up and
// signs the account out everywhere. It returns the account's email.
func ResetPassword(ctx context.Context, adminURL, token, newPassword string) (string, error) {
	if len(newPassword) < 8 || len(newPassword) > 1024 {
		return "", fmt.Errorf("password must be between 8 and 1024 characters")
	}
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	digest := sha256.Sum256([]byte(token))
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var accountID, email string
	// Deleting the row claims the token, so two submissions can't both use it.
	err = tx.QueryRow(ctx, `DELETE FROM control.password_resets r USING control.accounts a
WHERE r.token_hash=$1 AND r.expires_at > now() AND a.id=r.account_id
RETURNING a.id, a.email`, digest[:]).Scan(&accountID, &email)
	if err == pgx.ErrNoRows {
		return "", ErrInvalidResetToken
	}
	if err != nil {
		return "", err
	}
	if err = setPasswordTx(ctx, tx, accountID, newPassword); err != nil {
		return "", err
	}
	return email, tx.Commit(ctx)
}

// SetAccountPassword is the operator's reset: it sets a new password for the
// account with this email and signs it out everywhere, without the old
// password or an email.
func SetAccountPassword(ctx context.Context, adminURL, email, newPassword string) error {
	if len(newPassword) < 8 || len(newPassword) > 1024 {
		return fmt.Errorf("password must be between 8 and 1024 characters")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var accountID string
	if err = tx.QueryRow(ctx, `SELECT id FROM control.accounts WHERE email=$1 FOR UPDATE`, email).Scan(&accountID); err != nil {
		if err == pgx.ErrNoRows {
			return ErrNotFound
		}
		return err
	}
	if err = setPasswordTx(ctx, tx, accountID, newPassword); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func setPasswordTx(ctx context.Context, tx pgx.Tx, accountID, newPassword string) error {
	hash, err := auth.HashPassword(newPassword)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE control.accounts SET password_hash=$1 WHERE id=$2`, hash, accountID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM control.sessions WHERE account_id=$1`, accountID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM control.password_resets WHERE account_id=$1`, accountID)
	return err
}
