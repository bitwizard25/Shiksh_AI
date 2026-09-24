// Package database provides the Postgres connection pool, transactions and migrations.
package database

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Open connects to Postgres, retrying for a while so the service can start alongside the
// database. It requests UTF8 client encoding and, once connected, refuses a database whose
// server_encoding isn't UTF8: Shiksha AI stores Indic-script text (Devanagari, Tamil, and
// others), and a non-UTF8 database silently mangles it instead of failing loudly at write time.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.ConnConfig.RuntimeParams["client_encoding"] = "UTF8"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	backoff := 500 * time.Millisecond
	for attempt := 1; ; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			var enc string
			if err := pool.QueryRow(ctx, `SELECT current_setting('server_encoding')`).Scan(&enc); err != nil {
				pool.Close()
				return nil, fmt.Errorf("check database encoding: %w", err)
			}
			if enc != "UTF8" {
				pool.Close()
				return nil, fmt.Errorf("database encoding is %s; Shiksha AI requires a UTF8 database (CREATE DATABASE ... ENCODING 'UTF8')", enc)
			}
			return pool, nil
		}
		if attempt == 10 {
			pool.Close()
			return nil, fmt.Errorf("ping database after %d attempts: %w", attempt, err)
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}
