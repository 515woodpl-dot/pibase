package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/515woodpl-dot/pibase/internal/keys"
)

func TestM3StorageRLSAndSignedURLs(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "storage-integration-password"
	masterKey := bytes.Repeat([]byte{4}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, adminURL, "gateway-m3-storage", masterKey)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, p)
	exerciseM6cUpgrade(t, adminURL, p)
	root := t.TempDir()
	t.Setenv("PIBASE_STORAGE_ROOT", root)
	t.Setenv("PIBASE_STORAGE_MAX_BYTES", "5242880")
	server, err := New(ctx, databaseURL(t, adminURL, "_control", "authenticator", password), masterKey, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()

	requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/storage/v1/bucket", p.ServiceKey, `{"id":"public-files","public":true,"file_size_limit":1024}`, http.StatusCreated)
	requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/storage/v1/bucket", p.ServiceKey, `{"id":"private-files","public":false}`, http.StatusCreated)
	requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/storage/v1/bucket", p.ServiceKey, `{"id":"stream-files","public":true,"file_size_limit":4194304}`, http.StatusCreated)
	secret := gatewayJWTSecret(t, adminURL, p.ID, masterKey)
	userID := "11111111-1111-4111-8111-111111111111"
	userToken, err := keys.SignJWT(secret, map[string]any{"iss": "pibase", "aud": "authenticated", "role": "authenticated", "sub": userID, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	upload := requestStorage(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/storage/v1/object/public-files/hello.txt", p.AnonKey, userToken, []byte("hello storage"), "text/plain", http.StatusOK)
	if !bytes.Contains(upload, []byte(`"FullPath":"public-files/hello.txt"`)) {
		t.Fatalf("upload response: %s", upload)
	}
	response := requestStorage(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/storage/v1/object/public/public-files/hello.txt", "", "", nil, "", http.StatusOK)
	if string(response) != "hello storage" {
		t.Fatalf("public download: %q", response)
	}
	listed := requestAuthJSON(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/storage/v1/object/list/public-files", p.AnonKey, `{"limit":10}`, http.StatusOK)
	if !bytes.Contains(listed, []byte(`"name":"hello.txt"`)) {
		t.Fatalf("list response: %s", listed)
	}
	signed := requestStorage(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/storage/v1/object/sign/public-files/hello.txt", p.AnonKey, userToken, []byte(`{"expiresIn":60}`), "application/json", http.StatusOK)
	var signedResponse struct {
		URL string `json:"signedURL"`
	}
	if err := json.Unmarshal(signed, &signedResponse); err != nil || signedResponse.URL == "" {
		t.Fatalf("signed response: %s", signed)
	}
	requestStorage(t, httpServer.Client(), http.MethodGet, httpServer.URL+signedResponse.URL, "", "", nil, "", http.StatusOK)
	publicHeaders := requestStorageResponse(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/storage/v1/object/public/public-files/hello.txt", "", "", nil, "")
	_, _ = io.Copy(io.Discard, publicHeaders.Body)
	_ = publicHeaders.Body.Close()
	if publicHeaders.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(publicHeaders.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("unsafe download headers: nosniff=%q disposition=%q", publicHeaders.Header.Get("X-Content-Type-Options"), publicHeaders.Header.Get("Content-Disposition"))
	}
	requestStorage(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/storage/v1/object/private-files/secret.txt", p.AnonKey, userToken, []byte("private"), "text/plain", http.StatusOK)
	requestStorage(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/storage/v1/object/private-files/overflow.txt", p.AnonKey, userToken, bytes.Repeat([]byte("x"), 6<<20), "text/plain", http.StatusInsufficientStorage)
	requestStorage(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/storage/v1/object/private-files/secret.txt", p.AnonKey, "", nil, "", http.StatusNotFound)
	requestStorage(t, httpServer.Client(), http.MethodGet, httpServer.URL+"/storage/v1/object/private-files/secret.txt", p.AnonKey, userToken, nil, "", http.StatusOK)
	large := bytes.Repeat([]byte("z"), 2<<20)
	requestStorage(t, httpServer.Client(), http.MethodPost, httpServer.URL+"/storage/v1/object/stream-files/large.bin", p.AnonKey, userToken, large, "application/octet-stream", http.StatusOK)
	requestAuthJSON(t, httpServer.Client(), http.MethodDelete, httpServer.URL+"/storage/v1/object/public-files", p.ServiceKey, `{"prefixes":["hello.txt"]}`, http.StatusOK)
	if _, err := os.Stat(filepath.Join(root, p.ID, "public-files", "hello.txt")); !os.IsNotExist(err) {
		t.Fatalf("object file was not removed: %v", err)
	}
}

func requestStorage(t *testing.T, client *http.Client, method, endpoint, apiKey, bearer string, body []byte, contentType string, want int) []byte {
	t.Helper()
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("apikey", apiKey)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	content := make([]byte, 0)
	content, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, endpoint, resp.StatusCode, want, content)
	}
	return content
}

func requestStorageResponse(t *testing.T, client *http.Client, method, endpoint, apiKey, bearer string, body []byte, contentType string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if apiKey != "" {
		req.Header.Set("apikey", apiKey)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
