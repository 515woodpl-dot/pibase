package gateway

import (
	_ "embed"
	"net/http"
)

// The browser sign-in client, served so a site can load it straight from its
// gateway: <script src=".../client/v1/pibase-auth.js">. See docs/COMPATIBILITY.md.
//
//go:embed client/pibase-auth.js
var authClientJS []byte

func serveAuthClient(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(authClientJS)
}
