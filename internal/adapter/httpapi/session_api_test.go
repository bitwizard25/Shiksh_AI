package httpapi

import (
	"net/http"
	"testing"
)

type sessionBody struct {
	Session struct {
		ID        string `json:"id"`
		Subject   string `json:"subject"`
		Language  string `json:"language"`
		Grade     *int   `json:"grade"`
		Status    string `json:"status"`
		TurnCount int    `json:"turn_count"`
		Summary   string `json:"summary"`
		EndReason string `json:"end_reason"`
	} `json:"session"`
	WS struct {
		URL       string `json:"url"`
		Ticket    string `json:"ticket"`
		ExpiresAt string `json:"expires_at"`
	} `json:"ws"`
}

func (s *testServer) createSession(t *testing.T, bearer string, body map[string]any) sessionBody {
	t.Helper()
	var out sessionBody
	if code := s.do(t, "POST", "/v1/sessions", bearer, body, &out); code != http.StatusCreated {
		t.Fatalf("create session status = %d", code)
	}
	return out
}

func TestSubjectsEndpoint(t *testing.T) {
	s := newTestServer(t)
	var out []struct{ Code, Name string }
	if code := s.do(t, "GET", "/v1/subjects", "", nil, &out); code != http.StatusOK || len(out) != 10 || out[0].Code != "math" {
		t.Fatalf("subjects = %d %+v", code, out)
	}
}

func TestSessionsRequireAuth(t *testing.T) {
	s := newTestServer(t)
	for _, c := range [][2]string{
		{"POST", "/v1/sessions"}, {"GET", "/v1/sessions"}, {"GET", "/v1/sessions/x"},
		{"POST", "/v1/sessions/x/ticket"}, {"POST", "/v1/sessions/x/end"},
	} {
		if code := s.do(t, c[0], c[1], "", nil, nil); code != http.StatusUnauthorized {
			t.Errorf("%s %s status = %d, want 401", c[0], c[1], code)
		}
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := newTestServer(t)
	bearer := "Bearer " + s.register(t, "asha@example.com").AccessToken

	created := s.createSession(t, bearer, map[string]any{"subject": "math", "language": "ta", "grade": 7})
	if created.Session.Status != "created" || created.Session.Language != "ta" || *created.Session.Grade != 7 ||
		created.WS.Ticket == "" || created.WS.URL != "wss://api.example/v1/ws" || created.WS.ExpiresAt == "" {
		t.Fatalf("created = %+v", created)
	}
	id := created.Session.ID

	var detail struct {
		Session  map[string]any   `json:"session"`
		Messages []map[string]any `json:"messages"`
	}
	if code := s.do(t, "GET", "/v1/sessions/"+id, bearer, nil, &detail); code != http.StatusOK || detail.Messages == nil || len(detail.Messages) != 0 {
		t.Fatalf("get = %d %+v", code, detail)
	}

	var again sessionBody
	if code := s.do(t, "POST", "/v1/sessions/"+id+"/ticket", bearer, nil, &again); code != http.StatusOK || again.WS.Ticket == "" || again.WS.Ticket == created.WS.Ticket {
		t.Fatalf("ticket = %d %+v", code, again)
	}
	if code := s.do(t, "POST", "/v1/sessions/"+id+"/end", bearer, nil, nil); code != http.StatusAccepted {
		t.Fatalf("end status = %d", code)
	}
	if code := s.do(t, "POST", "/v1/sessions/"+id+"/end", bearer, nil, nil); code != http.StatusAccepted {
		t.Fatalf("second end status = %d", code)
	}
	var errBody struct{ Error struct{ Code string } }
	if code := s.do(t, "POST", "/v1/sessions/"+id+"/ticket", bearer, nil, &errBody); code != http.StatusConflict || errBody.Error.Code != "session_ended" {
		t.Fatalf("ticket after end = %d %+v", code, errBody)
	}
	if code := s.do(t, "GET", "/v1/sessions/"+id, bearer, nil, &detail); code != http.StatusOK || detail.Session["status"] != "ended" || detail.Session["end_reason"] != "learner" {
		t.Fatalf("get after end = %d %+v", code, detail.Session)
	}
}

func TestSessionCreateValidationAndDefaults(t *testing.T) {
	s := newTestServer(t)
	bearer := "Bearer " + s.register(t, "asha@example.com").AccessToken
	var e struct{ Error struct{ Code, Field string } }
	for name, body := range map[string]any{
		"unknown subject": map[string]any{"subject": "astrology"},
		"missing subject": map[string]any{},
		"bad language":    map[string]any{"subject": "math", "language": "xx"},
		"bad grade":       map[string]any{"subject": "math", "grade": 13},
	} {
		if code := s.do(t, "POST", "/v1/sessions", bearer, body, &e); code != http.StatusBadRequest || e.Error.Code != "validation_failed" {
			t.Errorf("%s: %d %+v", name, code, e)
		}
	}
	if code := s.do(t, "POST", "/v1/sessions", bearer, map[string]any{"subject": "math", "extra": 1}, nil); code != http.StatusBadRequest {
		t.Errorf("unknown field status = %d", code)
	}
	def := s.createSession(t, bearer, map[string]any{"subject": "science"})
	if def.Session.Language != "hi" || def.Session.Grade != nil {
		t.Fatalf("defaults = %+v", def.Session)
	}
}

func TestSessionOwnership(t *testing.T) {
	s := newTestServer(t)
	alice := "Bearer " + s.register(t, "alice@example.com").AccessToken
	bob := "Bearer " + s.register(t, "bob@example.com").AccessToken
	id := s.createSession(t, alice, map[string]any{"subject": "math"}).Session.ID
	for _, c := range [][2]string{{"GET", ""}, {"POST", "/ticket"}, {"POST", "/end"}} {
		if code := s.do(t, c[0], "/v1/sessions/"+id+c[1], bob, nil, nil); code != http.StatusNotFound {
			t.Errorf("bob %s %s = %d, want 404", c[0], c[1], code)
		}
	}
	if code := s.do(t, "GET", "/v1/sessions/not-a-uuid", alice, nil, nil); code != http.StatusNotFound {
		t.Errorf("malformed id = %d, want 404", code)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	s.do(t, "GET", "/v1/sessions", bob, nil, &list)
	if len(list.Items) != 0 {
		t.Fatalf("bob sees %d sessions", len(list.Items))
	}
}

func TestSessionListPagination(t *testing.T) {
	s := newTestServer(t)
	bearer := "Bearer " + s.register(t, "asha@example.com").AccessToken
	var ids []string
	for range 5 {
		ids = append(ids, s.createSession(t, bearer, map[string]any{"subject": "math"}).Session.ID)
	}
	type page struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
		Next *string `json:"next_cursor"`
	}
	var got []string
	path := "/v1/sessions?limit=2"
	for range 5 {
		var p page
		if code := s.do(t, "GET", path, bearer, nil, &p); code != http.StatusOK {
			t.Fatalf("list status = %d", code)
		}
		for _, it := range p.Items {
			got = append(got, it.ID)
		}
		if p.Next == nil {
			break
		}
		path = "/v1/sessions?limit=2&cursor=" + *p.Next
	}
	if len(got) != 5 {
		t.Fatalf("paged %d sessions, want 5", len(got))
	}
	for i := range ids { // newest first
		if got[i] != ids[len(ids)-1-i] {
			t.Fatalf("order = %v, created %v", got, ids)
		}
	}
	for _, bad := range []string{"limit=0", "limit=abc", "cursor=!!!", "cursor=Zm9v"} {
		if code := s.do(t, "GET", "/v1/sessions?"+bad, bearer, nil, nil); code != http.StatusBadRequest {
			t.Errorf("?%s status = %d, want 400", bad, code)
		}
	}
}

func TestSessionDetailIncludesMessages(t *testing.T) {
	s := newTestServer(t)
	bearer := "Bearer " + s.register(t, "asha@example.com").AccessToken
	id := s.createSession(t, bearer, map[string]any{"subject": "math"}).Session.ID
	for _, q := range []string{
		`INSERT INTO messages (session_id, turn_no, role, input_mode, content) VALUES ($1, 1, 'learner', 'text', 'hello')`,
		`INSERT INTO messages (session_id, turn_no, role, content) VALUES ($1, 1, 'tutor', 'namaste')`,
	} {
		if _, err := s.pool.Exec(t.Context(), q, id); err != nil {
			t.Fatal(err)
		}
	}
	var detail struct {
		Messages []struct {
			Role      string
			InputMode string `json:"input_mode"`
			Content   string
			Status    string
		} `json:"messages"`
	}
	s.do(t, "GET", "/v1/sessions/"+id, bearer, nil, &detail)
	if len(detail.Messages) != 2 || detail.Messages[0].Role != "learner" || detail.Messages[0].InputMode != "text" || detail.Messages[1].Content != "namaste" || detail.Messages[1].Status != "complete" {
		t.Fatalf("messages = %+v", detail.Messages)
	}
}

func TestSessionCreateRateLimit(t *testing.T) {
	s := newTestServer(t)
	bearer := "Bearer " + s.register(t, "asha@example.com").AccessToken
	for i := range 10 {
		if code := s.do(t, "POST", "/v1/sessions", bearer, map[string]any{"subject": "math"}, nil); code != http.StatusCreated {
			t.Fatalf("request %d status = %d", i, code)
		}
	}
	if code := s.do(t, "POST", "/v1/sessions", bearer, map[string]any{"subject": "math"}, nil); code != http.StatusTooManyRequests {
		t.Fatalf("11th request status = %d, want 429", code)
	}
}
