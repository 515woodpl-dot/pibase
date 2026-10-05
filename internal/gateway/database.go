package gateway

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (s *Server) projectPool(ctx context.Context, p *project) (*pgxpool.Pool, error) {
	s.poolMu.Lock()
	defer s.poolMu.Unlock()
	if pool := s.pools[p.id]; pool != nil {
		return pool, nil
	}
	config, err := pgxpool.ParseConfig(s.databaseURL)
	if err != nil {
		return nil, err
	}
	config.ConnConfig.Database = p.database
	config.MaxConns = 1
	config.MinConns = 0
	config.MaxConnIdleTime = 1 * time.Second
	config.MaxConnLifetime = 10 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	s.pools[p.id] = pool
	return pool, nil
}

// resetProjectPool drops idle physical connections without removing the
// concurrency-safe pool object. The scheduler scans every project, including
// idle ones, so retaining each scan connection would exhaust PostgreSQL's
// deliberately small max_connections budget. Reset is safe with concurrent
// acquired connections; pgxpool closes those when they are returned.
func (s *Server) resetProjectPool(projectID string) {
	s.poolMu.Lock()
	pool := s.pools[projectID]
	s.poolMu.Unlock()
	if pool != nil {
		pool.Reset()
	}
}

func (s *Server) transaction(ctx context.Context, who principal, fn func(pgx.Tx) error) error {
	select {
	case s.databaseSlots <- struct{}{}:
		defer func() { <-s.databaseSlots }()
	case <-time.After(5 * time.Second):
		return httpError(http.StatusServiceUnavailable, "project connection limit reached; retry shortly")
	case <-ctx.Done():
		return ctx.Err()
	}
	pool, err := s.projectPool(ctx, who.project)
	if err != nil {
		return err
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{who.role}.Sanitize()); err != nil {
		return fmt.Errorf("set request role: %w", err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('request.jwt.claims',$1,true), set_config('statement_timeout','5s',true)`, string(who.claims)); err != nil {
		return fmt.Errorf("set request context: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func enforceDatabaseQuota(ctx context.Context, tx pgx.Tx, maximum int64) error {
	var size int64
	if err := tx.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&size); err != nil {
		return err
	}
	if size >= maximum {
		return httpError(http.StatusInsufficientStorage, "project database size quota exceeded")
	}
	return nil
}
