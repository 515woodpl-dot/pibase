package gateway

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func cookieMode(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Pibase-Session-Mode")), "cookie")
}

func allowedCookieOrigin(origin string) bool {
	if os.Getenv("PIBASE_ENV") == "production" && !strings.HasPrefix(origin, "https://") {
		return false
	}
	for _, candidate := range strings.Split(os.Getenv("PIBASE_COOKIE_ORIGINS"), ",") {
		if strings.TrimSpace(candidate) == origin {
			return true
		}
	}
	return false
}

func cookieOriginAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	return origin == requestOrigin(r) || allowedCookieOrigin(origin)
}

func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || os.Getenv("PIBASE_ENV") == "production" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func refreshCookieName(p project) string { return "pibase_refresh_" + p.id }

func refreshCookie(r *http.Request, p project) string {
	cookie, err := r.Cookie(refreshCookieName(p))
	if err != nil {
		return ""
	}
	return cookie.Value
}

func refreshCookieFlags(r *http.Request) (bool, http.SameSite) {
	origin, _ := url.Parse(r.Header.Get("Origin"))
	crossOrigin := origin != nil && origin.Host != "" && r.Header.Get("Origin") != requestOrigin(r)
	secure := r.TLS != nil || os.Getenv("PIBASE_ENV") == "production" || crossOrigin
	sameSite := http.SameSiteLaxMode
	if crossOrigin {
		sameSite = http.SameSiteNoneMode
	}
	return secure, sameSite
}

func setRefreshCookie(w http.ResponseWriter, r *http.Request, p project, token string) {
	secure, sameSite := refreshCookieFlags(r)
	http.SetCookie(w, &http.Cookie{Name: refreshCookieName(p), Value: token, Path: "/auth/v1", HttpOnly: true,
		Secure: secure, SameSite: sameSite, MaxAge: 30 * 24 * 60 * 60, Expires: time.Now().Add(30 * 24 * time.Hour)})
}

func clearRefreshCookie(w http.ResponseWriter, r *http.Request, p project) {
	secure, sameSite := refreshCookieFlags(r)
	http.SetCookie(w, &http.Cookie{Name: refreshCookieName(p), Path: "/auth/v1", HttpOnly: true,
		Secure: secure, SameSite: sameSite, MaxAge: -1, Expires: time.Unix(0, 0)})
}
