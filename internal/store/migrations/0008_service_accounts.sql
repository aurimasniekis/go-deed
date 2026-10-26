-- +goose Up
-- Service accounts: non-human users that authenticate only with API tokens (no password, SSO identity or session).
ALTER TABLE users ADD COLUMN kind TEXT NOT NULL DEFAULT 'human' CHECK (kind IN ('human', 'service'));
ALTER TABLE users ADD COLUMN name TEXT COLLATE NOCASE;
ALTER TABLE users ADD COLUMN description TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX ux_users_name ON users (name) WHERE name IS NOT NULL;

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
