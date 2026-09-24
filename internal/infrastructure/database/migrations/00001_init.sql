-- +goose Up
CREATE TABLE users (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email               text NOT NULL,
    password_hash       text NOT NULL,
    display_name        text NOT NULL,
    preferred_lang      text NOT NULL DEFAULT 'hi',
    grade               smallint CHECK (grade BETWEEN 1 AND 12),
    terms_accepted_at   timestamptz NOT NULL,
    guardian_consent_at timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX users_email_uq ON users (lower(email));

CREATE TABLE refresh_tokens (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    family_id  uuid NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX refresh_tokens_family_idx ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens (user_id);

CREATE TABLE password_reset_tokens (
    token_hash bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);
CREATE INDEX password_reset_tokens_user_idx ON password_reset_tokens (user_id);

CREATE TABLE tutoring_sessions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id        uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    subject        text NOT NULL,
    language       text NOT NULL,
    grade          smallint,
    status         text NOT NULL DEFAULT 'created' CHECK (status IN ('created', 'active', 'ended')),
    conn_epoch     int NOT NULL DEFAULT 0,
    end_reason     text,
    summary        text,
    turn_count     int NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    started_at     timestamptz,
    last_active_at timestamptz,
    ended_at       timestamptz
);
CREATE INDEX tutoring_sessions_user_created_idx ON tutoring_sessions (user_id, created_at DESC);
CREATE INDEX tutoring_sessions_summary_idx ON tutoring_sessions (user_id, subject, ended_at DESC) WHERE summary IS NOT NULL;
CREATE INDEX tutoring_sessions_stale_idx ON tutoring_sessions ((coalesce(last_active_at, created_at))) WHERE status <> 'ended';

CREATE TABLE ws_tickets (
    token_hash bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    session_id uuid NOT NULL REFERENCES tutoring_sessions(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

CREATE TABLE messages (
    id         bigserial PRIMARY KEY,
    session_id uuid NOT NULL REFERENCES tutoring_sessions(id) ON DELETE CASCADE,
    turn_no    int NOT NULL,
    role       text NOT NULL CHECK (role IN ('learner', 'tutor')),
    input_mode text CHECK (input_mode IN ('voice', 'text')),
    content    text NOT NULL,
    status     text NOT NULL DEFAULT 'complete' CHECK (status IN ('complete', 'interrupted', 'failed')),
    audio_ms   int,
    latency    jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, turn_no, role)
);

-- +goose Down
DROP TABLE IF EXISTS messages;
DROP TABLE IF EXISTS ws_tickets;
DROP TABLE IF EXISTS tutoring_sessions;
DROP TABLE IF EXISTS password_reset_tokens;
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS users;
