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

func TestOwnerSQLMatchesKeywordsNotIdentifierSubstrings(t *testing.T) {
	for _, statement := range []string{
		`SELECT * FROM public.programs`,
		`SELECT copyright, grant_date FROM public."programs"`,
		`SELECT 'grant role' AS note`,
	} {
		if err := validateOwnerSQL(statement); err != nil {
			t.Fatalf("rejected %q: %v", statement, err)
		}
	}
	for _, statement := range []string{`GRANT SELECT ON public.programs TO PUBLIC`, `SET ROLE postgres`, `SET LOCAL ROLE x`, `SET SESSION ROLE x`, `SET session_authorization='x'`, `SET "role"='x'`, `SELECT set_config('role','x',false)`, `RESET ALL`} {
		if err := validateOwnerSQL(statement); err == nil {
			t.Fatalf("accepted %q", statement)
		}
	}
}

func TestSQLFilterComprehensive(t *testing.T) {
	// Statements that MUST be blocked.
	blocked := []string{
		// SET SESSION AUTHORIZATION variants
		`SET SESSION AUTHORIZATION 'postgres'`,
		`SET session authorization 'x'`,
		`SET LOCAL SESSION AUTHORIZATION 'x'`,
		`set local session authorization 'x'`,
		`SET  SESSION  AUTHORIZATION 'x'`,         // extra whitespace (tokenizer normalises)
		`SET/*comment*/SESSION AUTHORIZATION 'x'`, // comment between SET and SESSION
		`SET SESSION/**/AUTHORIZATION 'x'`,        // comment between SESSION and AUTHORIZATION
		`SET LOCAL/**/SESSION AUTHORIZATION 'x'`,  // comment after LOCAL
		// SET session_authorization (single token)
		`SET session_authorization = 'x'`,
		`SET LOCAL session_authorization = 'x'`,
		// SET ROLE / RESET ROLE
		`SET ROLE postgres`,
		`SET LOCAL ROLE anon`,
		`SET SESSION ROLE service_role`,
		`RESET ROLE`,
		`RESET SESSION AUTHORIZATION`,
		`RESET session authorization`,
		// RESET ALL
		`RESET ALL`,
		// GRANT / REVOKE / COPY as keywords
		`GRANT SELECT ON public.t TO PUBLIC`,
		`REVOKE ALL ON public.t FROM PUBLIC`,
		`COPY public.t TO STDOUT`,
		`COPY public.t FROM PROGRAM 'cat /etc/passwd'`,
		// set_config as keyword
		`SELECT set_config('role','x',false)`,
		// CREATE/ALTER/DROP ROLE
		`CREATE ROLE evil`,
		`ALTER ROLE postgres PASSWORD 'x'`,
		`DROP ROLE anon`,
		// CREATE/ALTER/DROP USER
		`CREATE USER evil`,
		`ALTER USER postgres PASSWORD 'x'`,
		`DROP USER anon`,
		// SET "role" (quoted identifier in SET position = keyword)
		`SET "role" = 'x'`,
	}
	for _, stmt := range blocked {
		if err := validateOwnerSQL(stmt); err == nil {
			t.Errorf("should block: %s", stmt)
		}
	}

	// Statements that MUST be allowed.
	allowed := []string{
		// Normal queries
		`SELECT 1`,
		`SELECT * FROM public.users`,
		`INSERT INTO public.logs(msg) VALUES('hello')`,
		// Quoted identifiers that happen to match keywords — allowed when not in SET position
		`SELECT "copy" FROM public.t`,
		`SELECT "grant" FROM public.t`,
		`SELECT "role" FROM public.t`,
		`SELECT "revoke" FROM public.t`,
		// column named grant_date, copyright
		`SELECT grant_date, copyright FROM public.programs`,
		// Keywords inside string literals
		`SELECT 'GRANT ROLE' AS note`,
		`SELECT 'SET SESSION AUTHORIZATION' AS note`,
		`SELECT 'RESET ALL' FROM public.t`,
		// Keywords inside dollar-quoted strings
		`SELECT $$GRANT ALL$$ AS note`,
		`SELECT $tag$SET ROLE postgres$tag$ AS note`,
		// Keywords inside comments
		`-- GRANT ALL\nSELECT 1`,
		`/* SET SESSION AUTHORIZATION 'x' */ SELECT 1`,
		// SET with normal config parameters
		`SET search_path TO public`,
		`SET LOCAL statement_timeout = '5s'`,
		`SET SESSION timezone = 'UTC'`,
		// Table or column named "role" or "user" in normal context
		`CREATE TABLE public.user_roles (role text)`,
		`SELECT role FROM public.user_roles`,
	}
	for _, stmt := range allowed {
		if err := validateOwnerSQL(stmt); err != nil {
			t.Errorf("should allow: %s — got: %v", stmt, err)
		}
	}
}

func TestDisplayValueMakesUUIDAndByteaReadable(t *testing.T) {
	id := [16]byte{0x00, 0xf4, 0x7c, 0x10, 0x34, 0xaa, 0x40, 0x50, 0x82, 0x5b, 0xe8, 0xd8, 0x78, 0x33, 0xde, 0x9e}
	if got := displayValue(id); got != "00f47c10-34aa-4050-825b-e8d87833de9e" {
		t.Fatalf("uuid = %v", got)
	}
	if got := displayValue([]byte{0xde, 0xad}); got != `\xdead` {
		t.Fatalf("bytea = %v", got)
	}
	if got := displayValue("text"); got != "text" {
		t.Fatalf("text = %v", got)
	}
}

func TestSQLFilterReviewBypasses(t *testing.T) {
	blocked := []string{
		`SELECT "set_config"('role','p_x_admin',true)`,
		`SELECT pg_catalog."set_config"('role','x',true)`,
		`SET U&"role" TO x`,
		`SET U&"\0072ole" TO x`,
		`SELECT U&"d\0061t\+000061" FROM t`,
		`DO $x$ BEGIN EXECUTE 'RESET ROLE'; END $x$`,
		`do 'begin null; end'`,
		`DO LANGUAGE plpgsql $$ BEGIN NULL; END $$`,
		`UPDATE pg_settings SET setting='x' WHERE name='role'`,
		`update pg_catalog."pg_settings" set setting='x' where name='role'`,
		`CREATE GROUP g`,
		`ALTER GROUP g ADD USER u`,
		`DROP GROUP g`,
		`create "role" x`,
		`ALTER "user" x PASSWORD 'y'`,
		`SET standard_conforming_strings = off`,
		// E-string backslash escape used to hide set_config inside a fake string.
		`SELECT E'\' ', set_config('role','x',true) --'`,
		`SELECT e'\\' , 1, E'\' ', set_config('role','x',true) --'`,
		// Routine bodies are checked, and dynamic SQL in them is refused.
		`CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN EXECUTE 'RESET ROLE'; END $$`,
		`CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN EXECUTE format('%s','x'); END $$`,
		`CREATE FUNCTION f() RETURNS text LANGUAGE sql AS 'select set_config(''role'',''x'',true)'`,
		`CREATE OR REPLACE PROCEDURE p() LANGUAGE sql AS $b$ GRANT SELECT ON t TO PUBLIC $b$`,
		`CREATE FUNCTION f() RETURNS void LANGUAGE plpgsql AS $$ BEGIN RESET ROLE; END $$`,
		`CREATE FUNCTION f() RETURNS void LANGUAGE sql SET role = x AS 'select 1'`,
		`ALTER FUNCTION f() SET role TO x`,
	}
	for _, stmt := range blocked {
		if err := validateOwnerSQL(stmt); err == nil {
			t.Errorf("should block: %s", stmt)
		}
	}
	allowed := []string{
		`INSERT INTO t(id) VALUES (1) ON CONFLICT (id) DO NOTHING`,
		`INSERT INTO t(id,n) VALUES (1,2) ON CONFLICT (id) DO UPDATE SET n = excluded.n`,
		`SELECT name, setting FROM pg_settings`,
		`SELECT E'it\'s fine', 'grant'`,
		`SELECT U&'caf\00e9'`,
		`SELECT $1::text`,
		`CREATE FUNCTION add(a int, b int) RETURNS int LANGUAGE sql AS $$ SELECT a + b $$`,
		`CREATE FUNCTION touch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.updated_at := now(); RETURN NEW; END $$`,
		`DROP FUNCTION add(int, int)`,
		`SELECT "do" FROM t`,
	}
	for _, stmt := range allowed {
		if err := validateOwnerSQL(stmt); err != nil {
			t.Errorf("should allow: %s — got: %v", stmt, err)
		}
	}
}
