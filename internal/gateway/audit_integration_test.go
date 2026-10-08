package gateway

// Regression tests for the Visual Edit integration audit (8 October 2026):
// logout must end a session's access tokens, supabase-js upserts must work
// without onConflict, projects must be able to narrow storage writes and
// create buckets from migrations, and oversized bodies must say why.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5"
)

type auditEnv struct {
	t                            *testing.T
	adminURL                     string
	master                       []byte
	p                            control.Project
	s                            *Server
	h                            *httptest.Server
	anon, authenticated, service string
	admin                        string
}

func newAuditEnv(t *testing.T, env map[string]string) *auditEnv {
	t.Helper()
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "audit-password"
	master := bytes.Repeat([]byte{11}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, adminURL, "audit", master)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupGatewayProject(t, adminURL, p) })
	t.Setenv("PIBASE_STORAGE_ROOT", t.TempDir())
	t.Setenv("PIBASE_SCHEDULER_DISABLED", "true")
	for k, v := range env {
		t.Setenv(k, v)
	}
	s, err := New(ctx, databaseURL(t, adminURL, "_control", "authenticator", password), master, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := httptest.NewServer(s.Handler())
	t.Cleanup(h.Close)
	e := &auditEnv{t: t, adminURL: adminURL, master: master, p: p, s: s, h: h}
	ctl, err := pgx.Connect(ctx, databaseURL(t, adminURL, "_control", parsedUsername(t, adminURL), urlPassword(t, adminURL)))
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close(ctx)
	if err := ctl.QueryRow(ctx, `SELECT anon_role,authenticated_role,service_role,admin_role FROM control.projects WHERE id=$1`, p.ID).
		Scan(&e.anon, &e.authenticated, &e.service, &e.admin); err != nil {
		t.Fatal(err)
	}
	return e
}

// sql runs statements in the project database as the project's admin role,
// the role customer migrations use.
func (e *auditEnv) sql(statement string) error {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL(e.t, e.adminURL, e.p.Database, parsedUsername(e.t, e.adminURL), urlPassword(e.t, e.adminURL)))
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{e.admin}.Sanitize()); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, statement); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (e *auditEnv) mustSQL(statement string) {
	e.t.Helper()
	if err := e.sql(statement); err != nil {
		e.t.Fatalf("%s: %v", statement, err)
	}
}

func (e *auditEnv) createUser(email string) string {
	e.t.Helper()
	created := requestAuthJSON(e.t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/admin/users", e.p.ServiceKey, `{"email":"`+email+`","password":"audit user password","email_confirm":true}`, http.StatusCreated)
	var out struct {
		ID   string `json:"id"`
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	_ = json.Unmarshal(created, &out)
	if out.ID == "" {
		out.ID = out.User.ID
	}
	if out.ID == "" {
		e.t.Fatalf("admin create user: %s", created)
	}
	return out.ID
}

type auditTokens struct{ Access, Refresh string }

func (e *auditEnv) login(email string) auditTokens {
	e.t.Helper()
	body := requestAuthJSON(e.t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/token?grant_type=password", e.p.AnonKey, `{"email":"`+email+`","password":"audit user password"}`, http.StatusOK)
	return parseTokens(e.t, body)
}

func (e *auditEnv) refresh(refresh string, want int) auditTokens {
	e.t.Helper()
	body := requestAuthJSON(e.t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/token?grant_type=refresh_token", e.p.AnonKey, `{"refresh_token":"`+refresh+`"}`, want)
	if want != http.StatusOK {
		return auditTokens{}
	}
	return parseTokens(e.t, body)
}

func parseTokens(t *testing.T, body []byte) auditTokens {
	t.Helper()
	var out struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Access == "" || out.Refresh == "" {
		t.Fatalf("token response: %s", body)
	}
	return auditTokens{out.Access, out.Refresh}
}

func tokenSession(t *testing.T, token string) string {
	t.Helper()
	parts := strings.Split(token, ".")
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	return sessionIDFromClaims(payload)
}

// status sends a request and returns only the status code.
func (e *auditEnv) status(method, path, bearer, body string) int {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.h.URL+path, strings.NewReader(body))
	req.Header.Set("apikey", e.p.AnonKey)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.h.Client().Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestAuditLogoutEndsTheSessionsAccessTokens(t *testing.T) {
	e := newAuditEnv(t, nil)
	e.mustSQL(`CREATE TABLE notes (id int PRIMARY KEY, body text);
CREATE POLICY notes_rw ON notes FOR ALL TO ` + pgx.Identifier{e.authenticated}.Sanitize() + ` USING (true) WITH CHECK (true);`)
	userID := e.createUser("audit-session@example.test")
	first := e.login("audit-session@example.test")
	other := e.login("audit-session@example.test") // a second device

	if got := e.status("POST", "/rest/v1/notes", first.Access, `{"id":1,"body":"hello"}`); got != http.StatusCreated {
		t.Fatalf("insert with a live session: %d", got)
	}
	// Refreshing keeps the session ID, and the access token from before the
	// refresh keeps working: another tab may still be using it.
	refreshed := e.refresh(first.Refresh, http.StatusOK)
	if tokenSession(t, refreshed.Access) == "" || tokenSession(t, refreshed.Access) != tokenSession(t, first.Access) {
		t.Fatalf("refresh changed the session: %s -> %s", tokenSession(t, first.Access), tokenSession(t, refreshed.Access))
	}
	if got := e.status("GET", "/rest/v1/notes?select=id", first.Access, ""); got != http.StatusOK {
		t.Fatalf("pre-refresh token after refresh: %d", got)
	}
	// A second tab refreshing with the same token moments later gets its own
	// token in the same session instead of ending everything.
	sibling := e.refresh(first.Refresh, http.StatusOK)
	if tokenSession(t, sibling.Access) != tokenSession(t, first.Access) {
		t.Fatal("concurrent refresh started a different session")
	}
	if got := e.status("GET", "/auth/v1/user", refreshed.Access, ""); got != http.StatusOK {
		t.Fatalf("session after concurrent refresh: %d", got)
	}

	// A realtime connection of this session is closed by logout.
	header := http.Header{"Authorization": {"Bearer " + refreshed.Access}}
	wsURL := "ws" + strings.TrimPrefix(e.h.URL, "http") + "/realtime/v1/websocket?apikey=" + e.p.AnonKey
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, header)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if got := e.status("POST", "/auth/v1/logout", refreshed.Access, "{}"); got != http.StatusNoContent {
		t.Fatalf("logout: %d", got)
	}
	for name, token := range map[string]string{"original": first.Access, "refreshed": refreshed.Access, "sibling": sibling.Access} {
		if got := e.status("PATCH", "/rest/v1/notes?id=eq.1", token, `{"body":"after logout"}`); got != http.StatusUnauthorized {
			t.Errorf("%s token: data write after logout = %d, want 401", name, got)
		}
		if got := e.status("POST", "/rest/v1/rpc/now", token, `{}`); got != http.StatusUnauthorized {
			t.Errorf("%s token: function call after logout = %d, want 401", name, got)
		}
		if got := e.status("GET", "/auth/v1/user", token, ""); got != http.StatusUnauthorized {
			t.Errorf("%s token: user after logout = %d, want 401", name, got)
		}
		if got := e.status("POST", "/storage/v1/object/list/anything", token, `{"prefix":""}`); got != http.StatusUnauthorized {
			t.Errorf("%s token: storage after logout = %d, want 401", name, got)
		}
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err == nil {
		t.Fatal("realtime connection stayed open after logout")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("realtime connection was not closed by logout")
	}
	// The ended session cannot be refreshed back to life, and using its
	// tokens does not end the user's other sessions.
	e.refresh(refreshed.Refresh, http.StatusBadRequest)
	e.refresh(sibling.Refresh, http.StatusBadRequest)
	if got := e.status("GET", "/rest/v1/notes?select=id", other.Access, ""); got != http.StatusOK {
		t.Fatalf("other device after logout: %d", got)
	}
	e.refresh(other.Refresh, http.StatusOK)

	// A token minted by the project's own server (no session_id) is not tied
	// to a sign-in session and keeps working until it expires.
	custom, err := keys.SignJWT(gatewayJWTSecret(t, e.adminURL, e.p.ID, e.master), map[string]any{
		"iss": "pibase", "aud": "authenticated", "role": "authenticated", "sub": userID,
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.status("GET", "/rest/v1/notes?select=id", custom, ""); got != http.StatusOK {
		t.Fatalf("server-minted token: %d", got)
	}
	// A session_id that is not a UUID is refused rather than queried.
	forged, _ := keys.SignJWT(gatewayJWTSecret(t, e.adminURL, e.p.ID, e.master), map[string]any{
		"iss": "pibase", "aud": "authenticated", "role": "authenticated", "sub": userID, "session_id": "x' OR '1'='1",
		"iat": time.Now().Unix(), "exp": time.Now().Add(time.Minute).Unix(),
	})
	if got := e.status("GET", "/rest/v1/notes?select=id", forged, ""); got != http.StatusUnauthorized {
		t.Fatalf("malformed session_id: %d", got)
	}
}

func TestAuditDeletedUserTokensStopWorking(t *testing.T) {
	e := newAuditEnv(t, nil)
	id := e.createUser("audit-deleted@example.test")
	tokens := e.login("audit-deleted@example.test")
	if got := e.status("GET", "/auth/v1/user", tokens.Access, ""); got != http.StatusOK {
		t.Fatalf("before delete: %d", got)
	}
	requestAuthJSON(t, e.h.Client(), http.MethodDelete, e.h.URL+"/auth/v1/admin/users/"+id, e.p.ServiceKey, "", http.StatusNoContent)
	if got := e.status("GET", "/auth/v1/user", tokens.Access, ""); got != http.StatusUnauthorized {
		t.Fatalf("after delete: %d", got)
	}
}

func TestAuditLateRefreshReuseIsStillTreatedAsTheft(t *testing.T) {
	e := newAuditEnv(t, map[string]string{"PIBASE_REFRESH_REUSE_SECONDS": "1"})
	e.createUser("audit-replay@example.test")
	stolen := e.login("audit-replay@example.test")
	legit := e.refresh(stolen.Refresh, http.StatusOK)
	time.Sleep(1500 * time.Millisecond)
	e.refresh(stolen.Refresh, http.StatusBadRequest)
	// Every session of the user is ended, including the legitimate one.
	if got := e.status("GET", "/auth/v1/user", legit.Access, ""); got != http.StatusUnauthorized {
		t.Fatalf("legitimate session after a late replay: %d", got)
	}
	e.refresh(legit.Refresh, http.StatusBadRequest)
}

func TestAuditUpsertWorksLikeSupabaseJS(t *testing.T) {
	e := newAuditEnv(t, nil)
	roles := pgx.Identifier{e.authenticated}.Sanitize() + ", " + pgx.Identifier{e.service}.Sanitize()
	e.mustSQL(`CREATE TABLE items (id int PRIMARY KEY, title text, note text);
CREATE POLICY items_rw ON items FOR ALL TO ` + roles + ` USING (true) WITH CHECK (true);
CREATE TABLE pairs (a int, b int, v text, PRIMARY KEY (b, a));
CREATE POLICY pairs_rw ON pairs FOR ALL TO ` + roles + ` USING (true) WITH CHECK (true);
CREATE TABLE loose (v text);
CREATE POLICY loose_rw ON loose FOR ALL TO ` + roles + ` USING (true) WITH CHECK (true);`)
	c := e.h.Client()
	post := func(path, body, prefer string, want int) []byte {
		t.Helper()
		return requestJSON(t, c, http.MethodPost, e.h.URL+path, e.p.ServiceKey, body, prefer, want)
	}
	read := func(path string) string {
		t.Helper()
		// Re-encode so the comparison ignores the database's JSON spacing.
		var v any
		if err := json.Unmarshal(requestJSON(t, c, http.MethodGet, e.h.URL+path, e.p.ServiceKey, "", "", http.StatusOK), &v); err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(v)
		return string(out)
	}
	// supabase-js upsert() without onConflict: the primary key is the target.
	post("/rest/v1/items", `{"id":1,"title":"first"}`, "resolution=merge-duplicates", http.StatusCreated)
	post("/rest/v1/items", `{"id":1,"title":"second"}`, "resolution=merge-duplicates", http.StatusCreated)
	if got := read("/rest/v1/items?select=id,title&id=eq.1"); got != `[{"id":1,"title":"second"}]` {
		t.Fatalf("after default upsert: %s", got)
	}
	// ignoreDuplicates leaves the existing row alone.
	post("/rest/v1/items", `{"id":1,"title":"ignored"}`, "resolution=ignore-duplicates", http.StatusCreated)
	if got := read("/rest/v1/items?select=title&id=eq.1"); got != `[{"title":"second"}]` {
		t.Fatalf("after ignoreDuplicates: %s", got)
	}
	// Arrays carry columns=; a key missing from one object becomes NULL.
	post(`/rest/v1/items?columns=%22id%22,%22title%22`, `[{"id":2,"title":"two"},{"id":3}]`, "resolution=merge-duplicates", http.StatusCreated)
	if got := read("/rest/v1/items?select=id,title&id=gte.2&order=id"); got != `[{"id":2,"title":"two"},{"id":3,"title":null}]` {
		t.Fatalf("array upsert: %s", got)
	}
	// Keys outside columns= are ignored, as in PostgREST.
	post(`/rest/v1/items?columns=%22id%22`, `[{"id":4,"title":"dropped"}]`, "", http.StatusCreated)
	if got := read("/rest/v1/items?select=title&id=eq.4"); got != `[{"title":null}]` {
		t.Fatalf("columns= filter: %s", got)
	}
	// A composite primary key is used in key order.
	post("/rest/v1/pairs", `{"a":1,"b":2,"v":"x"}`, "resolution=merge-duplicates", http.StatusCreated)
	post("/rest/v1/pairs", `{"a":1,"b":2,"v":"y"}`, "resolution=merge-duplicates", http.StatusCreated)
	if got := read("/rest/v1/pairs?select=v"); got != `[{"v":"y"}]` {
		t.Fatalf("composite key upsert: %s", got)
	}
	// Without a primary key the target must be named.
	body := post("/rest/v1/loose", `{"v":"x"}`, "resolution=merge-duplicates", http.StatusBadRequest)
	if !strings.Contains(string(body), "primary key") {
		t.Fatalf("no-key upsert message: %s", body)
	}
	// An explicit on_conflict still works.
	post("/rest/v1/items?on_conflict=id", `{"id":1,"title":"explicit"}`, "resolution=merge-duplicates", http.StatusCreated)
	if got := read("/rest/v1/items?select=title&id=eq.1"); got != `[{"title":"explicit"}]` {
		t.Fatalf("explicit on_conflict: %s", got)
	}
	// Compare-and-swap for concurrent editors: update only if the version is
	// still the one that was read. A stale writer gets [] back, not a silent
	// overwrite.
	e.mustSQL(`ALTER TABLE items ADD COLUMN version int NOT NULL DEFAULT 1`)
	cas := func(from, to int, title string) string {
		t.Helper()
		body := requestJSON(t, c, http.MethodPatch, e.h.URL+"/rest/v1/items?id=eq.1&version=eq."+strconv.Itoa(from), e.p.ServiceKey,
			`{"title":"`+title+`","version":`+strconv.Itoa(to)+`}`, "return=representation", http.StatusOK)
		var v any
		_ = json.Unmarshal(body, &v)
		out, _ := json.Marshal(v)
		return string(out)
	}
	if got := cas(1, 2, "editor A"); !strings.Contains(got, `"editor A"`) {
		t.Fatalf("first writer: %s", got)
	}
	if got := cas(1, 2, "editor B"); got != `[]` {
		t.Fatalf("stale writer was not told: %s", got)
	}
	if got := read("/rest/v1/items?select=title,version&id=eq.1"); got != `[{"title":"editor A","version":2}]` {
		t.Fatalf("after compare-and-swap: %s", got)
	}

	// Only the public schema is exposed; asking for another one is refused.
	for _, header := range []string{"Accept-Profile", "Content-Profile"} {
		req, _ := http.NewRequest(http.MethodGet, e.h.URL+"/rest/v1/items?select=id", nil)
		req.Header.Set("apikey", e.p.ServiceKey)
		req.Header.Set(header, "auth")
		resp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotAcceptable {
			t.Fatalf("%s: auth -> %d", header, resp.StatusCode)
		}
		req.Header.Set(header, "public")
		resp, err = c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: public -> %d", header, resp.StatusCode)
		}
	}
}

func TestAuditStorageHelpersHookAndMigrationBuckets(t *testing.T) {
	e := newAuditEnv(t, nil)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, databaseURL(t, e.adminURL, e.p.Database, parsedUsername(t, e.adminURL), urlPassword(t, e.adminURL)))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	// Path helpers behave like Supabase's.
	cases := []struct{ name, folders, file, ext string }{
		{"inline/a/b.png", "{inline,a}", "b.png", "png"},
		{"inline/b.png", "{inline}", "b.png", "png"},
		{"inline", "{}", "inline", "inline"},
		{"", "{}", "", ""},
	}
	for _, c := range cases {
		var folders string
		var file, ext *string
		if err := conn.QueryRow(ctx, `SELECT storage.foldername($1)::text, storage.filename($1), storage.extension($1)`, c.name).Scan(&folders, &file, &ext); err != nil {
			t.Fatal(err)
		}
		deref := func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		}
		if folders != c.folders || deref(file) != c.file || deref(ext) != c.ext {
			t.Errorf("%q: folders=%s file=%q ext=%q", c.name, folders, deref(file), deref(ext))
		}
	}

	// auth.jwt() and auth.role() read the request's verified claims, so
	// Supabase-style policies work unchanged.
	var role, sub string
	if err := conn.QueryRow(ctx, `SELECT set_config('request.jwt.claims', '{"role":"authenticated","sub":"11111111-1111-4111-8111-111111111111"}', false),
  auth.role(), auth.jwt() ->> 'sub'`).Scan(new(string), &role, &sub); err != nil || role != "authenticated" || sub != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("auth.role()=%q auth.jwt()->>'sub'=%q (%v)", role, sub, err)
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('request.jwt.claims', '', false)`); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(ctx, `SELECT coalesce(auth.role(), 'none')`).Scan(&role); err != nil || role != "none" {
		t.Fatalf("auth.role() without claims = %q (%v)", role, err)
	}

	// A migration (project admin role) can create a bucket, but not delete one
	// behind the storage API's back.
	e.mustSQL(`INSERT INTO storage.buckets (id, name, public) VALUES ('media', 'media', true)`)
	e.mustSQL(`DELETE FROM storage.buckets WHERE id = 'media'`)
	var buckets int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM storage.buckets WHERE id='media'`).Scan(&buckets); err != nil || buckets != 1 {
		t.Fatalf("bucket after admin delete: %d (%v)", buckets, err)
	}
	requestAuthJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/storage/v1/bucket", e.p.ServiceKey, `{"id":"other","public":true}`, http.StatusCreated)

	// The project narrows uploads to the media bucket: only listed editors,
	// only under inline/. Other buckets keep the default owner rule.
	auth := pgx.Identifier{e.authenticated}.Sanitize()
	e.mustSQL(`CREATE TABLE editors (user_id uuid PRIMARY KEY);
CREATE FUNCTION public.storage_write_allowed(bucket_id text, name text) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = public AS $$
  SELECT bucket_id <> 'media'
      OR ((storage.foldername(name))[1] = 'inline'
          AND EXISTS (SELECT 1 FROM public.editors WHERE user_id = auth.uid()))
$$;
REVOKE ALL ON FUNCTION public.storage_write_allowed(text, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.storage_write_allowed(text, text) TO ` + auth)
	editor := e.createUser("audit-editor@example.test")
	e.createUser("audit-member@example.test")
	e.mustSQL(`INSERT INTO editors VALUES ('` + editor + `')`)
	ed := e.login("audit-editor@example.test")
	member := e.login("audit-member@example.test")
	upload := func(path, bearer, key string, want int) {
		t.Helper()
		requestStorage(t, e.h.Client(), http.MethodPost, e.h.URL+"/storage/v1/object/"+path, key, bearer, []byte("png"), "image/png", want)
	}
	refused := requestStorage(t, e.h.Client(), http.MethodPost, e.h.URL+"/storage/v1/object/media/inline/member.png", e.p.AnonKey, member.Access, []byte("png"), "image/png", http.StatusForbidden)
	if !strings.Contains(string(refused), "row level security") {
		t.Fatalf("refusal does not say why: %s", refused)
	}
	upload("media/inline/editor.png", ed.Access, e.p.AnonKey, http.StatusOK)
	upload("media/elsewhere/editor.png", ed.Access, e.p.AnonKey, http.StatusForbidden)
	upload("media/inline", ed.Access, e.p.AnonKey, http.StatusForbidden)
	upload("other/member.png", member.Access, e.p.AnonKey, http.StatusOK)
	upload("media/anything/server.png", "", e.p.ServiceKey, http.StatusOK)
	// The hook also guards deletes: once removed from editors, the owner of
	// an object can no longer delete it, although the owner rule would allow it.
	e.mustSQL(`DELETE FROM editors`)
	del := `{"prefixes":["inline/editor.png"]}`
	requestStorage(t, e.h.Client(), http.MethodDelete, e.h.URL+"/storage/v1/object/media", e.p.AnonKey, ed.Access, []byte(del), "application/json", http.StatusNotFound)
	var still int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM storage.objects WHERE bucket_id='media' AND name='inline/editor.png'`).Scan(&still); err != nil || still != 1 {
		t.Fatalf("a removed editor deleted the object: %d (%v)", still, err)
	}
	// A hook that errors refuses the write instead of allowing it.
	e.mustSQL(`CREATE OR REPLACE FUNCTION public.storage_write_allowed(bucket_id text, name text) RETURNS boolean
LANGUAGE plpgsql STABLE AS $$ BEGIN RAISE EXCEPTION 'broken hook'; END $$`)
	broken := requestStorage(t, e.h.Client(), http.MethodPost, e.h.URL+"/storage/v1/object/other/after-broken-hook.png", e.p.AnonKey, member.Access, []byte("png"), "image/png", http.StatusBadRequest)
	if !strings.Contains(string(broken), "broken hook") {
		t.Fatalf("the project's own error message was hidden: %s", broken)
	}
	var created int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM storage.objects WHERE name='after-broken-hook.png'`).Scan(&created); err != nil || created != 0 {
		t.Fatalf("broken hook let a write through: %d (%v)", created, err)
	}
}

func TestAuditBodyLimitIsConfigurableAndExplained(t *testing.T) {
	e := newAuditEnv(t, map[string]string{"PIBASE_MAX_BODY_BYTES": "65536"})
	e.mustSQL(`CREATE TABLE docs (id int PRIMARY KEY, body text);
CREATE POLICY docs_rw ON docs FOR ALL TO ` + pgx.Identifier{e.service}.Sanitize() + ` USING (true) WITH CHECK (true);`)
	small := `{"id":1,"body":"` + strings.Repeat("a", 60000) + `"}`
	requestJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/rest/v1/docs", e.p.ServiceKey, small, "", http.StatusCreated)
	large := `{"id":2,"body":"` + strings.Repeat("a", 70000) + `"}`
	body := requestJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/rest/v1/docs", e.p.ServiceKey, large, "", http.StatusRequestEntityTooLarge)
	if !strings.Contains(string(body), "65536") {
		t.Fatalf("413 does not state the limit: %s", body)
	}
	t.Setenv("PIBASE_MAX_BODY_BYTES", "100")
	if _, err := New(context.Background(), databaseURL(t, e.adminURL, "_control", "authenticator", "audit-password"), e.master, 0, false); err == nil {
		t.Fatal("a 100-byte limit was accepted")
	}
}
