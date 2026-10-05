package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/515woodpl-dot/pibase/internal/adminsql"
	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/515woodpl-dot/pibase/internal/keys"
	"github.com/jackc/pgx/v5"
)

// TestM6ePartATableDrivenRelations uses the same REST path customers use.
func TestM6ePartATableDrivenRelations(t *testing.T) {
	adminURL := os.Getenv("PIBASE_TEST_ADMIN_DATABASE_URL")
	if adminURL == "" {
		t.Skip("PIBASE_TEST_ADMIN_DATABASE_URL is not set")
	}
	ctx := context.Background()
	const password = "m6e-part-a-password"
	master := bytes.Repeat([]byte{31}, 32)
	if err := control.Bootstrap(ctx, adminURL, password); err != nil {
		t.Fatal(err)
	}
	p, err := control.CreateProject(ctx, adminURL, "m6e-part-a-relations", master)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanupGatewayProject(t, adminURL, p)
	authURL := databaseURL(t, adminURL, "_control", "authenticator", password)
	sql := `CREATE TABLE public.plain(id integer, note text); INSERT INTO public.plain VALUES (1,'plain-secret');
CREATE TABLE public.ctas AS SELECT 2 AS id, 'ctas-secret'::text AS note;
SELECT 3 AS id, 'into-secret'::text AS note INTO public.sel_into;
CREATE TABLE public.like_table (LIKE public.plain INCLUDING ALL); INSERT INTO public.like_table VALUES (5,'like-secret');
CREATE TABLE public.part_parent(id integer, note text) PARTITION BY RANGE(id);
CREATE TABLE public.part_child PARTITION OF public.part_parent FOR VALUES FROM (0) TO (10); INSERT INTO public.part_parent VALUES (6,'partition-secret');
CREATE VIEW public.plain_view AS SELECT * FROM public.plain;
CREATE MATERIALIZED VIEW public.plain_mat AS SELECT * FROM public.plain;
CREATE SCHEMA app; CREATE TABLE app.moved(id integer, note text); INSERT INTO app.moved VALUES (4,'moved-secret'); ALTER TABLE app.moved SET SCHEMA public;`
	policySQL := `CREATE TABLE public.policy_rows(user_id uuid, note text);
CREATE POLICY policy_rows_user ON public.policy_rows FOR SELECT TO ` + pgx.Identifier{"p_" + p.ID + "_authenticated"}.Sanitize() + ` USING (user_id=auth.uid());
INSERT INTO public.policy_rows VALUES ('00000000-0000-0000-0000-000000000001','one'),('00000000-0000-0000-0000-000000000002','two');`
	sql += policySQL
	if err := adminsql.ApplyMigration(ctx, authURL, p.ID, "m6e_part_a_relations", sql); err != nil {
		t.Fatal(err)
	}
	server, err := New(ctx, authURL, master, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	hs := httptest.NewServer(server.Handler())
	defer hs.Close()
	jwtSecret := gatewayJWTSecret(t, adminURL, p.ID, master)
	token := m6eToken(t, jwtSecret, "00000000-0000-0000-0000-000000000001")
	relations := []string{"plain", "ctas", "sel_into", "like_table", "part_parent", "part_child", "plain_view", "plain_mat", "moved"}
	for _, relation := range relations {
		t.Run(relation, func(t *testing.T) {
			for _, credential := range []string{p.AnonKey, token} {
				status, body := m6eRequest(t, hs.Client(), http.MethodGet, hs.URL+"/rest/v1/"+relation+"?select=*", p.AnonKey, credential, "")
				if status == http.StatusOK {
					var rows []map[string]any
					if err := json.Unmarshal(body, &rows); err != nil || len(rows) != 0 {
						t.Fatalf("read exposed %s: status=%d body=%s err=%v", relation, status, body, err)
					}
				} else if status < 400 || status >= 500 {
					t.Fatalf("read %s: status=%d body=%s", relation, status, body)
				}
				status, body = m6eRequest(t, hs.Client(), http.MethodPost, hs.URL+"/rest/v1/"+relation, p.AnonKey, credential, `{"id":8,"note":"write"}`)
				if status >= 200 && status < 300 {
					t.Fatalf("write exposed %s: status=%d body=%s", relation, status, body)
				}
			}
		})
	}
	for _, relation := range relations {
		body := requestJSON(t, hs.Client(), http.MethodGet, hs.URL+"/rest/v1/"+relation+"?select=*", p.ServiceKey, "", "", http.StatusOK)
		var rows []map[string]any
		if err := json.Unmarshal(body, &rows); err != nil || len(rows) == 0 {
			t.Fatalf("service read %s: body=%s err=%v", relation, body, err)
		}
	}
	for _, user := range []string{"00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"} {
		body := requestAuthenticated(t, hs.Client(), hs.URL+"/rest/v1/policy_rows?select=note", p.AnonKey, m6eToken(t, jwtSecret, user))
		var rows []map[string]any
		if err := json.Unmarshal(body, &rows); err != nil || len(rows) != 1 {
			t.Fatalf("policy isolation %s: body=%s err=%v", user, body, err)
		}
	}
}

func m6eToken(t *testing.T, secret []byte, user string) string {
	t.Helper()
	token, err := keys.SignJWT(secret, map[string]any{"iss": "pibase", "aud": "authenticated", "role": "authenticated", "sub": user, "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func m6eRequest(t *testing.T, client *http.Client, method, endpoint, apiKey, credential, payload string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(method, endpoint, bytes.NewBufferString(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("apikey", apiKey)
	if credential != apiKey {
		request.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}
