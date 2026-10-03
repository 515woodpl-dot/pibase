package adminsql

import (
	"context"
	"strings"
	"testing"
)

func TestConnectProjectRejectsPrivilegedURLBeforeConnecting(t *testing.T) {
	_, err := connectProject(context.Background(), "postgres://postgres:secret@127.0.0.1:1/postgres", "project")
	if err == nil || !strings.Contains(err.Error(), "authenticator") {
		t.Fatalf("privileged URL error = %v, want authenticator requirement", err)
	}
}

func TestExtensionDDLIsOperatorOnly(t *testing.T) {
	for _, statement := range []string{
		`CREATE EXTENSION hstore`,
		`create/**/extension postgis`,
		`ALTER /* nested /* comment */ ok */ EXTENSION postgis UPDATE`,
		`DROP
EXTENSION vector`,
	} {
		if err := validateNoExtensionDDL(statement); err == nil {
			t.Fatalf("accepted %q", statement)
		}
	}
	for _, statement := range []string{
		`SELECT 'CREATE EXTENSION hstore'`,
		`SELECT $$DROP EXTENSION postgis$$`,
		`-- CREATE EXTENSION hstore
SELECT 1`,
		`CREATE TABLE public.extension (id bigint)`,
	} {
		if err := validateNoExtensionDDL(statement); err != nil {
			t.Fatalf("rejected %q: %v", statement, err)
		}
	}
}
