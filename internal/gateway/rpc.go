package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

type functionArgument struct {
	name       string
	typeSchema string
	typeName   string
}

type functionInfo struct {
	name       string
	returnsSet bool
	arguments  []functionArgument
}

func (s *Server) rpcHandler(w http.ResponseWriter, r *http.Request) {
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
	name := r.PathValue("function")
	if name == "" || strings.Contains(name, ".") {
		writeAPIError(w, httpError(http.StatusBadRequest, "invalid function"))
		return
	}
	buffer := newResponseBuffer()
	err = s.transaction(r.Context(), who, func(tx pgx.Tx) error {
		if err := enforceDatabaseQuota(r.Context(), tx, who.project.maxDatabaseBytes); err != nil {
			return err
		}
		function, err := loadFunction(r.Context(), tx, name)
		if err != nil {
			return err
		}
		var supplied map[string]json.RawMessage
		decoder := json.NewDecoder(r.Body)
		if err := decoder.Decode(&supplied); err != nil {
			return httpError(http.StatusBadRequest, "RPC body must be a JSON object")
		}
		arguments := append([]functionArgument(nil), function.arguments...)
		sort.Slice(arguments, func(i, j int) bool { return arguments[i].name < arguments[j].name })
		calls := make([]string, 0, len(arguments))
		values := make([]any, 0, len(arguments))
		for _, argument := range arguments {
			value, ok := supplied[argument.name]
			if !ok {
				return httpError(http.StatusBadRequest, fmt.Sprintf("missing RPC argument %q", argument.name))
			}
			delete(supplied, argument.name)
			values = append(values, value)
			placeholder := "$" + strconv.Itoa(len(values)) + "::jsonb"
			cast := "(" + placeholder + "#>>'{}')::" + pgx.Identifier{argument.typeSchema, argument.typeName}.Sanitize()
			calls = append(calls, pgx.Identifier{argument.name}.Sanitize()+" => "+cast)
		}
		if len(supplied) != 0 {
			return httpError(http.StatusBadRequest, "unknown RPC argument")
		}
		call := pgx.Identifier{"public", function.name}.Sanitize() + "(" + strings.Join(calls, ",") + ")"
		query := "SELECT to_jsonb(" + call + ")"
		if function.returnsSet {
			query = "SELECT COALESCE(jsonb_agg(to_jsonb(result)),'[]'::jsonb) FROM " + call + " result"
		}
		var body []byte
		if err := tx.QueryRow(r.Context(), query, values...).Scan(&body); err != nil {
			return err
		}
		buffer.WriteHeader(http.StatusOK)
		if _, err = buffer.Write(body); err != nil {
			return err
		}
		return enforceDatabaseQuota(r.Context(), tx, who.project.maxDatabaseBytes)
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	buffer.flush(w)
}

func loadFunction(ctx context.Context, tx pgx.Tx, name string) (functionInfo, error) {
	rows, err := tx.Query(ctx, `SELECT p.oid,p.proretset,
COALESCE(jsonb_agg(jsonb_build_object('name',p.proargnames[a.ordinality],
  'type_schema',tn.nspname,'type_name',t.typname) ORDER BY a.ordinality)
  FILTER (WHERE a.oid IS NOT NULL),'[]'::jsonb)
FROM pg_catalog.pg_proc p
JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace
LEFT JOIN LATERAL unnest(p.proargtypes::oid[]) WITH ORDINALITY a(oid,ordinality) ON true
LEFT JOIN pg_catalog.pg_type t ON t.oid=a.oid
LEFT JOIN pg_catalog.pg_namespace tn ON tn.oid=t.typnamespace
WHERE n.nspname='public' AND p.proname=$1 AND has_function_privilege(p.oid,'EXECUTE')
GROUP BY p.oid,p.proretset`, name)
	if err != nil {
		return functionInfo{}, err
	}
	defer rows.Close()
	var matches []functionInfo
	for rows.Next() {
		var oid uint32
		var raw []byte
		function := functionInfo{name: name}
		if err := rows.Scan(&oid, &function.returnsSet, &raw); err != nil {
			return functionInfo{}, err
		}
		var arguments []struct {
			Name       *string `json:"name"`
			TypeSchema string  `json:"type_schema"`
			TypeName   string  `json:"type_name"`
		}
		if err := json.Unmarshal(raw, &arguments); err != nil {
			return functionInfo{}, err
		}
		valid := true
		for _, argument := range arguments {
			if argument.Name == nil || *argument.Name == "" {
				valid = false
				break
			}
			function.arguments = append(function.arguments, functionArgument{name: *argument.Name, typeSchema: argument.TypeSchema, typeName: argument.TypeName})
		}
		if valid {
			matches = append(matches, function)
		}
	}
	if len(matches) == 0 {
		return functionInfo{}, httpError(http.StatusNotFound, "function not found, inaccessible, or has unnamed arguments")
	}
	if len(matches) > 1 {
		return functionInfo{}, httpError(http.StatusBadRequest, "overloaded functions are not supported")
	}
	return matches[0], nil
}
