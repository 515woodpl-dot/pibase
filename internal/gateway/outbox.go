package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/515woodpl-dot/pibase/internal/secretbox"
	"github.com/jackc/pgx/v5"
)

type outboxItem struct {
	id          int64
	url, method string
	headers     map[string]string
	body        []byte
	attempts    int
}

func (s *Server) runDeliveryWorker(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		cycle, cancel := context.WithTimeout(ctx, 10*time.Second)
		rows, err := s.control.Query(cycle, `SELECT id,database_name,anon_role,authenticated_role,service_role,admin_role,jwt_secret_encrypted,max_database_bytes FROM control.project_catalog`)
		if err == nil {
			for rows.Next() {
				var p project
				var encrypted []byte
				if rows.Scan(&p.id, &p.database, &p.anonRole, &p.authenticatedRole, &p.serviceRole, &p.adminRole, &encrypted, &p.maxDatabaseBytes) == nil {
					p.secret, _ = secretbox.Decrypt(s.masterKey, encrypted)
					s.runCronProject(cycle, &p)
					s.deliverProject(cycle, &p)
					s.resetProjectPool(p.id)
				}
			}
			rows.Close()
		}
		cancel()
	}
}

func (s *Server) runCronProject(ctx context.Context, p *project) {
	who := principal{project: p, role: p.adminRole, claims: []byte(`{"role":"service_role"}`)}
	_ = s.transaction(ctx, who, func(tx pgx.Tx) error {
		var id int64
		var schedule, sql, url, method string
		var headers []byte
		var body []byte
		err := tx.QueryRow(ctx, `SELECT id,schedule,COALESCE(sql,''),COALESCE(webhook_url,''),webhook_method,webhook_headers,webhook_body FROM cron.jobs WHERE enabled AND next_run_at<=now() ORDER BY next_run_at,id FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &schedule, &sql, &url, &method, &headers, &body)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		delay, err := cronInterval(schedule)
		if err != nil {
			_, _ = tx.Exec(ctx, `UPDATE cron.jobs SET enabled=false,last_error=$2 WHERE id=$1`, id, err.Error())
			return nil
		}
		if sql != "" {
			if _, err = tx.Exec(ctx, sql); err != nil {
				_, _ = tx.Exec(ctx, `UPDATE cron.jobs SET last_error=$2,next_run_at=now()+$3::interval WHERE id=$1`, id, err.Error(), delay.String())
				return nil
			}
		} else {
			// Isolate enqueue errors in a savepoint. PostgreSQL marks a
			// transaction aborted after the outbox-cap trigger raises, and the
			// job status must still be writable in the outer transaction.
			enqueue, beginErr := tx.Begin(ctx)
			if beginErr != nil {
				return beginErr
			}
			_, err = enqueue.Exec(ctx, `INSERT INTO cron.webhook_outbox(url,method,headers,body) VALUES($1,$2,$3,$4)`, url, method, headers, body)
			if err != nil {
				_ = enqueue.Rollback(ctx)
				// A full outbox is backpressure, not a reason to spin on this
				// due job every scheduler tick. Keep the error visible and retry
				// on its normal schedule after delivery makes room.
				_, _ = tx.Exec(ctx, `UPDATE cron.jobs SET last_error=$2,next_run_at=now()+$3::interval WHERE id=$1`, id, err.Error(), delay.String())
				return nil
			}
			if err = enqueue.Commit(ctx); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE cron.jobs SET last_run_at=now(),last_error=NULL,next_run_at=now()+$2::interval WHERE id=$1`, id, delay.String())
		return err
	})
}

func cronInterval(value string) (time.Duration, error) {
	if value == "* * * * *" {
		return time.Minute, nil
	}
	var minutes int
	if _, err := fmt.Sscanf(value, "*/%d * * * *", &minutes); err == nil && minutes > 0 && minutes <= 1440 {
		return time.Duration(minutes) * time.Minute, nil
	}
	return 0, fmt.Errorf("supported cron schedules are '* * * * *' and '*/N * * * *'")
}

func (s *Server) deliverProject(ctx context.Context, p *project) {
	var item outboxItem
	who := principal{project: p, role: p.adminRole, claims: []byte(`{"role":"service_role"}`)}
	err := s.transaction(ctx, who, func(tx pgx.Tx) error {
		var raw []byte
		err := tx.QueryRow(ctx, `WITH picked AS (SELECT id FROM cron.webhook_outbox WHERE delivered_at IS NULL AND dead_lettered_at IS NULL AND next_attempt_at<=now() AND (leased_until IS NULL OR leased_until<now()) ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 1) UPDATE cron.webhook_outbox o SET leased_until=now()+interval '1 minute',attempts=attempts+1 FROM picked WHERE o.id=picked.id RETURNING o.id,o.url,o.method,o.headers,o.body,o.attempts`).Scan(&item.id, &item.url, &item.method, &raw, &item.body, &item.attempts)
		if err == pgx.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, &item.headers)
	})
	if err != nil || item.id == 0 {
		return
	}
	_, err = validateWebhookURL(ctx, net.DefaultResolver, item.url)
	if err == nil {
		req, e := http.NewRequestWithContext(ctx, item.method, item.url, bytes.NewReader(item.body))
		err = e
		if e == nil {
			for k, v := range item.headers {
				if safeHeader(k, v) {
					req.Header.Set(k, v)
				}
			}
			req.Header.Set("Idempotency-Key", fmt.Sprintf("pibase-%s-%d", p.id, item.id))
			resp, e := webhookClient(net.DefaultResolver).Do(req)
			err = e
			if resp != nil {
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					err = fmt.Errorf("webhook status %d", resp.StatusCode)
				}
				resp.Body.Close()
			}
		}
	}
	_ = s.transaction(ctx, who, func(tx pgx.Tx) error {
		if err == nil {
			_, e := tx.Exec(ctx, `UPDATE cron.webhook_outbox SET delivered_at=now(),leased_until=NULL,last_error=NULL WHERE id=$1`, item.id)
			return e
		}
		if item.attempts >= maxWebhookAttempts {
			_, e := tx.Exec(ctx, `UPDATE cron.webhook_outbox SET dead_lettered_at=now(),leased_until=NULL,last_error=$2 WHERE id=$1`, item.id, err.Error())
			return e
		}
		_, e := tx.Exec(ctx, `UPDATE cron.webhook_outbox SET leased_until=NULL,next_attempt_at=now()+$2::interval,last_error=$3 WHERE id=$1`, item.id, retryDelay(item.attempts).String(), err.Error())
		return e
	})
}
