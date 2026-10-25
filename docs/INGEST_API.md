# Ingest API (source push protocol)

External programs (CI jobs, agents, scripts, services) write facts into Deed through one **source** with the source
protocol (ADR-006 B). Built-in providers, Terraform imports and the Pulse ingest consumer run through the same
`internal/ingest` service, so every path has the same reconciliation semantics: each source owns one facet per entity
and one assertion per relationship, and Deed merges them into the effective view (ADR-003).

All paths below are under `/api/v1`. Errors are RFC 9457 problems (`application/problem+json`) with a stable `code`.

## Sources and tokens
1. A project admin creates a source with the `push` provider (`POST /sources`):
   ```json
   {"project": "home", "name": "ci-inventory", "provider": "push", "layer": "discovered", "expected_interval_seconds": 3600}
   ```
   `layer` defaults to the provider's layer (`discovered` for `push`); use `observation` for a source that only
   writes observations. `config.pulse_source` lets the same source receive events from Pulse (see `docs/PULSE.md`).
   `policy` holds `on_absent` (`tombstone` default, `orphan`, `keep`), `max_removal_percent`, `allowed_kinds` and
   `default_ttl_seconds`.
2. Issue a token (project admin, recent sign-in). The secret is returned once:
   ```sh
   curl -s -X POST https://deed.example.com/api/v1/sources/src_EXAMPLE/tokens \
     -H "Authorization: Bearer $DEED_ADMIN_TOKEN" -H 'Content-Type: application/json' \
     -d '{"name": "ci", "expires_in_days": 90}' | jq -r .token     # deed_st_EXAMPLE…
   ```
   `expires_in_days` is 1–366 (default 90). `GET /sources/{id}/tokens` lists tokens without secrets;
   `DELETE /sources/{id}/tokens/{token}` revokes one. Deleting the source revokes its tokens.

A source token (`deed_st_…`) may call only the ingest endpoints of its own source, plus `GET /me`,
`GET /system/info` and the Terraform state push. Anything else is `403` (`source tokens may only use the ingest
endpoints of their source`), and another source's ID answers `404 source not found`. A user (session or
`deed_at_…` API token) with the project **editor** role may also call the ingest endpoints. A disabled source
answers `403 source is disabled`.

## Endpoints
| Method and path | Purpose |
|---|---|
| `POST /sources/{id}/items` | incremental upserts and deletes |
| `POST /sources/{id}/snapshots` | begin a snapshot (`201`) |
| `GET /sources/{id}/snapshots/{snapshot}` | snapshot state |
| `PUT /sources/{id}/snapshots/{snapshot}/batches/{seq}` | stage a batch (`seq` 1–1,000,000, idempotent per `seq`) |
| `POST /sources/{id}/snapshots/{snapshot}/commit` | commit or dry-run atomically |
| `POST /sources/{id}/snapshots/{snapshot}/abort` | discard the snapshot (`204`) |
| `POST /sources/{id}/observations` | time-bounded observed values and health (observation-layer sources) |
| `POST /sources/{id}/heartbeat` | liveness without data (`204`) |

## Items
A batch is `{"entities": [...], "relations": [...]}`.

**Entity item:** `ref` (`id`, `external_id`, or `kind` + `name`), `kind`, `name`, `external_id`, `metadata`, `spec`,
`observed`, `health` (`{"state": "ok|warning|failed|unknown", "reason": …}`), `revision`, `observed_at`, `expires_at`,
`delete`. An item needs `ref.id`, `external_id` or `kind` + `name`. When the reference does not resolve, Deed first
tries the kind's identity keys (a unique match converges on the existing entity), then creates the entity from
`kind` + `name` (a lowercase slug). Entity items are always written in the source's project.

**Relation item:** `type`, `from` and `to` refs (a ref may name another `project` for bridge relations), `external_id`,
`attributes`, `evidence`, `inferred`, `confidence` (0–1), `evidence_class` (`declared`, `structural`, `observed`,
`inferred`), `revision`, `observed_at`, `expires_at`, `delete`. Inferred relations must carry `evidence`. A relation
whose endpoint does not exist yet is recorded as **unresolved** and resolves when the entity appears. A relation with
`delete` and only an `external_id` is retracted by that ID.

```sh
curl -s -X POST https://deed.example.com/api/v1/sources/src_EXAMPLE/items \
  -H "Authorization: Bearer deed_st_EXAMPLE…" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: 7f3c2a90-run-1842' -d @- <<'JSON'
{
  "entities": [
    {"kind": "server", "name": "web-1", "external_id": "inv-1001", "revision": 42,
     "metadata": {"environment": "prod", "labels": {"rack": "r1"}},
     "spec": {"hostname": "web-1.example.com", "ipv4": "192.0.2.10"}}
  ],
  "relations": [
    {"type": "runs_on", "from": {"kind": "service", "name": "shop"}, "to": {"external_id": "inv-1001"}}
  ],
  "atomic": false
}
JSON
```

The response lists one result per item (`index`, `type`, `action`, `entity_id`/`relation_id`, `error`) and a
`summary` (`created`, `updated`, `unchanged`, `deleted`, `stale`, `not_found`, `unresolved`, `errors`). Actions are
`created`, `updated`, `unchanged`, `deleted`, `stale`, `not_found` and `unresolved`.

### Per-item errors
Each item runs in its own savepoint, so one bad item never leaves partial writes. A failed item has
`error: {code, message, fields[]}` and the rest are applied. With `"atomic": true` any item error rejects the whole
batch and the response has `"applied": false`. Common item errors: `unknown kind`, `unknown relationship type`,
`name must be a lowercase slug …`, `source policy does not allow kind …`, `facet exceeds 262144 bytes`,
`observed_at is in the future` (more than 5 minutes ahead), `expires_at is in the past`, metadata validation, values
that look like secrets (`looks like a secret; use {"secretRef": …}`), and endpoint-kind constraints of the
relationship type.

### Revisions and staleness
`revision` is an optional, per-source monotonic number. An upsert or delete whose revision is not newer than the
stored revision (or the revision of the deletion) is reported as `stale` and ignored, so delayed or replayed messages
never resurrect deleted facts. Without a revision, an entity item with an `observed_at` older than the stored one is stale;
otherwise Deed assigns the next revision itself. Sending identical facts again only refreshes liveness
(`unchanged`).

### Expiry
`expires_at` must be in the future. Without it, observation-layer facets expire after 3 × `expected_interval_seconds`
(1 hour when unset), and discovered facets after `policy.default_ttl_seconds` when that is set.

## Snapshots
A snapshot replaces what the source knows in one atomic commit.

```sh
API=https://deed.example.com/api/v1/sources/src_EXAMPLE
AUTH="Authorization: Bearer deed_st_EXAMPLE…"
SID=$(curl -s -X POST "$API/snapshots" -H "$AUTH" -H 'Content-Type: application/json' \
  -d '{"complete": true}' | jq -r .id)                       # generation 0 = last committed + 1
curl -s -X PUT "$API/snapshots/$SID/batches/1" -H "$AUTH" -H 'Content-Type: application/json' -d @batch-1.json
curl -s -X PUT "$API/snapshots/$SID/batches/2" -H "$AUTH" -H 'Content-Type: application/json' -d @batch-2.json
curl -s -X POST "$API/snapshots/$SID/commit" -H "$AUTH" -H 'Content-Type: application/json' -d '{"dry_run": true}'
curl -s -X POST "$API/snapshots/$SID/commit" -H "$AUTH" -H 'Content-Type: application/json' -d '{}'
```

- **Begin:** `generation` must be greater than the last committed generation (`0` picks last + 1); otherwise
  `409 stale_generation`. Only one snapshot per source may be open (`409 … is still open for this source; commit or
  abort it first`). An open snapshot expires after `sources.snapshot_timeout` (default 1 h) and its staged items are
  dropped.
- **Batches:** each `PUT` is checked structurally (known kind and relationship type, names, metadata) and staged;
  catalog effects appear only at commit. Re-sending the same `seq` with the same body returns the stored result with
  `"replayed": true`; a different body for an existing `seq` is `409`. Items that fail the structural check are
  listed in the batch's `errors` and fail again at commit. A snapshot holds at most 200,000 items.
- **Commit:** applies every staged item in one transaction. A **complete** snapshot then handles facts of this
  source that it no longer contains according to `policy.on_absent`: `tombstone` removes them, `orphan` keeps them
  and raises an `absent_from_source` finding, `keep` keeps them. A **partial** snapshot (`"complete": false`) never
  removes anything. If any item failed, absent facts are left alone unless `allow_partial_errors` is set. A commit
  fails with `409 stale_generation` when a newer generation was committed meanwhile.
- **Mass-removal guard:** with `tombstone`, a commit that would remove more than `policy.max_removal_percent` (default
  `limits.max_removal_percent`, 25%) of the source's live facts, and more than 3 facts, is refused with
  `409 mass_removal_refused` (`commit would remove N of M live facts …`). The refusal is audited and recorded as a
  failed run; set `"allow_mass_removal": true` to proceed.
- **Dry run:** `{"dry_run": true}` runs the whole commit and rolls it back. The result adds `changes` (what would
  change, at most 500) and `removed` (absent facts with the action that would apply). The snapshot stays open, so
  commit it for real or abort it afterwards.
- **Abort:** discards the staged items; the previous generation stays intact. A refused or failed commit also changes
  nothing and leaves the snapshot open.

The commit result carries `snapshot`, `summary` (including `removed` and `orphaned`), `results` (only errors,
unresolved and stale items, at most 500) and `removed` (at most 500). A successful commit publishes
`evt.deed.ingest.snapshot.committed`.

## Observations and heartbeats
`POST /sources/{id}/observations` with `{"observations": [...]}` (entity items) is accepted only from
observation-layer sources. Each item must reference an existing entity (otherwise `not_found`); only `observed`,
`health`, `revision`, `observed_at` and `expires_at` may be set: a request with `spec` or `metadata` on any item is
refused with `422` (declare those as items).
Observations never create entities.

```sh
curl -s -X POST https://deed.example.com/api/v1/sources/src_EXAMPLE/observations \
  -H "Authorization: Bearer deed_st_EXAMPLE…" -H 'Content-Type: application/json' \
  -d '{"observations": [{"ref": {"kind": "server", "name": "web-1"}, "observed": {"uptime_s": 86400},
       "health": {"state": "ok"}, "expires_at": "2027-01-01T12:00:00Z"}]}'
```

`POST /sources/{id}/heartbeat` (no body) records liveness without data. A source whose last successful sync or
heartbeat is older than 3 × `expected_interval_seconds` raises a `stale_source` finding.

## Idempotency
Any authenticated `POST` may carry `Idempotency-Key` (1–255 printable ASCII characters). A repeat with the same key,
credential, method, path and body within 24 hours gets the stored response with `Idempotent-Replayed: true` instead
of running again. The same key with a different request is `422 idempotency_key_reused`; a repeat while the first is
still running is `409 idempotency_in_progress`. Only 2xx responses are stored, so a failed request can be retried
with the same key. Snapshot batches (`PUT`) are idempotent by `seq` instead.

## Limits and backpressure
| Setting | Default | Effect |
|---|---|---|
| `limits.ingest_max_items` | 1000 (1–10,000) | items per `items` request, batch or observations request (`422 a request carries 1..N items`) |
| `limits.ingest_max_bytes` | 4 MiB | request body cap on ingest routes (items, snapshots, observations, heartbeat) |
| `limits.state_max_bytes` | 64 MiB | request body cap of `POST /sources/{id}/terraform/state` |
| `limits.ingest_per_source_rpm` | 600 | token bucket per source (burst: one sixth, at least 10) |
| `limits.max_removal_percent` | 25 | default mass-removal guard |
| `limits.max_entities` | 200,000 | creating beyond it fails with `the catalog reached its configured limit …` |
| `sources.snapshot_timeout` | 1h (1m–24h) | open snapshots expire |

Every ingest call (including Terraform state pushes) counts against the per-source rate limit. When it is exhausted Deed answers
`429 rate_limited` (`ingest rate limit for this source exceeded; retry after a few seconds`) with `Retry-After: 5`.
When the database is busy the answer is `503 unavailable` (`the database is busy; retry shortly`), also with
`Retry-After: 5`. Clients should back off and retry; with an `Idempotency-Key` a retried `POST` is safe.

## Go SDK
`pkg/deedclient` implements the protocol for Go programs:

```go
c, err := deedclient.New("https://deed.example.com", os.Getenv("DEED_SOURCE_TOKEN"), "src_EXAMPLE")
res, err := c.Sync(ctx, deedclient.Batch{Entities: entities, Relations: relations}, deedclient.CommitOptions{})
```

`Items`, `Observations`, `Heartbeat`, `Begin`, `PutBatch`, `Commit` and `Abort` map to the endpoints above. `Sync`
sends a complete snapshot (begin, batches of `WithBatchSize`, default 500 and at most 1000, commit) and aborts it on
failure. The client retries connection errors, 429 and 502–504 with jittered backoff (`WithRetries`, default 5),
honours `Retry-After` (capped at 5 minutes), reuses one `Idempotency-Key` across the retries of a request, never
follows redirects and never logs request bodies. `WithCAFile` adds a CA bundle. `cmd/deed-agent` is built on it.

## Metrics and audit
`deed_ingest_items_total{provider,action}`, `deed_ingest_rejected_total{provider,reason}` (`stale`, `invalid`,
`unresolved`), `deed_reconcile_operations_total{op,outcome}` and `deed_reconcile_duration_seconds{op}`. Item batches,
snapshot commits, refusals and aborts are recorded in the audit log (`ingest.items`, `ingest.snapshot_committed`,
`ingest.snapshot_aborted`); each write also updates the source's `last_run_at`, `last_success_at` and `last_error`.

## Validation status
Covered by `internal/ingest` tests (complete and partial snapshots, mass-removal guard, interrupted snapshots, batch
replay and generations, dry run, atomic batches, out-of-order revisions), `internal/httpapi/ingest_test.go`
(the HTTP protocol) and `pkg/deedclient` tests.
