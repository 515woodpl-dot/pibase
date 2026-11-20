package control

import (
	"context"
	"crypto/sha256"
	"errors"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/auth"
	"github.com/jackc/pgx/v5"
)

// Dashboard accounts that sign themselves up confirm their email address
// before they may create projects. Accounts made by the operator are
// confirmed from the start.

// EmailVerificationTTL is how long a confirmation link works. Accounts still
// unconfirmed after this long, with no projects, are removed.
const EmailVerificationTTL = 48 * time.Hour

// ErrInvalidVerifyToken means the link is unknown, used or expired.
var ErrInvalidVerifyToken = errors.New("this confirmation link is invalid or has expired; sign in and ask for a new one")

// ErrEmailNotVerified is returned when an unconfirmed account tries
// something that needs a confirmed email, such as creating a project.
var ErrEmailNotVerified = errors.New("confirm your email address first: open the link we emailed you")

// AccountVerified reports whether the account has confirmed its email.
func AccountVerified(ctx context.Context, adminURL, accountID string) (bool, error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return false, err
	}
	defer conn.Close(ctx)
	var verified bool
	err = conn.QueryRow(ctx, `SELECT email_verified_at IS NOT NULL FROM control.accounts WHERE id=$1`, accountID).Scan(&verified)
	if err == pgx.ErrNoRows {
		return false, ErrNotFound
	}
	return verified, err
}

// MarkAccountVerified confirms an account without a link (operator-made
// accounts, or servers that cannot send email).
func MarkAccountVerified(ctx context.Context, adminURL, accountID string) error {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `UPDATE control.accounts SET email_verified_at=COALESCE(email_verified_at,now()) WHERE id=$1`, accountID)
	return err
}

// CreateEmailVerification returns a new single-use confirmation token for an
// unconfirmed account. Older links of the account stop working. It returns
// "" when the account is already confirmed.
func CreateEmailVerification(ctx context.Context, adminURL, accountID string) (string, error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var verified bool
	if err := tx.QueryRow(ctx, `SELECT email_verified_at IS NOT NULL FROM control.accounts WHERE id=$1 FOR UPDATE`, accountID).Scan(&verified); err != nil {
		if err == pgx.ErrNoRows {
			return "", ErrNotFound
		}
		return "", err
	}
	if verified {
		return "", nil
	}
	token, digest, err := auth.RandomToken(32)
	if err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM control.email_verifications WHERE account_id=$1 OR expires_at <= now()`, accountID); err != nil {
		return "", err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO control.email_verifications(token_hash,account_id,expires_at) VALUES($1,$2,$3)`,
		digest, accountID, time.Now().UTC().Add(EmailVerificationTTL)); err != nil {
		return "", err
	}
	return token, tx.Commit(ctx)
}

// VerifyEmail uses up a confirmation token and confirms its account. It
// returns the account's email.
func VerifyEmail(ctx context.Context, adminURL, token string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 128 {
		return "", ErrInvalidVerifyToken
	}
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return "", err
	}
	defer conn.Close(ctx)
	digest := sha256.Sum256([]byte(token))
	var email string
	err = conn.QueryRow(ctx, `WITH used AS (
  DELETE FROM control.email_verifications WHERE token_hash=$1 AND expires_at > now() RETURNING account_id)
UPDATE control.accounts a SET email_verified_at=COALESCE(a.email_verified_at,now())
FROM used WHERE a.id=used.account_id RETURNING a.email`, digest[:]).Scan(&email)
	if err == pgx.ErrNoRows {
		return "", ErrInvalidVerifyToken
	}
	return email, err
}

// RemoveStaleUnverifiedAccounts deletes accounts that never confirmed their
// email within EmailVerificationTTL and own no projects, so abandoned or
// fake sign-ups do not pile up. It returns how many were removed.
func RemoveStaleUnverifiedAccounts(ctx context.Context, adminURL string) (int64, error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return 0, err
	}
	defer conn.Close(ctx)
	tag, err := conn.Exec(ctx, `DELETE FROM control.accounts a WHERE a.email_verified_at IS NULL
  AND a.created_at < now() - make_interval(secs => $1)
  AND NOT EXISTS (SELECT 1 FROM control.projects p WHERE p.owner_id=a.id)`, EmailVerificationTTL.Seconds())
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// AccountSummary is one row of the operator's account list.
type AccountSummary struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	Organization string     `json:"organization"`
	CreatedAt    time.Time  `json:"created_at"`
	VerifiedAt   *time.Time `json:"email_verified_at"`
	Projects     int        `json:"projects"`
}

// ListAccounts returns every dashboard account, newest first.
func ListAccounts(ctx context.Context, adminURL string) ([]AccountSummary, error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	rows, err := conn.Query(ctx, `SELECT a.id,a.email,a.organization,a.created_at,a.email_verified_at,
  (SELECT count(*) FROM control.projects p WHERE p.owner_id=a.id)
FROM control.accounts a ORDER BY a.created_at DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountSummary{}
	for rows.Next() {
		var a AccountSummary
		if err := rows.Scan(&a.ID, &a.Email, &a.Organization, &a.CreatedAt, &a.VerifiedAt, &a.Projects); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
