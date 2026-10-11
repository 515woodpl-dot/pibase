package gateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestSQLAuthAdminHelpers(t *testing.T) {
	e := newAuditEnv(t, nil)
	e.mustSQL(`CREATE TABLE public.staff_links (user_id uuid PRIMARY KEY,display_name text NOT NULL);
INSERT INTO public.staff_links(user_id,display_name)
VALUES(auth.admin_create_user('sql.crew',NULL,'sql initial password','{"role":"crew"}'::jsonb),'Crew');`)
	ctx := context.Background()
	db, err := pgx.Connect(ctx, databaseURL(t, e.adminURL, e.p.Database, parsedUsername(t, e.adminURL), urlPassword(t, e.adminURL)))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	var id string
	if err := db.QueryRow(ctx, `SELECT user_id::text FROM public.staff_links`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	login := requestAuthJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/token?grant_type=password", e.p.AnonKey,
		`{"username":"sql.crew","password":"sql initial password"}`, 200)
	token := parseTokens(t, login)
	e.mustSQL(`SELECT auth.admin_set_password('` + id + `'::uuid,'sql replacement password')`)
	if got := e.status("GET", "/auth/v1/user", token.Access, ""); got != 401 {
		t.Fatalf("SQL password reset left session active: %d", got)
	}
	requestAuthJSON(t, e.h.Client(), http.MethodPost, e.h.URL+"/auth/v1/token?grant_type=password", e.p.AnonKey,
		`{"username":"sql.crew","password":"sql replacement password"}`, 200)
}
