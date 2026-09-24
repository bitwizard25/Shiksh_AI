// Package repository implements the use-case repository ports on Postgres. Every query goes
// through database.Conn, so repositories join a use case's transaction automatically.
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

const userColumns = `id, email, password_hash, display_name, preferred_lang, grade,
	terms_accepted_at, guardian_consent_at, created_at, updated_at`

// Users implements usecase.UserRepository.
type Users struct{ pool *pgxpool.Pool }

var _ usecase.UserRepository = (*Users)(nil)

func NewUsers(pool *pgxpool.Pool) *Users { return &Users{pool: pool} }

func (r *Users) Create(ctx context.Context, u usecase.NewUser) (entity.User, error) {
	user, err := scanUser(database.Conn(ctx, r.pool).QueryRow(ctx, `
		INSERT INTO users (email, password_hash, display_name, preferred_lang, grade,
		                   terms_accepted_at, guardian_consent_at, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::timestamptz, CASE WHEN $7::boolean THEN $6::timestamptz END, $6::timestamptz, $6::timestamptz)
		RETURNING `+userColumns,
		string(u.Email), u.PasswordHash, u.DisplayName, u.PreferredLang, u.Grade, u.AcceptedAt, u.GuardianConsent))
	if isUniqueViolation(err) {
		return entity.User{}, entity.ErrEmailTaken
	}
	return user, err
}

func (r *Users) GetByEmail(ctx context.Context, email entity.Email) (entity.User, error) {
	return scanUser(database.Conn(ctx, r.pool).QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1)`, string(email)))
}

func (r *Users) GetByID(ctx context.Context, id uuid.UUID) (entity.User, error) {
	return scanUser(database.Conn(ctx, r.pool).QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

func (r *Users) UpdateProfile(ctx context.Context, id uuid.UUID, p usecase.ProfilePatch, now time.Time) (entity.User, error) {
	return scanUser(database.Conn(ctx, r.pool).QueryRow(ctx, `
		UPDATE users SET
			display_name   = coalesce($2, display_name),
			preferred_lang = coalesce($3, preferred_lang),
			grade          = coalesce($4, grade),
			updated_at     = $5
		WHERE id = $1
		RETURNING `+userColumns,
		id, p.DisplayName, p.PreferredLang, p.Grade, now))
}

func (r *Users) UpdatePassword(ctx context.Context, id uuid.UUID, passwordHash string, now time.Time) error {
	tag, err := database.Conn(ctx, r.pool).Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = $3 WHERE id = $1`, id, passwordHash, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return entity.ErrNotFound
	}
	return nil
}

func (r *Users) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := database.Conn(ctx, r.pool).Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return entity.ErrNotFound
	}
	return nil
}

func scanUser(row pgx.Row) (entity.User, error) {
	var (
		u     entity.User
		email string
	)
	err := row.Scan(&u.ID, &email, &u.PasswordHash, &u.DisplayName, &u.PreferredLang, &u.Grade,
		&u.TermsAcceptedAt, &u.GuardianConsentAt, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return entity.User{}, entity.ErrNotFound
	}
	if err != nil {
		return entity.User{}, err
	}
	u.Email = entity.Email(email)
	return u, nil
}
