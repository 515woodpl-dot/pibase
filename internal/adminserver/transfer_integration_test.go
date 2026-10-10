package adminserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/control"
)

func TestOwnerExportsAndImportsProject(t *testing.T) {
	hs, _, adminURL := resetTestServer(t, false)
	j := func(v any) string { b, _ := json.Marshal(v); return string(b) }
	signup := func(email string) *http.Cookie {
		status, body, resp := resetCall(t, hs, "POST", "/_pibase/auth/signup", j(map[string]string{"email": email, "password": "owner-password-1", "password_confirm": "owner-password-1", "organization": "Org"}), nil, nil)
		if status != http.StatusCreated {
			t.Fatalf("signup %s: %d %s", email, status, body)
		}
		return sessionCookie(resp)
	}
	stamp := time.Now().UnixNano()
	owner := signup(fmt.Sprintf("mover%d@example.test", stamp))
	stranger := signup(fmt.Sprintf("stranger%d@example.test", stamp))
	t.Cleanup(func() {
		for _, c := range []*http.Cookie{owner, stranger} {
			_, me, _ := resetCall(t, hs, "GET", "/_pibase/auth/me", "", nil, c)
			var a struct{ ID string }
			_ = json.Unmarshal(me, &a)
			if a.ID != "" {
				_ = control.DeleteAccount(context.Background(), adminURL, a.ID)
			}
		}
	})
	status, body, _ := resetCall(t, hs, "POST", "/_pibase/projects", `{"name":"moving shop"}`, nil, owner)
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	var p struct{ ID string }
	_ = json.Unmarshal(body, &p)

	if status, _, _ := resetCall(t, hs, "POST", "/_pibase/projects/"+p.ID+"/export", `{"passphrase":"long enough phrase"}`, nil, stranger); status != http.StatusForbidden {
		t.Fatalf("stranger export: %d", status)
	}
	if status, _, _ := resetCall(t, hs, "POST", "/_pibase/projects/"+p.ID+"/export", `{"passphrase":"short"}`, nil, owner); status != http.StatusBadRequest {
		t.Fatalf("short passphrase: %d", status)
	}
	status, exported, resp := resetCall(t, hs, "POST", "/_pibase/projects/"+p.ID+"/export", `{"passphrase":"long enough phrase"}`, nil, owner)
	if status != http.StatusOK || !bytes.HasPrefix(exported, []byte("PIBASEX1")) || !strings.Contains(resp.Header.Get("Content-Disposition"), ".pibx") {
		t.Fatalf("export: %d %q", status, resp.Header.Get("Content-Disposition"))
	}
	upload := func(cookie *http.Cookie, passphrase string) (int, string) {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("passphrase", passphrase)
		fw, _ := mw.CreateFormFile("file", "move.pibx")
		_, _ = fw.Write(exported)
		_ = mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/_pibase/projects/import", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(cookie)
		r, err := hs.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r.StatusCode, string(b)
	}
	// Still exists here, so importing it again is refused.
	if status, body := upload(owner, "long enough phrase"); status != http.StatusConflict {
		t.Fatalf("import while it exists: %d %s", status, body)
	}
	// Moving it: delete here, then import.
	if status, body, _ := resetCall(t, hs, "DELETE", "/_pibase/projects/"+p.ID, j(map[string]string{"confirm": p.ID}), nil, owner); status/100 != 2 {
		t.Fatalf("delete: %d %s", status, body)
	}
	if status, _ := upload(owner, "wrong passphrase here"); status != http.StatusUnprocessableEntity {
		t.Fatalf("wrong passphrase: %d", status)
	}
	status, body2 := upload(stranger, "long enough phrase")
	if status != http.StatusCreated || !strings.Contains(body2, p.ID) {
		t.Fatalf("import: %d %s", status, body2)
	}
	// It belongs to whoever imported it.
	_, list, _ := resetCall(t, hs, "GET", "/_pibase/projects", "", nil, stranger)
	if !strings.Contains(string(list), p.ID) {
		t.Fatalf("imported project not listed for its importer: %s", list)
	}
	_, list, _ = resetCall(t, hs, "GET", "/_pibase/projects", "", nil, owner)
	if strings.Contains(string(list), p.ID) {
		t.Fatal("imported project listed for the previous owner")
	}
	t.Cleanup(func() { _ = control.DeleteProject(context.Background(), adminURL, p.ID) })
	// The per-account project limit applies to imports too.
	for i := 0; i < 2; i++ {
		resetCall(t, hs, "POST", "/_pibase/projects", fmt.Sprintf(`{"name":"filler %d"}`, i), nil, stranger)
	}
	if status, body := upload(stranger, "long enough phrase"); status != http.StatusForbidden || !strings.Contains(body, "limit") {
		t.Fatalf("import over the limit: %d %s", status, body)
	}
}
