// Package dbtest provides throwaway, fully migrated Postgres databases for tests.
//
// By default it starts an embedded Postgres, so no Docker is needed. The first run downloads the
// Postgres binaries (~20 MB) into ~/.embedded-postgres-go. Set TEST_DATABASE_URL to use an
// existing server instead; that role needs the CREATEDB privilege.
package dbtest

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
)

var (
	serverURL    string     // maintenance database on the test server
	templateName string     // migrated template cloned for each test
	createMu     sync.Mutex // CREATE DATABASE ... TEMPLATE must not run concurrently
	dbCounter    atomic.Int64
)

// Main starts the database server, builds the migrated template, runs the tests and cleans up.
// Call it from TestMain: func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }
func Main(m *testing.M) int {
	ctx := context.Background()
	stop := func() {}
	if u := os.Getenv("TEST_DATABASE_URL"); u != "" {
		serverURL = u
	} else {
		u, stopEmbedded, err := startEmbedded()
		if err != nil {
			fmt.Fprintln(os.Stderr, "dbtest: start embedded postgres:", err)
			return 1
		}
		serverURL, stop = u, stopEmbedded
	}
	defer stop()

	templateName = fmt.Sprintf("shiksha_tpl_%d", os.Getpid())
	if err := createTemplate(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "dbtest: create template:", err)
		return 1
	}
	defer dropDatabase(ctx, templateName)

	return m.Run()
}

// NewPool returns a pool on a fresh database cloned from the migrated template.
// The database is dropped when the test ends.
func NewPool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("t_%d_%d", os.Getpid(), dbCounter.Add(1))

	createMu.Lock()
	err := execAdmin(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE %s", name, templateName))
	createMu.Unlock()
	if err != nil {
		t.Fatalf("dbtest: create database: %v", err)
	}

	pool, err := database.Open(ctx, databaseURL(name))
	if err != nil {
		t.Fatalf("dbtest: connect: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		dropDatabase(ctx, name)
	})
	return pool
}

// NewDatabaseURL creates an empty database from template0 with the given encoding (and C
// collation, so any encoding is accepted) and returns its connection URL. The database is
// dropped when the test ends. It exists to test database.Open's UTF8 guard against a
// deliberately non-UTF8 database.
func NewDatabaseURL(t testing.TB, encoding string) string {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("t_enc_%d_%d", os.Getpid(), dbCounter.Add(1))

	createMu.Lock()
	err := execAdmin(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE template0 ENCODING '%s' LC_COLLATE 'C' LC_CTYPE 'C'", name, encoding))
	createMu.Unlock()
	if err != nil {
		t.Fatalf("dbtest: create %s database: %v", encoding, err)
	}
	t.Cleanup(func() { dropDatabase(ctx, name) })
	return databaseURL(name)
}

func createTemplate(ctx context.Context) error {
	dropDatabase(ctx, templateName)
	if err := execAdmin(ctx, fmt.Sprintf("CREATE DATABASE %s TEMPLATE template0 ENCODING 'UTF8' LC_COLLATE 'C' LC_CTYPE 'C'", templateName)); err != nil {
		return err
	}
	pool, err := database.Open(ctx, databaseURL(templateName))
	if err != nil {
		return err
	}
	defer pool.Close() // a template must have no open connections when it is cloned
	return database.Migrate(ctx, pool)
}

func dropDatabase(ctx context.Context, name string) {
	_ = execAdmin(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", name))
}

func execAdmin(ctx context.Context, sql string) error {
	conn, err := pgx.Connect(ctx, serverURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, sql)
	return err
}

func databaseURL(name string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		panic(fmt.Sprintf("dbtest: bad server URL: %v", err))
	}
	u.Path = "/" + name
	return u.String()
}

func startEmbedded() (string, func(), error) {
	port, err := freePort()
	if err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "shiksha-pg-")
	if err != nil {
		return "", nil, err
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V17).
		Locale("C").
		Encoding("UTF8").
		Port(uint32(port)).
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		BinariesPath(filepath.Join(dir, "bin")).
		StartTimeout(60 * time.Second).
		Logger(io.Discard))
	if err := pg.Start(); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, err
	}
	u := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/postgres?sslmode=disable", port)
	return u, func() {
		_ = pg.Stop()
		_ = os.RemoveAll(dir)
	}, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
