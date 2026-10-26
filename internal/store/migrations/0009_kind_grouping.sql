-- +goose Up
-- Map grouping: entities that reach an entity of this kind through the listed relationship types (JSON
-- {"relationship_types": [...]}, '' = the kind does not group) can be grouped under it on the map.
ALTER TABLE kind_definitions ADD COLUMN grouping_json TEXT NOT NULL DEFAULT '';

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
