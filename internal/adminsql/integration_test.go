package adminsql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const integrationPassword = "adminsql-integration"

func integrationSetup(t *testing.T) (adminURL, authURL string) {
	t.Helper()
	adminURL = os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	if err := control.Bootstrap(context.Background(), adminURL, integrationPassword); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword("authenticator", integrationPassword)
	parsed.Path = "/_control"
	return adminURL, parsed.String()
}

func newProject(t *testing.T, adminURL, name string) control.Project {
	t.Helper()
	p, err := control.CreateProject(context.Background(), adminURL, name, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.DeleteProject(context.Background(), adminURL, p.ID) })
	return p
}

// TestOwnerConnectionReach opens the owner connection exactly as Run does and
// records what it can reach once the keyword filter is assumed bypassed.
func TestOwnerConnectionReach(t *testing.T) {
	adminURL, authURL := integrationSetup(t)
	ctx := context.Background()
	a := newProject(t, adminURL, "reach a")
	b := newProject(t, adminURL, "reach b")
	if err := ApplyMigration(ctx, authURL, a.ID, "seed", `CREATE TABLE public.secrets (id int PRIMARY KEY, val text); INSERT INTO public.secrets VALUES (1,'a-only')`); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigration(ctx, authURL, b.ID, "seed", `CREATE TABLE public.b_secrets (id int PRIMARY KEY, val text); INSERT INTO public.b_secrets VALUES (1,'b-only')`); err != nil {
		t.Fatal(err)
	}
	pa, err := lookupProject(ctx, authURL, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := lookupProject(ctx, authURL, b.ID)
	if err != nil {
		t.Fatal(err)
	}

	// probe runs setup then query in a fresh owner transaction on project A's
	// database and returns the single text result or the SQLSTATE.
	probe := func(setup, query string) (string, string) {
		t.Helper()
		conn, err := connectProject(ctx, authURL, pa.database)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{pa.role}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		if setup != "" {
			if _, err := tx.Exec(ctx, setup); err != nil {
				return "", sqlState(err)
			}
		}
		var out string
		if err := tx.QueryRow(ctx, query).Scan(&out); err != nil {
			return "", sqlState(err)
		}
		return out, ""
	}
	type expect struct {
		name, setup, query string
		ok                 bool
		value              string
	}
	cases := []expect{
		{"after RESET ROLE current_user", "RESET ROLE", "SELECT current_user::text", true, "authenticator"},
		{"after RESET ROLE read own project table", "RESET ROLE", "SELECT val FROM public.secrets", false, ""},
		{"after RESET ROLE read auth.users", "RESET ROLE", "SELECT count(*)::text FROM auth.users", false, ""},
		{"after RESET ROLE read server file", "RESET ROLE", "SELECT pg_read_file('/etc/passwd')", false, ""},
		{"after RESET ROLE create role", "RESET ROLE", "CREATE ROLE reach_probe", false, ""},
		{"after RESET ROLE session authorization", "RESET ROLE; SET SESSION AUTHORIZATION postgres", "SELECT current_user::text", false, ""},
		{"after RESET ROLE grant other project role", "RESET ROLE", "GRANT " + pgx.Identifier{pb.role}.Sanitize() + " TO " + pgx.Identifier{pa.role}.Sanitize(), false, ""},
		{"SET ROLE other admin current_user", "SET ROLE " + pgx.Identifier{pb.role}.Sanitize(), "SELECT current_user::text", true, pb.role},
		{"SET ROLE other admin read project A table", "SET ROLE " + pgx.Identifier{pb.role}.Sanitize(), "SELECT val FROM public.secrets", false, ""},
		{"SET ROLE other anon read project A table", "SET ROLE " + pgx.Identifier{pb.anonRole}.Sanitize(), "SELECT val FROM public.secrets", false, ""},
		{"project B table from project A database", "RESET ROLE", "SELECT val FROM public.b_secrets", false, ""},
		{"cross-database extensions installed", "", "SELECT count(*)::text FROM pg_extension WHERE extname IN ('dblink','postgres_fdw')", true, "0"},
	}
	for _, c := range cases {
		got, state := probe(c.setup, c.query)
		t.Logf("%-45s value=%q sqlstate=%s", c.name, got, state)
		if c.ok && (state != "" || got != c.value) {
			t.Errorf("%s: got value=%q sqlstate=%s, want %q", c.name, got, state, c.value)
		}
		if !c.ok && state == "" {
			t.Errorf("%s: reachable (value=%q), want an error", c.name, got)
		}
	}
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return "error"
}

func TestListTablesUsesOneProjectConnection(t *testing.T) {
	adminURL, authURL := integrationSetup(t)
	ctx := context.Background()
	p := newProject(t, adminURL, "twenty tables")
	var ddl strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&ddl, "CREATE TABLE public.t%02d (id int PRIMARY KEY); INSERT INTO public.t%02d VALUES (%d);", i, i, i)
	}
	if err := ApplyMigration(ctx, authURL, p.ID, "twenty", ddl.String()); err != nil {
		t.Fatal(err)
	}
	before := projectConnections.Load()
	tables, err := ListTables(ctx, authURL, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if opened := projectConnections.Load() - before; opened != 1 {
		t.Fatalf("ListTables opened %d project connections, want 1", opened)
	}
	if len(tables) != 20 {
		t.Fatalf("got %d tables, want 20 (pibase_migrations hidden)", len(tables))
	}
	for _, tb := range tables {
		if tb.Count != 1 || len(tb.PrimaryKey) != 1 {
			t.Fatalf("table %+v: want count 1 and one primary key column", tb)
		}
	}
}

func TestBrowseRowsPagingIsStable(t *testing.T) {
	adminURL, authURL := integrationSetup(t)
	ctx := context.Background()
	p := newProject(t, adminURL, "paging")
	if err := ApplyMigration(ctx, authURL, p.ID, "rows", `
CREATE TABLE public.with_pk (a int, b text, PRIMARY KEY (a, b));
INSERT INTO public.with_pk SELECT g % 7, 'r' || g FROM generate_series(1,61) g;
CREATE TABLE public.no_pk (v text);
INSERT INTO public.no_pk SELECT 'r' || g FROM generate_series(1,61) g;
CREATE TABLE public.no_columns ();`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		table   string
		columns []string
		pk      []string
	}{{"with_pk", []string{"a", "b"}, []string{"a", "b"}}, {"no_pk", []string{"v"}, nil}} {
		seen := map[string]bool{}
		for page, offset := range []int{0, 50} {
			res, err := BrowseRows(ctx, authURL, p.ID, tc.table, tc.columns, tc.pk, offset)
			if err != nil {
				t.Fatal(err)
			}
			rows := res["rows"].([]map[string]any)
			wantRows, wantMore := 50, true
			if page == 1 {
				wantRows, wantMore = 11, false
			}
			if len(rows) != wantRows || res["has_more"] != wantMore {
				t.Fatalf("%s offset %d: %d rows has_more=%v, want %d/%v", tc.table, offset, len(rows), res["has_more"], wantRows, wantMore)
			}
			for _, r := range rows {
				key := fmt.Sprint(r[tc.columns[len(tc.columns)-1]])
				if seen[key] {
					t.Fatalf("%s: row %s appears on two pages", tc.table, key)
				}
				seen[key] = true
			}
		}
		if len(seen) != 61 {
			t.Fatalf("%s: saw %d distinct rows across pages, want 61", tc.table, len(seen))
		}
	}
	res, err := BrowseRows(ctx, authURL, p.ID, "no_columns", nil, nil, 0)
	if err != nil || len(res["rows"].([]map[string]any)) != 0 {
		t.Fatalf("zero-column table: %v %v", res, err)
	}
}
