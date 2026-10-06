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
	return run(ctx, adminURL, projectID, statement, true)
}

// RunParams executes owner SQL with bound PostgreSQL parameters. It is used by
// operator UI actions whose values come from HTTP JSON rather than SQL text.
func RunParams(ctx context.Context, adminURL, projectID, statement string, args ...any) (Result, error) {
	return run(ctx, adminURL, projectID, statement, false, args...)
}

func run(ctx context.Context, adminURL, projectID, statement string, validate bool, args ...any) (Result, error) {
	if statement == "" {
		return Result{}, fmt.Errorf("SQL statement is required")
	}
	if validate {
		if err := validateOwnerSQL(statement); err != nil {
			return Result{}, err
		}
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
	rows, err := tx.Query(ctx, statement, args...)
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
	if err := validateNoExtensionDDL(sql); err != nil {
		return err
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
	if err := validateNoExtensionDDL(statement); err != nil {
		return err
	}
	tokens := sqlTokens(statement)
	for i, token := range tokens {
		token = sqlToken(token)
		next := ""
		if i+1 < len(tokens) {
			next = sqlToken(tokens[i+1])
		}
		if token == "grant" || token == "revoke" || token == "copy" || token == "program" || token == "set_config" {
			if strings.HasPrefix(tokens[i], "\x00") {
				continue
			}
			return fmt.Errorf("owner SQL contains a forbidden privilege-changing construct")
		}
		if token == "reset" && (i+1 >= len(tokens) || next == "all" || next == "role" || next == "session_authorization" || (next == "session" && i+2 < len(tokens) && sqlToken(tokens[i+2]) == "authorization")) {
			return fmt.Errorf("owner SQL contains a forbidden privilege-changing construct")
		}
		if token == "set" {
			j := i + 1
			for j < len(tokens) && (sqlToken(tokens[j]) == "local" || sqlToken(tokens[j]) == "session") {
				j++
			}
			if j < len(tokens) && (sqlToken(tokens[j]) == "role" || sqlToken(tokens[j]) == "session_authorization" || (sqlToken(tokens[j]) == "session" && j+1 < len(tokens) && sqlToken(tokens[j+1]) == "authorization")) {
				return fmt.Errorf("owner SQL contains a forbidden privilege-changing construct")
			}
		}
		if i+1 < len(tokens) && (token == "create" || token == "alter" || token == "drop") && tokens[i+1] == "role" {
			return fmt.Errorf("owner SQL contains a forbidden privilege-changing construct")
		}
	}
	return nil
}

func sqlToken(token string) string { return strings.TrimPrefix(token, "\x00") }

func validateNoExtensionDDL(statement string) error {
	tokens := sqlTokens(statement)
	for i := 1; i < len(tokens); i++ {
		if tokens[i] == "extension" && (tokens[i-1] == "create" || tokens[i-1] == "alter" || tokens[i-1] == "drop") {
			return fmt.Errorf("extensions can only be managed by the operator")
		}
	}
	return nil
}

// sqlTokens returns unquoted identifier tokens. It skips PostgreSQL line and
// nested block comments plus quoted and dollar-quoted strings, preventing a
// comment such as CREATE/**/EXTENSION from bypassing the operator boundary.
func sqlTokens(statement string) []string {
	var tokens []string
	for i := 0; i < len(statement); {
		switch {
		case i+1 < len(statement) && statement[i:i+2] == "--":
			i += 2
			for i < len(statement) && statement[i] != '\n' {
				i++
			}
		case i+1 < len(statement) && statement[i:i+2] == "/*":
			i += 2
			depth := 1
			for i < len(statement) && depth > 0 {
				if i+1 < len(statement) && statement[i:i+2] == "/*" {
					depth++
					i += 2
				} else if i+1 < len(statement) && statement[i:i+2] == "*/" {
					depth--
					i += 2
				} else {
					i++
				}
			}
		case statement[i] == '\'':
			i++
			for i < len(statement) {
				if statement[i] != '\'' {
					i++
					continue
				}
				if i+1 < len(statement) && statement[i+1] == '\'' {
					i += 2
					continue
				}
				i++
				break
			}
		case statement[i] == '"':
			i++
			var quoted strings.Builder
			for i < len(statement) {
				if statement[i] != '"' {
					quoted.WriteByte(statement[i])
					i++
					continue
				}
				if i+1 < len(statement) && statement[i+1] == '"' {
					quoted.WriteByte('"')
					i += 2
					continue
				}
				i++
				break
			}
			tokens = append(tokens, "\x00"+strings.ToLower(quoted.String()))
		case statement[i] == '$':
			end := i + 1
			for end < len(statement) && (statement[end] == '_' || statement[end] >= 'a' && statement[end] <= 'z' || statement[end] >= 'A' && statement[end] <= 'Z' || statement[end] >= '0' && statement[end] <= '9') {
				end++
			}
			if end < len(statement) && statement[end] == '$' {
				delimiter := statement[i : end+1]
				i = end + 1
				if close := strings.Index(statement[i:], delimiter); close >= 0 {
					i += close + len(delimiter)
				}
			} else {
				i++
			}
		case statement[i] == '_' || statement[i] >= 'a' && statement[i] <= 'z' || statement[i] >= 'A' && statement[i] <= 'Z':
			start := i
			for i < len(statement) && (statement[i] == '_' || statement[i] >= 'a' && statement[i] <= 'z' || statement[i] >= 'A' && statement[i] <= 'Z' || statement[i] >= '0' && statement[i] <= '9' || statement[i] == '$') {
				i++
			}
			tokens = append(tokens, strings.ToLower(statement[start:i]))
		default:
			i++
		}
	}
	return tokens
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
