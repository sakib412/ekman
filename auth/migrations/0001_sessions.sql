CREATE TABLE ekman_sessions (
    token_hash bytea       PRIMARY KEY,
    user_id    text        NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE INDEX ekman_sessions_user_id ON ekman_sessions (user_id);
