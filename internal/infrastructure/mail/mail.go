// Package mail implements the usecase.Mailer port.
package mail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// LogMailer writes messages to the log instead of sending them. Use it only in development.
type LogMailer struct{ Log *slog.Logger }

var _ usecase.Mailer = LogMailer{}

func (l LogMailer) Send(_ context.Context, m usecase.Message) error {
	l.Log.Info("email not sent (SMTP not configured)", "to", m.To, "subject", m.Subject, "body", m.Body)
	return nil
}

// SMTPMailer sends through an SMTP relay and upgrades to TLS with STARTTLS when the server offers it.
type SMTPMailer struct {
	Host string
	Port int
	User string
	Pass string
	From string // "Name <address>"
}

var _ usecase.Mailer = SMTPMailer{}

func (s SMTPMailer) Send(ctx context.Context, m usecase.Message) error {
	fromAddr, toAddr, err := envelopeAddresses(s.From, m.To)
	if err != nil {
		return err
	}
	msg, err := buildMessage(s.From, m, time.Now())
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.Host, strconv.Itoa(s.Port)))
	if err != nil {
		return fmt.Errorf("mail: dial: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("mail: handshake: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			return fmt.Errorf("mail: starttls: %w", err)
		}
	}
	if s.User != "" {
		// PlainAuth refuses to send credentials over an unencrypted connection to a remote host.
		if err := c.Auth(smtp.PlainAuth("", s.User, s.Pass, s.Host)); err != nil {
			return fmt.Errorf("mail: auth: %w", err)
		}
	}
	if err := c.Mail(fromAddr); err != nil {
		return fmt.Errorf("mail: MAIL FROM: %w", err)
	}
	if err := c.Rcpt(toAddr); err != nil {
		return fmt.Errorf("mail: RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("mail: DATA: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return fmt.Errorf("mail: write body: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mail: end body: %w", err)
	}
	return c.Quit()
}

// envelopeAddresses returns the bare addresses for the SMTP envelope (MAIL FROM / RCPT TO),
// stripping any display names from the header forms.
func envelopeAddresses(from, to string) (fromAddr, toAddr string, err error) {
	fromParsed, err := mail.ParseAddress(from)
	if err != nil {
		return "", "", fmt.Errorf("mail: bad From address: %w", err)
	}
	toParsed, err := mail.ParseAddress(to)
	if err != nil {
		return "", "", fmt.Errorf("mail: bad To address: %w", err)
	}
	return fromParsed.Address, toParsed.Address, nil
}

func buildMessage(from string, m usecase.Message, now time.Time) ([]byte, error) {
	for _, v := range []string{from, m.To, m.Subject} {
		if strings.ContainsAny(v, "\r\n") {
			return nil, errors.New("mail: header values must not contain line breaks")
		}
	}
	if _, err := mail.ParseAddress(m.To); err != nil {
		return nil, fmt.Errorf("mail: bad To address: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", m.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", m.Subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	body := strings.ReplaceAll(m.Body, "\r\n", "\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String()), nil
}
