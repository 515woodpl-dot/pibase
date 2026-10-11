package gateway

import (
	"context"
	"net/http"
	"testing"

	"github.com/515woodpl-dot/pibase/internal/control"
	"github.com/jackc/pgx/v5"
)

func TestStableRoleNamesForProjectMigrations(t *testing.T) {
	e := newAuditEnv(t, nil)
	e.mustSQL(`CREATE TABLE role_notes (id int PRIMARY KEY);
GRANT SELECT,INSERT ON role_notes TO authenticated;
CREATE POLICY role_notes_rw ON role_notes FOR ALL TO authenticated USING (true) WITH CHECK (true);`)
	e.createUser("stable-roles@example.test")
	tokens := e.login("stable-roles@example.test")
	if got := e.status("POST", "/rest/v1/role_notes", tokens.Access, `{"id":1}`); got != http.StatusCreated {
		t.Fatalf("authenticated role policy/grant not applied: %d", got)
	}
	// Bootstrap also restores membership on projects created before this change.
	ctx := context.Background()
	root, err := pgx.Connect(ctx, e.adminURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.Exec(ctx, `REVOKE authenticated FROM `+pgx.Identifier{e.authenticated}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	root.Close(ctx)
	if err := control.Bootstrap(ctx, e.adminURL, "audit-password"); err != nil {
		t.Fatal(err)
	}
	if got := e.status("GET", "/rest/v1/role_notes?select=id", tokens.Access, ""); got != http.StatusOK {
		t.Fatalf("stable role membership not restored: %d", got)
	}
}
