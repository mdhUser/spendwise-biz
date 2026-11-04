-- +goose Up
CREATE INDEX sessions_revoked_idx ON sessions (revoked_at);

-- +goose Down
DROP INDEX sessions_revoked_idx ON sessions;
