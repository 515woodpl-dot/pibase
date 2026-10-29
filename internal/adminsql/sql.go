package adminsql

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type project struct {
	database          string
	role              string
	anonRole          string
	authenticatedRole string
	serviceRole       string
}

// projectConnections counts connections opened to project databases.
var projectConnections atomic.Int64

type Result struct {
	Columns []string         `json:"columns,omitempty"`
	Rows    []map[string]any `json:"rows,omitempty"`
	Command string           `json:"command,omitempty"`
	// Results holds every statement's result when a script ran more than one.
	Results []Result `json:"results,omitempty"`
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
	var result Result
	if len(args) == 0 {
		statement = aliasAPIRoles(statement, map[string]string{"anon": p.anonRole, "authenticated": p.authenticatedRole, "service_role": p.serviceRole})
		result, err = runScript(ctx, tx, statement)
	} else {
		result, err = runStatement(ctx, tx, statement, args...)
	}
	if err != nil {
		return Result{}, err
	}
	// The script must leave the transaction (and with it SET LOCAL ROLE) open.
	if tx.Conn().PgConn().TxStatus() != 'T' {
		return Result{}, fmt.Errorf("transaction control statements are not allowed in the SQL tab")
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return result, nil
}

// maxResultRows caps the rows returned by one SQL tab run, across statements.
const maxResultRows = 1000

func runStatement(ctx context.Context, tx pgx.Tx, statement string, args ...any) (Result, error) {
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
		if len(result.Rows) >= maxResultRows {
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
			row[result.Columns[i]] = displayValue(value)
		}
		result.Rows = append(result.Rows, row)
	}
	result.Command = rows.CommandTag().String()
	if err := rows.Err(); err != nil {
		return Result{}, err
	}
	return result, nil
}

// runScript runs one or more statements separated by semicolons, as the
// Supabase SQL editor does, inside the caller's transaction. The returned
// Result is the last statement that produced columns (else the last one);
// Results lists every statement when there is more than one.
func runScript(ctx context.Context, tx pgx.Tx, script string) (Result, error) {
	typeMap := tx.Conn().TypeMap()
	reader := tx.Conn().PgConn().Exec(ctx, script)
	var all []Result
	total := 0
	for reader.NextResult() {
		rr := reader.ResultReader()
		fields := rr.FieldDescriptions()
		result := Result{}
		for _, field := range fields {
			result.Columns = append(result.Columns, field.Name)
		}
		for rr.NextRow() {
			if total >= maxResultRows {
				_, _ = rr.Close()
				_ = reader.Close()
				return Result{}, fmt.Errorf("SQL result exceeds 1000-row limit")
			}
			total++
			values := rr.Values()
			row := make(map[string]any, len(values))
			for i, raw := range values {
				row[result.Columns[i]] = decodeText(typeMap, fields[i].DataTypeOID, fields[i].Format, raw)
			}
			result.Rows = append(result.Rows, row)
		}
		tag, err := rr.Close()
		if err != nil {
			_ = reader.Close()
			return Result{}, err
		}
		result.Command = tag.String()
		all = append(all, result)
	}
	if err := reader.Close(); err != nil {
		return Result{}, err
	}
	if len(all) == 0 {
		return Result{}, nil
	}
	out := all[len(all)-1]
	for i := len(all) - 1; i >= 0; i-- {
		if len(all[i].Columns) > 0 {
			out = all[i]
			break
		}
	}
	if len(all) > 1 {
		out.Results = all
	}
	return out, nil
}

// decodeText turns a simple-protocol value into the same Go value the
// extended protocol would give, so numbers and booleans stay typed in JSON.
func decodeText(m *pgtype.Map, oid uint32, format int16, raw []byte) any {
	if raw == nil {
		return nil
	}
	if t, ok := m.TypeForOID(oid); ok {
		if v, err := t.Codec.DecodeValue(m, oid, format, raw); err == nil {
			return displayValue(v)
		}
	}
	return string(raw)
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
	if _, err := tx.Exec(ctx, aliasAPIRoles(sql, map[string]string{"anon": p.anonRole, "authenticated": p.authenticatedRole, "service_role": p.serviceRole})); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO public.pibase_migrations(name,checksum) VALUES($1,$2)`, name, checksum); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

var errForbidden = fmt.Errorf("owner SQL contains a forbidden privilege-changing construct")

// validateOwnerSQL is a guard rail for the SQL tab, not the security boundary:
// owner SQL runs as the project admin role, and ApplyMigration does not use
// this filter. As in the Supabase SQL editor, GRANT/REVOKE on objects and DO
// blocks are allowed. It rejects role changes and role membership grants,
// transaction control (which would end SET LOCAL ROLE), and DO or routine
// bodies that build dynamic SQL.
func validateOwnerSQL(statement string) error { return validateOwnerSQLDepth(statement, 0) }

func validateOwnerSQLDepth(statement string, depth int) error {
	if depth > 4 {
		return errForbidden
	}
	if err := validateNoExtensionDDL(statement); err != nil {
		return err
	}
	tokens := sqlTokens(statement)
	hasUpdate, routine := false, false
	for i, raw := range tokens {
		switch raw {
		case "update":
			hasUpdate = true
		case "function", "procedure":
			routine = true
		case "do":
			if i+1 >= len(tokens) || (tokens[i+1] != "nothing" && tokens[i+1] != "update") {
				routine = true
			}
		}
	}
	updateInStatement, statementStart := false, true
	for i, raw := range tokens {
		if raw == ";" {
			updateInStatement, statementStart = false, true
			continue
		}
		leading := statementStart
		statementStart = false
		if raw == "update" {
			updateInStatement = true
		}
		if raw == unicodeIdentifier {
			return fmt.Errorf(`owner SQL may not use U&"..." identifiers`)
		}
		token, quoted := sqlToken(raw), strings.HasPrefix(raw, "\x00")
		next := ""
		if i+1 < len(tokens) {
			next = sqlToken(tokens[i+1])
		}
		switch {
		case token == "set_config" || token == "standard_conforming_strings" || token == "backslash_quote":
			return errForbidden
		case token == "pg_settings" && hasUpdate:
			return errForbidden
		case !quoted && (token == "copy" || token == "program"):
			return errForbidden
		case !quoted && (token == "grant" || token == "revoke") && !grantsOnObject(tokens[i+1:]):
			return errForbidden
		// Dollar-quoted DO and routine bodies are checked separately. A SQL
		// standard BEGIN ATOMIC body is not quoted, so conservatively refuse
		// its leading END: an extra END would commit this transaction before
		// the status check below could roll it back.
		case !quoted && leading && depth == 0 && transactionControl(token, next):
			return fmt.Errorf("transaction control statements are not allowed in the SQL tab; each run is already one transaction")
		case !quoted && token == "reset" && (next == "" || next == "all" || next == "role" || next == "session_authorization" || (next == "session" && i+2 < len(tokens) && sqlToken(tokens[i+2]) == "authorization")):
			return errForbidden
		case !quoted && (token == "create" || token == "alter" || token == "drop") && (next == "role" || next == "user" || next == "group"):
			return errForbidden
		}
		// SET inside UPDATE ... SET or ON CONFLICT DO UPDATE SET assigns columns.
		if !quoted && token == "set" && !updateInStatement {
			j := i + 1
			for j < len(tokens) && sqlToken(tokens[j]) == "local" {
				j++
			}
			if j < len(tokens) && sqlToken(tokens[j]) == "session" {
				if j+1 < len(tokens) && sqlToken(tokens[j+1]) == "authorization" {
					return errForbidden
				}
				j++
			}
			if j < len(tokens) && (sqlToken(tokens[j]) == "role" || sqlToken(tokens[j]) == "session_authorization") {
				return errForbidden
			}
		}
	}
	if routine {
		for _, body := range sqlBodies(statement) {
			for _, t := range sqlTokens(body) {
				if t == "execute" {
					return fmt.Errorf("DO and routine bodies may not use dynamic EXECUTE in the SQL tab; apply them as a migration")
				}
			}
			if err := validateOwnerSQLDepth(body, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// aliasAPIRoles makes GRANT and REVOKE on anon, authenticated and
// service_role also name this project's own API roles. Those roles inherit
// from the stable ones, but new tables are granted to the project roles
// directly, so without this "revoke ... from anon" would change nothing.
func aliasAPIRoles(script string, roles map[string]string) string {
	tokens, spans, _ := lexSQLPositions(script)
	var out strings.Builder
	last := 0
	inGrant, inGrantees := false, false
	for i, token := range tokens {
		switch {
		case token == ";":
			inGrant, inGrantees = false, false
		case token == "grant" || token == "revoke":
			inGrant, inGrantees = true, false
		case inGrant && (token == "to" || token == "from"):
			inGrantees = true
		case inGrantees && (token == "with" || token == "granted" || token == "cascade" || token == "restrict"):
			inGrant, inGrantees = false, false
		case inGrantees && roles[token] != "":
			out.WriteString(script[last:spans[i][1]])
			out.WriteString(", " + pgx.Identifier{roles[token]}.Sanitize())
			last = spans[i][1]
		}
	}
	out.WriteString(script[last:])
	return out.String()
}

// grantsOnObject reports whether a GRANT or REVOKE (whose remaining tokens
// are given) names an object with ON. Without ON it changes role membership.
func grantsOnObject(rest []string) bool {
	for _, raw := range rest {
		if raw == ";" {
			return false
		}
		if raw == "on" {
			return true
		}
	}
	return false
}

// transactionControl reports whether a statement starting with token would
// commit, roll back or nest the SQL tab's transaction. END also closes a
// BEGIN ATOMIC routine bodies are refused in the SQL tab because a top-level
// END is otherwise indistinguishable from a body terminator here.
func transactionControl(token, next string) bool {
	switch token {
	case "begin", "commit", "rollback", "abort", "savepoint", "release":
		return true
	case "start":
		return next == "transaction"
	case "prepare":
		return next == "transaction"
	case "end":
		return true
	}
	return false
}

// displayValue keeps uuid and bytea readable in JSON instead of byte arrays.
func displayValue(v any) any {
	switch x := v.(type) {
	case [16]byte:
		h := hex.EncodeToString(x[:])
		return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
	case []byte:
		return `\x` + hex.EncodeToString(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = displayValue(e)
		}
		return out
	}
	return v
}

// unicodeIdentifier marks a U&"..." identifier, whose escapes could spell any keyword.
const unicodeIdentifier = "\x01"

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

// sqlTokens returns identifier tokens; quoted identifiers are prefixed with
// \x00. It skips line and nested block comments, string literals (including
// E” strings with backslash escapes) and dollar-quoted bodies, so a comment
// such as CREATE/**/EXTENSION cannot bypass the operator boundary.
func sqlTokens(statement string) []string {
	tokens, _ := lexSQL(statement)
	return tokens
}

// sqlBodies returns the contents of string literals and dollar-quoted bodies.
func sqlBodies(statement string) []string {
	_, bodies := lexSQL(statement)
	return bodies
}

// identStart matches PostgreSQL's scanner, which treats every byte >= 0x80
// as an identifier character.
func identStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}
func identPart(c byte) bool { return identStart(c) || c >= '0' && c <= '9' || c == '$' }

func lexSQL(statement string) (tokens, bodies []string) {
	tokens, _, bodies = lexSQLPositions(statement)
	return tokens, bodies
}

// lexSQLPositions is lexSQL that also returns where each token starts and
// ends in statement.
func lexSQLPositions(statement string) (tokens []string, spans [][2]int, bodies []string) {
	n := len(statement)
	add := func(token string, start, end int) {
		tokens = append(tokens, token)
		spans = append(spans, [2]int{start, end})
	}
	readString := func(i int, backslash bool) (int, string) {
		var b strings.Builder
		for i < n {
			c := statement[i]
			if backslash && c == '\\' && i+1 < n {
				b.WriteByte(statement[i+1])
				i += 2
				continue
			}
			if c == '\'' {
				if i+1 < n && statement[i+1] == '\'' {
					b.WriteByte('\'')
					i += 2
					continue
				}
				return i + 1, b.String()
			}
			b.WriteByte(c)
			i++
		}
		return i, b.String()
	}
	readQuoted := func(i int) (int, string) {
		var b strings.Builder
		for i < n {
			if statement[i] != '"' {
				b.WriteByte(statement[i])
				i++
				continue
			}
			if i+1 < n && statement[i+1] == '"' {
				b.WriteByte('"')
				i += 2
				continue
			}
			return i + 1, b.String()
		}
		return i, b.String()
	}
	for i := 0; i < n; {
		c := statement[i]
		switch {
		case i+1 < n && statement[i:i+2] == "--":
			for i < n && statement[i] != '\n' {
				i++
			}
		case i+1 < n && statement[i:i+2] == "/*":
			i += 2
			depth := 1
			for i < n && depth > 0 {
				if i+1 < n && statement[i:i+2] == "/*" {
					depth++
					i += 2
				} else if i+1 < n && statement[i:i+2] == "*/" {
					depth--
					i += 2
				} else {
					i++
				}
			}
		case (c == 'u' || c == 'U') && i+2 < n && statement[i+1] == '&' && statement[i+2] == '"':
			start := i
			i, _ = readQuoted(i + 3)
			add(unicodeIdentifier, start, i)
		case (c == 'u' || c == 'U') && i+2 < n && statement[i+1] == '&' && statement[i+2] == '\'':
			var body string
			i, body = readString(i+3, false)
			bodies = append(bodies, body)
		case (c == 'e' || c == 'E') && i+1 < n && statement[i+1] == '\'':
			var body string
			i, body = readString(i+2, true)
			bodies = append(bodies, body)
		case c == '\'':
			var body string
			i, body = readString(i+1, false)
			bodies = append(bodies, body)
		case c == '"':
			var q string
			start := i
			i, q = readQuoted(i + 1)
			add("\x00"+strings.ToLower(q), start, i)
		case c == '$' && i+1 < n && (statement[i+1] == '$' || identStart(statement[i+1])):
			end := i + 1
			for end < n && statement[end] != '$' && (identPart(statement[end])) {
				end++
			}
			if end < n && statement[end] == '$' {
				delimiter := statement[i : end+1]
				i = end + 1
				if close := strings.Index(statement[i:], delimiter); close >= 0 {
					bodies = append(bodies, statement[i:i+close])
					i += close + len(delimiter)
				} else {
					bodies = append(bodies, statement[i:])
					i = n
				}
			} else {
				i++
			}
		case identStart(c):
			start := i
			for i < n && identPart(statement[i]) {
				i++
			}
			add(strings.ToLower(statement[start:i]), start, i)
		case c == ';':
			add(";", i, i+1)
			i++
		default:
			i++
		}
	}
	return tokens, spans, bodies
}

// Table describes a public table for the operator dashboard.
type Table struct {
	Name         string   `json:"name"`
	Count        int64    `json:"count"`
	RLS          bool     `json:"rls"`
	AnonReadable bool     `json:"anon_readable"`
	PrimaryKey   []string `json:"primary_key"`
	Columns      []string `json:"columns"`
}

// ListTables returns every public table (except pibase_migrations) with its
// row count and whether the project's anon role can read it, using one
// project connection. anon_readable means: the anon role has SELECT on the
// table or on any of its columns, and RLS is off or a permissive SELECT/ALL
// policy applies to anon or PUBLIC and no restrictive policy for them is
// literally false. Policy expressions are not evaluated beyond that.
func ListTables(ctx context.Context, adminURL, projectID string) ([]Table, error) {
	p, err := lookupProject(ctx, adminURL, projectID)
	if err != nil {
		return nil, err
	}
	conn, err := connectProject(ctx, adminURL, p.database)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{p.role}.Sanitize()+`; SET LOCAL statement_timeout='30s'`); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `
SELECT c.relname, c.relrowsecurity,
 COALESCE((SELECT array_agg(a.attname::text ORDER BY k.ord) FROM pg_index i JOIN unnest(i.indkey) WITH ORDINALITY k(attnum,ord) ON true JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.attnum WHERE i.indrelid=c.oid AND i.indisprimary), ARRAY[]::text[]),
 COALESCE((SELECT array_agg(a.attname::text ORDER BY a.attnum) FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped), ARRAY[]::text[]),
 (has_table_privilege($1::text, c.oid, 'SELECT') OR has_any_column_privilege($1::text, c.oid, 'SELECT'))
 AND (NOT c.relrowsecurity OR (
   EXISTS (SELECT 1 FROM pg_policies pp WHERE pp.schemaname='public' AND pp.tablename=c.relname AND pp.permissive='PERMISSIVE' AND pp.cmd IN ('SELECT','ALL') AND (pp.roles @> ARRAY[$1::name] OR pp.roles @> ARRAY['public'::name]) AND COALESCE(pp.qual,'true') <> 'false')
   AND NOT EXISTS (SELECT 1 FROM pg_policies pp WHERE pp.schemaname='public' AND pp.tablename=c.relname AND pp.permissive='RESTRICTIVE' AND pp.cmd IN ('SELECT','ALL') AND (pp.roles @> ARRAY[$1::name] OR pp.roles @> ARRAY['public'::name]) AND pp.qual = 'false')))
FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.relname <> 'pibase_migrations'
ORDER BY c.relname`, p.anonRole)
	if err != nil {
		return nil, err
	}
	tables := []Table{}
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Name, &t.RLS, &t.PrimaryKey, &t.Columns, &t.AnonReadable); err != nil {
			rows.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range tables {
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM public."+pgx.Identifier{tables[i].Name}.Sanitize()).Scan(&tables[i].Count); err != nil {
			return nil, err
		}
	}
	return tables, tx.Commit(ctx)
}

// BrowseRows returns rows from a table with every column cast to text so that
// uuid, bigint, numeric, jsonb, bytea, arrays and timestamps display exactly.
// NULL is returned as JSON null. Rows are ordered by primary key (or ctid).
func BrowseRows(ctx context.Context, adminURL, projectID, table string, columns, pk []string, offset int) (map[string]any, error) {
	p, err := lookupProject(ctx, adminURL, projectID)
	if err != nil {
		return nil, err
	}
	conn, err := connectProject(ctx, adminURL, p.database)
	if err != nil {
		return nil, err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{p.role}.Sanitize()+`; SET LOCAL statement_timeout='30s'`); err != nil {
		return nil, err
	}
	if len(columns) == 0 {
		return map[string]any{"columns": []string{}, "primary_key": pk, "rows": []map[string]any{}, "offset": offset, "has_more": false}, nil
	}
	var selects []string
	for _, c := range columns {
		selects = append(selects, pgx.Identifier{c}.Sanitize()+"::text")
	}
	orderBy := "ctid"
	if len(pk) > 0 {
		parts := make([]string, len(pk))
		for i, c := range pk {
			parts[i] = pgx.Identifier{c}.Sanitize()
		}
		orderBy = strings.Join(parts, ",")
	}
	q := "SELECT " + strings.Join(selects, ",") + " FROM public." + pgx.Identifier{table}.Sanitize() +
		" ORDER BY " + orderBy + " LIMIT 51 OFFSET $1"
	rows, err := tx.Query(ctx, q, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make(map[string]any, len(vals))
		for i, v := range vals {
			row[columns[i]] = v
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_ = tx.Commit(ctx)
	hasMore := len(out) > 50
	if hasMore {
		out = out[:50]
	}
	return map[string]any{"columns": columns, "primary_key": pk, "rows": out, "offset": offset, "has_more": hasMore}, nil
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
	if err := conn.QueryRow(ctx, `SELECT database_name,admin_role,anon_role,authenticated_role,service_role FROM control.project_catalog WHERE id=$1`, id).Scan(&p.database, &p.role, &p.anonRole, &p.authenticatedRole, &p.serviceRole); err != nil {
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
	projectConnections.Add(1)
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
