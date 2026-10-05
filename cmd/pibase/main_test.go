package main

import "testing"

func TestValidateAdminAddr(t *testing.T) {
	if err := validateAdminAddr("127.0.0.1:18081", false); err != nil {
		t.Fatal(err)
	}
	if err := validateAdminAddr("0.0.0.0:18081", false); err == nil {
		t.Fatal("non-loopback listener accepted")
	}
	if err := validateAdminAddr("0.0.0.0:18081", true); err != nil {
		t.Fatal(err)
	}
}
