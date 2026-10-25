-- +goose Up
-- Unresolved relationship references keep the original item and the key of the missing endpoint, so they can be
-- retried when a matching entity appears instead of waiting for the source to send the relationship again.
ALTER TABLE unresolved_refs ADD COLUMN item_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE unresolved_refs ADD COLUMN missing_key TEXT NOT NULL DEFAULT '';
CREATE INDEX ix_unresolved_missing ON unresolved_refs (missing_key) WHERE missing_key != '';

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
