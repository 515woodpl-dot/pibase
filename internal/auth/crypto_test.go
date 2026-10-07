package auth

import "testing"

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("correct horse battery staple", hash) {
		t.Fatal("password should verify")
	}
	if VerifyPassword("wrong password", hash) {
		t.Fatal("wrong password verified")
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestRandomToken(t *testing.T) {
	token, digest, err := RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || len(digest) != 32 {
		t.Fatalf("invalid token result: %q/%d", token, len(digest))
	}
	other, otherDigest, err := RandomToken(32)
	if err != nil {
		t.Fatal(err)
	}
	if token == other || string(digest) == string(otherDigest) {
		t.Fatal("random tokens repeated")
	}
}
