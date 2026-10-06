package adminserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// TestAccountAuth covers U1–U10: signup, login, logout, and /auth/me.
func TestAccountAuth(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "t2a-auth-pw"
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(adminURL)
	parsed.User = url.UserPassword("authenticator", password)
	parsed.Path = "/_control"
	root := t.TempDir()
	s, err := New(Config{
		AdminURL:    adminURL,
		DatabaseURL: parsed.String(),
		Token:       "op-token",
		StorageRoot: filepath.Join(root, "f"),
		BackupRoot:  filepath.Join(root, "b"),
		MasterKey:   bytes.Repeat([]byte{7}, 32),
		BackupKey:   bytes.Repeat([]byte{9}, 32),
		Env:         "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()

	sfx := fmt.Sprintf("%d", time.Now().UnixNano())

	do := func(method, path, body string, cookie *http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	readBody := func(r *http.Response) []byte {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		return data
	}
	sessionCookie := func(r *http.Response) *http.Cookie {
		for _, c := range r.Cookies() {
			if c.Name == "pibase_session" {
				return c
			}
		}
		return nil
	}

	// U1: Signup valid → 201 + cookie
	t.Run("U1_signup_valid", func(t *testing.T) {
		resp := do("POST", "/_pibase/auth/signup", `{"email":"u1`+sfx+`@t2a.test","password":"longpassword"}`, nil)
		readBody(resp)
		if resp.StatusCode != 201 {
			t.Fatalf("want 201 got %d", resp.StatusCode)
		}
		if sessionCookie(resp) == nil {
			t.Fatal("want session cookie")
		}
	})

	// U2: Signup duplicate email → 409
	t.Run("U2_signup_duplicate", func(t *testing.T) {
		resp := do("POST", "/_pibase/auth/signup", `{"email":"u2`+sfx+`@t2a.test","password":"longpassword"}`, nil)
		readBody(resp)
		if resp.StatusCode != 201 {
			t.Fatalf("setup: want 201 got %d", resp.StatusCode)
		}
		resp2 := do("POST", "/_pibase/auth/signup", `{"email":"u2`+sfx+`@t2a.test","password":"anotherpassword"}`, nil)
		readBody(resp2)
		if resp2.StatusCode != 409 {
			t.Fatalf("want 409 got %d", resp2.StatusCode)
		}
	})

	// U3: Signup password < 8 chars → 400
	t.Run("U3_signup_short_password", func(t *testing.T) {
		resp := do("POST", "/_pibase/auth/signup", `{"email":"u3`+sfx+`@t2a.test","password":"short"}`, nil)
		readBody(resp)
		if resp.StatusCode != 400 {
			t.Fatalf("want 400 got %d", resp.StatusCode)
		}
	})

	// U4: Login correct → 200 + cookie
	t.Run("U4_login_correct", func(t *testing.T) {
		resp := do("POST", "/_pibase/auth/signup", `{"email":"u4`+sfx+`@t2a.test","password":"correctpassword"}`, nil)
		readBody(resp)
		if resp.StatusCode != 201 {
			t.Fatalf("setup: want 201 got %d", resp.StatusCode)
		}
		resp2 := do("POST", "/_pibase/auth/login", `{"email":"u4`+sfx+`@t2a.test","password":"correctpassword"}`, nil)
		readBody(resp2)
		if resp2.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp2.StatusCode)
		}
		if sessionCookie(resp2) == nil {
			t.Fatal("want session cookie")
		}
	})

	// U5: Login wrong password → 401
	t.Run("U5_login_wrong_password", func(t *testing.T) {
		resp := do("POST", "/_pibase/auth/signup", `{"email":"u5`+sfx+`@t2a.test","password":"correctpassword"}`, nil)
		readBody(resp)
		if resp.StatusCode != 201 {
			t.Fatalf("setup: want 201 got %d", resp.StatusCode)
		}
		resp2 := do("POST", "/_pibase/auth/login", `{"email":"u5`+sfx+`@t2a.test","password":"wrongpassword"}`, nil)
		readBody(resp2)
		if resp2.StatusCode != 401 {
			t.Fatalf("want 401 got %d", resp2.StatusCode)
		}
	})

	// U6: Login absent email → 401 (same shape as wrong password)
	t.Run("U6_login_absent_email", func(t *testing.T) {
		resp := do("POST", "/_pibase/auth/login", `{"email":"nobody@t2a.test","password":"anypassword"}`, nil)
		readBody(resp)
		if resp.StatusCode != 401 {
			t.Fatalf("want 401 got %d", resp.StatusCode)
		}
	})

	// U7: Logout valid session → 200, cookie cleared (Max-Age <= 0)
	t.Run("U7_logout_valid", func(t *testing.T) {
		resp := do("POST", "/_pibase/auth/signup", `{"email":"u7`+sfx+`@t2a.test","password":"longpassword"}`, nil)
		readBody(resp)
		cookie := sessionCookie(resp)
		if cookie == nil {
			t.Fatal("want session cookie from signup")
		}
		resp2 := do("POST", "/_pibase/auth/logout", "", cookie)
		readBody(resp2)
		if resp2.StatusCode != 200 {
			t.Fatalf("want 200 got %d", resp2.StatusCode)
		}
		for _, c := range resp2.Cookies() {
			if c.Name == "pibase_session" && c.MaxAge > 0 {
				t.Fatalf("cookie Max-Age should be <=0, got %d", c.MaxAge)
			}
		}
	})

	// U8: Logout no cookie → 401
	t.Run("U8_logout_no_cookie", func(t *testing.T) {
		resp := do("POST", "/_pibase/auth/logout", "", nil)
		readBody(resp)
		if resp.StatusCode != 401 {
			t.Fatalf("want 401 got %d", resp.StatusCode)
		}
	})

	// U9: GET /auth/me valid → 200 with id/email/created_at
	t.Run("U9_me_valid", func(t *testing.T) {
		resp := do("POST", "/_pibase/auth/signup", `{"email":"u9`+sfx+`@t2a.test","password":"longpassword"}`, nil)
		cookie := sessionCookie(resp)
		readBody(resp)
		if cookie == nil {
			t.Fatal("want session cookie from signup")
		}
		resp2 := do("GET", "/_pibase/auth/me", "", cookie)
		body := readBody(resp2)
		if resp2.StatusCode != 200 {
			t.Fatalf("want 200 got %d: %s", resp2.StatusCode, body)
		}
		var acc map[string]any
		if err := json.Unmarshal(body, &acc); err != nil {
			t.Fatalf("parse me response: %v", err)
		}
		if acc["id"] == "" || acc["email"] != "u9"+sfx+"@t2a.test" || acc["created_at"] == nil {
			t.Fatalf("me response missing fields: %s", body)
		}
	})

	// U10: GET /auth/me with invalid/expired token → 401
	t.Run("U10_me_invalid_token", func(t *testing.T) {
		fakeCookie := &http.Cookie{Name: "pibase_session", Value: "notarealtoken"}
		resp := do("GET", "/_pibase/auth/me", "", fakeCookie)
		readBody(resp)
		if resp.StatusCode != 401 {
			t.Fatalf("want 401 got %d", resp.StatusCode)
		}
	})
}

// TestAccountOwnership covers I1–I12: project ownership enforcement.
func TestAccountOwnership(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "t2a-own-pw"
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(adminURL)
	parsed.User = url.UserPassword("authenticator", password)
	parsed.Path = "/_control"
	root := t.TempDir()
	s, err := New(Config{
		AdminURL:    adminURL,
		DatabaseURL: parsed.String(),
		Token:       "op-token",
		StorageRoot: filepath.Join(root, "f"),
		BackupRoot:  filepath.Join(root, "b"),
		MasterKey:   bytes.Repeat([]byte{7}, 32),
		BackupKey:   bytes.Repeat([]byte{9}, 32),
		Env:         "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()

	sfx := fmt.Sprintf("%d", time.Now().UnixNano())

	// Helper: signup and return session cookie.
	signup := func(email, pw string) *http.Cookie {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+"/_pibase/auth/signup",
			strings.NewReader(`{"email":"`+email+`","password":"`+pw+`"}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 201 {
			t.Fatalf("signup %s: want 201 got %d", email, resp.StatusCode)
		}
		for _, c := range resp.Cookies() {
			if c.Name == "pibase_session" {
				return c
			}
		}
		t.Fatalf("signup %s: no session cookie", email)
		return nil
	}

	// Helper: make a request with cookie auth.
	callAs := func(cookie *http.Cookie, method, path, body string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, data
	}

	// Helper: make an operator request.
	callOp := func(method, path, body string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer op-token")
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, data
	}

	alice := signup("alice"+sfx+"@t2a.test", "alicepassword")
	bob := signup("bob"+sfx+"@t2a.test", "bobpassword")

	// I1: Account creates project, list returns only that project.
	t.Run("I1_list_own_projects", func(t *testing.T) {
		code, body := callAs(alice, "POST", "/_pibase/projects", `{"name":"alice project"}`)
		if code != 201 {
			t.Fatalf("want 201 got %d: %s", code, body)
		}
		var p map[string]any
		json.Unmarshal(body, &p)
		projID := p["id"].(string)
		defer control.DeleteProject(ctx, adminURL, projID)

		code2, list := callAs(alice, "GET", "/_pibase/projects", "")
		if code2 != 200 {
			t.Fatalf("list: want 200 got %d", code2)
		}
		if !bytes.Contains(list, []byte(projID)) {
			t.Fatalf("alice project missing from list: %s", list)
		}
		// Bob should not see alice's project.
		_, bobList := callAs(bob, "GET", "/_pibase/projects", "")
		if bytes.Contains(bobList, []byte(projID)) {
			t.Fatal("alice's project visible to bob")
		}
	})

	// I2: Second account cannot access first account's project SQL endpoint → 403.
	t.Run("I2_other_account_forbidden_sql", func(t *testing.T) {
		code, body := callAs(alice, "POST", "/_pibase/projects", `{"name":"alice sql test"}`)
		if code != 201 {
			t.Fatalf("want 201 got %d", code)
		}
		var p map[string]any
		json.Unmarshal(body, &p)
		projID := p["id"].(string)
		defer control.DeleteProject(ctx, adminURL, projID)

		code2, _ := callAs(bob, "POST", "/_pibase/projects/"+projID+"/sql", `{"sql":"SELECT 1"}`)
		if code2 != 403 {
			t.Fatalf("want 403 got %d", code2)
		}
	})

	// I3: Operator token accesses any project → 200.
	t.Run("I3_operator_sees_all", func(t *testing.T) {
		code, body := callAs(alice, "POST", "/_pibase/projects", `{"name":"alice op test"}`)
		if code != 201 {
			t.Fatalf("want 201 got %d", code)
		}
		var p map[string]any
		json.Unmarshal(body, &p)
		projID := p["id"].(string)
		defer control.DeleteProject(ctx, adminURL, projID)

		code2, _ := callOp("POST", "/_pibase/projects/"+projID+"/sql", `{"sql":"SELECT 1"}`)
		if code2 != 200 {
			t.Fatalf("operator: want 200 got %d", code2)
		}
	})

	// I4: Account cannot access extensions endpoint → 403.
	t.Run("I4_account_cannot_manage_extensions", func(t *testing.T) {
		code, body := callAs(alice, "POST", "/_pibase/projects", `{"name":"alice ext test"}`)
		if code != 201 {
			t.Fatalf("want 201 got %d", code)
		}
		var p map[string]any
		json.Unmarshal(body, &p)
		projID := p["id"].(string)
		defer control.DeleteProject(ctx, adminURL, projID)

		code2, _ := callAs(alice, "POST", "/_pibase/projects/"+projID+"/extensions/pgvector", "")
		if code2 != 403 {
			t.Fatalf("want 403 got %d", code2)
		}
	})

	// I5: Project created by account has owner_id = account.id in DB.
	t.Run("I5_owner_id_set_in_db", func(t *testing.T) {
		// Get alice's account ID first via /auth/me
		req, _ := http.NewRequest("GET", hs.URL+"/_pibase/auth/me", nil)
		req.AddCookie(alice)
		resp, _ := hs.Client().Do(req)
		var acc map[string]any
		json.NewDecoder(resp.Body).Decode(&acc)
		resp.Body.Close()
		aliceID := acc["id"].(string)

		code, body := callAs(alice, "POST", "/_pibase/projects", `{"name":"alice db check"}`)
		if code != 201 {
			t.Fatalf("want 201 got %d", code)
		}
		var p map[string]any
		json.Unmarshal(body, &p)
		projID := p["id"].(string)
		defer control.DeleteProject(ctx, adminURL, projID)

		dbURL, _ := url.Parse(adminURL)
		dbURL.Path = "/_control"
		conn, err := pgx.Connect(ctx, dbURL.String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		var ownerID *string
		err = conn.QueryRow(ctx, `SELECT owner_id::text FROM control.projects WHERE id=$1`, projID).Scan(&ownerID)
		if err != nil {
			t.Fatal(err)
		}
		if ownerID == nil || *ownerID != aliceID {
			t.Fatalf("owner_id = %v, want %s", ownerID, aliceID)
		}
	})

	// I6: Project created by operator has owner_id = NULL.
	t.Run("I6_operator_project_no_owner", func(t *testing.T) {
		code, body := callOp("POST", "/_pibase/projects", `{"name":"op owned"}`)
		if code != 201 {
			t.Fatalf("want 201 got %d", code)
		}
		var p map[string]any
		json.Unmarshal(body, &p)
		projID := p["id"].(string)
		defer control.DeleteProject(ctx, adminURL, projID)

		dbURL, _ := url.Parse(adminURL)
		dbURL.Path = "/_control"
		conn, err := pgx.Connect(ctx, dbURL.String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close(ctx)
		var ownerID *string
		err = conn.QueryRow(ctx, `SELECT owner_id::text FROM control.projects WHERE id=$1`, projID).Scan(&ownerID)
		if err != nil {
			t.Fatal(err)
		}
		if ownerID != nil {
			t.Fatalf("owner_id should be NULL, got %s", *ownerID)
		}
	})

	// I7: No auth → /_pibase/projects/... → 401.
	t.Run("I7_no_auth_401", func(t *testing.T) {
		code, _ := callAs(nil, "GET", "/_pibase/projects", "")
		if code != 401 {
			t.Fatalf("want 401 got %d", code)
		}
		code2, _ := callAs(nil, "POST", "/_pibase/projects", `{"name":"x"}`)
		if code2 != 401 {
			t.Fatalf("POST projects: want 401 got %d", code2)
		}
	})

	// I8: Invalid session cookie → 401.
	t.Run("I8_invalid_session_401", func(t *testing.T) {
		fakeCookie := &http.Cookie{Name: "pibase_session", Value: "invalidsessiontoken"}
		code, _ := callAs(fakeCookie, "GET", "/_pibase/projects", "")
		if code != 401 {
			t.Fatalf("want 401 got %d", code)
		}
	})

	// I9: Login then logout then reuse cookie → 401.
	t.Run("I9_revoked_session_401", func(t *testing.T) {
		cookie := signup("i9"+sfx+"@t2a.test", "longpassword9")
		// Logout
		req, _ := http.NewRequest("POST", hs.URL+"/_pibase/auth/logout", nil)
		req.AddCookie(cookie)
		resp, _ := hs.Client().Do(req)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("logout: want 200 got %d", resp.StatusCode)
		}
		// Reuse same cookie — should be 401 now
		code, _ := callAs(cookie, "GET", "/_pibase/projects", "")
		if code != 401 {
			t.Fatalf("want 401 after logout got %d", code)
		}
	})

	// I10: GET /projects with account session returns only owned projects.
	t.Run("I10_account_list_filtered", func(t *testing.T) {
		// Alice creates one, operator creates one
		code, body := callAs(alice, "POST", "/_pibase/projects", `{"name":"alice filter test"}`)
		if code != 201 {
			t.Fatalf("want 201 got %d", code)
		}
		var aliceP map[string]any
		json.Unmarshal(body, &aliceP)
		aliceProjID := aliceP["id"].(string)
		defer control.DeleteProject(ctx, adminURL, aliceProjID)

		code2, body2 := callOp("POST", "/_pibase/projects", `{"name":"op filter test"}`)
		if code2 != 201 {
			t.Fatalf("want 201 got %d", code2)
		}
		var opP map[string]any
		json.Unmarshal(body2, &opP)
		opProjID := opP["id"].(string)
		defer control.DeleteProject(ctx, adminURL, opProjID)

		_, aliceList := callAs(alice, "GET", "/_pibase/projects", "")
		if !bytes.Contains(aliceList, []byte(aliceProjID)) {
			t.Fatal("alice list missing alice's project")
		}
		if bytes.Contains(aliceList, []byte(opProjID)) {
			t.Fatal("alice list should not contain operator's project")
		}
	})

	// I11: Account deletes own project, then GET → 404.
	t.Run("I11_delete_own_then_404", func(t *testing.T) {
		code, body := callAs(alice, "POST", "/_pibase/projects", `{"name":"alice delete me"}`)
		if code != 201 {
			t.Fatalf("want 201 got %d", code)
		}
		var p map[string]any
		json.Unmarshal(body, &p)
		projID := p["id"].(string)

		code2, _ := callAs(alice, "DELETE", "/_pibase/projects/"+projID, `{"confirm":"`+projID+`"}`)
		if code2 != 204 {
			t.Fatalf("delete: want 204 got %d", code2)
		}
		// Subsequent access should 404 (project gone)
		code3, _ := callAs(alice, "POST", "/_pibase/projects/"+projID+"/sql", `{"sql":"SELECT 1"}`)
		if code3 != 404 {
			t.Fatalf("want 404 after delete got %d", code3)
		}
	})

	// I12: Account cannot delete another account's project → 403.
	t.Run("I12_cannot_delete_others_project", func(t *testing.T) {
		code, body := callAs(alice, "POST", "/_pibase/projects", `{"name":"alice keep"}`)
		if code != 201 {
			t.Fatalf("want 201 got %d", code)
		}
		var p map[string]any
		json.Unmarshal(body, &p)
		projID := p["id"].(string)
		defer control.DeleteProject(ctx, adminURL, projID)

		code2, _ := callAs(bob, "DELETE", "/_pibase/projects/"+projID, `{"confirm":"`+projID+`"}`)
		if code2 != 403 {
			t.Fatalf("want 403 got %d", code2)
		}
		// Project still exists
		code3, _ := callAs(alice, "POST", "/_pibase/projects/"+projID+"/sql", `{"sql":"SELECT 1"}`)
		if code3 != 200 {
			t.Fatalf("alice project gone after bob delete attempt: %d", code3)
		}
	})
}

// TestAccountSelfService covers U11–U14 (password change) and I13–I17 (account deletion + API URL).
func TestAccountSelfService(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "t2b-self-pw"
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(adminURL)
	parsed.User = url.UserPassword("authenticator", password)
	parsed.Path = "/_control"
	root := t.TempDir()
	newServer := func(publicURL string) *httptest.Server {
		s, err := New(Config{
			AdminURL:    adminURL,
			DatabaseURL: parsed.String(),
			Token:       "op-token",
			StorageRoot: filepath.Join(root, "f"),
			BackupRoot:  filepath.Join(root, "b"),
			MasterKey:   bytes.Repeat([]byte{7}, 32),
			BackupKey:   bytes.Repeat([]byte{9}, 32),
			Env:         "test",
			PublicURL:   publicURL,
		})
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(s.Handler())
	}

	sfx := fmt.Sprintf("%d", time.Now().UnixNano())

	do := func(hs *httptest.Server, method, path, body string, cookie *http.Cookie) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	readBody := func(r *http.Response) []byte {
		defer r.Body.Close()
		data, _ := io.ReadAll(r.Body)
		return data
	}
	sessionCookie := func(r *http.Response) *http.Cookie {
		for _, c := range r.Cookies() {
			if c.Name == "pibase_session" {
				return c
			}
		}
		return nil
	}
	signup := func(hs *httptest.Server, email, pw string) *http.Cookie {
		t.Helper()
		resp := do(hs, "POST", "/_pibase/auth/signup",
			`{"email":"`+email+`","password":"`+pw+`"}`, nil)
		readBody(resp)
		if resp.StatusCode != 201 {
			t.Fatalf("signup %s: want 201 got %d", email, resp.StatusCode)
		}
		c := sessionCookie(resp)
		if c == nil {
			t.Fatalf("signup %s: no cookie", email)
		}
		return c
	}

	hs := newServer("")
	defer hs.Close()

	// U11: POST /auth/password valid → 204, new password works
	t.Run("U11_password_change_valid", func(t *testing.T) {
		email := "u11" + sfx + "@t2b.test"
		cookie := signup(hs, email, "originalpassword")

		resp := do(hs, "POST", "/_pibase/auth/password",
			`{"current_password":"originalpassword","new_password":"changedpassword"}`, cookie)
		readBody(resp)
		if resp.StatusCode != 204 {
			t.Fatalf("want 204 got %d", resp.StatusCode)
		}
		// New password should work for login
		resp2 := do(hs, "POST", "/_pibase/auth/login",
			`{"email":"`+email+`","password":"changedpassword"}`, nil)
		readBody(resp2)
		if resp2.StatusCode != 200 {
			t.Fatalf("login with new password: want 200 got %d", resp2.StatusCode)
		}
		// Old password should NOT work
		resp3 := do(hs, "POST", "/_pibase/auth/login",
			`{"email":"`+email+`","password":"originalpassword"}`, nil)
		readBody(resp3)
		if resp3.StatusCode != 401 {
			t.Fatalf("login with old password: want 401 got %d", resp3.StatusCode)
		}
	})

	// U12: POST /auth/password wrong current → 401
	t.Run("U12_password_change_wrong_current", func(t *testing.T) {
		cookie := signup(hs, "u12"+sfx+"@t2b.test", "originalpassword")
		resp := do(hs, "POST", "/_pibase/auth/password",
			`{"current_password":"wrongpassword","new_password":"changedpassword"}`, cookie)
		readBody(resp)
		if resp.StatusCode != 401 {
			t.Fatalf("want 401 got %d", resp.StatusCode)
		}
	})

	// U13: POST /auth/password short new → 400
	t.Run("U13_password_change_short_new", func(t *testing.T) {
		cookie := signup(hs, "u13"+sfx+"@t2b.test", "originalpassword")
		resp := do(hs, "POST", "/_pibase/auth/password",
			`{"current_password":"originalpassword","new_password":"short"}`, cookie)
		readBody(resp)
		if resp.StatusCode != 400 {
			t.Fatalf("want 400 got %d", resp.StatusCode)
		}
	})

	// U14: POST /auth/password no session → 401
	t.Run("U14_password_change_no_session", func(t *testing.T) {
		resp := do(hs, "POST", "/_pibase/auth/password",
			`{"current_password":"originalpassword","new_password":"changedpassword"}`, nil)
		readBody(resp)
		if resp.StatusCode != 401 {
			t.Fatalf("want 401 got %d", resp.StatusCode)
		}
	})

	// I13: DELETE /auth/account valid confirm → 204, account + owned projects gone
	t.Run("I13_delete_account_valid", func(t *testing.T) {
		email := "i13" + sfx + "@t2b.test"
		cookie := signup(hs, email, "deletepassword")

		// Create a project as this account
		req, _ := http.NewRequest("POST", hs.URL+"/_pibase/projects",
			strings.NewReader(`{"name":"to delete"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, _ := hs.Client().Do(req)
		var p map[string]any
		json.NewDecoder(resp.Body).Decode(&p)
		resp.Body.Close()
		if resp.StatusCode != 201 {
			t.Fatalf("create project: want 201 got %d", resp.StatusCode)
		}
		projID := p["id"].(string)

		// Delete the account
		resp2 := do(hs, "DELETE", "/_pibase/auth/account",
			`{"confirm":"`+email+`"}`, cookie)
		readBody(resp2)
		if resp2.StatusCode != 204 {
			t.Fatalf("delete account: want 204 got %d", resp2.StatusCode)
		}

		// Session cookie should be cleared
		for _, c := range resp2.Cookies() {
			if c.Name == "pibase_session" && c.MaxAge > 0 {
				t.Fatal("session cookie should be cleared after account deletion")
			}
		}

		// Old session should no longer work
		resp3 := do(hs, "GET", "/_pibase/auth/me", "", cookie)
		readBody(resp3)
		if resp3.StatusCode != 401 {
			t.Fatalf("me after deletion: want 401 got %d", resp3.StatusCode)
		}

		// Project should be gone (operator can confirm via list)
		req2, _ := http.NewRequest("GET", hs.URL+"/_pibase/projects", nil)
		req2.Header.Set("Authorization", "Bearer op-token")
		resp4, _ := hs.Client().Do(req2)
		body4, _ := io.ReadAll(resp4.Body)
		resp4.Body.Close()
		if bytes.Contains(body4, []byte(projID)) {
			t.Fatalf("project still listed after account deletion: %s", body4)
		}
	})

	// I14: DELETE /auth/account wrong confirm → 400
	t.Run("I14_delete_account_wrong_confirm", func(t *testing.T) {
		cookie := signup(hs, "i14"+sfx+"@t2b.test", "deletepassword")
		resp := do(hs, "DELETE", "/_pibase/auth/account",
			`{"confirm":"wrongemail@t2b.test"}`, cookie)
		readBody(resp)
		if resp.StatusCode != 400 {
			t.Fatalf("want 400 got %d", resp.StatusCode)
		}
	})

	// I15: DELETE /auth/account no session → 401
	t.Run("I15_delete_account_no_session", func(t *testing.T) {
		resp := do(hs, "DELETE", "/_pibase/auth/account",
			`{"confirm":"someone@t2b.test"}`, nil)
		readBody(resp)
		if resp.StatusCode != 401 {
			t.Fatalf("want 401 got %d", resp.StatusCode)
		}
	})

	// I16: GET /projects with PublicURL set → api_url present
	t.Run("I16_api_url_present", func(t *testing.T) {
		hs2 := newServer("https://api.example.com")
		defer hs2.Close()
		email := "i16" + sfx + "@t2b.test"
		cookie := signup(hs2, email, "longpassword")
		req, _ := http.NewRequest("POST", hs2.URL+"/_pibase/projects",
			strings.NewReader(`{"name":"api url test"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, _ := hs2.Client().Do(req)
		var p map[string]any
		json.NewDecoder(resp.Body).Decode(&p)
		resp.Body.Close()
		if resp.StatusCode != 201 {
			t.Fatalf("create: want 201 got %d", resp.StatusCode)
		}
		projID := p["id"].(string)
		defer control.DeleteProject(ctx, adminURL, projID)

		req2, _ := http.NewRequest("GET", hs2.URL+"/_pibase/projects", nil)
		req2.AddCookie(cookie)
		resp2, _ := hs2.Client().Do(req2)
		var stats []map[string]any
		json.NewDecoder(resp2.Body).Decode(&stats)
		resp2.Body.Close()
		want := "https://api.example.com/" + projID
		found := false
		for _, s := range stats {
			if s["id"] == projID {
				if s["api_url"] != want {
					t.Fatalf("api_url = %v, want %s", s["api_url"], want)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("project %s not found in listing", projID)
		}
	})

	// I17: GET /projects without PublicURL → api_url absent/empty
	t.Run("I17_api_url_absent", func(t *testing.T) {
		// hs has no PublicURL
		email := "i17" + sfx + "@t2b.test"
		cookie := signup(hs, email, "longpassword")
		req, _ := http.NewRequest("POST", hs.URL+"/_pibase/projects",
			strings.NewReader(`{"name":"no url test"}`))
		req.Header.Set("Content-Type", "application/json")
		req.AddCookie(cookie)
		resp, _ := hs.Client().Do(req)
		var p map[string]any
		json.NewDecoder(resp.Body).Decode(&p)
		resp.Body.Close()
		if resp.StatusCode != 201 {
			t.Fatalf("create: want 201 got %d", resp.StatusCode)
		}
		projID := p["id"].(string)
		defer control.DeleteProject(ctx, adminURL, projID)

		req2, _ := http.NewRequest("GET", hs.URL+"/_pibase/projects", nil)
		req2.AddCookie(cookie)
		resp2, _ := hs.Client().Do(req2)
		body2, _ := io.ReadAll(resp2.Body)
		resp2.Body.Close()
		if bytes.Contains(body2, []byte(`"api_url"`)) {
			t.Fatalf("api_url should be absent when PublicURL is empty: %s", body2)
		}
	})
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
