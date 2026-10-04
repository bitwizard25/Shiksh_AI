package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bitwizard25/Shiksh_AI/internal/entity"
	"github.com/bitwizard25/Shiksh_AI/internal/infrastructure/database"
	"github.com/bitwizard25/Shiksh_AI/internal/usecase"
)

const messageColumns = `id, session_id, turn_no, role, coalesce(input_mode, ''), content, status, audio_ms, latency, created_at`

// Messages implements usecase.MessageRepository.
type Messages struct{ pool *pgxpool.Pool }

var _ usecase.MessageRepository = (*Messages)(nil)

func NewMessages(pool *pgxpool.Pool) *Messages { return &Messages{pool: pool} }

// messageOrder sorts a turn's learner message before its tutor message.
const messageOrder = `turn_no, CASE role WHEN 'learner' THEN 0 ELSE 1 END`

func (r *Messages) Recent(ctx context.Context, sessionID uuid.UUID, n int) ([]entity.Message, error) {
	rows, err := database.Conn(ctx, r.pool).Query(ctx, `
		SELECT `+messageColumns+` FROM (
			SELECT * FROM messages WHERE session_id = $1
			ORDER BY turn_no DESC, CASE role WHEN 'learner' THEN 1 ELSE 0 END LIMIT $2
		) recent ORDER BY `+messageOrder, sessionID, n)
	return collectMessages(rows, err)
}

func (r *Messages) List(ctx context.Context, sessionID uuid.UUID) ([]entity.Message, error) {
	rows, err := database.Conn(ctx, r.pool).Query(ctx, `
		SELECT `+messageColumns+` FROM messages WHERE session_id = $1 ORDER BY `+messageOrder, sessionID)
	return collectMessages(rows, err)
}

func collectMessages(rows pgx.Rows, err error) ([]entity.Message, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []entity.Message
	for rows.Next() {
		var m entity.Message
		var sid uuid.UUID
		if err := rows.Scan(&m.ID, &sid, &m.TurnNo, &m.Role, &m.InputMode, &m.Content, &m.Status, &m.AudioMs, &m.Latency, &m.CreatedAt); err != nil {
			return nil, err
		}
		m.SessionID = sid.String()
		out = append(out, m)
	}
	return out, rows.Err()
}
