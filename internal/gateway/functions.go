package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// /functions/v1/<name> forwards to the site's own server-side code (for
// example a Next.js route handler at <functions URL>/<name>), so code written
// for supabase.functions.invoke keeps working without PIBASE running any
// customer code. The gateway checks the caller's key and token first, drops
// any identity headers the caller sent, and adds the verified ones.

const (
	functionsTimeout     = 120 * time.Second
	functionsHeaderLimit = 60 * time.Second
)

var functionSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._~-]{0,127}$`)

var hopByHop = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

var (
	publicFunctionsClient = sync.OnceValue(func() *http.Client {
		return newFunctionsClient(publicTransport(net.DefaultResolver, functionsHeaderLimit))
	})
	privateFunctionsClient = sync.OnceValue(func() *http.Client {
		return newFunctionsClient(&http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSHandshakeTimeout:   5 * time.Second,
			ResponseHeaderTimeout: functionsHeaderLimit,
		})
	})
)

func newFunctionsClient(t *http.Transport) *http.Client {
	// Redirects go back to the caller untouched; the gateway never follows
	// one to an address nobody checked.
	return &http.Client{Transport: t, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (s *Server) functionsTarget(ctx context.Context, projectID string) (string, bool, error) {
	var target *string
	var private bool
	err := s.control.QueryRow(ctx, `SELECT functions_url, functions_private FROM control.project_catalog WHERE id=$1`, projectID).Scan(&target, &private)
	if err != nil || target == nil {
		return "", false, err
	}
	return *target, private, nil
}

// functionPath turns the request's name into escaped path segments, refusing
// anything that could climb out of the functions URL.
func functionPath(name string) (string, bool) {
	segments := strings.Split(name, "/")
	for i, segment := range segments {
		if segment == "." || segment == ".." || !functionSegment.MatchString(segment) {
			return "", false
		}
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/"), true
}

func (s *Server) functionsProxy(w http.ResponseWriter, r *http.Request) {
	who, err := s.authenticate(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if err := s.enforceRate(who); err != nil {
		writeAPIError(w, err)
		return
	}
	defer s.recordRequest(r.Context(), who.project.id)
	name, ok := functionPath(r.PathValue("name"))
	if !ok {
		writeAPIError(w, httpError(http.StatusNotFound, "function not found"))
		return
	}
	target, private, err := s.functionsTarget(r.Context(), who.project.id)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if target == "" {
		writeAPIError(w, httpError(http.StatusNotFound, "this project has no functions address; set one in the project's settings"))
		return
	}
	upstreamURL := target + "/" + name
	if r.URL.RawQuery != "" {
		upstreamURL += "?" + r.URL.RawQuery
	}
	ctx, cancel := context.WithTimeout(r.Context(), functionsTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL, r.Body)
	if err != nil {
		writeAPIError(w, httpError(http.StatusBadGateway, "invalid functions address"))
		return
	}
	req.ContentLength = r.ContentLength
	for key, values := range r.Header {
		canonical := http.CanonicalHeaderKey(key)
		// apikey is the caller's project key and stays here. Identity headers
		// are set below, from the verified token only.
		if hopByHop[canonical] || canonical == "Host" || canonical == "Apikey" || canonical == "Cookie" || canonical == "Content-Length" ||
			strings.HasPrefix(canonical, "X-Pibase-") || strings.HasPrefix(canonical, "X-Forwarded-") || canonical == "Forwarded" || canonical == "X-Real-Ip" {
			continue
		}
		req.Header[canonical] = values
	}
	var claims struct {
		Role string `json:"role"`
		Sub  string `json:"sub"`
	}
	_ = json.Unmarshal(who.claims, &claims)
	role := claims.Role
	if role == "" {
		role = "anon"
	}
	req.Header.Set("X-Pibase-Role", role)
	req.Header.Set("X-Pibase-Project-Id", who.project.id)
	if role == "authenticated" && claims.Sub != "" {
		req.Header.Set("X-Pibase-User-Id", claims.Sub)
	}
	client := publicFunctionsClient()
	if private {
		client = privateFunctionsClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		message := "the project's server did not answer"
		status := http.StatusBadGateway
		var timeout net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
			status = http.StatusGatewayTimeout
			message = "the project's server took too long to answer"
		} else if strings.Contains(err.Error(), "not a public address") || strings.Contains(err.Error(), "non-public address") {
			message = "the functions address is not public; ask the operator to allow a private address"
		}
		writeAPIError(w, httpError(status, message))
		return
	}
	defer resp.Body.Close()
	for key, values := range resp.Header {
		canonical := http.CanonicalHeaderKey(key)
		// CORS is decided by the gateway; cookies belong to the site's own origin.
		if hopByHop[canonical] || canonical == "Set-Cookie" || canonical == "Content-Length" || strings.HasPrefix(canonical, "Access-Control-") {
			continue
		}
		w.Header()[canonical] = values
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			return // io.EOF ends the body; any other error ends it early
		}
	}
}
