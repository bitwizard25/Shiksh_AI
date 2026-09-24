package bootstrap

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/config"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database/dbtest"
)

func TestMain(m *testing.M) { os.Exit(dbtest.Main(m)) }

func testApp(t *testing.T) *App {
	t.Helper()
	cfg, err := config.LoadFrom(map[string]string{
		"DATABASE_URL":     "postgres://unused", // the pool below is injected directly
		"JWT_SECRET":       strings.Repeat("k", 32),
		"HTTP_ADDR":        "127.0.0.1:0",
		"ADMIN_ADDR":       "127.0.0.1:0",
		"SHUTDOWN_TIMEOUT": "5s",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &App{cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil)), pool: dbtest.NewPool(t)}
}

func TestAPIHandlerIsFullyWired(t *testing.T) {
	srv := httptest.NewServer(testApp(t).apiHandler())
	defer srv.Close()
	body := `{"email":"asha@example.com","password":"correct horse","display_name":"Asha","terms_accepted":true}`
	resp, err := http.Post(srv.URL+"/v1/auth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("register through the wired stack = %d, want 201", resp.StatusCode)
	}
}

func TestRunShutsDownWhenContextIsCancelled(t *testing.T) {
	a := testApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, []Role{RoleAPI}) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil after graceful shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func TestRunRejectsUnimplementedRole(t *testing.T) {
	if err := testApp(t).Run(context.Background(), []Role{"worker"}); err == nil {
		t.Fatal("Run accepted a role it cannot start")
	}
}
