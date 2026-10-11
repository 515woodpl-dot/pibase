package gateway

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/515woodpl-dot/pibase/internal/proxyip"
	"github.com/jackc/pgx/v5"
)

func signInReason(err error) string {
	if err == nil {
		return "success"
	}
	var requestErr requestError
	if errors.As(err, &requestErr) {
		switch requestErr.message {
		case "invalid login credentials":
			return "invalid_credentials"
		case "user is banned":
			return "banned"
		case "email not confirmed":
			return "email_unconfirmed"
		}
	}
	return "internal_error"
}

func (s *Server) recordSignInEvent(p project, r *http.Request, email, userID, reason string) {
	// Use the request context through the separate committed transaction: failed
	// login transactions roll back, but their audit events must survive.
	normalized := strings.ToLower(strings.TrimSpace(email))
	if len(normalized) > 320 {
		normalized = normalized[:320]
	}
	ip := proxyip.ClientIP(r, s.trustedProxies)
	var id any
	if userID != "" {
		id = userID
	}
	err := s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		_, e := tx.Exec(r.Context(), `INSERT INTO auth.sign_in_events(email,user_id,success,reason,client_ip) VALUES($1,$2,$3,$4,$5)`, normalized, id, reason == "success", reason, ip)
		return e
	})
	if err != nil {
		slog.Warn("sign-in audit write failed", "project", p.id, "error", err)
	}
}

func (s *Server) authAdminSignInEvents(w http.ResponseWriter, r *http.Request) {
	p, err := s.serviceProject(r.Context(), r)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 500 {
			writeAPIError(w, httpError(400, "limit must be between 1 and 500"))
			return
		}
	}
	events := make([]map[string]any, 0)
	err = s.adminTx(r.Context(), p, func(tx pgx.Tx) error {
		rows, e := tx.Query(r.Context(), `SELECT id::text,email,user_id::text,success,reason,client_ip,created_at FROM auth.sign_in_events ORDER BY created_at DESC,id DESC LIMIT $1`, limit)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id, email, reason string
			var userID, ip *string
			var success bool
			var created time.Time
			if e := rows.Scan(&id, &email, &userID, &success, &reason, &ip, &created); e != nil {
				return e
			}
			events = append(events, map[string]any{"id": id, "email": email, "user_id": userID, "success": success, "reason": reason, "client_ip": ip, "created_at": created.UTC().Format(time.RFC3339Nano)})
		}
		return rows.Err()
	})
	if err != nil {
		writeAPIError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"events": events})
}
