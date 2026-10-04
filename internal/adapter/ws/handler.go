package ws

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase/conversation"
)

const (
	readLimit    = 64 << 10
	pingInterval = 20 * time.Second
	pingTimeout  = 10 * time.Second
)

// Tickets exchanges a single-use ticket for the session it opens (usecase.Sessions implements it).
type Tickets interface {
	RedeemTicket(ctx context.Context, token string) (userID, sessionID uuid.UUID, err error)
}

// Options configures the handler.
type Options struct {
	Hub            *conversation.Hub
	Tickets        Tickets
	Log            *slog.Logger
	AllowedOrigins []string // browser origins allowed to connect; native clients send no Origin
	Dropped        func()   // counts stale frames dropped by the outbox
}

// Handler serves GET /v1/ws.
type Handler struct {
	hub     *conversation.Hub
	tickets Tickets
	log     *slog.Logger
	origins []string
	dropped func()
}

// NewHandler builds the handler.
func NewHandler(o Options) *Handler {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	var patterns []string
	for _, origin := range o.AllowedOrigins {
		if u, err := url.Parse(origin); err == nil && u.Host != "" {
			patterns = append(patterns, u.Host)
		}
	}
	return &Handler{hub: o.Hub, tickets: o.Tickets, log: o.Log, origins: patterns, dropped: o.Dropped}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Both checks come before the ticket is redeemed, so a rejected connect does not burn the ticket.
	if !offersSubprotocol(r) {
		http.Error(w, "the shiksha.v1 WebSocket subprotocol is required", http.StatusBadRequest)
		return
	}
	if h.hub.Draining() {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "server is shutting down, retry shortly", http.StatusServiceUnavailable)
		return
	}
	userID, sessionID, err := h.tickets.RedeemTicket(r.Context(), r.URL.Query().Get("ticket"))
	if err != nil {
		if errors.Is(err, entity.ErrTokenInvalid) {
			http.Error(w, "ticket is invalid, used or expired", http.StatusUnauthorized)
			return
		}
		h.log.Error("redeem ticket failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols:    []string{Subprotocol},
		CompressionMode: websocket.CompressionDisabled,
		OriginPatterns:  h.origins,
	})
	if err != nil {
		return // Accept already wrote the response
	}
	if c.Subprotocol() != Subprotocol {
		_ = c.Close(websocket.StatusProtocolError, "unsupported subprotocol")
		return
	}
	c.SetReadLimit(readLimit)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	out := NewOutbox(ctx, c, func(err error) {
		h.log.Debug("websocket write failed", "session_id", sessionID, "err", err)
		cancel()
	}, h.dropped)

	info := h.hub.Serve(ctx, conversation.Connection{
		UserID: userID, SessionID: sessionID, Out: out,
		Attach: func(s *conversation.Session) {
			go h.read(ctx, c, s)
			go h.ping(ctx, c, cancel)
		},
	})
	out.Close(info.Code, info.Reason)
}

func offersSubprotocol(r *http.Request) bool {
	for _, v := range r.Header.Values("Sec-WebSocket-Protocol") {
		for _, p := range strings.Split(v, ",") {
			if strings.TrimSpace(p) == Subprotocol {
				return true
			}
		}
	}
	return false
}

// read forwards client messages to the session until the connection ends.
func (h *Handler) read(ctx context.Context, c *websocket.Conn, s *conversation.Session) {
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			s.Closed(err)
			return
		}
		if typ == websocket.MessageBinary {
			s.Audio(data)
			continue
		}
		msg, err := DecodeClient(data)
		if err != nil {
			msg = conversation.ClientMsg{Type: MsgInvalid}
		}
		s.Message(msg)
	}
}

// ping keeps the connection alive. coder/websocket only completes a Ping while Read is running,
// which is why the actor must never stall its reader.
func (h *Handler) ping(ctx context.Context, c *websocket.Conn, cancel context.CancelFunc) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, done := context.WithTimeout(ctx, pingTimeout)
			err := c.Ping(pctx)
			done()
			if err != nil {
				cancel()
				return
			}
		}
	}
}
