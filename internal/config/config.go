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
