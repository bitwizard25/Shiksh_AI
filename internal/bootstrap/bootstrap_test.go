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

	"github.com/coder/websocket"
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
		"PROVIDERS":        "fake",
	})
	if err != nil {
		t.Fatal(err)
	}
	return &App{cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil)), pool: dbtest.NewPool(t)}
}

// testProviders builds the configured (fake) providers, installs them on a as Run does, and returns them.
func testProviders(t *testing.T, a *App) Providers {
	t.Helper()
	prov, err := BuildProviders(context.Background(), a.cfg.Providers, prometheus.NewRegistry(), nil)
	if err != nil {
		t.Fatal(err)
	}
	a.prov = prov
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

func TestAPIHandlerServesSessions(t *testing.T) {
	a := testApp(t)
	srv := httptest.NewServer(a.apiHandler(testProviders(t, a)))
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
	if err := testApp(t).Run(context.Background(), []Role{"bogus"}); err == nil {
		t.Fatal("Run accepted a role it cannot start")
	}
}

// The realtime role runs on the providers from BuildProviders, so a provider stack that cannot be
// built (here: real providers without keys) stops Run before any role starts.
func TestRealtimeRoleFailsWhenProvidersCannotBeBuilt(t *testing.T) {
	a := testApp(t)
	a.cfg.Providers.Mode = config.ProvidersReal
	err := a.Run(context.Background(), []Role{RoleRealtime})
	if err == nil || !strings.Contains(err.Error(), "build providers") {
		t.Fatalf("Run = %v, want a provider build error", err)
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

// TestRealtimeAndAPIShareOneListener runs the whole stack: register and create a session over REST,
// talk over the WebSocket with fake providers, and end the live session over REST.
func TestRealtimeAndAPIShareOneListener(t *testing.T) {
	a := testApp(t)
	testProviders(t, a)
	a.hub = a.newHub()
	srv := httptest.NewServer(a.httpHandler([]Role{RoleAPI, RoleRealtime}))
	defer srv.Close()

	call := func(method, path, bearer, body string) (int, []byte) {
		req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	_, b := call("POST", "/v1/auth/register", "", `{"email":"asha@example.com","password":"correct horse","display_name":"Asha","terms_accepted":true}`)
	var reg struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.Unmarshal(b, &reg)
	code, b := call("POST", "/v1/sessions", reg.AccessToken, `{"subject":"math","language":"en"}`)
	var created struct {
		Session struct{ ID string }
		WS      struct{ Ticket string }
	}
	_ = json.Unmarshal(b, &created)
	if code != http.StatusCreated {
		t.Fatalf("create session = %d %s", code, b)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/v1/ws?ticket="+created.WS.Ticket,
		&websocket.DialOptions{Subprotocols: []string{"shiksha.v1"}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()
	next := func() map[string]any {
		typ, data, err := c.Read(ctx)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		m := map[string]any{"binary": typ == websocket.MessageBinary}
		if typ == websocket.MessageText {
			_ = json.Unmarshal(data, &m)
		}
		return m
	}
	if m := next(); m["type"] != "session.ready" {
		t.Fatalf("first message = %v", m)
	}
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"text.input","text":"what is a fraction"}`))
	for m := next(); m["type"] != "tutor.done"; m = next() {
	}

	// Ending over REST reaches the live connection through the hub.
	if code, _ := call("POST", "/v1/sessions/"+created.Session.ID+"/end", reg.AccessToken, ""); code != http.StatusAccepted {
		t.Fatalf("end = %d", code)
	}
	var ended bool
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			if websocket.CloseStatus(err) != websocket.StatusNormalClosure {
				t.Fatalf("close = %v (%v)", websocket.CloseStatus(err), err)
			}
			break
		}
		ended = ended || (typ == websocket.MessageText && strings.Contains(string(data), `"session.ended"`))
	}
	if !ended {
		t.Fatal("the live session was not told it ended")
	}
	_, b = call("GET", "/v1/sessions/"+created.Session.ID, reg.AccessToken, "")
	var detail struct {
		Session struct {
			Status    string
			EndReason string `json:"end_reason"`
		}
		Messages []struct{ Role string }
	}
	_ = json.Unmarshal(b, &detail)
	if detail.Session.Status != "ended" || detail.Session.EndReason != "learner" || len(detail.Messages) != 2 {
		t.Fatalf("session after end = %s", b)
	}
}

func TestAPIOnlyHandlerHasNoWebSocketRoute(t *testing.T) {
	a := testApp(t)
	testProviders(t, a)
	srv := httptest.NewServer(a.httpHandler([]Role{RoleAPI}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/ws?ticket=x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 without the realtime role", resp.StatusCode)
	}
}
