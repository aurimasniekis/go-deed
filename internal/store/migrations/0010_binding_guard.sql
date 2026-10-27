-- +goose Up
-- Bindings may name a guard query (the binding applies to an entity only when it returns data) and a value to use when
-- the main query returns nothing (instead of "no data"), so "0 when absent" does not paint every entity healthy.
ALTER TABLE bindings ADD COLUMN guard TEXT NOT NULL DEFAULT '';
ALTER TABLE bindings ADD COLUMN when_empty REAL;

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
