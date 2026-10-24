-- 0002_identity: users, local credentials, MFA, OIDC identities, sessions, throttles, setup links, API tokens,
-- projects and project membership (ADR-004, ADR-005).

-- +goose Up
CREATE TABLE users (
  id                TEXT PRIMARY KEY,
  email             TEXT COLLATE NOCASE UNIQUE,
  display_name      TEXT NOT NULL,
  status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  installation_role TEXT CHECK (installation_role IN ('owner', 'admin', 'auditor')),
  role_source       TEXT NOT NULL DEFAULT 'manual',
  created_at        INTEGER NOT NULL,
  updated_at        INTEGER NOT NULL,
  last_login_at     INTEGER
) STRICT, WITHOUT ROWID;

CREATE TABLE local_credentials (
  user_id        TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  password_phc   TEXT NOT NULL,
  params_version INTEGER NOT NULL,
  changed_at     INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE mfa_totp (
  user_id      TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  secret_enc   BLOB NOT NULL,
  confirmed_at INTEGER NOT NULL,
  last_step    INTEGER NOT NULL DEFAULT 0
) STRICT, WITHOUT ROWID;

CREATE TABLE mfa_totp_pending (
  user_id    TEXT PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
  secret_enc BLOB NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE recovery_codes (
  code_mac   BLOB PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  used_at    INTEGER
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_recovery_user ON recovery_codes (user_id);

CREATE TABLE identities (
  id            TEXT PRIMARY KEY,
  user_id       TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  provider_slug TEXT NOT NULL,
  issuer        TEXT NOT NULL,
  subject       TEXT NOT NULL,
  email         TEXT,
  groups_json   TEXT NOT NULL DEFAULT '[]',
  created_at    INTEGER NOT NULL,
  last_login_at INTEGER,
  UNIQUE (issuer, subject)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_identities_user ON identities (user_id);

CREATE TABLE sessions (
  id_mac              BLOB PRIMARY KEY,
  public_id           TEXT NOT NULL UNIQUE,
  user_id             TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  csrf_secret         BLOB NOT NULL,
  auth_method         TEXT NOT NULL,
  provider_slug       TEXT NOT NULL DEFAULT '',
  auth_at             INTEGER NOT NULL,
  mfa_at              INTEGER,
  limited             INTEGER NOT NULL DEFAULT 0,
  needs_enroll        INTEGER NOT NULL DEFAULT 0,
  created_at          INTEGER NOT NULL,
  last_seen_at        INTEGER NOT NULL,
  absolute_expires_at INTEGER NOT NULL,
  ip                  TEXT NOT NULL DEFAULT '',
  user_agent          TEXT NOT NULL DEFAULT '',
  revoked_at          INTEGER,
  revoke_reason       TEXT
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_sessions_user ON sessions (user_id);
CREATE INDEX ix_sessions_expiry ON sessions (absolute_expires_at);

CREATE TABLE throttles (
  key             TEXT PRIMARY KEY,
  failures        INTEGER NOT NULL,
  next_allowed_at INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

-- Single-use links: bootstrap (first owner), recovery (break-glass), invite and reset.
CREATE TABLE setup_tokens (
  id         TEXT PRIMARY KEY,
  token_mac  BLOB NOT NULL UNIQUE,
  purpose    TEXT NOT NULL CHECK (purpose IN ('bootstrap', 'recovery', 'invite', 'reset')),
  user_id    TEXT REFERENCES users (id) ON DELETE CASCADE,
  email      TEXT,
  role       TEXT,
  created_by TEXT,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at    INTEGER
) STRICT, WITHOUT ROWID;

CREATE TABLE oidc_states (
  state_mac     BLOB PRIMARY KEY,
  provider_slug TEXT NOT NULL,
  secrets_enc   BLOB NOT NULL,
  purpose       TEXT NOT NULL,
  return_to     TEXT NOT NULL DEFAULT '/',
  session_mac   BLOB,
  created_at    INTEGER NOT NULL,
  expires_at    INTEGER NOT NULL
) STRICT, WITHOUT ROWID;

CREATE TABLE projects (
  id           TEXT PRIMARY KEY,
  name         TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL,
  description  TEXT NOT NULL DEFAULT '',
  allow_http_links INTEGER NOT NULL DEFAULT 0,
  synthetic    INTEGER NOT NULL DEFAULT 0,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  version      INTEGER NOT NULL DEFAULT 1
) STRICT, WITHOUT ROWID;

CREATE TABLE project_members (
  project_id  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  user_id     TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
  role        TEXT NOT NULL CHECK (role IN ('viewer', 'editor', 'admin')),
  role_source TEXT NOT NULL DEFAULT 'manual',
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (project_id, user_id)
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_members_user ON project_members (user_id);

-- Bridges permit relations across two projects (ADR-004).
CREATE TABLE bridges (
  id            TEXT PRIMARY KEY,
  from_project  TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  to_project    TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  allowed_types TEXT NOT NULL DEFAULT '[]',
  description   TEXT NOT NULL DEFAULT '',
  created_at    INTEGER NOT NULL,
  UNIQUE (from_project, to_project)
) STRICT, WITHOUT ROWID;

-- API tokens (deed_at_) act for their creator with a role subset; source tokens (deed_st_) ingest into one source.
CREATE TABLE api_tokens (
  id               TEXT PRIMARY KEY,
  token_mac        BLOB NOT NULL UNIQUE,
  kind             TEXT NOT NULL CHECK (kind IN ('user', 'source')),
  name             TEXT NOT NULL,
  user_id          TEXT REFERENCES users (id) ON DELETE CASCADE,
  source_id        TEXT,
  installation_role TEXT CHECK (installation_role IN ('admin', 'auditor')),
  project_role     TEXT CHECK (project_role IN ('viewer', 'editor', 'admin')),
  projects_json    TEXT NOT NULL DEFAULT '[]',
  created_by       TEXT,
  created_at       INTEGER NOT NULL,
  expires_at       INTEGER NOT NULL,
  last_used_at     INTEGER,
  last_used_ip     TEXT,
  revoked_at       INTEGER
) STRICT, WITHOUT ROWID;

CREATE INDEX ix_tokens_user ON api_tokens (user_id);
CREATE INDEX ix_tokens_source ON api_tokens (source_id);

-- +goose Down
SELECT 'forward-only migrations: restore the pre-migration snapshot instead';
