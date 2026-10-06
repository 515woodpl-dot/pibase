package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/jackc/pgx/v5"
)

func reviewStorageServer(t *testing.T) (*Server, *httptest.Server, control.Project, string, []byte) {
	t.Helper()
	admin := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if admin == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL required")
	}
	ctx := context.Background()
	master := bytes.Repeat([]byte{9}, 32)
	if err := control.Bootstrap(ctx, admin, "review-password"); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, admin, "adversarial-review", master)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupGatewayProject(t, admin, p) })
	root := t.TempDir()
	t.Setenv("PIBASE_STORAGE_ROOT", root)
	t.Setenv("PIBASE_SCHEDULER_DISABLED", "true")
	s, err := New(ctx, databaseURL(t, admin, "_control", "authenticator", "review-password"), master, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	h := httptest.NewServer(s.Handler())
	t.Cleanup(h.Close)
	return s, h, p, root, master
}

func TestReviewBucketTraversal(t *testing.T) {
	s, h, attacker, root, master := reviewStorageServer(t)
	admin := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	victim, err := control.CreateProject(context.Background(), admin, "review-victim", master)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, admin, victim)
	requestAuthJSON(t, h.Client(), "POST", h.URL+"/storage/v1/bucket", victim.ServiceKey, `{"id":"private-files","public":false}`, 201)
	requestStorage(t, h.Client(), "POST", h.URL+"/storage/v1/object/private-files/keep.txt", victim.ServiceKey, "", []byte("victim data"), "text/plain", 200)
	for _, name := range []string{".", "..", "%2e%2e", "%252e%252e"} {
		requestAuthJSON(t, h.Client(), "POST", h.URL+"/storage/v1/bucket", attacker.ServiceKey, `{"id":"`+name+`"}`, 400)
	}
	// Simulate a bucket retained from a vulnerable release.
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("apikey", attacker.ServiceKey)
	p, err := s.serviceProject(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	err = s.storageTransaction(context.Background(), storageServicePrincipal(p), func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO storage.buckets(id,name) VALUES('..','..')`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	deletion := requestStorageResponse(t, h.Client(), "DELETE", h.URL+"/storage/v1/bucket/%2e%2e", attacker.ServiceKey, "", nil, "")
	defer deletion.Body.Close()
	if deletion.StatusCode != 400 {
		t.Fatalf("legacy unsafe bucket delete=%d", deletion.StatusCode)
	}
	data, err := os.ReadFile(filepath.Join(root, victim.ID, "private-files", "keep.txt"))
	if err != nil || string(data) != "victim data" {
		t.Fatalf("victim file changed: %q %v", data, err)
	}
	if _, err := s.storageFile(&p, "..", "keep.txt"); err == nil {
		t.Fatal("unsafe catalog bucket accepted")
	}
	if _, err := s.storageFile(&p, "private-files", "../keep.txt"); err == nil {
		t.Fatal("unsafe catalog object accepted")
	}

}

func TestReviewSlowUploadDoesNotBlockDelete(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		t.Run(map[bool]string{false: "unauthorized", true: "authorized"}[authorized], func(t *testing.T) { reviewSlowUpload(t, authorized) })
	}
}

func reviewSlowUpload(t *testing.T, authorized bool) {
	s, h, p, root, master := reviewStorageServer(t)
	requestAuthJSON(t, h.Client(), "POST", h.URL+"/storage/v1/bucket", p.ServiceKey, `{"id":"public-files","public":true}`, 201)
	requestStorage(t, h.Client(), "POST", h.URL+"/storage/v1/object/public-files/keep.txt", p.ServiceKey, "", []byte("owner data"), "text/plain", 200)
	secret := gatewayJWTSecret(t, os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL"), p.ID, master)
	token, err := keys.SignJWT(secret, map[string]any{"iss": "pibase", "aud": "authenticated", "role": "authenticated", "sub": "22222222-2222-4222-8222-222222222222", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	upload := httptest.NewRequest("POST", "/storage/v1/object/public-files/keep.txt", reader)
	upload.Header.Set("apikey", p.AnonKey)
	upload.Header.Set("Authorization", "Bearer "+token)
	upload.Header.Set("x-upsert", "true")
	if authorized {
		upload.URL.Path = "/storage/v1/object/public-files/slow.txt"
		upload.Header.Set("apikey", p.ServiceKey)
		upload.Header.Del("Authorization")
	}
	uploadResult := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.Handler().ServeHTTP(uploadResult, upload) }()
	deadline := time.Now().Add(2 * time.Second)
	if !authorized {
		select {
		case <-done:
			if uploadResult.Code != 403 {
				t.Fatalf("unauthorized upload=%d %s", uploadResult.Code, uploadResult.Body.String())
			}
		case <-time.After(2 * time.Second):
			writer.Close()
			<-done
			t.Fatal("unauthorized upload read the body")
		}
	}
	for authorized {
		paths, _ := filepath.Glob(filepath.Join(root, p.ID, "public-files", "slow.txt.tmp-*"))
		if len(paths) > 0 {
			break
		}
		if time.Now().After(deadline) {
			writer.Close()
			<-done
			t.Fatal("upload did not reach body read")
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	deletion := httptest.NewRequest("DELETE", "/storage/v1/object/public-files", bytes.NewBufferString(`{"prefixes":["keep.txt"]}`)).WithContext(ctx)
	deletion.Header.Set("apikey", p.ServiceKey)
	deleteResult := httptest.NewRecorder()
	s.Handler().ServeHTTP(deleteResult, deletion)
	writer.Close()
	<-done
	if authorized && uploadResult.Code != 200 {
		t.Fatalf("authorized upload=%d %s", uploadResult.Code, uploadResult.Body.String())
	}
	if deleteResult.Code != http.StatusOK {
		t.Fatalf("unauthorized upload blocked owner delete: delete=%d body=%s; upload eventually=%d", deleteResult.Code, deleteResult.Body.String(), uploadResult.Code)
	}
}

func TestReviewStorageFileValidation(t *testing.T) {
	s := &Server{storageRoot: t.TempDir()}
	p := &project{id: "project-id"}
	for _, name := range []string{"file.txt", "nested/file.txt", "100%.txt", "literal%20space.txt"} {
		path, err := s.storageFile(p, "bucket", name)
		if err != nil || path != filepath.Join(s.storageRoot, p.id, "bucket", name) {
			t.Fatalf("valid name %q: %s %v", name, path, err)
		}
	}
	for _, name := range []string{"..", "../file.txt", "/file.txt", "nested/../../file.txt"} {
		if _, err := s.storageFile(p, "bucket", name); err == nil {
			t.Fatalf("unsafe name accepted: %q", name)
		}
	}
}
