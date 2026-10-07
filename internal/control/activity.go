package control

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/515woodpl-dot/pibase/internal/secretbox"
	"github.com/jackc/pgx/v5"
)

func encryptKeys(masterKey []byte, anonKey, serviceKey string) ([]byte, []byte, error) {
	anon, err := secretbox.Encrypt(masterKey, []byte(anonKey))
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt anon key: %w", err)
	}
	service, err := secretbox.Encrypt(masterKey, []byte(serviceKey))
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt service key: %w", err)
	}
	return anon, service, nil
}

// ProjectKeys holds a project's credentials as the dashboard shows them.
// AnonKey and ServiceKey are empty for projects whose keys were issued before
// keys were stored encrypted; rotating the keys once makes them viewable.
type ProjectKeys struct {
	AnonKey    string `json:"anon_key"`
	ServiceKey string `json:"service_key"`
	// JWTSecret is the HS256 signing secret, base64 (standard) encoded. Decode
	// it to bytes before using it to verify user access tokens.
	JWTSecret string `json:"jwt_secret"`
}

func GetProjectKeys(ctx context.Context, adminURL, projectID string, masterKey []byte) (ProjectKeys, error) {
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return ProjectKeys{}, err
	}
	defer conn.Close(ctx)
	var secret, anon, service []byte
	err = conn.QueryRow(ctx, `SELECT jwt_secret_encrypted,anon_key_encrypted,service_key_encrypted FROM control.projects WHERE id=$1`, projectID).Scan(&secret, &anon, &service)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProjectKeys{}, ErrNotFound
	}
	if err != nil {
		return ProjectKeys{}, err
	}
	var out ProjectKeys
	plain, err := secretbox.Decrypt(masterKey, secret)
	if err != nil {
		return ProjectKeys{}, fmt.Errorf("decrypt jwt secret: %w", err)
	}
	out.JWTSecret = base64.StdEncoding.EncodeToString(plain)
	if anon != nil && service != nil {
		a, err := secretbox.Decrypt(masterKey, anon)
		if err != nil {
			return ProjectKeys{}, fmt.Errorf("decrypt anon key: %w", err)
		}
		s, err := secretbox.Decrypt(masterKey, service)
		if err != nil {
			return ProjectKeys{}, fmt.Errorf("decrypt service key: %w", err)
		}
		out.AnonKey, out.ServiceKey = string(a), string(s)
	}
	return out, nil
}

// ActivityServices lists the services the gateway records, in display order.
var ActivityServices = []string{"database", "auth", "storage", "realtime"}

type ActivityPoint struct {
	Time     time.Time `json:"t"`
	Requests int64     `json:"requests"`
	Warnings int64     `json:"warnings"`
	Errors   int64     `json:"errors"`
}

type ServiceActivity struct {
	Service  string          `json:"service"`
	Requests int64           `json:"requests"`
	Warnings int64           `json:"warnings"`
	Errors   int64           `json:"errors"`
	Series   []ActivityPoint `json:"series"`
}

// Activity summarises a project's requests over a window. Warnings are 4xx
// responses and errors are 5xx. SuccessRate is the share of requests that
// were neither, and is nil when there were no requests.
type Activity struct {
	From          time.Time         `json:"from"`
	To            time.Time         `json:"to"`
	BucketSeconds int64             `json:"bucket_seconds"`
	Requests      int64             `json:"requests"`
	Warnings      int64             `json:"warnings"`
	Errors        int64             `json:"errors"`
	SuccessRate   *float64          `json:"success_rate"`
	Total         []ActivityPoint   `json:"total"`
	Services      []ServiceActivity `json:"services"`
}

// ProjectActivity aggregates control.project_activity into fixed buckets that
// end at now. Every bucket is present, so charts have a complete time axis.
func ProjectActivity(ctx context.Context, adminURL, projectID string, window, bucket time.Duration, now time.Time) (Activity, error) {
	if bucket < time.Minute || window < bucket || window%bucket != 0 {
		return Activity{}, fmt.Errorf("invalid activity window")
	}
	conn, err := controlDBConn(ctx, adminURL)
	if err != nil {
		return Activity{}, err
	}
	defer conn.Close(ctx)
	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM control.projects WHERE id=$1)`, projectID).Scan(&exists); err != nil {
		return Activity{}, err
	}
	if !exists {
		return Activity{}, ErrNotFound
	}
	step := int64(bucket / time.Second)
	end := now.UTC().Unix()/step*step + step // exclusive; includes the current bucket
	start := end - int64(window/time.Second)
	n := int(int64(window/time.Second) / step)
	a := Activity{From: time.Unix(start, 0).UTC(), To: time.Unix(end, 0).UTC(), BucketSeconds: step, Total: points(start, step, n)}
	index := map[string]int{}
	for i, name := range ActivityServices {
		index[name] = i
		a.Services = append(a.Services, ServiceActivity{Service: name, Series: points(start, step, n)})
	}
	rows, err := conn.Query(ctx, `SELECT service,
  (floor(extract(epoch FROM minute)/$2)*$2)::bigint AS bucket,
  COALESCE(sum(requests),0)::bigint,
  COALESCE(sum(requests) FILTER (WHERE status_class=4),0)::bigint,
  COALESCE(sum(requests) FILTER (WHERE status_class=5),0)::bigint
FROM control.project_activity
WHERE project_id=$1 AND minute >= to_timestamp($3) AND minute < to_timestamp($4)
GROUP BY 1,2`, projectID, step, start, end)
	if err != nil {
		return Activity{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var service string
		var at, total, warnings, errs int64
		if err := rows.Scan(&service, &at, &total, &warnings, &errs); err != nil {
			return Activity{}, err
		}
		si, ok := index[service]
		i := int((at - start) / step)
		if !ok || i < 0 || i >= n {
			continue
		}
		for _, p := range []*ActivityPoint{&a.Services[si].Series[i], &a.Total[i]} {
			p.Requests += total
			p.Warnings += warnings
			p.Errors += errs
		}
		a.Services[si].Requests += total
		a.Services[si].Warnings += warnings
		a.Services[si].Errors += errs
		a.Requests += total
		a.Warnings += warnings
		a.Errors += errs
	}
	if err := rows.Err(); err != nil {
		return Activity{}, err
	}
	if a.Requests > 0 {
		rate := float64(a.Requests-a.Warnings-a.Errors) / float64(a.Requests)
		a.SuccessRate = &rate
	}
	return a, nil
}

func points(start, step int64, n int) []ActivityPoint {
	out := make([]ActivityPoint, n)
	for i := range out {
		out[i].Time = time.Unix(start+int64(i)*step, 0).UTC()
	}
	return out
}
