-- +goose Up
-- Installation admins may change the map colour of built-in relationship types (ADR-011c). Seeding keeps `color` at
-- the built-in default from the binary, so the override survives upgrades and resetting it restores the default.
ALTER TABLE relationship_types ADD COLUMN color_override TEXT NOT NULL DEFAULT '';

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
