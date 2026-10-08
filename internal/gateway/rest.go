package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxRows = 1000

func (s *Server) restHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	who, err := s.authenticate(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if err := s.enforceRate(who); err != nil {
		writeAPIError(w, err)
		return
	}
	defer s.recordRequest(r.Context(), who.project.id)
	relation := r.PathValue("relation")
	if relation == "" || strings.Contains(relation, ".") {
		writeAPIError(w, httpError(http.StatusBadRequest, "invalid relation"))
		return
	}
	if err := checkSchemaProfile(r); err != nil {
		writeAPIError(w, err)
		return
	}
	buffer := newResponseBuffer()
	err = s.transaction(r.Context(), who, func(tx pgx.Tx) error {
		table, err := loadTable(r.Context(), tx, "public", relation)
		if err != nil {
			return err
		}
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			return selectRows(r.Context(), buffer, r, tx, table)
		case http.MethodPost:
			if err := enforceDatabaseQuota(r.Context(), tx, who.project.maxDatabaseBytes); err != nil {
				return err
			}
			if err := insertRows(r.Context(), buffer, r, tx, table); err != nil {
				return err
			}
			return enforceDatabaseQuota(r.Context(), tx, who.project.maxDatabaseBytes)
		case http.MethodPatch:
			if err := enforceDatabaseQuota(r.Context(), tx, who.project.maxDatabaseBytes); err != nil {
				return err
			}
			if err := updateRows(r.Context(), buffer, r, tx, table); err != nil {
				return err
			}
			return enforceDatabaseQuota(r.Context(), tx, who.project.maxDatabaseBytes)
		case http.MethodDelete:
			return deleteRows(r.Context(), buffer, r, tx, table)
		default:
			return httpError(http.StatusMethodNotAllowed, "method not allowed")
		}
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	buffer.flush(w)
}

type queryPlan struct {
	columns []string
	where   string
	args    []any
	order   string
	limit   int
	offset  int
}

type embedSelection struct {
	relation string
	columns  []string
}

func buildQuery(values url.Values, table tableInfo) (queryPlan, error) {
	return buildQueryAt(values, table, 1)
}

func buildQueryAt(values url.Values, table tableInfo, firstArg int) (queryPlan, error) {
	plan := queryPlan{limit: maxRows}
	selection := values.Get("select")
	if selection == "" || selection == "*" {
		plan.columns = append(plan.columns, table.ordered...)
	} else {
		for _, name := range strings.Split(selection, ",") {
			name = strings.TrimSpace(name)
			if name == "" || strings.ContainsAny(name, "()") {
				return plan, httpError(http.StatusBadRequest, "embedded or computed select syntax is not supported here")
			}
			if _, err := table.column(name); err != nil {
				return plan, err
			}
			plan.columns = append(plan.columns, name)
		}
	}
	if raw := values.Get("limit"); raw != "" {
		v, err := boundedInt("limit", raw, 0, maxRows)
		if err != nil {
			return plan, err
		}
		plan.limit = v
	}
	if raw := values.Get("offset"); raw != "" {
		v, err := boundedInt("offset", raw, 0, 1<<31-1)
		if err != nil {
			return plan, err
		}
		plan.offset = v
	}
	if raw := values.Get("order"); raw != "" {
		parts := strings.Split(raw, ",")
		orders := make([]string, 0, len(parts))
		for _, item := range parts {
			bits := strings.Split(strings.TrimSpace(item), ".")
			if len(bits) > 2 || len(bits) == 0 {
				return plan, httpError(http.StatusBadRequest, "invalid order syntax")
			}
			if _, err := table.column(bits[0]); err != nil {
				return plan, err
			}
			direction := "ASC"
			if len(bits) == 2 {
				switch bits[1] {
				case "asc":
				case "desc":
					direction = "DESC"
				default:
					return plan, httpError(http.StatusBadRequest, "order direction must be asc or desc")
				}
			}
			orders = append(orders, pgx.Identifier{bits[0]}.Sanitize()+" "+direction)
		}
		plan.order = " ORDER BY " + strings.Join(orders, ",")
	}
	var predicates []string
	for name, expressions := range values {
		switch name {
		case "select", "limit", "offset", "order":
			continue
		}
		if name == "or" {
			for _, expression := range expressions {
				parts := strings.TrimSpace(expression)
				if len(parts) < 2 || parts[0] != '(' || parts[len(parts)-1] != ')' {
					return plan, httpError(http.StatusBadRequest, "or filter must use or=(column.operator.value,...) syntax")
				}
				items := splitFilterList(parts[1 : len(parts)-1])
				if len(items) == 0 || len(items) > 20 {
					return plan, httpError(http.StatusBadRequest, "or filter requires 1 to 20 expressions")
				}
				ors := make([]string, 0, len(items))
				for _, item := range items {
					bits := strings.SplitN(item, ".", 3)
					if len(bits) != 3 {
						return plan, httpError(http.StatusBadRequest, "invalid or filter expression")
					}
					column, columnErr := table.column(bits[0])
					if columnErr != nil {
						return plan, columnErr
					}
					predicate, args, filterErr := filterExpression(column, bits[1]+"."+bits[2], len(plan.args)+firstArg)
					if filterErr != nil {
						return plan, filterErr
					}
					ors = append(ors, predicate)
					plan.args = append(plan.args, args...)
				}
				predicates = append(predicates, "("+strings.Join(ors, " OR ")+")")
			}
			continue
		}
		if strings.HasPrefix(name, "not.") {
			columnName := strings.TrimPrefix(name, "not.")
			column, columnErr := table.column(columnName)
			if columnErr != nil {
				return plan, columnErr
			}
			for _, expression := range expressions {
				predicate, args, filterErr := filterExpression(column, expression, len(plan.args)+firstArg)
				if filterErr != nil {
					return plan, filterErr
				}
				predicates = append(predicates, "NOT ("+predicate+")")
				plan.args = append(plan.args, args...)
			}
			continue
		}
		column, err := table.column(name)
		if err != nil {
			return plan, err
		}
		for _, expression := range expressions {
			predicate, args, err := filterExpression(column, expression, len(plan.args)+firstArg)
			if err != nil {
				return plan, err
			}
			predicates = append(predicates, predicate)
			plan.args = append(plan.args, args...)
		}
	}
	if len(predicates) > 0 {
		plan.where = " WHERE " + strings.Join(predicates, " AND ")
	}
	return plan, nil
}

func splitFilterList(value string) []string {
	var out []string
	start, depth := 0, 0
	for i, r := range value {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(value[start:i]))
				start = i + 1
			}
		}
	}
	if start < len(value) {
		out = append(out, strings.TrimSpace(value[start:]))
	}
	return out
}

func writeFilters(values url.Values, table tableInfo, firstArg int) (queryPlan, error) {
	filtered := make(url.Values)
	for key, value := range values {
		switch key {
		case "select", "order", "limit", "offset", "on_conflict", "columns":
		default:
			filtered[key] = value
		}
	}
	return buildQueryAt(filtered, table, firstArg)
}

func filterExpression(column columnInfo, expression string, firstArg int) (string, []any, error) {
	operator, value, found := strings.Cut(expression, ".")
	if !found {
		return "", nil, httpError(http.StatusBadRequest, "filter must use operator.value syntax")
	}
	identifier := pgx.Identifier{column.name}.Sanitize()
	placeholder := "$" + strconv.Itoa(firstArg)
	switch operator {
	case "eq", "neq", "gt", "gte", "lt", "lte", "like", "ilike":
		operators := map[string]string{"eq": "=", "neq": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<=", "like": "LIKE", "ilike": "ILIKE"}
		return identifier + " " + operators[operator] + " " + column.cast(placeholder), []any{value}, nil
	case "is":
		switch value {
		case "null":
			return identifier + " IS NULL", nil, nil
		case "true":
			return identifier + " IS TRUE", nil, nil
		case "false":
			return identifier + " IS FALSE", nil, nil
		default:
			return "", nil, httpError(http.StatusBadRequest, "is filter supports null, true, or false")
		}
	case "in":
		if len(value) < 2 || value[0] != '(' || value[len(value)-1] != ')' {
			return "", nil, httpError(http.StatusBadRequest, "in filter must use in.(value,value) syntax")
		}
		items := strings.Split(value[1:len(value)-1], ",")
		if len(items) == 0 || len(items) > 100 {
			return "", nil, httpError(http.StatusBadRequest, "in filter requires 1 to 100 values")
		}
		args := make([]any, 0, len(items))
		casts := make([]string, 0, len(items))
		for i, item := range items {
			args = append(args, item)
			casts = append(casts, column.cast("$"+strconv.Itoa(firstArg+i)))
		}
		return identifier + " IN (" + strings.Join(casts, ",") + ")", args, nil
	default:
		return "", nil, httpError(http.StatusBadRequest, fmt.Sprintf("unsupported filter operator %q", operator))
	}
}

func selectRows(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, table tableInfo) error {
	baseSelection, embeds, err := parseSelection(r.URL.Query().Get("select"))
	if err != nil {
		return err
	}
	values := r.URL.Query()
	if len(embeds) > 0 {
		values = cloneValues(values)
		values.Set("select", strings.Join(baseSelection, ","))
	}
	plan, err := buildQuery(values, table)
	if err != nil {
		return err
	}
	columns := make([]string, 0, len(plan.columns)+len(embeds))
	for _, name := range plan.columns {
		identifier := pgx.Identifier{name}.Sanitize()
		columns = append(columns, pgx.Identifier{"base"}.Sanitize()+"."+identifier+" AS "+identifier)
	}
	for _, embed := range embeds {
		relation, err := loadRelationship(ctx, tx, table, embed.relation)
		if err != nil {
			return err
		}
		selected := embed.columns
		if len(selected) == 0 {
			selected = relation.table.ordered
		}
		var relatedColumns []string
		for _, name := range selected {
			if _, err := relation.table.column(name); err != nil {
				return err
			}
			relatedColumns = append(relatedColumns, pgx.Identifier{name}.Sanitize())
		}
		relatedAlias := pgx.Identifier{"related"}.Sanitize()
		comparison := relatedAlias + "." + pgx.Identifier{relation.relatedColumn}.Sanitize() + "=" + pgx.Identifier{"base"}.Sanitize() + "." + pgx.Identifier{relation.baseColumn}.Sanitize()
		inner := "SELECT " + strings.Join(relatedColumns, ",") + " FROM " + relation.table.identifier() + " " + relatedAlias + " WHERE " + comparison
		expression := "(SELECT to_jsonb(embedded) FROM (" + inner + " LIMIT 1) embedded)"
		if relation.many {
			expression = "(SELECT COALESCE(jsonb_agg(to_jsonb(embedded)),'[]'::jsonb) FROM (" + inner + ") embedded)"
		}
		columns = append(columns, expression+" AS "+pgx.Identifier{embed.relation}.Sanitize())
	}
	if strings.Contains(r.Header.Get("Prefer"), "count=exact") {
		var count int64
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM "+table.identifier()+plan.where, plan.args...).Scan(&count); err != nil {
			return err
		}
		end := plan.offset + plan.limit - 1
		if count == 0 || int64(plan.offset) >= count {
			end = plan.offset
		}
		if int64(end) >= count && count > 0 {
			end = int(count - 1)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("%d-%d/%d", plan.offset, end, count))
		w.Header().Set("Range-Unit", "items")
	}
	query := "SELECT COALESCE(jsonb_agg(to_jsonb(q)),'[]'::jsonb) FROM (SELECT " + strings.Join(columns, ",") + " FROM " + table.identifier() + " " + pgx.Identifier{"base"}.Sanitize() + plan.where + plan.order + fmt.Sprintf(" LIMIT %d OFFSET %d", plan.limit, plan.offset) + ") q"
	var body []byte
	if err := tx.QueryRow(ctx, query, plan.args...).Scan(&body); err != nil {
		return err
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return nil
	}
	body, err = shapeResponse(r, body)
	if err != nil {
		return err
	}
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(body)
	return err
}

func parseSelection(raw string) ([]string, []embedSelection, error) {
	if raw == "" || raw == "*" {
		return nil, nil, nil
	}
	var tokens []string
	start, depth := 0, 0
	for i, char := range raw {
		switch char {
		case '(':
			depth++
			if depth > 1 {
				return nil, nil, httpError(http.StatusBadRequest, "only one-level embedding is supported")
			}
		case ')':
			depth--
			if depth < 0 {
				return nil, nil, httpError(http.StatusBadRequest, "invalid select syntax")
			}
		case ',':
			if depth == 0 {
				tokens = append(tokens, raw[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return nil, nil, httpError(http.StatusBadRequest, "invalid select syntax")
	}
	tokens = append(tokens, raw[start:])
	var base []string
	var embeds []embedSelection
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		open := strings.IndexByte(token, '(')
		if open < 0 {
			base = append(base, token)
			continue
		}
		if !strings.HasSuffix(token, ")") || open == 0 {
			return nil, nil, httpError(http.StatusBadRequest, "invalid embedded select syntax")
		}
		relation := strings.TrimSpace(token[:open])
		inside := token[open+1 : len(token)-1]
		selection := embedSelection{relation: relation}
		if inside != "*" && inside != "" {
			for _, name := range strings.Split(inside, ",") {
				selection.columns = append(selection.columns, strings.TrimSpace(name))
			}
		}
		embeds = append(embeds, selection)
	}
	if len(base) == 0 {
		base = []string{"*"}
	}
	return base, embeds, nil
}

func cloneValues(values url.Values) url.Values {
	clone := make(url.Values, len(values))
	for key, items := range values {
		clone[key] = append([]string(nil), items...)
	}
	return clone
}

func insertRows(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, table tableInfo) error {
	body, columns, err := decodeWriteBody(r, table, true)
	if err != nil {
		return err
	}
	quoted := quoteColumns(columns)
	selected := prefixedColumns("input", columns)
	statement := "INSERT INTO " + table.identifier() + " (" + strings.Join(quoted, ",") + ") SELECT " + strings.Join(selected, ",") + " FROM jsonb_populate_recordset(NULL::" + table.identifier() + ",$1::jsonb) input"
	prefer := r.Header.Get("Prefer")
	merge := strings.Contains(prefer, "resolution=merge-duplicates")
	ignore := strings.Contains(prefer, "resolution=ignore-duplicates")
	if merge || ignore {
		// Like PostgREST, the conflict target defaults to the primary key, so
		// supabase-js upsert() works without an explicit onConflict.
		var names []string
		if raw := r.URL.Query().Get("on_conflict"); raw != "" {
			for _, name := range strings.Split(raw, ",") {
				name = strings.TrimSpace(name)
				if _, err := table.column(name); err != nil {
					return err
				}
				names = append(names, name)
			}
		} else {
			names, err = primaryKeyColumns(ctx, tx, table)
			if err != nil {
				return err
			}
		}
		if len(names) == 0 && merge {
			return httpError(http.StatusBadRequest, "upsert needs on_conflict when the table has no primary key")
		}
		conflict := make([]string, 0, len(names))
		conflictSet := make(map[string]bool)
		for _, name := range names {
			conflict = append(conflict, pgx.Identifier{name}.Sanitize())
			conflictSet[name] = true
		}
		target := ""
		if len(conflict) > 0 {
			target = " (" + strings.Join(conflict, ",") + ")"
		}
		var updates []string
		for _, name := range columns {
			if !conflictSet[name] {
				identifier := pgx.Identifier{name}.Sanitize()
				updates = append(updates, identifier+"=EXCLUDED."+identifier)
			}
		}
		if ignore || len(updates) == 0 {
			statement += " ON CONFLICT" + target + " DO NOTHING"
		} else {
			statement += " ON CONFLICT" + target + " DO UPDATE SET " + strings.Join(updates, ",")
		}
	}
	return executeMutation(ctx, w, r, tx, statement, []any{body}, http.StatusCreated)
}

// primaryKeyColumns returns the table's primary key columns in key order, or
// none when the table has no primary key.
func primaryKeyColumns(ctx context.Context, tx pgx.Tx, table tableInfo) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT a.attname
FROM pg_catalog.pg_index i
JOIN pg_catalog.pg_class c ON c.oid=i.indrelid
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
CROSS JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum,position)
JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.attnum
WHERE n.nspname=$1 AND c.relname=$2 AND i.indisprimary
ORDER BY k.position`, table.schema, table.name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// checkSchemaProfile refuses requests for any schema other than public, the
// only one the data API exposes, instead of silently using public.
func checkSchemaProfile(r *http.Request) error {
	for _, header := range []string{"Accept-Profile", "Content-Profile"} {
		if value := strings.TrimSpace(r.Header.Get(header)); value != "" && value != "public" {
			return httpError(http.StatusNotAcceptable, "only the public schema is exposed")
		}
	}
	return nil
}

func updateRows(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, table tableInfo) error {
	body, columns, err := decodeWriteBody(r, table, false)
	if err != nil {
		return err
	}
	plan, err := writeFilters(r.URL.Query(), table, 2)
	if err != nil {
		return err
	}
	var assignments []string
	for _, name := range columns {
		identifier := pgx.Identifier{name}.Sanitize()
		assignments = append(assignments, identifier+"=(jsonb_populate_record(NULL::"+table.identifier()+",$1::jsonb))."+identifier)
	}
	statement := "UPDATE " + table.identifier() + " SET " + strings.Join(assignments, ",") + plan.where
	return executeMutation(ctx, w, r, tx, statement, append([]any{body}, plan.args...), http.StatusOK)
}

func deleteRows(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, table tableInfo) error {
	plan, err := writeFilters(r.URL.Query(), table, 1)
	if err != nil {
		return err
	}
	statement := "DELETE FROM " + table.identifier() + plan.where
	return executeMutation(ctx, w, r, tx, statement, plan.args, http.StatusOK)
}

func executeMutation(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, statement string, args []any, representationStatus int) error {
	if strings.Contains(r.Header.Get("Prefer"), "return=representation") {
		var body []byte
		query := "WITH changed AS (" + statement + " RETURNING *) SELECT COALESCE(jsonb_agg(to_jsonb(changed)),'[]'::jsonb) FROM changed"
		if err := tx.QueryRow(ctx, query, args...).Scan(&body); err != nil {
			return err
		}
		body, err := shapeResponse(r, body)
		if err != nil {
			return err
		}
		w.WriteHeader(representationStatus)
		_, err = w.Write(body)
		return err
	}
	tag, err := tx.Exec(ctx, statement, args...)
	if err != nil {
		return err
	}
	w.Header().Set("Preference-Applied", "return=minimal")
	w.Header().Set("X-Affected-Rows", strconv.FormatInt(tag.RowsAffected(), 10))
	if representationStatus == http.StatusCreated {
		w.WriteHeader(http.StatusCreated)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
	return nil
}

func shapeResponse(r *http.Request, body []byte) ([]byte, error) {
	if !strings.Contains(r.Header.Get("Accept"), "application/vnd.pgrst.object+json") {
		return body, nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, httpError(http.StatusNotAcceptable, "single-object response requires exactly one row")
	}
	return rows[0], nil
}

func decodeWriteBody(r *http.Request, table tableInfo, allowArray bool) ([]byte, []string, error) {
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, nil, bodyError(err, "body must be valid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, nil, httpError(http.StatusBadRequest, "body must contain one JSON value")
	}
	var records []map[string]any
	switch typed := value.(type) {
	case map[string]any:
		records = []map[string]any{typed}
	case []any:
		if !allowArray || len(typed) == 0 || len(typed) > maxRows {
			return nil, nil, httpError(http.StatusBadRequest, "body array size is not allowed")
		}
		for _, item := range typed {
			record, ok := item.(map[string]any)
			if !ok {
				return nil, nil, httpError(http.StatusBadRequest, "body array must contain objects")
			}
			records = append(records, record)
		}
	default:
		return nil, nil, httpError(http.StatusBadRequest, "body must be an object or array of objects")
	}
	seen := make(map[string]bool)
	if raw := r.URL.Query().Get("columns"); raw != "" && allowArray {
		// supabase-js lists the union of keys for array writes; keys outside
		// the list are ignored and missing keys become NULL.
		for _, name := range strings.Split(raw, ",") {
			name = strings.Trim(strings.TrimSpace(name), `"`)
			if _, err := table.column(name); err != nil {
				return nil, nil, err
			}
			seen[name] = true
		}
	} else {
		for _, record := range records {
			for name := range record {
				if _, err := table.column(name); err != nil {
					return nil, nil, err
				}
				seen[name] = true
			}
		}
	}
	if len(seen) == 0 {
		return nil, nil, httpError(http.StatusBadRequest, "body object must not be empty")
	}
	var columns []string
	for _, name := range table.ordered {
		if seen[name] {
			columns = append(columns, name)
		}
	}
	if !allowArray {
		encoded, _ := json.Marshal(records[0])
		return encoded, columns, nil
	}
	encoded, _ := json.Marshal(records)
	return encoded, columns, nil
}

func quoteColumns(columns []string) []string {
	result := make([]string, 0, len(columns))
	for _, name := range columns {
		result = append(result, pgx.Identifier{name}.Sanitize())
	}
	return result
}

func prefixedColumns(prefix string, columns []string) []string {
	result := make([]string, 0, len(columns))
	for _, name := range columns {
		result = append(result, pgx.Identifier{prefix}.Sanitize()+"."+pgx.Identifier{name}.Sanitize())
	}
	return result
}

func boundedInt(name, raw string, min, max int) (int, error) {
	v, err := strconv.Atoi(raw)
	if err != nil || v < min || v > max {
		return 0, httpError(http.StatusBadRequest, fmt.Sprintf("%s must be between %d and %d", name, min, max))
	}
	return v, nil
}

func writeAPIError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	message := "internal server error"
	code := "PIBASE"
	var details, hint any
	var requestErr requestError
	if errors.As(err, &requestErr) {
		status, message = requestErr.status, requestErr.message
		if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "1")
		}
	} else {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			code = pgErr.Code
			switch pgErr.Code {
			case "23505":
				status = http.StatusConflict
			case "42501":
				status = http.StatusForbidden
			case "57014":
				status = http.StatusRequestTimeout
			case "53300":
				status = http.StatusServiceUnavailable
				w.Header().Set("Retry-After", "1")
			default:
				status = http.StatusBadRequest
			}
			message = "database request failed"
			switch pgErr.Code {
			case "23505":
				message = "duplicate key value violates a unique constraint"
			case "23503":
				message = "related record does not exist"
			case "23514":
				message = "record violates a data constraint"
			case "42501":
				message = "not allowed by the project's permissions or row level security"
			case "P0001":
				// Raised on purpose by the project's own SQL (RAISE EXCEPTION),
				// so the text is meant for the caller, as in PostgREST.
				message = pgErr.Message
				if len(message) > 300 {
					message = message[:300]
				}
			}
			if pgErr.Detail != "" {
				details = pgErr.Detail
			}
			if pgErr.Hint != "" {
				hint = pgErr.Hint
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "message": message, "details": details, "hint": hint})
}
