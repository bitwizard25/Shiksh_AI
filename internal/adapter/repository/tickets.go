package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

// Tickets implements usecase.TicketRepository. Only SHA-256 hashes of tickets are stored.
type Tickets struct{ pool *pgxpool.Pool }

var _ usecase.TicketRepository = (*Tickets)(nil)

func NewTickets(pool *pgxpool.Pool) *Tickets { return &Tickets{pool: pool} }

func (r *Tickets) Create(ctx context.Context, userID, sessionID uuid.UUID, hash []byte, expiresAt time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx, `
		INSERT INTO ws_tickets (token_hash, user_id, session_id, expires_at) VALUES ($1, $2, $3, $4)`,
		hash, userID, sessionID, expiresAt)
	return err
}

// Redeem is a single conditional UPDATE, so of several concurrent callers exactly one wins.
func (r *Tickets) Redeem(ctx context.Context, hash []byte, now time.Time) (uuid.UUID, uuid.UUID, error) {
	var userID, sessionID uuid.UUID
	err := database.Conn(ctx, r.pool).QueryRow(ctx, `
		UPDATE ws_tickets t SET used_at = $2
		FROM tutoring_sessions s
		WHERE t.token_hash = $1 AND t.used_at IS NULL AND t.expires_at > $2
		  AND s.id = t.session_id AND s.status <> 'ended'
		RETURNING t.user_id, t.session_id`, hash, now).Scan(&userID, &sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, uuid.Nil, entity.ErrTokenInvalid
	}
	return userID, sessionID, err
}

func (r *Tickets) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	tag, err := database.Conn(ctx, r.pool).Exec(ctx, `DELETE FROM ws_tickets WHERE expires_at < $1`, now)
	return tag.RowsAffected(), err
}
