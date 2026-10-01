package adminsql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type project struct {
	database string
	role     string
}

type Result struct {
	Columns []string         `json:"columns,omitempty"`
	Rows    []map[string]any `json:"rows,omitempty"`
	Command string           `json:"command,omitempty"`
}

func Run(ctx context.Context, adminURL, projectID, statement string) (Result, error) {
	if statement == "" {
		return Result{}, fmt.Errorf("SQL statement is required")
	}
	if err := validateOwnerSQL(statement); err != nil {
		return Result{}, err
	}
	p, err := lookupProject(ctx, adminURL, projectID)
	if err != nil {
		return Result{}, err
	}
	conn, err := connectProject(ctx, adminURL, p.database)
	if err != nil {
		return Result{}, err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{p.role}.Sanitize()+`; SET LOCAL statement_timeout='30s'`); err != nil {
		return Result{}, err
	}
	rows, err := tx.Query(ctx, statement)
	if err != nil {
		return Result{}, err
	}
	fields := rows.FieldDescriptions()
	result := Result{}
	for _, field := range fields {
		result.Columns = append(result.Columns, field.Name)
	}
	for rows.Next() {
		if len(result.Rows) >= 1000 {
			rows.Close()
			return Result{}, fmt.Errorf("SQL result exceeds 1000-row limit")
		}
		values, err := rows.Values()
		if err != nil {
			rows.Close()
			return Result{}, err
		}
		row := make(map[string]any, len(values))
		for i, value := range values {
			row[result.Columns[i]] = value
		}
		result.Rows = append(result.Rows, row)
	}
	result.Command = rows.CommandTag().String()
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return result, nil
}

func ApplyMigration(ctx context.Context, adminURL, projectID, name, sql string) error {
	if name == "" || sql == "" {
		return fmt.Errorf("migration name and SQL are required")
	}
	p, err := lookupProject(ctx, adminURL, projectID)
	if err != nil {
		return err
	}
	conn, err := connectProject(ctx, adminURL, p.database)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{p.role}.Sanitize()+`; SET LOCAL statement_timeout='30s'; SELECT pg_advisory_xact_lock(1885957734)`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS public.pibase_migrations (
name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(sql))
	checksum := hex.EncodeToString(digest[:])
	var existing string
	err = tx.QueryRow(ctx, `SELECT checksum FROM public.pibase_migrations WHERE name=$1`, name).Scan(&existing)
	if err == nil {
		if existing != checksum {
			return fmt.Errorf("migration %q was already applied with a different checksum", name)
		}
		return tx.Commit(ctx)
	}
	if err != pgx.ErrNoRows {
		return err
	}
	if _, err := tx.Exec(ctx, sql); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.pibase_migrations(name,checksum) VALUES($1,$2)`, name, checksum); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func validateOwnerSQL(statement string) error {
	lower := strings.ToLower(statement)
	for _, forbidden := range []string{"reset role", "set role", "set session authorization", "reset session authorization", "create role", "alter role", "drop role", "grant ", "revoke ", "copy ", "program"} {
		if strings.Contains(lower, forbidden) {
			return fmt.Errorf("owner SQL contains a forbidden privilege-changing construct")
		}
	}
	return nil
}

func MarshalResult(result Result) ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}

func lookupProject(ctx context.Context, adminURL, id string) (project, error) {
	controlURL, err := withDatabase(adminURL, "_control")
	if err != nil {
		return project{}, err
	}
	conn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		return project{}, err
	}
	defer conn.Close(ctx)
	var p project
	if err := conn.QueryRow(ctx, `SELECT database_name,admin_role FROM control.project_catalog WHERE id=$1`, id).Scan(&p.database, &p.role); err != nil {
		if err == pgx.ErrNoRows {
			return project{}, fmt.Errorf("project %q not found", id)
		}
		return project{}, err
	}
	return p, nil
}

func connectProject(ctx context.Context, adminURL, database string) (*pgx.Conn, error) {
	projectURL, err := withDatabase(adminURL, database)
	if err != nil {
		return nil, err
	}
	config, err := pgx.ParseConfig(projectURL)
	if err != nil {
		return nil, err
	}
	// Raw owner SQL must never inherit a superuser session. In particular,
	// PostgreSQL accepts comments between RESET SESSION AUTHORIZATION tokens, so
	// a keyword filter cannot safely contain a superuser connection. The gateway
	// and operator tools therefore connect only as the constrained runtime role.
	if config.User != "authenticator" {
		return nil, fmt.Errorf("owner SQL requires a PIBASE_DATABASE_URL for the authenticator role")
	}
	config.ConnectTimeout = 5 * time.Second
	return pgx.ConnectConfig(ctx, config)
}

func withDatabase(raw, database string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return "", fmt.Errorf("database URL must use postgres or postgresql scheme")
	}
	parsed.Path = "/" + database
	return parsed.String(), nil
}
