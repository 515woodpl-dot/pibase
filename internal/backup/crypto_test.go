package backup

import (
	"bytes"
	"io"
	"testing"
)

func TestEncryptedStreamRoundTripAndTamper(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = byte(i)
	}
	plain := bytes.Repeat([]byte("pibase"), 30000)
	var encrypted bytes.Buffer
	w, err := newEncryptWriter(&encrypted, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := newDecryptReader(bytes.NewReader(encrypted.Bytes()), key)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("round trip mismatch")
	}
	damaged := append([]byte(nil), encrypted.Bytes()...)
	damaged[len(damaged)-20] ^= 1
	r, err = newDecryptReader(bytes.NewReader(damaged), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(r); err == nil {
		t.Fatal("tampered backup accepted")
	}
	appended := append(append([]byte(nil), encrypted.Bytes()...), 0x01)
	r, err = newDecryptReader(bytes.NewReader(appended), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.ReadAll(r); err == nil {
		t.Fatal("backup with appended data accepted")
	}
}
