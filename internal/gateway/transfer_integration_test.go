package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/backup"
	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
)

// A project exported from one server and imported into another (different
// master key, different file folder) keeps working for its existing website
// and users: same key, same signed-in sessions, same password, data,
// policies, files and 2FA.
func TestProjectMovesToAnotherServer(t *testing.T) {
	e := newAuditEnv(t, nil)
	ctx := context.Background()
	oldRoot := os.Getenv("PIBASE_STORAGE_ROOT")

	// What a real project looks like: a table with a policy, a user with a
	// session and 2FA, a stored file and its settings.
	e.mustSQL(`CREATE TABLE public.notes (id bigserial PRIMARY KEY, owner uuid NOT NULL DEFAULT auth.uid() REFERENCES auth.users(id), body text NOT NULL);
CREATE POLICY own_notes ON public.notes FOR ALL USING (auth.uid() = owner) WITH CHECK (auth.uid() = owner);
CREATE FUNCTION public.note_count() RETURNS bigint LANGUAGE sql STABLE AS $$ SELECT count(*) FROM public.notes $$;`)
	userID := e.createUser("mover@example.test")
	tokens := e.login("mover@example.test")
	requestAuthJSONBearer(t, e.h.Client(), http.MethodPost, e.h.URL+"/rest/v1/notes", e.p.AnonKey, tokens.Access, `{"body":"from the old server"}`, http.StatusCreated)
	enrolled := requestAuthJSONBearer(t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/factors", e.p.AnonKey, tokens.Access, `{"factor_type":"totp","friendly_name":"phone"}`, http.StatusOK)
	var factor struct {
		ID   string `json:"id"`
		TOTP struct {
			Secret string `json:"secret"`
		} `json:"totp"`
	}
	if err := json.Unmarshal(enrolled, &factor); err != nil || factor.ID == "" {
		t.Fatalf("enroll: %s", enrolled)
	}
	requestAuthJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/storage/v1/bucket", e.p.ServiceKey, `{"name":"media","public":false}`, http.StatusCreated)
	requestStorage(t, e.h.Client(), http.MethodPost, e.h.URL+"/storage/v1/object/media/docs/a.txt", e.p.ServiceKey, "", []byte("hello from the pi"), "text/plain", http.StatusOK)
	if err := control.SetLimits(ctx, e.adminURL, e.p.ID, 123456789); err != nil {
		t.Fatal(err)
	}
	if _, err := control.SetPublicURL(ctx, e.adminURL, e.p.ID, "https://pibase.client.example"); err != nil {
		t.Fatal(err)
	}

	// Export, then the project disappears from this "server".
	exportFile := filepath.Join(t.TempDir(), "move.pibx")
	f, err := os.Create(exportFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.ExportProject(ctx, e.adminURL, e.p.ID, oldRoot, e.master, "correct horse battery", f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := control.DeleteProject(ctx, e.adminURL, e.p.ID); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(filepath.Join(oldRoot, e.p.ID))
	if raw, _ := os.ReadFile(exportFile); bytes.Contains(raw, []byte("from the old server")) || bytes.Contains(raw, []byte(e.p.ServiceKey)) {
		t.Fatal("export file is not encrypted")
	}

	// The new server: another master key and file folder.
	newMaster := bytes.Repeat([]byte{33}, 32)
	newRoot := t.TempDir()
	if _, err := backup.ImportProject(ctx, e.adminURL, newRoot, newMaster, exportFile, "wrong passphrase!!", backup.ImportOptions{}); !errors.Is(err, backup.ErrBadPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	result, err := backup.ImportProject(ctx, e.adminURL, newRoot, newMaster, exportFile, "correct horse battery", backup.ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProjectID != e.p.ID {
		t.Fatalf("project id changed: %s", result.ProjectID)
	}
	if _, err := backup.ImportProject(ctx, e.adminURL, newRoot, newMaster, exportFile, "correct horse battery", backup.ImportOptions{}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second import: %v", err)
	}
	t.Setenv("PIBASE_STORAGE_ROOT", newRoot)
	s2, err := New(ctx, databaseURL(t, e.adminURL, "_control", "authenticator", "audit-password"), newMaster, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	h2 := httptest.NewServer(s2.Handler())
	defer h2.Close()
	c := h2.Client()

	// The website's key and the user's existing session still work.
	notes := requestAuthJSONBearer(t, c, http.MethodGet, h2.URL+"/rest/v1/notes?select=body", e.p.AnonKey, tokens.Access, "", http.StatusOK)
	if !strings.Contains(string(notes), "from the old server") {
		t.Fatalf("notes after move: %s", notes)
	}
	if anon := requestAuthJSON(t, c, http.MethodGet, h2.URL+"/rest/v1/notes?select=body", e.p.AnonKey, "", http.StatusOK); string(anon) != "[]" {
		t.Fatalf("policy lost: anon sees %s", anon)
	}
	requestAuthJSONBearer(t, c, http.MethodPost, h2.URL+"/rest/v1/notes", e.p.AnonKey, tokens.Access, `{"body":"from the new server"}`, http.StatusCreated)
	if count := requestAuthJSONBearer(t, c, http.MethodPost, h2.URL+"/rest/v1/rpc/note_count", e.p.AnonKey, tokens.Access, `{}`, http.StatusOK); strings.TrimSpace(string(count)) != "2" {
		t.Fatalf("function after move: %s", count)
	}
	refreshed := parseTokens(t, requestAuthJSON(t, c, http.MethodPost, h2.URL+"/auth/v1/token?grant_type=refresh_token", e.p.AnonKey, `{"refresh_token":"`+tokens.Refresh+`"}`, http.StatusOK))
	if refreshed.Access == "" {
		t.Fatal("refresh after move")
	}
	requestAuthJSON(t, c, http.MethodPost, h2.URL+"/auth/v1/token?grant_type=password", e.p.AnonKey, `{"email":"mover@example.test","password":"audit user password"}`, http.StatusOK)
	file := requestStorage(t, c, http.MethodGet, h2.URL+"/storage/v1/object/media/docs/a.txt", e.p.ServiceKey, "", nil, "", http.StatusOK)
	if string(file) != "hello from the pi" {
		t.Fatalf("file after move: %q", file)
	}
	// 2FA works with the new server's key: the original secret still verifies.
	challenge := requestAuthJSONBearer(t, c, http.MethodPost, h2.URL+"/auth/v1/factors/"+factor.ID+"/challenge", e.p.AnonKey, refreshed.Access, `{}`, http.StatusOK)
	var challenged struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(challenge, &challenged)
	code, err := totpCode(factor.TOTP.Secret, time.Now().Unix()/30)
	if err != nil {
		t.Fatal(err)
	}
	requestAuthJSONBearer(t, c, http.MethodPost, h2.URL+"/auth/v1/factors/"+factor.ID+"/verify", e.p.AnonKey, refreshed.Access, `{"challenge_id":"`+challenged.ID+`","code":"`+code+`"}`, http.StatusOK)

	// Settings came along; the temporary restore login is gone.
	ctl, err := pgx.Connect(ctx, databaseURL(t, e.adminURL, "_control", parsedUsername(t, e.adminURL), urlPassword(t, e.adminURL)))
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Close(ctx)
	var limit int64
	var publicURL string
	if err := ctl.QueryRow(ctx, `SELECT max_database_bytes, COALESCE(public_url,'') FROM control.projects WHERE id=$1`, e.p.ID).Scan(&limit, &publicURL); err != nil || limit != 123456789 || publicURL != "https://pibase.client.example" {
		t.Fatalf("settings: %d %q %v", limit, publicURL, err)
	}
	var leftovers int
	if err := ctl.QueryRow(ctx, `SELECT count(*) FROM pg_roles WHERE rolname LIKE 'p\_%\_import'`).Scan(&leftovers); err != nil || leftovers != 0 {
		t.Fatalf("temporary import roles left: %d %v", leftovers, err)
	}
	_ = userID
}

// An import can't take over another project: keys must be signed by the
// export's own secret, and an existing project's keys or ID are refused.
func TestImportCannotImpersonateAnotherProject(t *testing.T) {
	e := newAuditEnv(t, nil)
	ctx := context.Background()
	k, err := control.GetProjectKeys(ctx, e.adminURL, e.p.ID, e.master)
	if err != nil {
		t.Fatal(err)
	}
	otherSecret := bytes.Repeat([]byte{44}, 32)
	// Someone who only knows the victim's public anon key can't sign a
	// matching service key, so the keys don't verify.
	if _, err := control.CreateProjectWithIdentity(ctx, e.adminURL, "evil", e.master, control.ProjectIdentity{
		ID: "abcdef01", JWTSecret: otherSecret, AnonKey: e.p.AnonKey, ServiceKey: e.p.ServiceKey}); err == nil || !strings.Contains(err.Error(), "not signed") {
		t.Fatalf("forged keys accepted: %v", err)
	}
	// Even with the real secret, a second copy beside the original is refused.
	secret := mustDecodeBase64(t, k.JWTSecret)
	if _, err := control.CreateProjectWithIdentity(ctx, e.adminURL, "copy", e.master, control.ProjectIdentity{
		ID: "abcdef02", JWTSecret: secret, AnonKey: k.AnonKey, ServiceKey: k.ServiceKey}); !errors.Is(err, control.ErrDuplicate) {
		t.Fatalf("duplicate keys accepted: %v", err)
	}
	for _, bad := range []string{"../etc", "ABCDEF01", "abc", "abcdef0g"} {
		if _, err := control.CreateProjectWithIdentity(ctx, e.adminURL, "x", e.master, control.ProjectIdentity{ID: bad, JWTSecret: secret, AnonKey: k.AnonKey, ServiceKey: k.ServiceKey}); err == nil {
			t.Fatalf("bad id %q accepted", bad)
		}
	}
}

func mustDecodeBase64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
