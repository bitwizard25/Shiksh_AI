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

const refreshColumns = `user_id, family_id, expires_at, used_at, revoked_at`

// Tokens implements usecase.TokenRepository. Only SHA-256 hashes of tokens are stored.
type Tokens struct{ pool *pgxpool.Pool }

var _ usecase.TokenRepository = (*Tokens)(nil)

func NewTokens(pool *pgxpool.Pool) *Tokens { return &Tokens{pool: pool} }

func (r *Tokens) CreateRefresh(ctx context.Context, userID, familyID uuid.UUID, hash []byte, expiresAt time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx, `
		INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at) VALUES ($1, $2, $3, $4)`,
		userID, familyID, hash, expiresAt)
	return err
}

// ConsumeRefresh is a single conditional UPDATE, so of several concurrent callers exactly one wins.
func (r *Tokens) ConsumeRefresh(ctx context.Context, hash []byte, now time.Time) (entity.RefreshToken, error) {
	tok, err := scanRefresh(database.Conn(ctx, r.pool).QueryRow(ctx, `
		UPDATE refresh_tokens SET used_at = $2
		WHERE token_hash = $1 AND used_at IS NULL AND revoked_at IS NULL AND expires_at > $2
		RETURNING `+refreshColumns, hash, now))
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.RefreshToken{}, entity.ErrTokenInvalid
	}
	return tok, err
}

func (r *Tokens) FindRefresh(ctx context.Context, hash []byte) (entity.RefreshToken, error) {
	tok, err := scanRefresh(database.Conn(ctx, r.pool).QueryRow(ctx,
		`SELECT `+refreshColumns+` FROM refresh_tokens WHERE token_hash = $1`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.RefreshToken{}, entity.ErrNotFound
	}
	return tok, err
}

func (r *Tokens) RevokeFamily(ctx context.Context, familyID uuid.UUID, now time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`, familyID, now)
	return err
}

func (r *Tokens) RevokeFamilyOf(ctx context.Context, hash []byte, now time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx, `
		UPDATE refresh_tokens SET revoked_at = $2
		WHERE revoked_at IS NULL
		  AND family_id = (SELECT family_id FROM refresh_tokens WHERE token_hash = $1)`, hash, now)
	return err
}

func (r *Tokens) RevokeAllForUser(ctx context.Context, userID uuid.UUID, now time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`, userID, now)
	return err
}

func (r *Tokens) CreatePasswordReset(ctx context.Context, userID uuid.UUID, hash []byte, expiresAt time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx, `
		INSERT INTO password_reset_tokens (token_hash, user_id, expires_at) VALUES ($1, $2, $3)`,
		hash, userID, expiresAt)
	return err
}

func (r *Tokens) ConsumePasswordReset(ctx context.Context, hash []byte, now time.Time) (uuid.UUID, error) {
	var userID uuid.UUID
	err := database.Conn(ctx, r.pool).QueryRow(ctx, `
		UPDATE password_reset_tokens SET used_at = $2
		WHERE token_hash = $1 AND used_at IS NULL AND expires_at > $2
		RETURNING user_id`, hash, now).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, entity.ErrTokenInvalid
	}
	return userID, err
}

func (r *Tokens) InvalidatePasswordResets(ctx context.Context, userID uuid.UUID, now time.Time) error {
	_, err := database.Conn(ctx, r.pool).Exec(ctx,
		`UPDATE password_reset_tokens SET used_at = $2 WHERE user_id = $1 AND used_at IS NULL`, userID, now)
	return err
}

func scanRefresh(row pgx.Row) (entity.RefreshToken, error) {
	var t entity.RefreshToken
	err := row.Scan(&t.UserID, &t.FamilyID, &t.ExpiresAt, &t.UsedAt, &t.RevokedAt)
	return t, err
}
