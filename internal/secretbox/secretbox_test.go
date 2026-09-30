package secretbox

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"testing"
)

func TestEncryptUsesNonceAndRoundTrips(t *testing.T) {
	key := bytes.Repeat([]byte{1}, 32)
	plaintext := []byte("secret")
	a, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encrypt(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("ciphertexts should differ")
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	got, err := gcm.Open(nil, a[:gcm.NonceSize()], a[gcm.NonceSize():], nil)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("decrypt: %q, %v", got, err)
	}
	got, err = Decrypt(key, a)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("Decrypt: %q, %v", got, err)
	}
	if _, err := Decrypt(key, []byte("short")); err == nil {
		t.Fatal("Decrypt accepted truncated ciphertext")
	}
}
