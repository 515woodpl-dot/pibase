package control

import "testing"

func TestRedirectAllowed(t *testing.T) {
	a := AuthSettings{SiteURL: "https://app.example.com", RedirectURLs: []string{"https://app.example.com/welcome", "http://localhost:3000/*", "https://preview.example.net/auth/"}}
	for target, want := range map[string]bool{
		"https://app.example.com":                  true,
		"https://app.example.com/":                 true,
		"https://app.example.com/account?tab=1":    true,
		"https://APP.example.com/x":                true,
		"https://app.example.com.evil.net/":        false,
		"https://app.example.com@evil.net/":        false,
		"http://app.example.com/":                  false,
		"https://evil.net/https://app.example.com": false,
		"http://localhost:3000/":                   true,
		"http://localhost:3000/reset?x=1":          true,
		"http://localhost:30001/":                  false,
		"https://preview.example.net/auth/":        true,
		"https://preview.example.net/auth":         true,
		"https://preview.example.net/auth/other":   false,
		"javascript:alert(1)":                      false,
		"//app.example.com/":                       false,
		"":                                         false,
	} {
		if got := a.RedirectAllowed(target); got != want {
			t.Errorf("RedirectAllowed(%q) = %v, want %v", target, got, want)
		}
	}
	// A site URL with a path allows only pages under that path.
	b := AuthSettings{SiteURL: "https://example.com/app"}
	for target, want := range map[string]bool{"https://example.com/app": true, "https://example.com/app/x": true, "https://example.com/apple": false, "https://example.com/": false} {
		if got := b.RedirectAllowed(target); got != want {
			t.Errorf("site with path: RedirectAllowed(%q) = %v, want %v", target, got, want)
		}
	}
	if (AuthSettings{}).RedirectAllowed("https://example.com") {
		t.Error("nothing should be allowed with no site URL and no list")
	}
}

func TestNormalizeAuthSettingsInput(t *testing.T) {
	if got, err := NormalizeSiteURL(" https://app.example.com/ "); err != nil || got != "https://app.example.com" {
		t.Fatalf("site URL = %q, %v", got, err)
	}
	for _, bad := range []string{"app.example.com", "ftp://x.com", "https://user:pw@x.com", "https://x.com/#frag"} {
		if _, err := NormalizeSiteURL(bad); err == nil {
			t.Errorf("site URL %q should be refused", bad)
		}
	}
	got, err := NormalizeRedirectURLs([]string{"", " https://a.com/x ", "https://a.com/x", "http://localhost:3000/*"})
	if err != nil || len(got) != 2 || got[0] != "https://a.com/x" || got[1] != "http://localhost:3000/*" {
		t.Fatalf("redirect URLs = %q, %v", got, err)
	}
	for _, bad := range []string{"https://a.com*", "https://*.a.com/", "a.com/x", "https://a.com/x*y"} {
		if _, err := NormalizeRedirectURLs([]string{bad}); err == nil {
			t.Errorf("redirect URL %q should be refused", bad)
		}
	}
}
