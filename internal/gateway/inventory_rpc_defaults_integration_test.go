package gateway

import (
	"net/http"
	"strings"
	"testing"
)

func TestRPCOptionalDefaultArguments(t *testing.T) {
	e := newAuditEnv(t, nil)
	e.mustSQL(`CREATE FUNCTION public.add_with_default(a integer,b integer DEFAULT 7) RETURNS integer LANGUAGE sql AS $$ SELECT a+b $$;
GRANT EXECUTE ON FUNCTION public.add_with_default(integer,integer) TO authenticated;`)
	e.createUser("rpc-default@example.test")
	token := e.login("rpc-default@example.test").Access
	for _, tc := range []struct{ body, want string }{{`{"a":3}`, "10"}, {`{"a":3,"b":2}`, "5"}} {
		got := requestAuthJSONBearer(t, e.h.Client(), http.MethodPost, e.h.URL+"/rest/v1/rpc/add_with_default", e.p.AnonKey, token, tc.body, 200)
		if strings.TrimSpace(string(got)) != tc.want {
			t.Fatalf("RPC %s = %s, want %s", tc.body, got, tc.want)
		}
	}
}
