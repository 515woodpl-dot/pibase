package adminserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/515woodpl-dot/pibase/internal/auth"
	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
)

// TestOperatorDashboardEndpoints drives every M7a endpoint against a real
// PostgreSQL cluster: create, list, migrate, query, extension, back up, list
// backups, refuse a mistyped delete, delete.
func TestOperatorDashboardEndpoints(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "m7a-admin-password"
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	parsed.User = url.UserPassword("authenticator", password)
	parsed.Path = "/_control"
	root := t.TempDir()
	s, err := New(Config{
		AdminURL: adminURL, DatabaseURL: parsed.String(), Token: "m7a-token",
		StorageRoot: filepath.Join(root, "files"), BackupRoot: filepath.Join(root, "backups"),
		MasterKey: bytes.Repeat([]byte{7}, 32), BackupKey: bytes.Repeat([]byte{9}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	call := func(method, path, body string, want int) []byte {
		t.Helper()
		request, err := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer m7a-token")
		response, err := hs.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, response.StatusCode, want, data)
		}
		return data
	}

	var created struct{ ID, AnonKey, ServiceKey string }
	raw := call(http.MethodPost, "/_pibase/projects", `{"name":"m7a dashboard"}`, http.StatusCreated)
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	created.ID, _ = fields["id"].(string)
	created.AnonKey, _ = fields["anon_key"].(string)
	created.ServiceKey, _ = fields["service_key"].(string)
	if created.ID == "" || created.AnonKey == "" || created.ServiceKey == "" {
		t.Fatalf("create did not return id and keys: %s", raw)
	}
	deleted := false
	defer func() {
		if !deleted {
			_ = control.DeleteProject(context.Background(), adminURL, created.ID)
		}
	}()
	project := "/_pibase/projects/" + created.ID

	// Keys are shown once: the listing must not contain them.
	listing := call(http.MethodGet, "/_pibase/projects", "", http.StatusOK)
	if !bytes.Contains(listing, []byte(created.ID)) {
		t.Fatalf("project missing from list: %s", listing)
	}
	if bytes.Contains(listing, []byte(created.AnonKey)) || bytes.Contains(listing, []byte(created.ServiceKey)) {
		t.Fatal("project list exposes API keys")
	}

	migration, _ := json.Marshal(map[string]string{"name": "0001_m7a", "sql": `CREATE TABLE public."odd ""name"(id integer PRIMARY KEY, note text); INSERT INTO public."odd ""name" VALUES (1,'<script>alert(1)</script>'); CREATE TABLE public.programs ("program id" integer PRIMARY KEY, "copy " text, "grant_date" text); INSERT INTO public.programs VALUES (1,'first','today');`})
	call(http.MethodPost, project+"/migrations", string(migration), http.StatusNoContent)
	query, _ := json.Marshal(map[string]string{"sql": `SELECT note FROM public."odd ""name"`})
	result := call(http.MethodPost, project+"/sql", string(query), http.StatusOK)
	var rows struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(result, &rows); err != nil || len(rows.Rows) != 1 || rows.Rows[0]["note"] != "<script>alert(1)</script>" {
		t.Fatalf("sql result: %s err=%v", result, err)
	}
	// M7b catalog and row endpoints cover the required programs fixture and
	// deliberately awkward quoted names. Values remain JSON-bound parameters.
	tables := call(http.MethodGet, project+"/tables", "", http.StatusOK)
	if !bytes.Contains(tables, []byte(`"programs"`)) || !bytes.Contains(tables, []byte(`"program id"`)) {
		t.Fatalf("table catalog: %s", tables)
	}
	if bytes.Contains(tables, []byte(`pibase_migrations`)) {
		t.Fatal("pibase_migrations must be hidden from the table list")
	}
	browse := call(http.MethodGet, project+"/tables/programs/rows", "", http.StatusOK)
	if !bytes.Contains(browse, []byte(`"copy "`)) || !bytes.Contains(browse, []byte(`"first"`)) {
		t.Fatalf("row browse: %s", browse)
	}
	createdUser := call(http.MethodPost, project+"/users", `{"email":"m7b@example.test","password":"a sufficiently long password","email_confirm":true}`, http.StatusCreated)
	var user map[string]string
	if err := json.Unmarshal(createdUser, &user); err != nil || user["id"] == "" {
		t.Fatalf("user create: %s", createdUser)
	}
	// M7b-02: verify the stored hash is argon2id and VerifyPassword accepts it.
	{
		dbURL, err := url.Parse(adminURL)
		if err != nil {
			t.Fatal(err)
		}
		dbURL.Path = "/" + fields["database"].(string)
		conn, err := pgx.Connect(ctx, dbURL.String())
		if err != nil {
			t.Fatal(err)
		}
		var storedHash string
		if err := conn.QueryRow(ctx, `SELECT password_hash FROM auth.users WHERE id=$1`, user["id"]).Scan(&storedHash); err != nil {
			conn.Close(ctx)
			t.Fatalf("read password hash: %v", err)
		}
		conn.Close(ctx)
		if !strings.HasPrefix(storedHash, "$pibase$argon2id$") {
			t.Fatalf("password hash is not argon2id: %s", storedHash[:min(len(storedHash), 20)])
		}
		if !auth.VerifyPassword("a sufficiently long password", storedHash) {
			t.Fatal("VerifyPassword rejects the dashboard-created hash")
		}
		if auth.VerifyPassword("wrong password", storedHash) {
			t.Fatal("VerifyPassword accepts wrong password")
		}
	}
	call(http.MethodGet, project+"/users", "", http.StatusOK)
	call(http.MethodDelete, project+"/users/"+user["id"], `{"confirm":"wrong"}`, http.StatusBadRequest)
	call(http.MethodDelete, project+"/users/"+user["id"], `{"confirm":"`+user["id"]+`"}`, http.StatusNoContent)
	applied, _ := json.Marshal(map[string]string{"sql": `SELECT name FROM public.pibase_migrations`})
	if listed := call(http.MethodPost, project+"/sql", string(applied), http.StatusOK); !bytes.Contains(listed, []byte("0001_m7a")) {
		t.Fatalf("applied migration not listed: %s", listed)
	}
	// M7b-09: migrations list endpoint returns applied migrations.
	migList := call(http.MethodGet, project+"/migrations", "", http.StatusOK)
	if !bytes.Contains(migList, []byte("0001_m7a")) || !bytes.Contains(migList, []byte("checksum")) {
		t.Fatalf("migrations list: %s", migList)
	}
	call(http.MethodPost, project+"/extensions/unknown", "", http.StatusNotFound)

	var made struct{ File string }
	if err := json.Unmarshal(call(http.MethodPost, project+"/backup", "", http.StatusCreated), &made); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "backups", made.File))
	if err != nil || info.IsDir() || info.Size() == 0 || !strings.HasSuffix(made.File, ".pib") {
		t.Fatalf("backup file %q: info=%v err=%v", made.File, info, err)
	}
	var backups []map[string]any
	if err := json.Unmarshal(call(http.MethodGet, project+"/backups", "", http.StatusOK), &backups); err != nil || len(backups) != 1 || backups[0]["name"] != made.File {
		t.Fatalf("backups list: %v err=%v", backups, err)
	}
	// Another project's listing must not show this project's archive.
	if other := call(http.MethodGet, "/_pibase/projects/zzzzzzzz/backups", "", http.StatusOK); strings.TrimSpace(string(other)) != "[]" {
		t.Fatalf("backup listed under another project: %s", other)
	}

	call(http.MethodDelete, project, `{"confirm":"wrong"}`, http.StatusBadRequest)
	call(http.MethodDelete, project, `{"confirm":"`+created.ID+`"}`, http.StatusNoContent)
	deleted = true
	if after := call(http.MethodGet, "/_pibase/projects", "", http.StatusOK); bytes.Contains(after, []byte(`"`+created.ID+`"`)) {
		t.Fatalf("project still listed after delete: %s", after)
	}
}

// TestBrowseTextCasting verifies that browse returns every column as text,
// NULL as JSON null, and paging is stable across two pages.
func TestBrowseTextCasting(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "m7b-browse-pw"
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(adminURL)
	parsed.User = url.UserPassword("authenticator", password)
	parsed.Path = "/_control"
	root := t.TempDir()
	s, _ := New(Config{
		AdminURL: adminURL, DatabaseURL: parsed.String(), Token: "tok",
		StorageRoot: filepath.Join(root, "f"), BackupRoot: filepath.Join(root, "b"),
		MasterKey: bytes.Repeat([]byte{7}, 32), BackupKey: bytes.Repeat([]byte{9}, 32),
	})
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	call := func(method, path, body string, want int) []byte {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, want, data)
		}
		return data
	}
	var proj map[string]any
	json.Unmarshal(call("POST", "/_pibase/projects", `{"name":"browse test"}`, 201), &proj)
	id := proj["id"].(string)
	defer control.DeleteProject(ctx, adminURL, id)
	project := "/_pibase/projects/" + id

	mig := `CREATE TABLE public.types_test (
	  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
	  big bigint NOT NULL DEFAULT 0,
	  num numeric(10,2),
	  ts timestamptz,
	  j jsonb,
	  b bytea,
	  arr int[],
	  flag bool,
	  gen integer GENERATED ALWAYS AS (1+1) STORED,
	  nullable text
	);
	INSERT INTO public.types_test (id,big,num,ts,j,b,arr,flag,nullable)
	VALUES ('4d81ca00-0000-4000-8000-000000000001', 9007199254740993, 12345.67,
	  '2026-01-15 12:00:00+00', '{"key":"val"}', '\xdeadbeef', ARRAY[1,2,3], true, NULL);
	INSERT INTO public.types_test (id,big,num,ts,j,b,arr,flag,nullable)
	VALUES ('4d81ca00-0000-4000-8000-000000000002', 0, 0.00, NULL, NULL, NULL, NULL, false, '');`
	migBody, _ := json.Marshal(map[string]string{"name": "types", "sql": mig})
	call("POST", project+"/migrations", string(migBody), 204)

	data := call("GET", project+"/tables/types_test/rows", "", 200)
	var browse struct {
		Columns []string             `json:"columns"`
		Rows    []map[string]*string `json:"rows"`
		HasMore bool                 `json:"has_more"`
		Offset  int                  `json:"offset"`
	}
	if err := json.Unmarshal(data, &browse); err != nil {
		t.Fatalf("parse browse: %v\n%s", err, data)
	}
	if len(browse.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(browse.Rows))
	}
	r := browse.Rows[0]
	expect := map[string]string{
		"id":   "4d81ca00-0000-4000-8000-000000000001",
		"big":  "9007199254740993",
		"num":  "12345.67",
		"j":    `{"key": "val"}`,
		"b":    `\xdeadbeef`,
		"arr":  "{1,2,3}",
		"flag": "true",
		"gen":  "2",
	}
	for col, want := range expect {
		got := r[col]
		if got == nil {
			t.Errorf("column %q is null, want %q", col, want)
		} else if *got != want {
			t.Errorf("column %q = %q, want %q", col, *got, want)
		}
	}
	if r["nullable"] != nil {
		t.Errorf("nullable should be null, got %q", *r["nullable"])
	}
	if r["ts"] == nil || !strings.Contains(*r["ts"], "2026") {
		t.Errorf("ts = %v, want a timestamp string", r["ts"])
	}
	// Row 2: empty string vs null
	r2 := browse.Rows[1]
	if r2["nullable"] == nil || *r2["nullable"] != "" {
		t.Errorf("empty string should be empty, got %v", r2["nullable"])
	}
	if r2["j"] != nil {
		t.Errorf("null jsonb should be null, got %q", *r2["j"])
	}
	if browse.HasMore {
		t.Error("has_more should be false with 2 rows")
	}
}

// TestAnonReadable verifies the four-table matrix for anon_readable.
func TestAnonReadable(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "m7b-anon-pw"
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(adminURL)
	parsed.User = url.UserPassword("authenticator", password)
	parsed.Path = "/_control"
	root := t.TempDir()
	s, _ := New(Config{
		AdminURL: adminURL, DatabaseURL: parsed.String(), Token: "tok",
		StorageRoot: filepath.Join(root, "f"), BackupRoot: filepath.Join(root, "b"),
		MasterKey: bytes.Repeat([]byte{7}, 32), BackupKey: bytes.Repeat([]byte{9}, 32),
	})
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	call := func(method, path, bd string, want int) []byte {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(bd))
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, want, data)
		}
		return data
	}
	var proj map[string]any
	json.Unmarshal(call("POST", "/_pibase/projects", `{"name":"anon test"}`, 201), &proj)
	id := proj["id"].(string)
	defer control.DeleteProject(ctx, adminURL, id)
	project := "/_pibase/projects/" + id

	// Find the actual anon role name
	database := proj["database"].(string)
	anonRole := strings.Replace(database, "p_", "p_", 1) + "_anon"

	sql := `
	CREATE TABLE public.granted_no_rls (id int PRIMARY KEY);
	ALTER TABLE public.granted_no_rls DISABLE ROW LEVEL SECURITY;
	GRANT SELECT ON public.granted_no_rls TO ` + anonRole + `;
	CREATE TABLE public.granted_rls_no_policy (id int PRIMARY KEY);
	GRANT SELECT ON public.granted_rls_no_policy TO ` + anonRole + `;
	ALTER TABLE public.granted_rls_no_policy ENABLE ROW LEVEL SECURITY;
	CREATE TABLE public.granted_rls_policy (id int PRIMARY KEY);
	GRANT SELECT ON public.granted_rls_policy TO ` + anonRole + `;
	ALTER TABLE public.granted_rls_policy ENABLE ROW LEVEL SECURITY;
	CREATE POLICY anon_read ON public.granted_rls_policy FOR SELECT TO PUBLIC USING (true);
	CREATE TABLE public.ungranted (id int PRIMARY KEY);
	CREATE TABLE public.restrictive_false (id int PRIMARY KEY);
	GRANT SELECT ON public.restrictive_false TO ` + anonRole + `;
	ALTER TABLE public.restrictive_false ENABLE ROW LEVEL SECURITY;
	CREATE POLICY open_read ON public.restrictive_false FOR SELECT TO PUBLIC USING (true);
	CREATE POLICY shut ON public.restrictive_false AS RESTRICTIVE FOR SELECT TO PUBLIC USING (false);
	CREATE TABLE public.restrictive_only (id int PRIMARY KEY);
	GRANT SELECT ON public.restrictive_only TO ` + anonRole + `;
	ALTER TABLE public.restrictive_only ENABLE ROW LEVEL SECURITY;
	CREATE POLICY only_r ON public.restrictive_only AS RESTRICTIVE FOR SELECT TO PUBLIC USING (true);
	CREATE TABLE public.using_false (id int PRIMARY KEY);
	GRANT SELECT ON public.using_false TO ` + anonRole + `;
	ALTER TABLE public.using_false ENABLE ROW LEVEL SECURITY;
	CREATE POLICY never ON public.using_false FOR SELECT TO PUBLIC USING (false);
	CREATE TABLE public.other_role_policy (id int PRIMARY KEY);
	GRANT SELECT ON public.other_role_policy TO ` + anonRole + `;
	ALTER TABLE public.other_role_policy ENABLE ROW LEVEL SECURITY;
	CREATE POLICY auth_only ON public.other_role_policy FOR SELECT TO ` + strings.TrimSuffix(anonRole, "_anon") + `_authenticated USING (true);
	CREATE TABLE public.column_grant (id int PRIMARY KEY, name text);
	ALTER TABLE public.column_grant DISABLE ROW LEVEL SECURITY;
	GRANT SELECT (name) ON public.column_grant TO ` + anonRole + `;`
	migBody, _ := json.Marshal(map[string]string{"name": "anon_test", "sql": sql})
	call("POST", project+"/migrations", string(migBody), 204)

	var tables []struct {
		Name         string `json:"name"`
		AnonReadable bool   `json:"anon_readable"`
	}
	json.Unmarshal(call("GET", project+"/tables", "", 200), &tables)
	m := map[string]bool{}
	for _, tbl := range tables {
		m[tbl.Name] = tbl.AnonReadable
	}
	if !m["granted_no_rls"] {
		t.Error("granted_no_rls should be anon readable")
	}
	if m["granted_rls_no_policy"] {
		t.Error("granted_rls_no_policy should NOT be anon readable")
	}
	if !m["granted_rls_policy"] {
		t.Error("granted_rls_policy should be anon readable")
	}
	if m["ungranted"] {
		t.Error("ungranted should NOT be anon readable")
	}
	for name, want := range map[string]bool{"restrictive_false": false, "restrictive_only": false, "using_false": false, "other_role_policy": false, "column_grant": true} {
		if got, ok := m[name]; !ok || got != want {
			t.Errorf("%s: anon_readable=%v (listed=%v), want %v", name, got, ok, want)
		}
	}
}

// TestOwnerSQLRoleIsolation verifies that the authenticator connection used by
// owner SQL cannot escape into another project's data via RESET ROLE or SET ROLE.
func TestOwnerSQLRoleIsolation(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "isolation-test"
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(adminURL)
	parsed.User = url.UserPassword("authenticator", password)
	parsed.Path = "/_control"
	root := t.TempDir()
	s, err := New(Config{
		AdminURL: adminURL, DatabaseURL: parsed.String(), Token: "tok",
		StorageRoot: filepath.Join(root, "files"), BackupRoot: filepath.Join(root, "backups"),
		MasterKey: bytes.Repeat([]byte{7}, 32), BackupKey: bytes.Repeat([]byte{9}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	call := func(method, path, bd string, want int) []byte {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(bd))
		req.Header.Set("Authorization", "Bearer tok")
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, resp.StatusCode, want, data)
		}
		return data
	}

	// Create two projects
	var projA, projB map[string]any
	json.Unmarshal(call("POST", "/_pibase/projects", `{"name":"iso A"}`, 201), &projA)
	json.Unmarshal(call("POST", "/_pibase/projects", `{"name":"iso B"}`, 201), &projB)
	idA := projA["id"].(string)
	idB := projB["id"].(string)
	defer control.DeleteProject(ctx, adminURL, idA)
	defer control.DeleteProject(ctx, adminURL, idB)
	pathA := "/_pibase/projects/" + idA

	// Create a table with data in project A
	migSQL := `CREATE TABLE public.secrets (id int PRIMARY KEY, val text); INSERT INTO public.secrets VALUES (1,'classified');`
	mb, _ := json.Marshal(map[string]string{"name": "seed", "sql": migSQL})
	call("POST", pathA+"/migrations", string(mb), 204)

	// Normal query works
	qb, _ := json.Marshal(map[string]string{"sql": "SELECT val FROM public.secrets"})
	data := call("POST", pathA+"/sql", string(qb), 200)
	if !strings.Contains(string(data), "classified") {
		t.Fatalf("normal query should see data: %s", data)
	}

	// RESET ROLE — blocked by SQL filter
	qb, _ = json.Marshal(map[string]string{"sql": "RESET ROLE"})
	call("POST", pathA+"/sql", string(qb), 400)

	// SET ROLE to another project's admin — blocked by SQL filter
	dbB := projB["database"].(string)
	adminRoleB := strings.Replace(dbB, "p_", "p_", 1) + "_admin"
	qb, _ = json.Marshal(map[string]string{"sql": "SET ROLE " + adminRoleB})
	call("POST", pathA+"/sql", string(qb), 400)

	// SET SESSION AUTHORIZATION — blocked
	qb, _ = json.Marshal(map[string]string{"sql": "SET SESSION AUTHORIZATION 'postgres'"})
	call("POST", pathA+"/sql", string(qb), 400)

	// RESET ALL — blocked
	qb, _ = json.Marshal(map[string]string{"sql": "RESET ALL"})
	call("POST", pathA+"/sql", string(qb), 400)

	// Spellings found by the independent review are refused at the endpoint.
	for _, bypass := range []string{
		`SELECT "set_config"('role','` + adminRoleB + `',true)`,
		`SET U&"role" TO ` + adminRoleB,
		`DO $x$ BEGIN EXECUTE 'RESET ROLE'; END $x$`,
		`UPDATE pg_settings SET setting='x' WHERE name='role'`,
		`CREATE GROUP g`,
		`SELECT E'\' ', set_config('role','x',true) --'`,
	} {
		qb, _ = json.Marshal(map[string]string{"sql": bypass})
		call("POST", pathA+"/sql", string(qb), 400)
	}
}
