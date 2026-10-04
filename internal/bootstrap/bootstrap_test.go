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

func TestAPIHandlerServesSessions(t *testing.T) {
	srv := httptest.NewServer(testApp(t).apiHandler())
	defer srv.Close()
	post := func(path, auth, body string) *http.Response {
		req, _ := http.NewRequest("POST", srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := post("/v1/auth/register", "", `{"email":"asha@example.com","password":"correct horse","display_name":"Asha","terms_accepted":true}`)
	var reg struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&reg)
	resp.Body.Close()

	resp = post("/v1/sessions", reg.AccessToken, `{"subject":"math"}`)
	defer resp.Body.Close()
	var out struct {
		WS struct{ URL, Ticket string } `json:"ws"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusCreated || out.WS.Ticket == "" || out.WS.URL != "ws://localhost:8080/v1/ws" {
		t.Fatalf("create session = %d %+v", resp.StatusCode, out)
	}
}

func TestWorkerRoleSweepsStaleSessions(t *testing.T) {
	a := testApp(t)
	a.cfg.SweepInterval = 50 * time.Millisecond
	a.cfg.SessionResumeGrace = time.Minute
	ctx := context.Background()
	var userID, sessionID string
	if err := a.pool.QueryRow(ctx, `INSERT INTO users (email, password_hash, display_name, terms_accepted_at)
		VALUES ('w@example.com', 'x', 'W', now()) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := a.pool.QueryRow(ctx, `INSERT INTO tutoring_sessions (user_id, subject, language, created_at)
		VALUES ($1, 'math', 'hi', now() - interval '1 hour') RETURNING id`, userID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx, []Role{RoleWorker}) }()

	deadline := time.Now().Add(10 * time.Second)
	for {
		var status string
		if err := a.pool.QueryRow(ctx, `SELECT status FROM tutoring_sessions WHERE id = $1`, sessionID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "ended" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not end the stale session")
		}
		time.Sleep(25 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
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
	if err := testApp(t).Run(context.Background(), []Role{"realtime"}); err == nil {
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
