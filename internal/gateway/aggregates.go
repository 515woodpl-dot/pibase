package gateway

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

var aggregateToken = regexp.MustCompile(`^(?:(?:[A-Za-z_][A-Za-z0-9_]*):)?(?:count\(\*?\)|[A-Za-z_][A-Za-z0-9_]*\.(?:sum|avg|min|max|count)\(\))$`)
var aggregateAlias = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func aggregateSelection(selection string) bool {
	for _, suffix := range []string{"count()", "count(*)", ".sum()", ".avg()", ".min()", ".max()", ".count()"} {
		if strings.Contains(selection, suffix) {
			return true
		}
	}
	return false
}

// selectAggregates supports a bounded PostgREST-shaped aggregate subset. All
// source rows are read under the caller's ordinary PostgreSQL RLS context.
func selectAggregates(ctx context.Context, w http.ResponseWriter, r *http.Request, tx pgx.Tx, table tableInfo) error {
	values := cloneValues(r.URL.Query())
	if values.Get("order") != "" {
		return httpError(400, "order is not supported with aggregates")
	}
	selection := values.Get("select")
	values.Set("select", "*")
	plan, err := buildQuery(values, table)
	if err != nil {
		return err
	}
	var columns, groups []string
	used := map[string]bool{}
	hasAggregate := false
	for _, raw := range strings.Split(selection, ",") {
		token := strings.TrimSpace(raw)
		if token == "" {
			return httpError(400, "empty aggregate selection")
		}
		alias, expression, named := strings.Cut(token, ":")
		if !named {
			expression = alias
			alias = ""
		}
		if strings.Contains(expression, "(") {
			if !aggregateToken.MatchString(token) {
				return httpError(400, "invalid aggregate selection")
			}
			function := ""
			column := ""
			if expression == "count()" || expression == "count(*)" {
				function = "count"
			} else {
				column, function, _ = strings.Cut(expression, ".")
				function = strings.TrimSuffix(function, "()")
				if _, err := table.column(column); err != nil {
					return err
				}
			}
			if alias == "" {
				alias = function
				if column != "" {
					alias = column + "_" + function
				}
			}
			if !aggregateAlias.MatchString(alias) || used[alias] {
				return httpError(400, "invalid or duplicate aggregate alias")
			}
			used[alias] = true
			arg := "*"
			if column != "" {
				arg = pgx.Identifier{column}.Sanitize()
			}
			columns = append(columns, strings.ToUpper(function)+"("+arg+") AS "+pgx.Identifier{alias}.Sanitize())
			hasAggregate = true
		} else {
			if named || token == "*" {
				return httpError(400, "invalid grouped column")
			}
			if _, err := table.column(token); err != nil {
				return err
			}
			if used[token] {
				return httpError(400, "duplicate selected column")
			}
			used[token] = true
			quoted := pgx.Identifier{token}.Sanitize()
			columns = append(columns, quoted)
			groups = append(groups, quoted)
		}
	}
	if !hasAggregate {
		return httpError(400, "aggregate is required")
	}
	query := "SELECT COALESCE(jsonb_agg(to_jsonb(q)),'[]'::jsonb) FROM (SELECT " + strings.Join(columns, ",") + " FROM " + table.identifier() + plan.where
	if len(groups) > 0 {
		query += " GROUP BY " + strings.Join(groups, ",")
	}
	query += fmt.Sprintf(" LIMIT %d OFFSET %d) q", plan.limit, plan.offset)
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
