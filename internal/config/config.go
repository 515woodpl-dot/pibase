package config

import (
	"fmt"
	"os"
	"strconv"
)

func Required(name string) (string, error) {
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return v, nil
}

func Int(name string, fallback int) (int, error) {
	if os.Getenv(name) == "" {
		return fallback, nil
	}
	v, err := strconv.Atoi(os.Getenv(name))
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	return v, nil
}

// WritableDir checks that this process can create files in dir. A data folder
// owned by another user (for example a fresh Docker volume still owned by the
// image's UID) otherwise passes health checks and fails only on the first
// upload or backup.
func WritableDir(name, dir string) error {
	f, err := os.CreateTemp(dir, ".pibase-write-check-*")
	if err != nil {
		return fmt.Errorf("%s %s is not writable by uid %d gid %d (set PIBASE_UID/PIBASE_GID to the folder's owner, or chown it to them): %w", name, dir, os.Getuid(), os.Getgid(), err)
	}
	f.Close()
	return os.Remove(f.Name())
}
