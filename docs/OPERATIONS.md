# Operations

Running Deed day to day: what lives on disk, how writes are serialized, background jobs, retention, the CLI, keys,
the audit log, health, logs, metrics, upgrades and troubleshooting. Deployment options are in docs/DEPLOYMENT.md,
backups in docs/BACKUP.md, telemetry integrations in docs/TELEMETRY.md.

## Data directory
`data_dir` (default `/var/lib/deed`, must be absolute) is created with mode 0700. An existing directory must be
owned by the user Deed runs as and have no group or other permission bits; database files are tightened to 0600 on
every open.

| Path | Content |
|---|---|
| `deed.db` | the SQLite database (WAL mode, `synchronous=FULL`, foreign keys on, incremental auto-vacuum) |
| `deed.db-wal`, `deed.db-shm` | SQLite write-ahead log and shared memory; part of the live database, never delete them while it is in use |
| `deed.lock` | exclusive `flock` held for the life of the process; contains the PID |
| `backups/pre-migrate-<from>-<to>-<time>.db` | `VACUUM INTO` snapshot taken before migrations are applied |
| `deed.db.pre-restore-<time>` | the database replaced by `deed restore` |
| `deed.db.restore` | staged database of a restore in progress; Deed refuses to start while it exists |
| `dev-kek` | development mode only: generated KEK when `kek.file` is empty |

The data directory must be on local storage: NFS, SMB/CIFS, FUSE, 9p, Ceph, AFS, Lustre and similar filesystems are
refused (`storage.allow_network_fs` overrides this in development mode only). In production the KEK file must live
outside `data_dir`. Local `backups/` snapshots are **not** off-host backups; configure docs/BACKUP.md.

## Single-writer model
One process owns a data directory; a second `deed serve` or any offline CLI command fails to take `deed.lock`.
Inside the process, all writes go through one writer connection using `BEGIN IMMEDIATE`; a transaction that hits
`SQLITE_BUSY` is retried up to 5 times with jittered backoff (10 ms doubling to 1 s), and `busy_timeout` is 5 s.
Reads use a separate read-only pool of `max(4, GOMAXPROCS)` connections. Every pooled connection is checked at
start for the required pragmas. Write transactions are kept short and never perform network I/O; side effects such
as live-update notifications and audit file lines run only after commit.

Scale up, not out: there are no replicas.

## Migrations
Migrations are embedded in the binary and run automatically on `deed serve` (and `deed migrate`) under the lock:
1. A database newer than the binary is refused (no downgrades).
2. If the database already exists and migrations are pending, a `VACUUM INTO` snapshot is written to
   `backups/pre-migrate-<from>-<to>-<time>.db`; the newest `storage.pre_migration_snapshots` (default 3) are kept.
3. Migrations are applied. On failure the database stays at the last successful version and the error names the
   snapshot directory.

`deed migrate -config deed.yaml` applies pending migrations without serving and prints
`schema version N (latest M)`.

## Background jobs
Interval jobs run once at start and then on their interval. Failures are logged (`background job failed`,
`event.action: job.<name>`) and counted in `deed_jobs_runs_total{job,result}`; the job runs again next interval.

| Job | Interval | Work |
|---|---|---|
| `expire_facts` | 30 s | recompute entities and relations whose facets or assertions expired (500 per run) |
| `freshness` | 5 min | recompute freshness of entities with runtime facets |
| `snapshot_expiry` | 1 min | abort snapshots begun but not committed within `sources.snapshot_timeout` (1 h) |
| `findings` | 2 min | evaluate findings, then alerts (docs/FINDINGS.md) |
| `findings_prune` | 24 h | delete closed findings older than `retention.closed_findings` |
| `catalog_metrics` | 1 min | export entity, relation and source-freshness gauges |
| `outbox_maintain` | 1 min | dead-letter the oldest events beyond 100,000 pending; delete published or dead events older than `retention.outbox` |
| `compact` | 6 h | delete tombstoned entities, relations, facets and assertions older than `retention.tombstones`; prune the change log, expired idempotency keys and old source runs |
| `checkpoint` | 10 min | passive WAL checkpoint, then return up to 2000 free pages to the filesystem |
| `sessions_prune` | 1 h | delete expired and revoked sessions, expired OIDC states, setup links, pending TOTP enrollments and stale throttles |
| `pulse_rollups_prune` | 1 h | delete `pulse_tail` rollups beyond the telemetry retention |
| `versions` | `versions.interval` (12 h) | version checks, when `versions.enabled` |

Long-running loops: `backup` (checks every minute, docs/BACKUP.md), `scheduler` (pull sources, when
`sources.scheduler_enabled`, `sources.concurrency` at a time), and, when Pulse is enabled, `pulse_relay`,
`pulse_tail` and `pulse_ingest` (docs/PULSE.md). Pulse connection failures never block start-up; each connection
retries every 30 s.

## Retention
Every value is a duration between 1 h and 10 years.

| Key | Default | Applies to |
|---|---|---|
| `retention.tombstones` | `720h` (30 d) | how long deleted entities and relations stay as tombstones before compaction |
| `retention.change_log` | `8760h` (365 d) | change-log rows |
| `retention.outbox` | `168h` (7 d) | published and dead-lettered outbox events |
| `retention.source_runs` | `720h` (30 d) | source run history |
| `retention.telemetry_minute` | `168h` (7 d) | per-minute Pulse tail rollups |
| `retention.telemetry_hour` | `2160h` (90 d) | per-hour Pulse tail rollups |
| `retention.closed_findings` | `2160h` (90 d) | closed findings |

The audit log has no retention setting and is never pruned.

## CLI
Every command reads `-config` (default `$DEED_CONFIG`). Commands marked *offline* open the data directory and need
the lock (stop the server first) and the KEK.

| Command | Purpose |
|---|---|
| `deed version [-json]` | build information |
| `deed serve` | run the server |
| `deed migrate` | apply pending migrations (offline) |
| `deed config check` | validate configuration and print it with inline secrets redacted |
| `deed openapi` | print the OpenAPI 3.1 document |
| `deed healthcheck [-url …]` | probe `/readyz` (default `http://127.0.0.1:9464/readyz`); exit 1 when not ready |
| `deed keys init -out FILE` | write a new KEK (mode 0400; the file must not exist) |
| `deed keys status` | KEK ID and key versions per purpose (offline) |
| `deed keys rotate -purpose field\|token\|backup-meta` | new active key version (offline) |
| `deed keys rotate-kek -new-kek FILE` | re-wrap every data key under a new KEK (offline) |
| `deed admin bootstrap` | single-use link creating the first owner, valid `auth.setup_token_ttl` (30 min) (offline) |
| `deed admin recover -email …` | break-glass login link, 15 min, audited, raises a critical finding (offline; docs/SSO.md) |
| `deed admin create-project -name … [-display-name …]` | create a project (offline) |
| `deed backup keygen\|run\|list\|verify\|verify-key` | encrypted backups (docs/BACKUP.md) |
| `deed restore` | restore a backup (offline; docs/BACKUP.md) |
| `deed import terraform` | push a Terraform state to a running server (docs/TERRAFORM_IMPORT.md) |
| `deed audit verify` | verify the audit hash chain (offline); exit 3 when it fails |
| `deed demo seed [-scale N]` | create the labelled synthetic demo project (offline) |

Because `deed admin bootstrap` is offline, run it **before** the first `deed serve` (it creates and migrates the
database), then start the server and open the link within its validity.

## Keyring and KEK rotation
A 32-byte KEK (base64 in a 0400 file) wraps per-purpose data keys stored in the database:

| Purpose | Used for |
|---|---|
| `field` | sealed columns: integration and source credentials, TOTP secrets, OIDC login state, sensitive audit details |
| `token` | keyed MACs of sessions, API and source tokens, recovery codes |
| `backup-meta` | signing key of backup manifests |

`kek.source` is `file` (default; `kek.file`), `credential` (systemd `LoadCredential=`; `kek.file` names the
credential) or `env` (`DEED_KEK`, discouraged). A KEK file readable by group or others, or owned by a user other
than Deed's or root, is refused (systemd credentials may be group-readable).

- **Purpose rotation:** `deed keys rotate -purpose field` creates a new active version; the previous one becomes
  decrypt-only, so existing sealed values stay readable. Values are not re-encrypted, and there is no CLI to retire
  old versions. After rotating `token`, API tokens, source tokens and recovery codes still match (lookups try every
  non-retired version), but browser sessions and unused setup links (bootstrap, recovery, invite, reset)
  are looked up with the active key only, so everyone signs in again and outstanding links must be issued again.
  Rotating `backup-meta` changes the backup verification key (docs/BACKUP.md).
- **KEK rotation:** stop Deed, then
  ```sh
  deed keys init -out /etc/deed/kek.new
  deed keys rotate-kek -config /etc/deed/deed.yaml -new-kek /etc/deed/kek.new
  ```
  Every data key is re-wrapped in one transaction and verified. Point `kek.file` at the new file, start Deed and take
  a backup (`POST /api/v1/backups`): older backups still escrow the old KEK.

Both rotations are audited (`keyring.rotated`, `keyring.kek_rotated`).

## Audit log
Every security-relevant action is appended to `audit_events` in the same transaction as the change. Each row stores
`SHA-256(prev_hash || row)`, so deleted, reordered or modified rows are detectable. This is tamper-evident, not
tamper-proof: someone with write access to the database can rewrite the whole chain. Ship a copy off-host with
`log.audit_file` (one ECS 8.11 JSON line per committed event, `event.dataset: deed.audit`, mode 0600, append; `-`
writes to stdout).

- `GET /api/v1/audit` lists records (filters: `project`, `target_type`, `target_id`, `actor`, `action` prefix,
  `outcome`, `before`, `limit` ≤ 500). Owners, admins and auditors see everything; project admins their projects.
- `POST /api/v1/audit/verify` (owner, admin, auditor) or `deed audit verify` (offline) walks the chain and returns
  `events`, `ok`, and on failure `broken_at` and `problem` (`sequence gap … (events deleted)`, `prev_hash does not
  match …`, `hash mismatch (event modified)`).

## Health
The management listener (`management.listen`, default `127.0.0.1:9464`; keep it private) serves:

| Path | Meaning |
|---|---|
| `GET /healthz` | liveness: `ok` while the process serves HTTP |
| `GET /readyz` | `ready`, or 503 `not ready: <reason>` when the keyring is not loaded, storage health is not `ok`, or the database does not answer within 2 s |
| `GET /metrics` | Prometheus exposition |
| `/debug/pprof/…` | only with `management.pprof: true`; refused on a non-loopback address in production |

Storage health degrades on a writer busy for more than 30 s, a full disk or an I/O error, and recovers on the next
successful write (disk full: once a probe write can allocate space). Three consecutive I/O errors or detected
corruption mark it `failed` until restart. `GET /api/v1/system/info` (public) reports version and sign-in options.

On SIGINT/SIGTERM Deed marks itself not ready, shuts down HTTP (15 s), stops background loops, checkpoints the WAL
and releases the lock.

## Logs
Logs go to stderr as JSON (`log.format: text` for development) at `log.level` (`info`). Attribute names follow ECS:
`event.action`, `event.outcome`, `error.message`, `http.request.method`, `url.path`, `http.response.status_code`,
`event.duration_ms`, `source.ip`, `http.request.id`, `http.route`. Request logs are at debug level (`log.access_log:
true` raises them to info; 5xx are always warn) and never contain query strings, bodies or credentials. Attributes
whose key names a secret (password, token, secret, cookie, authorization, …) are replaced with `[REDACTED]`.

## Metrics
All metrics are prefixed `deed_`. Labels are bounded (HTTP routes are grouped as `ingest`, `stream`, `graph`,
`auth`, `api`, `assets`, `ui`).

| Area | Metrics |
|---|---|
| Process | `ready`, `build_info{version,go_version}`, Go runtime and process collectors |
| Storage | `store_tx_duration_seconds{tx,result}`, `store_tx_busy_retries_total`, `store_writer_wait_seconds`, `store_writer_queue`, `store_db_bytes`, `store_wal_bytes` |
| HTTP and auth | `http_requests_total{route,method,code}`, `http_request_duration_seconds{route}`, `auth_logins_total{method,outcome}`, `authz_denied_total{reason}`, `audit_events_total{outcome}` |
| Catalog and sources | `catalog_entities{group}`, `catalog_relations`, `reconcile_operations_total`, `reconcile_duration_seconds`, `ingest_items_total`, `ingest_rejected_total`, `source_runs_total`, `source_last_success_timestamp_seconds`, `source_freshness_state` |
| Jobs, outbox, Pulse | `jobs_runs_total{job,result}`, `outbox_events{state}`, `outbox_publish_total{result}`, `outbox_oldest_pending_age_seconds`, `pulse_connected{conn}`, `pulse_consumed_total`, `pulse_tail_messages_total` |
| Other | `findings_open`, `alerts_firing`, `alerts_transitions_total`, `live_*`, `cache_requests_total`, telemetry (docs/TELEMETRY.md), backup (docs/BACKUP.md) |

`deploy/monitoring/` holds a Grafana dashboard and Prometheus alert rules (`DeedDown`, `DeedNotReady`,
`DeedHigh5xxRate`, `DeedSlowWriteTransactions`, `DeedWriterContention`, `DeedSourceStale`, `DeedSourceRunsFailing`,
`DeedOutboxBacklog`, `DeedOutboxDeadLetters`, `DeedPulseDisconnected`, `DeedTelemetryIntegrationDown`,
`DeedBackupMissing`, `DeedBackupFailing`, `DeedCriticalAlertsFiring`, `DeedLiveClientsResyncing`).

## Upgrades
1. Take a backup (`POST /api/v1/backups`) and note the current version (`deed version`).
2. Replace the binary or image and restart. Pending migrations run after the pre-migration snapshot.
3. To roll back after a migration: stop Deed, move `deed.db`, `deed.db-wal` and `deed.db-shm` aside, copy the
   matching `backups/pre-migrate-*.db` to `deed.db` (mode 0600, owned by the service user) and start the previous
   binary. Changes made after the snapshot are lost; restoring a backup (docs/BACKUP.md) is the alternative.

`/api/v1` changes are additive (docs/VERSIONING.md).

## Troubleshooting

| Message | Meaning |
|---|---|
| `another deed process owns this data directory (…/deed.lock)` | a server (or another CLI command) holds the lock; stop it for offline commands, or use the API |
| `data directory … is on a nfs filesystem; a writable SQLite database requires local storage` | move `data_dir` to local storage |
| `… has mode 0755; it must not be accessible by group or others …` / `… is owned by uid N but deed runs as uid M` | fix ownership and permissions of `data_dir` or the KEK file |
| `master key (KEK) not available: … does not exist (generate one with …)` | `kek.file` is wrong or missing |
| `master key (KEK) does not match this data directory: keys are wrapped by KEK … but the configured KEK is …` | wrong KEK for this database (for example after a restore, use the restored KEK) |
| `database schema is newer than this binary: database at version N, binary supports up to M; …` | run a newer `deed`, or restore a snapshot or backup |
| `migration failed (database left at the last successful version; pre-migration snapshot in …)` | fix the cause and restart, or roll back as above |
| `storage corrupt: … reported: …; restore from the latest verified backup` | the start-up `quick_check` failed; restore (docs/BACKUP.md) |
| `a restore was interrupted (…/deed.db.restore exists); rerun deed restore or remove the file` | finish or abandon the restore |
| `invalid configuration:` followed by a list | every validation problem is listed; fix them all |
| `unknown environment override(s): DEED_…` | a misspelt or unsupported `DEED_*` variable |
| `/readyz`: `not ready: storage degraded: database writer busy for more than 30s` | sustained write contention; see `deed_store_*` metrics |
| `/readyz`: `not ready: storage degraded: database disk full` | free disk space; readiness recovers by itself |
| `/readyz`: `not ready: storage failed: repeated database I/O errors` or `… database corruption detected` | check the disk, restart; restore if corruption persists |
| log `pulse unavailable; retrying in 30s` | Pulse unreachable; events wait in the outbox |
| log `outbox backlog limit exceeded; oldest events dead-lettered` | more than 100,000 events could not be published |
| log `background job failed` | see `event.action` (`job.<name>`) and `error.message` |
