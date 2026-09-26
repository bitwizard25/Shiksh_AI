package bootstrap

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

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

func testProviders(t *testing.T, a *App) Providers {
	t.Helper()
	prov, err := BuildProviders(context.Background(), a.cfg.Providers, prometheus.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return prov
}

func TestAPIHandlerIsFullyWired(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(a.apiHandler(testProviders(t, a)))
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

	langsResp, err := http.Get(srv.URL + "/v1/languages")
	if err != nil {
		t.Fatal(err)
	}
	defer langsResp.Body.Close()
	var langs []struct {
		Code      string `json:"code"`
		Available bool   `json:"available"`
	}
	if err := json.NewDecoder(langsResp.Body).Decode(&langs); err != nil {
		t.Fatal(err)
	}
	if len(langs) != 9 || !langs[0].Available {
		t.Fatalf("languages through the wired stack = %+v", langs)
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

func TestRunReportsServerStartFailure(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer l.Close()

	a := testApp(t)
	a.cfg.HTTPAddr = l.Addr().String()

	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), []Role{RoleAPI}) }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run returned nil, want error for port conflict")
		}
		if !strings.Contains(err.Error(), "serve "+l.Addr().String()) {
			t.Errorf("Run error = %v, want to mention %q", err, "serve "+l.Addr().String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after port conflict")
	}
}
