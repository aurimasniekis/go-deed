-- 0004_operations: findings, alerts, saved views, custom icons, telemetry integrations and bindings, Pulse tail
-- rollups, version checks and backup runs (ADR-007, ADR-008, ADR-010, ADR-011b).

-- +goose Up
CREATE TABLE findings (
  id            TEXT PRIMARY KEY,
  dedup_key     TEXT NOT NULL UNIQUE,            -- type + subject
  type          TEXT NOT NULL,
  severity      TEXT NOT NULL CHECK (severity IN ('critical', 'warning', 'info')),
  class         TEXT NOT NULL CHECK (class IN ('confirmed', 'warning', 'unknown', 'stale', 'not_applicable')),
  project_id    TEXT,
  entity_id     TEXT,
  relation_id   TEXT,
  source_id     TEXT,
  title         TEXT NOT NULL,
  message       TEXT NOT NULL DEFAULT '',
  evidence_json TEXT NOT NULL DEFAULT '{}',
  state         TEXT NOT NULL CHECK (state IN ('open', 'closed')),
  acknowledged_at INTEGER,
  acknowledged_by TEXT,
  first_seen_at INTEGER NOT NULL,
  last_seen_at  INTEGER NOT NULL,
  closed_at     INTEGER
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_findings_open ON findings (state, severity, project_id);
CREATE INDEX ix_findings_entity ON findings (entity_id, state);
CREATE INDEX ix_findings_type ON findings (type, state);

CREATE TABLE alert_rules (
  id              TEXT PRIMARY KEY,
  name            TEXT NOT NULL UNIQUE,
  description     TEXT NOT NULL DEFAULT '',
  enabled         INTEGER NOT NULL DEFAULT 1,
  matcher_json    TEXT NOT NULL,                 -- {types, min_severity, projects, labels}
  for_seconds     INTEGER NOT NULL DEFAULT 0,
  cooldown_seconds INTEGER NOT NULL DEFAULT 3600,
  severity        TEXT NOT NULL DEFAULT '' CHECK (severity IN ('', 'critical', 'warning', 'info')),
  builtin         INTEGER NOT NULL DEFAULT 0,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL,
  version         INTEGER NOT NULL DEFAULT 1
) STRICT, WITHOUT ROWID;

CREATE TABLE alerts (
  id               TEXT PRIMARY KEY,
  rule_id          TEXT NOT NULL REFERENCES alert_rules (id) ON DELETE CASCADE,
  finding_id       TEXT NOT NULL,
  dedup_key        TEXT NOT NULL UNIQUE,         -- rule + finding dedup key
  project_id       TEXT,
  state            TEXT NOT NULL CHECK (state IN ('pending', 'firing', 'resolved', 'suppressed')),
  severity         TEXT NOT NULL,
  title            TEXT NOT NULL,
  evidence_json    TEXT NOT NULL DEFAULT '{}',
  started_at       INTEGER NOT NULL,
  fired_at         INTEGER,
  resolved_at      INTEGER,
  last_notified_at INTEGER,
  notifications    INTEGER NOT NULL DEFAULT 0,
  silenced_by      TEXT,
  updated_at       INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_alerts_state ON alerts (state, severity);

CREATE TABLE silences (
  id           TEXT PRIMARY KEY,
  matcher_json TEXT NOT NULL,
  comment      TEXT NOT NULL DEFAULT '',
  created_by   TEXT NOT NULL,
  starts_at    INTEGER NOT NULL,
  ends_at      INTEGER NOT NULL,
  created_at   INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE saved_views (
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL CHECK (kind IN ('inventory', 'map', 'dashboard')),
  name        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  project_id  TEXT REFERENCES projects (id) ON DELETE CASCADE,
  owner_id    TEXT NOT NULL,
  shared      INTEGER NOT NULL DEFAULT 0,
  state_json  TEXT NOT NULL DEFAULT '{}',
  layout_json TEXT NOT NULL DEFAULT '{}',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  version     INTEGER NOT NULL DEFAULT 1
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_views_owner ON saved_views (owner_id, kind);
CREATE INDEX ix_views_project ON saved_views (project_id, kind) WHERE shared = 1;

CREATE TABLE custom_icons (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL UNIQUE,
  svg        BLOB NOT NULL,
  sha256     TEXT NOT NULL,
  monochrome INTEGER NOT NULL DEFAULT 0,
  created_by TEXT NOT NULL,
  created_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- Telemetry integrations (ADR-008): connection settings, sealed credentials and the outbound allowlist.
CREATE TABLE integrations (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  adapter      TEXT NOT NULL,
  config_json  TEXT NOT NULL DEFAULT '{}',
  secrets_enc  BLOB,
  enabled      INTEGER NOT NULL DEFAULT 1,
  last_test_at INTEGER,
  last_test_ok INTEGER,
  last_error   TEXT,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  version      INTEGER NOT NULL DEFAULT 1
) STRICT, WITHOUT ROWID;

-- Named bindings: admin-authored query templates with typed parameters bound to entity fields.
CREATE TABLE bindings (
  id             TEXT PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE,
  title          TEXT NOT NULL,
  integration_id TEXT NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
  kinds_json     TEXT NOT NULL DEFAULT '[]',
  query          TEXT NOT NULL,
  params_json    TEXT NOT NULL DEFAULT '[]',
  shape          TEXT NOT NULL CHECK (shape IN ('scalar', 'timeseries', 'table', 'link')),
  unit           TEXT NOT NULL DEFAULT '',
  thresholds_json TEXT NOT NULL DEFAULT '{}',
  freshness_seconds INTEGER NOT NULL DEFAULT 300,
  role           TEXT NOT NULL DEFAULT 'chart' CHECK (role IN ('chart', 'health', 'edge_rate', 'link')),
  relationship_type TEXT NOT NULL DEFAULT '',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  version        INTEGER NOT NULL DEFAULT 1
) STRICT, WITHOUT ROWID;

-- Pulse tail rollups: counts per (publisher source, subject template, type) per bucket; never raw messages.
CREATE TABLE pulse_rollups (
  source_id      TEXT NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
  granularity    TEXT NOT NULL CHECK (granularity IN ('minute', 'hour')),
  bucket         INTEGER NOT NULL,
  publisher      TEXT NOT NULL,
  subject        TEXT NOT NULL,
  ce_type        TEXT NOT NULL,
  messages       INTEGER NOT NULL,
  bytes          INTEGER NOT NULL,
  sampled        INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (source_id, granularity, bucket, publisher, subject, ce_type)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_pulse_rollups_time ON pulse_rollups (granularity, bucket);

-- Version intelligence (§13): available versions and end-of-life data with provenance.
CREATE TABLE version_checks (
  entity_id     TEXT PRIMARY KEY REFERENCES entities (id) ON DELETE CASCADE,
  product       TEXT NOT NULL DEFAULT '',
  cycle         TEXT NOT NULL DEFAULT '',
  latest        TEXT NOT NULL DEFAULT '',
  latest_source TEXT NOT NULL DEFAULT '',
  eol           TEXT NOT NULL DEFAULT '',
  eol_date      TEXT NOT NULL DEFAULT '',
  support_until TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'unknown' CHECK (status IN ('current', 'update_available', 'eol', 'unknown', 'not_applicable')),
  evidence_json TEXT NOT NULL DEFAULT '{}',
  checked_at    INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE backup_runs (
  id             TEXT PRIMARY KEY,
  started_at     INTEGER NOT NULL,
  finished_at    INTEGER,
  result         TEXT NOT NULL DEFAULT 'running' CHECK (result IN ('running', 'ok', 'error')),
  trigger        TEXT NOT NULL,
  object_key     TEXT NOT NULL DEFAULT '',
  size_bytes     INTEGER NOT NULL DEFAULT 0,
  plain_sha256   TEXT NOT NULL DEFAULT '',
  schema_version INTEGER NOT NULL DEFAULT 0,
  error          TEXT NOT NULL DEFAULT ''
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_backup_runs_time ON backup_runs (started_at);

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
