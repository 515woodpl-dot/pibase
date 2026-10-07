package gateway

import (
	"testing"
	"time"
)

func TestTOTPMatchesRFC6238SHA1Vector(t *testing.T) {
	// RFC 6238's first SHA-1 vector is 94287082 with eight digits. The
	// interoperable six-digit form keeps the final six digits.
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	code, err := totpCode(secret, 1)
	if err != nil || code != "287082" {
		t.Fatalf("code=%q err=%v", code, err)
	}
	if counter, ok := verifyTOTP(secret, code, time.Unix(59, 0)); !ok || counter != 1 {
		t.Fatalf("verify counter=%d ok=%v", counter, ok)
	}
	if _, ok := verifyTOTP(secret, "287083", time.Unix(59, 0)); ok {
		t.Fatal("accepted incorrect TOTP")
	}
}

func TestQRSVG(t *testing.T) {
	got := qrSVG([][]bool{{false, true}, {true, false}})
	want := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 2 2" shape-rendering="crispEdges"><rect width="100%" height="100%" fill="white"/><path fill="black" d="M1 0h1v1h-1zM0 1h1v1h-1z"/></svg>`
	if got != want {
		t.Fatalf("SVG=%q", got)
	}
}
