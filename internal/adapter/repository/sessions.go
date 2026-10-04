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

const sessionColumns = `id, user_id, subject, language, grade, status, conn_epoch,
	coalesce(end_reason, ''), coalesce(summary, ''), turn_count, created_at, started_at, last_active_at, ended_at`

// Sessions implements usecase.SessionRepository.
type Sessions struct{ pool *pgxpool.Pool }

var _ usecase.SessionRepository = (*Sessions)(nil)

func NewSessions(pool *pgxpool.Pool) *Sessions { return &Sessions{pool: pool} }

func (r *Sessions) Create(ctx context.Context, s usecase.NewSession) (entity.TutoringSession, error) {
	return scanSession(database.Conn(ctx, r.pool).QueryRow(ctx, `
		INSERT INTO tutoring_sessions (user_id, subject, language, grade, created_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+sessionColumns, s.UserID, s.Subject, s.Language, s.Grade, s.Now))
}

func (r *Sessions) Get(ctx context.Context, id uuid.UUID) (entity.TutoringSession, error) {
	return scanSession(database.Conn(ctx, r.pool).QueryRow(ctx,
		`SELECT `+sessionColumns+` FROM tutoring_sessions WHERE id = $1`, id))
}

// List uses keyset pagination on (created_at, id), never OFFSET.
func (r *Sessions) List(ctx context.Context, userID uuid.UUID, limit int, after *usecase.SessionCursor) ([]entity.TutoringSession, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if after == nil {
		rows, err = database.Conn(ctx, r.pool).Query(ctx, `
			SELECT `+sessionColumns+` FROM tutoring_sessions
			WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, userID, limit)
	} else {
		rows, err = database.Conn(ctx, r.pool).Query(ctx, `
			SELECT `+sessionColumns+` FROM tutoring_sessions
			WHERE user_id = $1 AND (created_at, id) < ($2, $3)
			ORDER BY created_at DESC, id DESC LIMIT $4`, userID, after.CreatedAt, after.ID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entity.TutoringSession
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Sessions) End(ctx context.Context, id uuid.UUID, reason string, now time.Time) (bool, error) {
	tag, err := database.Conn(ctx, r.pool).Exec(ctx, `
		UPDATE tutoring_sessions SET status = 'ended', end_reason = $2, ended_at = $3
		WHERE id = $1 AND status <> 'ended'`, id, reason, now)
	return tag.RowsAffected() == 1, err
}

func (r *Sessions) ClaimStale(ctx context.Context, cutoff time.Time, limit int, now time.Time) ([]uuid.UUID, error) {
	rows, err := database.Conn(ctx, r.pool).Query(ctx, `
		UPDATE tutoring_sessions SET status = 'ended', end_reason = 'swept', ended_at = $3
		WHERE id IN (
			SELECT id FROM tutoring_sessions
			WHERE status <> 'ended' AND coalesce(last_active_at, created_at) < $1
			ORDER BY coalesce(last_active_at, created_at)
			FOR UPDATE SKIP LOCKED LIMIT $2)
		RETURNING id`, cutoff, limit, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Sessions) SetSummary(ctx context.Context, id uuid.UUID, summary string) error {
	tag, err := database.Conn(ctx, r.pool).Exec(ctx, `UPDATE tutoring_sessions SET summary = $2 WHERE id = $1`, id, summary)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return entity.ErrNotFound
	}
	return nil
}

func (r *Sessions) RecentSummaries(ctx context.Context, userID uuid.UUID, subject string, n int) ([]string, error) {
	rows, err := database.Conn(ctx, r.pool).Query(ctx, `
		SELECT summary FROM tutoring_sessions
		WHERE user_id = $1 AND subject = $2 AND summary IS NOT NULL
		ORDER BY ended_at DESC NULLS LAST, id DESC LIMIT $3`, userID, subject, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanSession(row pgx.Row) (entity.TutoringSession, error) {
	var s entity.TutoringSession
	err := row.Scan(&s.ID, &s.UserID, &s.Subject, &s.Language, &s.Grade, &s.Status, &s.ConnEpoch,
		&s.EndReason, &s.Summary, &s.TurnCount, &s.CreatedAt, &s.StartedAt, &s.LastActiveAt, &s.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.TutoringSession{}, entity.ErrNotFound
	}
	return s, err
}
