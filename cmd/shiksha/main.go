// Command shiksha runs the Shiksha AI backend.
//
//	shiksha serve [--roles=api] [--migrate=true]   run roles (default: every role in this build)
//	shiksha migrate                                 apply database migrations and exit
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/bitwizard25/Shiksh_AI/internal/bootstrap"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
)

const usage = `usage:
  shiksha serve [--roles=api] [--migrate=true]   run roles (default: every role in this build)
  shiksha migrate                                 apply database migrations and exit`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "shiksha:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("missing command\n" + usage)
	}
	switch args[0] {
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		rolesFlag := fs.String("roles", "", "comma-separated roles to run (default: all)")
		migrate := fs.Bool("migrate", true, "apply database migrations before serving")
		if err := fs.Parse(args[1:]); err != nil {
			return fmt.Errorf("%w\n%s", err, usage)
		}
		roles, err := bootstrap.ParseRoles(*rolesFlag)
		if err != nil {
			return err
		}
		return withApp(func(ctx context.Context, app *bootstrap.App) error {
			if *migrate {
				if err := app.Migrate(ctx); err != nil {
					return err
				}
			}
			return app.Run(ctx, roles)
		})
	case "migrate":
		return withApp(func(ctx context.Context, app *bootstrap.App) error { return app.Migrate(ctx) })
	case "help", "-h", "--help":
		fmt.Println(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
}

// withApp loads configuration, sets up logging and signal handling, opens the app and runs fn.
func withApp(fn func(ctx context.Context, app *bootstrap.App) error) error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return fmt.Errorf("load .env: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.SlogLevel()}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	app, err := bootstrap.New(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer app.Close()
	return fn(ctx, app)
}
