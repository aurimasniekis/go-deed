-- 0003_catalog: kinds, relationship types, sources, entities, facets, relations, assertions, conflicts, change log,
-- snapshots and search (ADR-002, ADR-003, ADR-013).

-- +goose Up
CREATE TABLE kind_definitions (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL UNIQUE,          -- e.g. server, custom.example.com/widget
  group_name      TEXT NOT NULL,                 -- infra, software, network, identity, events, observability, org, custom.*
  builtin         INTEGER NOT NULL DEFAULT 0,
  title           TEXT NOT NULL,
  plural          TEXT NOT NULL,
  description     TEXT NOT NULL DEFAULT '',
  icon            TEXT NOT NULL DEFAULT '',
  color           TEXT NOT NULL DEFAULT '',
  identity_keys   TEXT NOT NULL DEFAULT '[]',    -- JSON array of JSON pointers into the effective object
  support_json    TEXT NOT NULL DEFAULT '{}',    -- {modeling, terraform, runtime, native}
  storage_version TEXT NOT NULL,
  deprecated      INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  version         INTEGER NOT NULL DEFAULT 1
) STRICT, WITHOUT ROWID;

CREATE TABLE kind_versions (
  kind_id     TEXT NOT NULL REFERENCES kind_definitions (id) ON DELETE CASCADE,
  version     TEXT NOT NULL,                     -- v1, v2, …
  schema_json TEXT NOT NULL,
  breaking    INTEGER NOT NULL DEFAULT 0,
  migration   TEXT NOT NULL DEFAULT '' CHECK (migration IN ('', 'revalidate', 'mark_invalid')),
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (kind_id, version)
) STRICT, WITHOUT ROWID;

CREATE TABLE relationship_types (
  id               TEXT PRIMARY KEY,
  name             TEXT NOT NULL UNIQUE,
  inverse_name     TEXT NOT NULL DEFAULT '',
  title            TEXT NOT NULL,
  description      TEXT NOT NULL DEFAULT '',
  directed         INTEGER NOT NULL DEFAULT 1,
  from_kinds       TEXT NOT NULL DEFAULT '[]',
  to_kinds         TEXT NOT NULL DEFAULT '[]',
  default_evidence TEXT NOT NULL DEFAULT 'structural' CHECK (default_evidence IN ('structural', 'observed')),
  cardinality      TEXT NOT NULL DEFAULT 'many' CHECK (cardinality IN ('one', 'many')),
  attributes_schema TEXT NOT NULL DEFAULT '',
  impact           TEXT NOT NULL DEFAULT 'forward' CHECK (impact IN ('forward', 'reverse', 'both', 'none')),
  color            TEXT NOT NULL DEFAULT '',
  builtin          INTEGER NOT NULL DEFAULT 0,
  created_at       INTEGER NOT NULL,
  updated_at       INTEGER NOT NULL,
  version          INTEGER NOT NULL DEFAULT 1
) STRICT, WITHOUT ROWID;

-- Sources own facets and relation assertions (ADR-003, ADR-013).
CREATE TABLE sources (
  id                TEXT PRIMARY KEY,
  project_id        TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  name              TEXT NOT NULL,
  display_name      TEXT NOT NULL,
  provider          TEXT NOT NULL,                 -- manual, push, terraform_state, pulse_admin, pulse_tail, github, …
  layer             TEXT NOT NULL CHECK (layer IN ('declared', 'imported', 'discovered', 'observation', 'annotation', 'override')),
  system            INTEGER NOT NULL DEFAULT 0,    -- built-in per-project sources (api, terraform, ui)
  enabled           INTEGER NOT NULL DEFAULT 1,
  config_json       TEXT NOT NULL DEFAULT '{}',
  secrets_enc       BLOB,
  policy_json       TEXT NOT NULL DEFAULT '{}',
  schedule_seconds  INTEGER NOT NULL DEFAULT 0,
  expected_interval_seconds INTEGER NOT NULL DEFAULT 0,
  last_generation   INTEGER NOT NULL DEFAULT 0,
  last_run_at       INTEGER,
  last_success_at   INTEGER,
  last_heartbeat_at INTEGER,
  last_error        TEXT,
  next_run_at       INTEGER,
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  version           INTEGER NOT NULL DEFAULT 1,
  UNIQUE (project_id, name)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_sources_due ON sources (next_run_at) WHERE enabled = 1 AND schedule_seconds > 0;

-- entities is a rowid table so the FTS index can reference it by rowid.
CREATE TABLE entities (
  rid            INTEGER PRIMARY KEY,
  id             TEXT NOT NULL UNIQUE,
  project_id     TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  kind           TEXT NOT NULL,
  name           TEXT NOT NULL,
  display_name   TEXT NOT NULL DEFAULT '',
  description    TEXT NOT NULL DEFAULT '',
  environment    TEXT NOT NULL DEFAULT '',
  owner          TEXT NOT NULL DEFAULT '',        -- entity ID of a group/team
  lifecycle      TEXT NOT NULL DEFAULT '',
  icon           TEXT NOT NULL DEFAULT '',
  labels_json    TEXT NOT NULL DEFAULT '{}',
  effective_json TEXT NOT NULL DEFAULT '{}',      -- merged metadata + spec
  provenance_json TEXT NOT NULL DEFAULT '{}',     -- JSON pointer → provenance
  observed_json  TEXT NOT NULL DEFAULT '{}',      -- status.observed (observation layer)
  health         TEXT NOT NULL DEFAULT 'unknown' CHECK (health IN ('ok', 'warning', 'failed', 'unknown')),
  health_json    TEXT NOT NULL DEFAULT '{}',
  freshness      TEXT NOT NULL DEFAULT 'unknown' CHECK (freshness IN ('fresh', 'aging', 'stale', 'unknown')),
  last_seen_at   INTEGER,
  schema_version TEXT NOT NULL DEFAULT '',
  valid          INTEGER NOT NULL DEFAULT 1,
  validation_json TEXT NOT NULL DEFAULT '[]',
  declared       INTEGER NOT NULL DEFAULT 0,      -- 1 while any declared facet is live
  facet_count    INTEGER NOT NULL DEFAULT 0,
  version_declared  TEXT NOT NULL DEFAULT '',
  version_observed  TEXT NOT NULL DEFAULT '',
  version_available TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  tombstoned_at  INTEGER,
  version        INTEGER NOT NULL DEFAULT 1,
  UNIQUE (project_id, kind, name)
) STRICT;

CREATE INDEX ix_entities_project_kind ON entities (project_id, kind) WHERE tombstoned_at IS NULL;
CREATE INDEX ix_entities_kind ON entities (kind) WHERE tombstoned_at IS NULL;
CREATE INDEX ix_entities_env ON entities (environment) WHERE tombstoned_at IS NULL;
CREATE INDEX ix_entities_owner ON entities (owner) WHERE tombstoned_at IS NULL;
CREATE INDEX ix_entities_health ON entities (health) WHERE tombstoned_at IS NULL;
CREATE INDEX ix_entities_updated ON entities (updated_at);
CREATE INDEX ix_entities_tombstoned ON entities (tombstoned_at) WHERE tombstoned_at IS NOT NULL;

CREATE TABLE entity_aliases (
  project_id TEXT NOT NULL,
  kind       TEXT NOT NULL,
  name       TEXT NOT NULL,
  entity_id  TEXT NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (project_id, kind, name)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_aliases_entity ON entity_aliases (entity_id);

CREATE TABLE entity_external_ids (
  source_id   TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  external_id TEXT NOT NULL,
  entity_id   TEXT NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (source_id, external_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_external_entity ON entity_external_ids (entity_id);

-- Identity keys of live entities for convergence and duplicate detection.
CREATE TABLE entity_identity_keys (
  project_id TEXT NOT NULL,
  kind       TEXT NOT NULL,
  key_value  TEXT NOT NULL,                       -- "<pointer>=<value>"
  entity_id  TEXT NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
  PRIMARY KEY (project_id, kind, key_value, entity_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_identity_entity ON entity_identity_keys (entity_id);

CREATE TABLE facets (
  id                 TEXT PRIMARY KEY,
  entity_id          TEXT NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
  source_id          TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  layer              TEXT NOT NULL,
  generation         INTEGER NOT NULL DEFAULT 0,
  revision           INTEGER NOT NULL DEFAULT 0,
  fields_json        TEXT NOT NULL,
  observed_at        INTEGER NOT NULL,
  expires_at         INTEGER,
  tombstoned_at      INTEGER,
  tombstone_revision INTEGER NOT NULL DEFAULT 0,
  first_claimed_at   INTEGER NOT NULL,
  external_id        TEXT NOT NULL DEFAULT '',
  created_at         INTEGER NOT NULL,
  updated_at         INTEGER NOT NULL,
  version            INTEGER NOT NULL DEFAULT 1,
  UNIQUE (entity_id, source_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_facets_source ON facets (source_id, tombstoned_at);
CREATE INDEX ix_facets_expiry ON facets (expires_at) WHERE expires_at IS NOT NULL AND tombstoned_at IS NULL;

CREATE TABLE conflicts (
  entity_id      TEXT NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
  pointer        TEXT NOT NULL,
  layer          TEXT NOT NULL,
  winner_source  TEXT NOT NULL,
  loser_source   TEXT NOT NULL,
  winner_value   TEXT NOT NULL,
  loser_value    TEXT NOT NULL,
  first_seen_at  INTEGER NOT NULL,
  last_seen_at   INTEGER NOT NULL,
  PRIMARY KEY (entity_id, pointer, loser_source)
) STRICT, WITHOUT ROWID;

CREATE TABLE relations (
  rid           INTEGER PRIMARY KEY,
  id            TEXT NOT NULL UNIQUE,
  project_id    TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  type          TEXT NOT NULL,
  from_id       TEXT NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
  to_id         TEXT NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
  to_project_id TEXT NOT NULL,
  bridge_id     TEXT,
  attributes_json TEXT NOT NULL DEFAULT '{}',
  evidence_class TEXT NOT NULL DEFAULT 'declared',  -- strongest live evidence: declared, structural, observed, inferred
  inferred      INTEGER NOT NULL DEFAULT 0,          -- 1 when every live assertion is inferred
  last_seen_at  INTEGER,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL,
  live          INTEGER NOT NULL DEFAULT 1,
  tombstoned_at INTEGER,
  UNIQUE (type, from_id, to_id)
) STRICT;

CREATE INDEX ix_relations_from ON relations (from_id, type) WHERE live = 1;
CREATE INDEX ix_relations_to ON relations (to_id, type) WHERE live = 1;
CREATE INDEX ix_relations_project ON relations (project_id) WHERE live = 1;

CREATE TABLE relation_assertions (
  id                 TEXT PRIMARY KEY,
  relation_id        TEXT NOT NULL REFERENCES relations (id) ON DELETE CASCADE,
  source_id          TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  layer              TEXT NOT NULL,
  evidence_class     TEXT NOT NULL,
  inferred           INTEGER NOT NULL DEFAULT 0,
  evidence_json      TEXT NOT NULL DEFAULT '{}',
  confidence         REAL NOT NULL DEFAULT 1.0,
  attributes_json    TEXT NOT NULL DEFAULT '{}',
  generation         INTEGER NOT NULL DEFAULT 0,
  revision           INTEGER NOT NULL DEFAULT 0,
  external_id        TEXT NOT NULL DEFAULT '',
  first_seen_at      INTEGER NOT NULL,
  last_seen_at       INTEGER NOT NULL,
  expires_at         INTEGER,
  tombstoned_at      INTEGER,
  tombstone_revision INTEGER NOT NULL DEFAULT 0,
  UNIQUE (relation_id, source_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_assertions_source ON relation_assertions (source_id, tombstoned_at);
CREATE INDEX ix_assertions_expiry ON relation_assertions (expires_at) WHERE expires_at IS NOT NULL AND tombstoned_at IS NULL;

-- Relation endpoints a source referenced that do not exist (yet): surfaced as "missing relationship target".
CREATE TABLE unresolved_refs (
  id            TEXT PRIMARY KEY,
  source_id     TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  project_id    TEXT NOT NULL,
  type          TEXT NOT NULL,
  from_ref      TEXT NOT NULL,
  to_ref        TEXT NOT NULL,
  from_entity   TEXT,
  external_id   TEXT NOT NULL DEFAULT '',
  first_seen_at INTEGER NOT NULL,
  last_seen_at  INTEGER NOT NULL,
  UNIQUE (source_id, type, from_ref, to_ref)
) STRICT, WITHOUT ROWID;

-- The change log feeds entity history and SSE resume (ADR-009).
CREATE TABLE change_log (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  at          INTEGER NOT NULL,
  project_id  TEXT NOT NULL,
  object_type TEXT NOT NULL CHECK (object_type IN ('entity', 'relation', 'source', 'kind', 'reltype', 'finding', 'project')),
  object_id   TEXT NOT NULL,
  action      TEXT NOT NULL,
  actor_type  TEXT NOT NULL DEFAULT '',
  actor_id    TEXT NOT NULL DEFAULT '',
  source_id   TEXT NOT NULL DEFAULT '',
  summary_json TEXT NOT NULL DEFAULT '{}'
) STRICT;

CREATE INDEX ix_change_object ON change_log (object_id, seq);
CREATE INDEX ix_change_project ON change_log (project_id, seq);
CREATE INDEX ix_change_at ON change_log (at);

-- Snapshot staging (ADR-003): nothing touches the catalog until commit.
CREATE TABLE snapshots (
  id           TEXT PRIMARY KEY,
  source_id    TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  generation   INTEGER NOT NULL,
  complete     INTEGER NOT NULL DEFAULT 1,
  state        TEXT NOT NULL CHECK (state IN ('open', 'committed', 'aborted', 'failed', 'expired')),
  item_count   INTEGER NOT NULL DEFAULT 0,
  error_count  INTEGER NOT NULL DEFAULT 0,
  stats_json   TEXT NOT NULL DEFAULT '{}',
  error        TEXT NOT NULL DEFAULT '',
  created_by   TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  finished_at  INTEGER
) STRICT, WITHOUT ROWID;

CREATE UNIQUE INDEX ux_snapshots_open ON snapshots (source_id) WHERE state = 'open';
CREATE INDEX ix_snapshots_source ON snapshots (source_id, created_at);

CREATE TABLE snapshot_batches (
  snapshot_id TEXT NOT NULL REFERENCES snapshots (id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  request_hash BLOB NOT NULL,
  item_count  INTEGER NOT NULL,
  error_count INTEGER NOT NULL,
  errors_json TEXT NOT NULL DEFAULT '[]',
  received_at INTEGER NOT NULL,
  PRIMARY KEY (snapshot_id, seq)
) STRICT, WITHOUT ROWID;

CREATE TABLE snapshot_items (
  snapshot_id TEXT NOT NULL REFERENCES snapshots (id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  idx         INTEGER NOT NULL,
  item_json   TEXT NOT NULL,
  PRIMARY KEY (snapshot_id, seq, idx)
) STRICT, WITHOUT ROWID;

CREATE TABLE source_runs (
  id           TEXT PRIMARY KEY,
  source_id    TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  trigger      TEXT NOT NULL,                     -- schedule, manual, push, preview, snapshot
  started_at   INTEGER NOT NULL,
  finished_at  INTEGER,
  result       TEXT NOT NULL DEFAULT 'running' CHECK (result IN ('running', 'ok', 'partial', 'error', 'dry_run')),
  stats_json   TEXT NOT NULL DEFAULT '{}',
  diagnostics_json TEXT NOT NULL DEFAULT '[]',
  error        TEXT NOT NULL DEFAULT ''
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_runs_source ON source_runs (source_id, started_at);

-- Full-text search over live entities (maintained by the catalog).
CREATE VIRTUAL TABLE entity_search USING fts5 (
  name, display_name, description, kind, labels, extra,
  tokenize = 'unicode61 remove_diacritics 2 tokenchars ''-_.:/'''
);

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
