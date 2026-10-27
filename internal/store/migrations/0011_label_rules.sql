-- +goose Up
-- Label rules add labels to a project's entities by kind, describing source or existing labels, below every source:
-- a source's own label for a key always wins, and a rule never keeps an entity alive (ADR-003, docs/LABEL_RULES.md).
CREATE TABLE label_rules (
  id          TEXT PRIMARY KEY,
  project_id  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  match_json  TEXT NOT NULL,
  labels_json TEXT NOT NULL,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  version     INTEGER NOT NULL DEFAULT 1,
  UNIQUE (project_id, name)
) STRICT, WITHOUT ROWID;

CREATE INDEX label_rules_project ON label_rules (project_id, created_at);

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
