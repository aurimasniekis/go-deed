-- +goose Up
-- Outbox retention deletes published and dead-lettered rows by age; without these indexes every maintenance run
-- scanned the whole outbox while holding the writer.
CREATE INDEX ix_outbox_published ON outbox (published_at) WHERE published_at IS NOT NULL;
CREATE INDEX ix_outbox_dead ON outbox (dead_at) WHERE dead_at IS NOT NULL;
