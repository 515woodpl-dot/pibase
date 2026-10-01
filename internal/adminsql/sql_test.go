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
