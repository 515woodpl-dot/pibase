package keys

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestVerifyJWT(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	token, err := JWT(secret, "anon")
	if err != nil {
		t.Fatal(err)
	}
	claims, err := VerifyJWT(token, secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if claims.Role != "anon" || claims.Issuer != "pibase" {
		t.Fatalf("claims: %+v", claims)
	}
	parts := strings.Split(token, ".")
	parts[2] = base64.RawURLEncoding.EncodeToString([]byte("bad signature"))
	if _, err := VerifyJWT(strings.Join(parts, "."), secret, time.Now()); err == nil {
		t.Fatal("accepted invalid signature")
	}
}

func TestEqualHash(t *testing.T) {
	if !EqualHash("key", Hash("key")) || EqualHash("other", Hash("key")) {
		t.Fatal("hash comparison failed")
	}
}
