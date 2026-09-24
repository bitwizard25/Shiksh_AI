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
		Version(embeddedpostgres.V16).
		Port(uint32(*port)).
		Database("shiksha").
		DataPath(filepath.Join(abs, "data")).
		RuntimePath(filepath.Join(abs, "runtime")).
		BinariesPath(filepath.Join(abs, "bin")).
		StartTimeout(60 * time.Second))
	if err := pg.Start(); err != nil {
		log.Fatalf("devdb: start postgres: %v", err)
	}
	fmt.Printf("Postgres is ready.\nDATABASE_URL=postgres://postgres:postgres@127.0.0.1:%d/shiksha?sslmode=disable\nPress Ctrl+C to stop.\n", *port)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	if err := pg.Stop(); err != nil {
		log.Fatalf("devdb: stop postgres: %v", err)
	}
}
