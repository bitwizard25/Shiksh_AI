package usecase

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// Subjects lists the subjects the tutor teaches, in display order.
func Subjects() []entity.Subject { return entity.Subjects() }

// SessionsDeps are the ports the Sessions interactor needs.
type SessionsDeps struct {
	Users     UserRepository
	Sessions  SessionRepository
	Messages  MessageRepository
	Tickets   TicketRepository
	Tx        TxManager
	Opaque    OpaqueTokens
	Summaries *Summaries   // may be nil: summaries are then skipped
	Live      LiveSessions // may be nil: no realtime role in this process
}

// SessionsConfig holds the interactor's settings.
type SessionsConfig struct {
	TicketTTL time.Duration
}

// Ticket is a single-use credential for opening the WebSocket of one session.
type Ticket struct {
	Token     string
	ExpiresAt time.Time
}

// StartedSession is a session together with the ticket to connect to it.
type StartedSession struct {
	Session entity.TutoringSession
	Ticket  Ticket
}

// SessionInput is what a learner chooses when starting a session. Empty fields fall back to the profile.
type SessionInput struct {
	Subject  string
	Language string
	Grade    *int
}

// SessionPage is one page of a learner's sessions, newest first.
type SessionPage struct {
	Items []entity.TutoringSession
	Next  *SessionCursor // nil on the last page
}

// SessionDetail is a session with its conversation.
type SessionDetail struct {
	Session  entity.TutoringSession
	Messages []entity.Message
}

// Sessions is the interactor for a learner's tutoring sessions.
type Sessions struct {
	d   SessionsDeps
	cfg SessionsConfig
	now func() time.Time
}

// NewSessions builds the interactor. now defaults to time.Now.
func NewSessions(d SessionsDeps, cfg SessionsConfig, now func() time.Time) *Sessions {
	if now == nil {
		now = time.Now
	}
	return &Sessions{d: d, cfg: cfg, now: now}
}

// Create validates the input, starts a session and issues the ticket to connect to it.
func (s *Sessions) Create(ctx context.Context, userID uuid.UUID, in SessionInput) (StartedSession, error) {
	if err := entity.ValidateSubject(in.Subject); err != nil {
		return StartedSession{}, err
	}
	user, err := s.d.Users.GetByID(ctx, userID)
	if err != nil {
		return StartedSession{}, err
	}
	lang := in.Language
	if lang == "" {
		lang = user.PreferredLang
	}
	if _, ok := entity.LookupLanguage(lang); !ok {
		return StartedSession{}, &entity.ValidationError{Field: "language", Message: "is not a supported language"}
	}
	grade := in.Grade
	if grade == nil {
		grade = user.Grade
	}
	if err := entity.ValidateGrade(grade); err != nil {
		return StartedSession{}, err
	}

	var out StartedSession
	err = s.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		sess, err := s.d.Sessions.Create(ctx, NewSession{
			UserID: userID, Subject: in.Subject, Language: lang, Grade: grade, Now: s.now(),
		})
		if err != nil {
			return err
		}
		ticket, err := s.issueTicket(ctx, userID, sess.ID)
		if err != nil {
			return err
		}
		out = StartedSession{Session: sess, Ticket: ticket}
		return nil
	})
	return out, err
}

// NewTicket issues a fresh ticket so the learner can reconnect. entity.ErrSessionEnded if it is over.
func (s *Sessions) NewTicket(ctx context.Context, userID, sessionID uuid.UUID) (Ticket, error) {
	sess, err := s.owned(ctx, userID, sessionID)
	if err != nil {
		return Ticket{}, err
	}
	if sess.Ended() {
		return Ticket{}, entity.ErrSessionEnded
	}
	return s.issueTicket(ctx, userID, sessionID)
}

// End ends the learner's session and queues its summary. Ending an ended session does nothing.
func (s *Sessions) End(ctx context.Context, userID, sessionID uuid.UUID) error {
	sess, err := s.owned(ctx, userID, sessionID)
	if err != nil {
		return err
	}
	if s.d.Live != nil && s.d.Live.End(sessionID) {
		return nil // the live session finishes itself: it saves what the learner heard, ends and summarizes
	}
	ended, err := s.d.Sessions.End(ctx, sessionID, entity.EndLearner, s.now())
	if err != nil {
		return err
	}
	if ended && sess.NeedsSummary() && s.d.Summaries != nil {
		s.d.Summaries.Submit(sessionID)
	}
	return nil
}

// List returns a page of the learner's sessions, newest first. limit <= 0 means the default page size.
func (s *Sessions) List(ctx context.Context, userID uuid.UUID, limit int, after *SessionCursor) (SessionPage, error) {
	if limit <= 0 {
		limit = defaultPageSize
	}
	limit = min(limit, maxPageSize)
	items, err := s.d.Sessions.List(ctx, userID, limit+1, after)
	if err != nil {
		return SessionPage{}, err
	}
	page := SessionPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		last := page.Items[limit-1]
		page.Next = &SessionCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	return page, nil
}

// Get returns one of the learner's sessions with its messages.
func (s *Sessions) Get(ctx context.Context, userID, sessionID uuid.UUID) (SessionDetail, error) {
	sess, err := s.owned(ctx, userID, sessionID)
	if err != nil {
		return SessionDetail{}, err
	}
	msgs, err := s.d.Messages.List(ctx, sessionID)
	if err != nil {
		return SessionDetail{}, err
	}
	return SessionDetail{Session: sess, Messages: msgs}, nil
}

// RedeemTicket exchanges a ticket for the session it opens. The realtime WebSocket handler calls it.
// entity.ErrTokenInvalid for an unknown, used, expired or ended-session ticket.
func (s *Sessions) RedeemTicket(ctx context.Context, token string) (userID, sessionID uuid.UUID, err error) {
	return s.d.Tickets.Redeem(ctx, s.d.Opaque.Hash(token), s.now())
}

// owned returns the session if it belongs to the user. Someone else's session looks like a missing one.
func (s *Sessions) owned(ctx context.Context, userID, sessionID uuid.UUID) (entity.TutoringSession, error) {
	sess, err := s.d.Sessions.Get(ctx, sessionID)
	if err != nil {
		return entity.TutoringSession{}, err
	}
	if sess.UserID != userID {
		return entity.TutoringSession{}, entity.ErrNotFound
	}
	return sess, nil
}

func (s *Sessions) issueTicket(ctx context.Context, userID, sessionID uuid.UUID) (Ticket, error) {
	plain, hash, err := s.d.Opaque.New()
	if err != nil {
		return Ticket{}, err
	}
	expires := s.now().Add(s.cfg.TicketTTL)
	if err := s.d.Tickets.Create(ctx, userID, sessionID, hash, expires); err != nil {
		return Ticket{}, err
	}
	return Ticket{Token: plain, ExpiresAt: expires}, nil
}
