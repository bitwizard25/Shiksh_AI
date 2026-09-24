package database_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
)

func TestOpenRejectsNonUTF8Database(t *testing.T) {
	url := dbtest.NewDatabaseURL(t, "SQL_ASCII")
	_, err := database.Open(context.Background(), url)
	if err == nil || !strings.Contains(err.Error(), "UTF8") {
		t.Fatalf("Open on a SQL_ASCII database: err = %v, want an error mentioning UTF8", err)
	}
}

func TestTestDatabasesAreUTF8(t *testing.T) {
	pool := dbtest.NewPool(t)
	var enc string
	if err := pool.QueryRow(context.Background(), `SELECT current_setting('server_encoding')`).Scan(&enc); err != nil {
		t.Fatalf("query server_encoding: %v", err)
	}
	if enc != "UTF8" {
		t.Fatalf("server_encoding = %q, want UTF8", enc)
	}
}

func TestMigrationsCreateSchema(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	for _, table := range []string{"users", "refresh_tokens", "password_reset_tokens", "tutoring_sessions", "ws_tickets", "messages"} {
		var exists bool
		if err := pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatalf("check %s: %v", table, err)
		}
		if !exists {
			t.Errorf("table %s was not created", table)
		}
	}
}

func TestMigrateIsIdempotentAndLeavesPoolUsable(t *testing.T) {
	pool := dbtest.NewPool(t)
	ctx := context.Background()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pool unusable after Migrate: %v", err)
	}
}

func TestTxManager(t *testing.T) {
	pool := dbtest.NewPool(t)
	tm := database.NewTxManager(pool)
	ctx := context.Background()
	boom := errors.New("boom")

	insert := func(ctx context.Context, email string) error {
		_, err := database.Conn(ctx, pool).Exec(ctx,
			`INSERT INTO users (email, password_hash, display_name, terms_accepted_at) VALUES ($1, 'h', 'n', now())`, email)
		return err
	}
	count := func(email string) int { // always reads through the pool, i.e. outside any transaction
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE email = $1`, email).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("commits", func(t *testing.T) {
		if err := tm.WithinTx(ctx, func(ctx context.Context) error { return insert(ctx, "commit@example.com") }); err != nil {
			t.Fatal(err)
		}
		if count("commit@example.com") != 1 {
			t.Fatal("committed row missing")
		}
	})
	t.Run("uses the transaction", func(t *testing.T) {
		err := tm.WithinTx(ctx, func(ctx context.Context) error {
			if err := insert(ctx, "isolated@example.com"); err != nil {
				return err
			}
			if n := count("isolated@example.com"); n != 0 {
				t.Errorf("uncommitted row visible outside the transaction (%d)", n)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
	t.Run("rolls back on error", func(t *testing.T) {
		err := tm.WithinTx(ctx, func(ctx context.Context) error {
			if err := insert(ctx, "rollback@example.com"); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) || count("rollback@example.com") != 0 {
			t.Fatalf("err = %v, rows = %d; want boom and 0", err, count("rollback@example.com"))
		}
	})
	t.Run("nested call joins the outer transaction", func(t *testing.T) {
		err := tm.WithinTx(ctx, func(ctx context.Context) error {
			if err := tm.WithinTx(ctx, func(ctx context.Context) error { return insert(ctx, "inner@example.com") }); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) || count("inner@example.com") != 0 {
			t.Fatalf("inner write survived outer rollback: err=%v rows=%d", err, count("inner@example.com"))
		}
	})
	t.Run("Conn without a transaction uses the pool", func(t *testing.T) {
		if err := insert(ctx, "direct@example.com"); err != nil || count("direct@example.com") != 1 {
			t.Fatalf("direct insert: err=%v rows=%d", err, count("direct@example.com"))
		}
	})
}
