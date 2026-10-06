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

	"github.com/515woodpl-dot/pibase/internal/control"
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
	browse := call(http.MethodGet, project+"/tables/programs/rows", "", http.StatusOK)
	if !bytes.Contains(browse, []byte(`"copy "`)) || !bytes.Contains(browse, []byte(`"first"`)) {
		t.Fatalf("row browse: %s", browse)
	}
	call(http.MethodPut, project+"/tables/programs/rows", `{"key":{"program id":1},"values":{"copy ":"changed","grant_date":"tomorrow"}}`, http.StatusOK)
	call(http.MethodDelete, project+"/tables/programs/rows", `{"key":{"program id":1},"confirm":"wrong"}`, http.StatusBadRequest)
	call(http.MethodDelete, project+"/tables/programs/rows", `{"key":{"program id":1},"confirm":"1"}`, http.StatusOK)
	createdUser := call(http.MethodPost, project+"/users", `{"email":"m7b@example.test","password":"a sufficiently long password","email_confirm":true}`, http.StatusCreated)
	var user map[string]string
	if err := json.Unmarshal(createdUser, &user); err != nil || user["id"] == "" {
		t.Fatalf("user create: %s", createdUser)
	}
	call(http.MethodGet, project+"/users", "", http.StatusOK)
	call(http.MethodDelete, project+"/users/"+user["id"], `{"confirm":"wrong"}`, http.StatusBadRequest)
	call(http.MethodDelete, project+"/users/"+user["id"], `{"confirm":"`+user["id"]+`"}`, http.StatusNoContent)
	applied, _ := json.Marshal(map[string]string{"sql": `SELECT name FROM public.pibase_migrations`})
	if listed := call(http.MethodPost, project+"/sql", string(applied), http.StatusOK); !bytes.Contains(listed, []byte("0001_m7a")) {
		t.Fatalf("applied migration not listed: %s", listed)
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
