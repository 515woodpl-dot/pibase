package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/adminsql"
	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/515woodpl-dot/pibase/internal/secretbox"
	"github.com/jackc/pgx/v5"
)

func TestRESTWithRLS(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "integration-auth-password"
	masterKey := bytes.Repeat([]byte{9}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, adminURL, "gateway-integration", masterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, p)

	prefix := "p_" + p.ID
	setup := `CREATE TABLE public.categories (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL UNIQUE
);
CREATE TABLE public.todos (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  category_id bigint REFERENCES public.categories(id),
  user_id text,
  title text NOT NULL UNIQUE,
  visible boolean NOT NULL DEFAULT false
);
ALTER TABLE public.categories ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.todos ENABLE ROW LEVEL SECURITY;
GRANT SELECT ON public.categories TO ` + pgx.Identifier{prefix + "_anon"}.Sanitize() + `;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.categories TO ` + pgx.Identifier{prefix + "_service_role"}.Sanitize() + `;
GRANT SELECT ON public.todos TO ` + pgx.Identifier{prefix + "_anon"}.Sanitize() + `;
GRANT SELECT ON public.todos TO ` + pgx.Identifier{prefix + "_authenticated"}.Sanitize() + `;
GRANT SELECT,INSERT,UPDATE,DELETE ON public.todos TO ` + pgx.Identifier{prefix + "_service_role"}.Sanitize() + `;
GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + pgx.Identifier{prefix + "_service_role"}.Sanitize() + `;
CREATE POLICY anon_categories ON public.categories FOR SELECT TO ` + pgx.Identifier{prefix + "_anon"}.Sanitize() + ` USING (true);
CREATE POLICY service_categories ON public.categories FOR ALL TO ` + pgx.Identifier{prefix + "_service_role"}.Sanitize() + ` USING (true) WITH CHECK (true);
CREATE POLICY anon_visible ON public.todos FOR SELECT TO ` + pgx.Identifier{prefix + "_anon"}.Sanitize() + ` USING (visible);
CREATE POLICY authenticated_owner ON public.todos FOR SELECT TO ` + pgx.Identifier{prefix + "_authenticated"}.Sanitize() + ` USING (user_id=(current_setting('request.jwt.claims',true)::jsonb->>'sub'));
CREATE POLICY service_all ON public.todos FOR ALL TO ` + pgx.Identifier{prefix + "_service_role"}.Sanitize() + ` USING (true) WITH CHECK (true);
CREATE FUNCTION public.add_numbers(a integer,b integer) RETURNS integer LANGUAGE sql IMMUTABLE AS 'SELECT a+b';
REVOKE ALL ON FUNCTION public.add_numbers(integer,integer) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION public.add_numbers(integer,integer) TO ` + pgx.Identifier{prefix + "_anon"}.Sanitize() + `;`
	if err := adminsql.ApplyMigration(ctx, adminURL, p.ID, "0001_rest_fixture", setup); err != nil {
		t.Fatal(err)
	}
	if err := adminsql.ApplyMigration(ctx, adminURL, p.ID, "0001_rest_fixture", setup); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	result, err := adminsql.Run(ctx, adminURL, p.ID, `SELECT current_user AS role`)
	if err != nil || len(result.Rows) != 1 || result.Rows[0]["role"] != prefix+"_admin" {
		t.Fatalf("owner SQL boundary: %+v, %v", result, err)
	}

	authURL := databaseURL(t, adminURL, "_control", "authenticator", password)
	server, err := New(ctx, authURL, masterKey, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/rest/v1/categories", p.ServiceKey,
		`{"name":"work"}`, "return=representation", http.StatusCreated)
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/rest/v1/todos", p.ServiceKey,
		`[{"category_id":1,"user_id":"user-1","title":"private","visible":false},{"category_id":1,"user_id":"user-2","title":"public","visible":true}]`, "return=representation", http.StatusCreated)
	response := requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/rest/v1/todos?select=id,title&order=id.asc", p.AnonKey, "", "count=exact", http.StatusOK)
	var rows []map[string]any
	if err := json.Unmarshal(response, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["title"] != "public" {
		t.Fatalf("RLS response: %s", response)
	}
	jwtSecret := gatewayJWTSecret(t, adminURL, p.ID, masterKey)
	userToken, err := keys.SignJWT(jwtSecret, map[string]any{"iss": "pibase", "aud": "authenticated", "role": "authenticated", "sub": "user-1", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	response = requestAuthenticated(t, httpServer.Client(), httpServer.URL+"/rest/v1/todos?select=title", p.AnonKey, userToken)
	if err := json.Unmarshal(response, &rows); err != nil || len(rows) != 1 || rows[0]["title"] != "private" {
		t.Fatalf("authenticated RLS response: %s, %v", response, err)
	}
	response = requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/rest/v1/todos?title=eq.private", p.ServiceKey, "", "", http.StatusOK)
	if err := json.Unmarshal(response, &rows); err != nil || len(rows) != 1 {
		t.Fatalf("filtered response: %s, %v", response, err)
	}
	requestJSON(t, httpServer.Client(), http.MethodPatch, httpServer.URL+"/rest/v1/todos?title=eq.private", p.ServiceKey,
		`{"visible":true}`, "return=representation", http.StatusOK)
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/rest/v1/todos?on_conflict=title", p.ServiceKey,
		`{"category_id":1,"user_id":"user-2","title":"public","visible":true}`, "resolution=merge-duplicates,return=representation", http.StatusCreated)
	response = requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/rest/v1/todos?order=id.asc", p.AnonKey, "", "", http.StatusOK)
	if err := json.Unmarshal(response, &rows); err != nil || len(rows) != 2 {
		t.Fatalf("updated RLS response: %s, %v", response, err)
	}
	response = requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/rest/v1/todos?select=title,categories(name)&title=eq.public", p.AnonKey, "", "", http.StatusOK)
	var compact bytes.Buffer
	_ = json.Compact(&compact, response)
	if !bytes.Contains(compact.Bytes(), []byte(`"categories":{"name":"work"}`)) {
		t.Fatalf("forward embed response: %s", response)
	}
	response = requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/rest/v1/categories?select=name,todos(title)", p.AnonKey, "", "", http.StatusOK)
	compact.Reset()
	_ = json.Compact(&compact, response)
	if !bytes.Contains(compact.Bytes(), []byte(`"todos":[`)) || !bytes.Contains(compact.Bytes(), []byte(`"title":"public"`)) {
		t.Fatalf("reverse embed response: %s", response)
	}
	requestJSON(t, httpServer.Client(), http.MethodDelete, httpServer.URL+"/rest/v1/todos?title=eq.private", p.ServiceKey, "", "", http.StatusNoContent)
	response = requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/rest/v1/rpc/add_numbers", p.AnonKey,
		`{"a":2,"b":3}`, "", http.StatusOK)
	if string(response) != "5" {
		t.Fatalf("RPC response: %s", response)
	}
	requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/rest/v1/todos?or=(title.eq.public)", p.ServiceKey, "", "", http.StatusOK)
	requestJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/rest/v1/todos", "not-a-key", "", "", http.StatusUnauthorized)

	controlURL := databaseURL(t, adminURL, "_control", parsedUsername(t, adminURL), urlPassword(t, adminURL))
	controlConn, err := pgx.Connect(ctx, controlURL)
	if err != nil {
		t.Fatal(err)
	}
	defer controlConn.Close(ctx)
	var requests int64
	if err := controlConn.QueryRow(ctx, `SELECT requests FROM control.project_usage WHERE project_id=$1 AND day=current_date`, p.ID).Scan(&requests); err != nil || requests < 1 {
		t.Fatalf("usage count: %d, %v", requests, err)
	}
	if _, err := controlConn.Exec(ctx, `UPDATE control.projects SET max_database_bytes=1 WHERE id=$1`, p.ID); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/rest/v1/todos", p.ServiceKey,
		`{"title":"over-quota"}`, "", http.StatusInsufficientStorage)
}

func TestM2AuthAndUID(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "integration-auth-password"
	masterKey := bytes.Repeat([]byte{7}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, adminURL, "gateway-m2-auth", masterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, p)
	prefix := "p_" + p.ID
	fixture := `CREATE TABLE public.private_rows (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, user_id uuid NOT NULL, value text NOT NULL);
ALTER TABLE public.private_rows ENABLE ROW LEVEL SECURITY;
GRANT SELECT ON public.private_rows TO ` + pgx.Identifier{prefix + "_authenticated"}.Sanitize() + `;
CREATE POLICY owner_only ON public.private_rows FOR SELECT TO ` + pgx.Identifier{prefix + "_authenticated"}.Sanitize() + ` USING (auth.uid() = user_id);`
	if err := adminsql.ApplyMigration(ctx, adminURL, p.ID, "0001_m2_auth", fixture); err != nil {
		t.Fatal(err)
	}
	authURL := databaseURL(t, adminURL, "_control", "authenticator", password)
	t.Setenv("PIBASE_AUTH_EXPOSE_TOKENS", "true")
	server, err := New(ctx, authURL, masterKey, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	email := "m2-" + p.ID + "@example.test"
	signup := requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/signup", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)
	var created struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
		Confirmation string `json:"confirmation_token"`
	}
	if err := json.Unmarshal(signup, &created); err != nil || created.User.ID == "" || created.Confirmation == "" {
		t.Fatalf("signup response: %s", signup)
	}
	requestAuthJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/auth/v1/verify?token="+url.QueryEscape(created.Confirmation)+"&type=signup", p.AnonKey, "", http.StatusOK)
	login := requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/token?grant_type=password", p.AnonKey, `{"email":"`+email+`","password":"correct horse battery staple"}`, http.StatusOK)
	var session struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal(login, &session); err != nil || session.Access == "" || session.Refresh == "" {
		t.Fatalf("login response: %s", login)
	}
	rows, err := pgx.Connect(ctx, databaseURL(t, adminURL, p.Database, "postgres", urlPassword(t, adminURL)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = rows.Exec(ctx, `INSERT INTO public.private_rows(user_id,value) VALUES($1,'owned'),('00000000-0000-0000-0000-000000000001','other')`, created.User.ID)
	rows.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	response := requestAuthenticated(t, httpServer.Client(), httpServer.URL+"/rest/v1/private_rows?select=value", p.AnonKey, session.Access)
	var ownedRows []map[string]string
	if err := json.Unmarshal(response, &ownedRows); err != nil || len(ownedRows) != 1 || ownedRows[0]["value"] != "owned" {
		t.Fatalf("auth.uid RLS response: %s", response)
	}
	rotated := requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/token?grant_type=refresh_token", p.AnonKey, `{"refresh_token":"`+session.Refresh+`"}`, http.StatusOK)
	var next struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal(rotated, &next); err != nil || next.Access == "" || next.Refresh == "" {
		t.Fatalf("refresh response: %s", rotated)
	}
	requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/token?grant_type=refresh_token", p.AnonKey, `{"refresh_token":"`+session.Refresh+`"}`, http.StatusBadRequest)
	requestAuthJSONBearer(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/logout", p.AnonKey, next.Access, "{}", http.StatusNoContent)
	requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/token?grant_type=refresh_token", p.AnonKey, `{"refresh_token":"`+next.Refresh+`"}`, http.StatusBadRequest)
	recovery := requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/recover", p.AnonKey, `{"email":"`+email+`"}`, http.StatusOK)
	var reset struct {
		Token string `json:"reset_token"`
	}
	if err := json.Unmarshal(recovery, &reset); err != nil || reset.Token == "" {
		t.Fatalf("recovery response: %s", recovery)
	}
	resetSession := requestAuthJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/auth/v1/verify?token="+url.QueryEscape(reset.Token)+"&type=recovery", p.AnonKey, "", http.StatusOK)
	var resetTokens struct {
		Access string `json:"access_token"`
	}
	if err := json.Unmarshal(resetSession, &resetTokens); err != nil || resetTokens.Access == "" {
		t.Fatalf("recovery verify response: %s", resetSession)
	}
	requestAuthJSONBearer(t, httpServer.Client(), http.MethodPatch, httpServer.URL+"/auth/v1/user", p.AnonKey, resetTokens.Access, `{"password":"new password 123"}`, http.StatusOK)
	admin := requestAuthJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/auth/v1/admin/users", p.ServiceKey, "", http.StatusOK)
	if !bytes.Contains(admin, []byte(email)) {
		t.Fatalf("admin list response: %s", admin)
	}
	adminCreated := requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/auth/v1/admin/users", p.ServiceKey, `{"email":"admin-`+p.ID+`@example.test","password":"admin password 123","email_confirm":true,"ban_duration":"1h"}`, http.StatusCreated)
	var createdAdmin struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(adminCreated, &createdAdmin); err != nil || createdAdmin.User.ID == "" {
		t.Fatalf("admin create response: %s", adminCreated)
	}
	requestAuthJSON(t, httpServer.Client(), http.MethodPut, httpServer.URL+"/auth/v1/admin/users/"+createdAdmin.User.ID, p.ServiceKey, `{"ban_duration":"none","user_metadata":{"admin":true}}`, http.StatusOK)
	requestAuthJSON(t, httpServer.Client(), http.MethodDelete, httpServer.URL+"/auth/v1/admin/users/"+createdAdmin.User.ID, p.ServiceKey, "", http.StatusNoContent)
	requestAuthJSON(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/auth/v1/admin/users", p.AnonKey, "", http.StatusForbidden)
}

func requestAuthJSON(t *testing.T, client *http.Client, method, endpoint, apiKey, body string, want int) []byte {
	return requestAuthJSONBearer(t, client, method, endpoint, apiKey, "", body, want)
}

func requestAuthJSONBearer(t *testing.T, client *http.Client, method, endpoint, apiKey, bearer, body string, want int) []byte {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("apikey", apiKey)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	content, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, endpoint, resp.StatusCode, want, content)
	}
	return content
}

func requestAuthenticated(t *testing.T, client *http.Client, endpoint, apiKey, token string) []byte {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("apikey", apiKey)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated request: status=%d body=%s", response.StatusCode, content)
	}
	return content
}

func gatewayJWTSecret(t *testing.T, adminURL, projectID string, masterKey []byte) []byte {
	t.Helper()
	controlURL := databaseURL(t, adminURL, "_control", parsedUsername(t, adminURL), urlPassword(t, adminURL))
	conn, err := pgx.Connect(context.Background(), controlURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	var encrypted []byte
	if err := conn.QueryRow(context.Background(), `SELECT jwt_secret_encrypted FROM control.projects WHERE id=$1`, projectID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	secret, err := secretbox.Decrypt(masterKey, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func requestJSON(t *testing.T, client *http.Client, method, endpoint, apiKey, body, prefer string, want int) []byte {
	t.Helper()
	request, err := http.NewRequest(method, endpoint, bytes.NewBufferString(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("apikey", apiKey)
	if prefer != "" {
		request.Header.Set("Prefer", prefer)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, _ := io.ReadAll(response.Body)
	if response.StatusCode != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, endpoint, response.StatusCode, want, content)
	}
	return content
}

func databaseURL(t *testing.T, raw, database, username, password string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + database
	parsed.User = url.UserPassword(username, password)
	return parsed.String()
}

func urlPassword(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := parsed.User.Password()
	return password
}

func cleanupGatewayProject(t *testing.T, adminURL string, p control.Project) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close(ctx)
	_, _ = conn.Exec(ctx, `DROP DATABASE IF EXISTS `+pgx.Identifier{p.Database}.Sanitize()+` WITH (FORCE)`)
	for _, suffix := range []string{"_anon", "_authenticated", "_service_role", "_admin"} {
		_, _ = conn.Exec(ctx, `DROP ROLE IF EXISTS `+pgx.Identifier{"p_" + p.ID + suffix}.Sanitize())
	}
	controlURL := databaseURL(t, adminURL, "_control", parsedUsername(t, adminURL), urlPassword(t, adminURL))
	controlConn, err := pgx.Connect(ctx, controlURL)
	if err == nil {
		_, _ = controlConn.Exec(ctx, `DELETE FROM control.projects WHERE id=$1`, p.ID)
		controlConn.Close(ctx)
	}
}

func parsedUsername(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.User.Username()
}
