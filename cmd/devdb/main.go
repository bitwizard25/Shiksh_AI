// Command devdb runs a local Postgres for development without Docker.
// Data persists in .devdata/pg between runs. Stop it with Ctrl+C.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
	"github.com/jackc/pgx/v5"
)

func main() {
	port := flag.Uint("port", 5433, "port to listen on")
	dir := flag.String("dir", ".devdata/pg", "directory for binaries and data")
	flag.Parse()

	abs, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatalf("devdb: %v", err)
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V17).
		Locale("C").
		Encoding("UTF8").
		Port(uint32(*port)).
		Database("shiksha").
		DataPath(filepath.Join(abs, "data")).
		RuntimePath(filepath.Join(abs, "runtime")).
		BinariesPath(filepath.Join(abs, "bin")).
		StartTimeout(60 * time.Second))
	if err := pg.Start(); err != nil {
		log.Fatalf("devdb: start postgres: %v", err)
	}

	databaseURL := fmt.Sprintf("postgres://postgres:postgres@127.0.0.1:%d/shiksha?sslmode=disable", *port)
	if err := checkUTF8(abs, pg, databaseURL); err != nil {
		log.Fatalf("devdb: %v", err)
	}
	fmt.Printf("Postgres is ready.\nDATABASE_URL=%s\nPress Ctrl+C to stop.\n", databaseURL)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	if err := pg.Stop(); err != nil {
		log.Fatalf("devdb: stop postgres: %v", err)
	}
}

// checkUTF8 guards against reusing an old dev cluster created before Shiksha AI required UTF8:
// such a cluster silently mangles Indic-script text instead of failing loudly at write time.
func checkUTF8(dir string, pg *embeddedpostgres.EmbeddedPostgres, databaseURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		_ = pg.Stop()
		return fmt.Errorf("connect to check database encoding: %w", err)
	}
	defer conn.Close(ctx)

	var enc string
	if err := conn.QueryRow(ctx, `SELECT current_setting('server_encoding')`).Scan(&enc); err != nil {
		_ = pg.Stop()
		return fmt.Errorf("check database encoding: %w", err)
	}
	if enc != "UTF8" {
		_ = pg.Stop()
		return fmt.Errorf("existing dev cluster in %s is %s, not UTF8: stop devdb, delete %s, and start it again", dir, enc, dir)
	}
	return nil
}
