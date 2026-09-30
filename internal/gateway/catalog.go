package gateway

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type columnInfo struct {
	name       string
	typeSchema string
	typeName   string
}

func (c columnInfo) cast(placeholder string) string {
	return "(" + placeholder + "::text)::" + pgx.Identifier{c.typeSchema, c.typeName}.Sanitize()
}

type tableInfo struct {
	schema  string
	name    string
	columns map[string]columnInfo
	ordered []string
}

type relationship struct {
	table         tableInfo
	baseColumn    string
	relatedColumn string
	many          bool
}

func loadTable(ctx context.Context, tx pgx.Tx, schema, name string) (tableInfo, error) {
	rows, err := tx.Query(ctx, `SELECT a.attname,tn.nspname,t.typname
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace
JOIN pg_catalog.pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
JOIN pg_catalog.pg_type t ON t.oid=a.atttypid
JOIN pg_catalog.pg_namespace tn ON tn.oid=t.typnamespace
WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind IN ('r','p','v','m')
  AND has_table_privilege(c.oid,'SELECT')
ORDER BY a.attnum`, schema, name)
	if err != nil {
		return tableInfo{}, err
	}
	defer rows.Close()
	table := tableInfo{schema: schema, name: name, columns: make(map[string]columnInfo)}
	for rows.Next() {
		var c columnInfo
		if err := rows.Scan(&c.name, &c.typeSchema, &c.typeName); err != nil {
			return tableInfo{}, err
		}
		table.columns[c.name] = c
		table.ordered = append(table.ordered, c.name)
	}
	if err := rows.Err(); err != nil {
		return tableInfo{}, err
	}
	if len(table.columns) == 0 {
		return tableInfo{}, httpError(http.StatusNotFound, "relation not found or not accessible")
	}
	return table, nil
}

func loadRelationship(ctx context.Context, tx pgx.Tx, base tableInfo, relatedName string) (relationship, error) {
	rows, err := tx.Query(ctx, `SELECT rn.nspname,rc.relname,ba.attname,ra.attname,false
FROM pg_catalog.pg_constraint fk
JOIN pg_catalog.pg_class bc ON bc.oid=fk.conrelid
JOIN pg_catalog.pg_namespace bn ON bn.oid=bc.relnamespace
JOIN pg_catalog.pg_class rc ON rc.oid=fk.confrelid
JOIN pg_catalog.pg_namespace rn ON rn.oid=rc.relnamespace
JOIN pg_catalog.pg_attribute ba ON ba.attrelid=bc.oid AND ba.attnum=fk.conkey[1]
JOIN pg_catalog.pg_attribute ra ON ra.attrelid=rc.oid AND ra.attnum=fk.confkey[1]
WHERE fk.contype='f' AND array_length(fk.conkey,1)=1 AND bn.nspname=$1 AND bc.relname=$2 AND rn.nspname='public' AND rc.relname=$3
UNION ALL
SELECT rn.nspname,rc.relname,ba.attname,ra.attname,true
FROM pg_catalog.pg_constraint fk
JOIN pg_catalog.pg_class bc ON bc.oid=fk.confrelid
JOIN pg_catalog.pg_namespace bn ON bn.oid=bc.relnamespace
JOIN pg_catalog.pg_class rc ON rc.oid=fk.conrelid
JOIN pg_catalog.pg_namespace rn ON rn.oid=rc.relnamespace
JOIN pg_catalog.pg_attribute ba ON ba.attrelid=bc.oid AND ba.attnum=fk.confkey[1]
JOIN pg_catalog.pg_attribute ra ON ra.attrelid=rc.oid AND ra.attnum=fk.conkey[1]
WHERE fk.contype='f' AND array_length(fk.conkey,1)=1 AND bn.nspname=$1 AND bc.relname=$2 AND rn.nspname='public' AND rc.relname=$3`, base.schema, base.name, relatedName)
	if err != nil {
		return relationship{}, err
	}
	defer rows.Close()
	var matches []relationship
	for rows.Next() {
		var schema, name string
		var relation relationship
		if err := rows.Scan(&schema, &name, &relation.baseColumn, &relation.relatedColumn, &relation.many); err != nil {
			return relationship{}, err
		}
		relation.table = tableInfo{schema: schema, name: name}
		matches = append(matches, relation)
	}
	rows.Close()
	if len(matches) == 0 {
		return relationship{}, httpError(http.StatusBadRequest, fmt.Sprintf("no accessible relationship to %q", relatedName))
	}
	if len(matches) > 1 {
		return relationship{}, httpError(http.StatusBadRequest, fmt.Sprintf("relationship to %q is ambiguous", relatedName))
	}
	matches[0].table, err = loadTable(ctx, tx, matches[0].table.schema, matches[0].table.name)
	if err != nil {
		return relationship{}, err
	}
	return matches[0], nil
}

func (t tableInfo) identifier() string {
	return pgx.Identifier{t.schema, t.name}.Sanitize()
}

func (t tableInfo) column(name string) (columnInfo, error) {
	c, ok := t.columns[name]
	if !ok {
		return columnInfo{}, httpError(http.StatusBadRequest, fmt.Sprintf("unknown column %q", name))
	}
	return c, nil
}
