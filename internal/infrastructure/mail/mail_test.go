package mail

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

var testNow = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

func TestBuildMessage(t *testing.T) {
	msg, err := buildMessage("Shiksha AI <no-reply@example.com>", usecase.Message{
		To: "asha@example.com", Subject: "पासवर्ड reset", Body: "line1\nline2",
	}, testNow)
	if err != nil {
		t.Fatalf("buildMessage: %v", err)
	}
	s := string(msg)
	for _, want := range []string{
		"From: Shiksha AI <no-reply@example.com>\r\n",
		"To: asha@example.com\r\n",
		"Subject: =?utf-8?q?",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"\r\n\r\nline1\r\nline2",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("message missing %q:\n%s", want, s)
		}
	}
}

func TestBuildMessageRejectsHeaderInjection(t *testing.T) {
	from := "Shiksha AI <no-reply@example.com>"
	for name, m := range map[string]usecase.Message{
		"subject": {To: "a@example.com", Subject: "hi\r\nBcc: evil@example.com"},
		"to":      {To: "a@example.com\r\nBcc: evil@example.com", Subject: "hi"},
		"bad to":  {To: "not an address", Subject: "hi"},
	} {
		if _, err := buildMessage(from, m, testNow); err == nil {
			t.Errorf("%s: err = nil, want rejection", name)
		}
	}
}

func TestLogMailerLogsAndSucceeds(t *testing.T) {
	var buf bytes.Buffer
	m := LogMailer{Log: slog.New(slog.NewTextHandler(&buf, nil))}
	if err := m.Send(context.Background(), usecase.Message{To: "a@example.com", Subject: "s", Body: "b"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.Contains(buf.String(), "a@example.com") {
		t.Fatalf("log output %q does not mention the recipient", buf.String())
	}
}

func TestEnvelopeAddresses(t *testing.T) {
	fromAddr, toAddr, err := envelopeAddresses("Shiksha AI <no-reply@example.com>", "Asha <asha@example.com>")
	if err != nil {
		t.Fatalf("envelopeAddresses: %v", err)
	}
	if fromAddr != "no-reply@example.com" {
		t.Errorf("fromAddr = %q, want %q", fromAddr, "no-reply@example.com")
	}
	if toAddr != "asha@example.com" {
		t.Errorf("toAddr = %q, want %q", toAddr, "asha@example.com")
	}

	// Test bare address To
	fromAddr, toAddr, err = envelopeAddresses("Shiksha AI <no-reply@example.com>", "asha@example.com")
	if err != nil {
		t.Fatalf("envelopeAddresses with bare To: %v", err)
	}
	if toAddr != "asha@example.com" {
		t.Errorf("toAddr = %q, want %q", toAddr, "asha@example.com")
	}

	// Test invalid To address
	_, _, err = envelopeAddresses("Shiksha AI <no-reply@example.com>", "not an address")
	if err == nil {
		t.Errorf("envelopeAddresses with invalid To: err = nil, want rejection")
	}
	if !strings.Contains(err.Error(), "bad To address") {
		t.Errorf("err = %v, want to contain %q", err, "bad To address")
	}
}
