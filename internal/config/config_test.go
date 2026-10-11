package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritableDir(t *testing.T) {
	dir := t.TempDir()
	if err := WritableDir("storage root", dir); err != nil {
		t.Fatalf("writable folder rejected: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("check left %d files behind", len(entries))
	}
	err := WritableDir("storage root", filepath.Join(dir, "missing"))
	if err == nil || !strings.Contains(err.Error(), "storage root") || !strings.Contains(err.Error(), "PIBASE_UID") {
		t.Fatalf("missing folder: got %v", err)
	}
	if os.Getuid() != 0 {
		readOnly := filepath.Join(dir, "ro")
		if err := os.Mkdir(readOnly, 0o500); err != nil {
			t.Fatal(err)
		}
		if err := WritableDir("storage root", readOnly); err == nil {
			t.Fatal("read-only folder accepted")
		}
	}
}
