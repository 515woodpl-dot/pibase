package auth

import (
	"golang.org/x/crypto/bcrypt"
	"testing"
)

func TestImportedBcryptPassword(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("imported secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidImportedPasswordHash(string(hash)) || !VerifyPassword("imported secret", string(hash)) {
		t.Fatal("valid bcrypt hash refused")
	}
	if VerifyPassword("wrong", string(hash)) {
		t.Fatal("wrong bcrypt password accepted")
	}
	if ValidImportedPasswordHash("$2b$10$invalid") {
		t.Fatal("invalid bcrypt hash accepted")
	}
}

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
