# ADR-006: Ingestion paths

## A. Terraform state (read-only)
- Parsers: raw state v4 (`version: 4`) and `terraform show -json` (`format_version` 1.x), streaming decode with a
  64 MiB cap. Raw state is never stored, logged or echoed in errors.
- Data-driven mappings (embedded `internal/tfimport/mappings/*.yaml` + per-source custom mappings): resource type →
  kind, name template, identity keys, **allowlisted** attribute paths → envelope/spec fields, icon, relationship
  inference rules (`dependencies`, attribute joins such as `cloudflare_record.content == hcloud_server.ipv4_address`).
  `sensitive_attributes` paths are dropped even when allowlisted; secret-looking values (PEM, private keys, long
  high-entropy strings in non-allowlisted shapes) are refused.
- External identity: `<module address>.<type>.<name>[<index>]` plus the provider's `id` attribute.
- Unsupported resources: counted per type in diagnostics, nothing persisted.
- Sources: push (`POST /api/v1/sources/{id}/terraform/state`, `deed import terraform`), S3 reader (minio-go,
  `GetObject` only), Terraform HTTP backend reader (`GET`). Every import has a preview (dry-run diff).
- Imported facts are an `imported` facet per entity; each import is a complete snapshot of that source with
  `on_absent: tombstone` and the mass-removal guard.

## B. Source protocol (external API)
`/api/v1/sources/{id}/…` authenticated with a source token or an editor's API token:
`POST items` (incremental), `POST snapshots` (begin) → `PUT snapshots/{sid}/batches/{seq}` → `POST
snapshots/{sid}/commit|abort`, `POST heartbeat`, `POST observations` (telemetry summaries with `expires_at`).
Limits: ≤ 1,000 items and ≤ 4 MiB per request; `Idempotency-Key`; per-item structured errors; `429` with
`Retry-After` when the per-source rate limit is exceeded and `503` with `Retry-After` when the database is busy.

## C. Pulse
- (a) **Ingest consumer**: durable `deed_ingest` on `evt.deed.ingest.>`; envelopes `deed.ingest.items.v1`,
  `deed.ingest.observations.v1`, `deed.ingest.heartbeat.v1` mirroring the API payloads, attributed to the push source
  named in the payload, which accepts the event only when its `pulse_source` config equals the publisher's `ce-source`.
  Decode/validation failures are `Terminal`; transient store errors NAK with backoff.
- (b) **Admin adapter** (`pulse_admin` provider): `GET /api/admin/v1/clients`, `/streams`, `/streams/{n}/consumers`,
  `/subjects/tree`. Clients → `service` (or mapped existing entity); grants → *declared* `publishes`/`subscribes_to`
  edges to `nats_subject` (pattern) entities; durables → *declared* `subscribes_to` via consumer config; subject
  activity → `observation` facets on subject entities.
- (c) **Tail aggregator** (`pulse_tail` provider): read-only ordered subscription to `evt.>` (no durable, no acks),
  per-minute counts by `(ce-source, subject template, ce-type)`; flushed every minute as *observed* `publishes`
  assertions with `expires_at = now + max(3×window, 10 min)` and rollup rows. Raw messages are never persisted.
- Subject templating: tokens matching ULID, UUID, numeric, hex ≥ 8, or base32 IDs become `*`; subjects are cut at 16 tokens (ending in `>`).
- UI labels each edge's evidence: *declared grant*, *consumer config*, *observed message (sampled)*.
