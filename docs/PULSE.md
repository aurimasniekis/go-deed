# Pulse integration

Deed talks to Pulse, the NATS JetStream event bus, in four ways (ADR-006 C, ADR-007):

| Part | Direction | What it does |
|---|---|---|
| Outbox relay | Deed → Pulse | publishes Deed's own domain events (`evt.deed.>`) after their transaction committed |
| `pulse_admin` source | Pulse admin API → catalog | pull provider: clients, grants, streams, durable consumers, subject activity |
| `pulse_tail` source | Pulse → catalog | read-only `evt.>` subscription that counts messages into observed `publishes` edges and rollups |
| Ingest consumer | Pulse → catalog | durable subscription accepting `deed.ingest.*.v1` envelopes for push sources |

Pulse is optional. A Pulse outage never blocks start-up or catalog writes: every connection retries every 30
seconds and events wait in the outbox.

## Configuration

```yaml
pulse:
  enabled: true
  url: tls://events.example.com:4222
  creds_file: /etc/deed/pulse.creds
  ca_file: /etc/deed/pulse-ca.pem
  source: deed
  publish: true
  ingest: true
  ingest_filter: evt.deed.ingest.>
  ingest_durable: deed_ingest
```

| Key | Default | Meaning |
|---|---|---|
| `pulse.enabled` | `false` | Connect to Pulse at all. The relay, tail and ingest consumer run only when enabled. |
| `pulse.url` | — | `nats://` or `tls://` URL; production mode requires `tls://` |
| `pulse.creds_file` | — | Pulse client `.creds` file issued for Deed; required in production mode |
| `pulse.ca_file` | — | extra CA for the Pulse TLS endpoint |
| `pulse.source` | `deed` | CloudEvents source (`ce-source`) of Deed's events; a lowercase slug |
| `pulse.publish` | `true` | run the outbox relay; when `false`, events stay in the outbox |
| `pulse.ingest` | `true` | run the ingest consumer |
| `pulse.ingest_filter` | `evt.deed.ingest.>` | subject filter of the ingest consumer; must start with `evt.` and contain a wildcard, whose token names the publisher (see [Ingest consumer](#ingest-consumer)) |
| `pulse.ingest_durable` | `deed_ingest` | durable consumer name, `^[a-z0-9][a-z0-9_]{0,47}$` (a Pulse durable grant name; the consumer is `<client-id>_<name>`) |
| `retention.outbox` | `168h` | how long published and dead-lettered outbox rows are kept |
| `retention.telemetry_minute` / `telemetry_hour` | `168h` / `2160h` | how long tail rollups are kept |

Every key can be overridden with `DEED_PULSE_*` environment variables (`DEED_PULSE_URL`, …).

**Pulse clients and grants.** Create Deed's client in Pulse (Clients page or `pulse clients create`) with:

| Grant | For |
|---|---|
| `publish:evt.deed.>` | the outbox relay |
| `subscribe:evt.>` | `pulse_tail` sources (narrow it to match the tails' `filter`) |
| `durable` named `deed_ingest` on `evt.deed.ingest.>` | the ingest consumer (`pulse.ingest_durable`, `pulse.ingest_filter`) |

An agent that feeds the push source with `pulse_source: billing` gets `publish:evt.deed.ingest.billing.>` and nothing
else under `evt.deed.`. `pulse.ca_file` is Pulse's internal CA (System page, "Connect → Internal CA", or `connect.ca_cert`
in `GET /api/admin/v1/system/info`) as long as the NATS listener serves the bootstrap certificate.

Without `creds_file` (development mode only) Deed connects to an unauthenticated NATS, such as the one in
`deploy/compose.dev.yaml`, and wraps it in the same Pulse client. Deed opens up to three named connections:
`publisher` (relay), `tail` and `ingest`; `deed_pulse_connected{conn}` reports each.

## Outbox relay
Domain changes insert an `outbox` row in the same SQLite transaction as the change, so an event exists exactly when
the change committed. Payloads hold IDs, names, kinds and change summaries only: `outbox.Enqueue` rejects any payload
key that looks secret, and the project ID is added as `project_id` when the event belongs to a project.

| Subject | Type | Emitted when |
|---|---|---|
| `evt.deed.catalog.entity.{created,updated,revived,tombstoned}` | `deed.catalog.entity_<action>.v1` | an entity's effective state changes (pure observation updates are not published) |
| `evt.deed.catalog.relation.{created,removed}` | `deed.catalog.relation_<action>.v1` | a relationship appears or disappears |
| `evt.deed.catalog.project.{created,deleted}` | `deed.catalog.project_<action>.v1` | project lifecycle |
| `evt.deed.catalog.source.deleted` | `deed.catalog.source_deleted.v1` | a source is deleted |
| `evt.deed.catalog.kind.{created,updated,version_published,deleted}` | `deed.catalog.kind_<action>.v1` | custom kind changes |
| `evt.deed.catalog.reltype.{created,updated,deleted,color_updated,color_reset}` | `deed.catalog.reltype_<action>.v1` | custom relationship type changes; map colour changes of any type (`color_reset`: a built-in type back to its default) |
| `evt.deed.ingest.snapshot.committed` | `deed.ingest.snapshot_committed.v1` | a source snapshot commits (counts of created/updated/removed/errors) |
| `evt.deed.findings.finding.{opened,closed}` | `deed.findings.finding_<action>.v1` | the findings engine opens or closes a finding |
| `evt.deed.alert.alert.{raised,resolved}` | `deed.alert.alert_<action>.v1` | alert notifications ([FINDINGS.md](FINDINGS.md)) |

The relay polls every 2 seconds and publishes up to 100 due rows per round, draining backlogs without waiting. Each
event is published with the outbox ID as its CloudEvents ID (`Nats-Msg-Id`) and the row's creation time, so
consumers can deduplicate retries on the event ID.

- A failed publish is retried after 2^(n-1) seconds with ±50 % jitter, capped at 5 minutes. The round stops at
  the first failure (Pulse is probably unreachable).
- A row is dead-lettered after 50 attempts or 24 hours. More than 100,000 pending rows dead-letters the oldest.
- An `outbox_maintain` job (every minute) enforces the backlog bound, deletes published and dead rows older than
  `retention.outbox` and exports the backlog metrics.
- Any dead-lettered event in the last 24 hours opens an `events_dead_lettered` finding.

Owners, admins and auditors see the outbox state (pending, dead, published in 24 h, oldest pending, last error)
in `GET /api/v1/dashboard` under `outbox`.

## `pulse_admin` source
A pull provider (default schedule 300 s, layer `imported`) that reads the Pulse admin API at
`<admin_url>/api/admin/v1`: `/system/info`, `/clients`, `/streams`, `/streams/{name}/consumers` and
`/subjects/tree`. The credential is a **viewer** PAT, stored as the sealed secret `token`; Pulse accepts PATs only
on its admin listener.

```json
{
  "provider": "pulse_admin", "project": "platform", "name": "pulse-admin",
  "config": {"admin_url": "http://127.0.0.1:8080", "insecure_http": true},
  "secrets": {"token": "EXAMPLE-VIEWER-PAT"}
}
```

| Config key | Default | Meaning |
|---|---|---|
| `admin_url` | required | base URL of the admin listener; `http://` is refused unless `insecure_http` |
| `stream` | `EVENTS` | stream whose subject index is walked |
| `subject_prefix` | `evt` | only grants and subjects under this prefix are imported |
| `max_subjects` | `2000` (10–20000) | subject-index nodes visited; hitting the cap makes the run partial |
| `allow_cidrs` | — | private CIDRs the admin URL may resolve to (SSRF guard) |
| `insecure_http` | `false` | allow `http://`, intended for a loopback SSH tunnel |
| `include_internal_consumers` | `false` | also import pulsed's own durables (outbound webhook consumers `pulse_out_…`, flagged `internal`) |

Only durable consumers are imported: ephemeral ones (the tail's, explorer sessions, pulsed's own rollup reader) are
skipped. Stream and consumer settings are copied as Pulse reports them (`storage: File`, `retention: Limits`,
`ack_policy: AckExplicit`).

What it models:
- the Pulse server as a `nats_system` (health `failed` when pulsed is not ready or NATS is disconnected);
- every non-revoked client as a `service` with `spec.pulse_client_id`; a status other than `active` is a warning;
- publish/request grants as `publishes` and subscribe/reply grants as `subscribes_to` edges to `nats_subject`
  entities (evidence class **declared**, basis `pulse_grant`);
- streams (`nats_stream`, `contains`-ed by the system) and durable consumers (`nats_consumer`, `contains`-ed by
  the stream) with `subscribes_to` edges for each filter subject. A durable named `<clientID>_<name>` also gives
  that client a `subscribes_to` edge (basis `consumer_config`);
- leaf subjects of the subject index, templated (below), with retained message/byte counts, last seen time and last
  type; the last type becomes an `event_contract` `carried_on` the subject (evidence **observed**, basis
  `subject_index_last_type`);
- wildcard grants and filters `contains` the concrete templates they match (evidence **structural**).

Pulse does not record which client published a message, so the admin source never produces observed publishers;
those come from the tail.

## `pulse_tail` source
A tail source has no schedule: while `pulse.enabled` is on, Deed keeps one subscription per enabled `pulse_tail`
source (re-checked every 30 seconds). The subscription is read-only and ephemeral (only new messages, no durable,
no acks). Messages are counted per `(ce-source, subject template, ce-type)`; message bodies are never stored.
A subscribe grant lets a client create ephemeral consumers but not delete them, so when a tail stops NATS logs a
`Publish Violation` for `$JS.API.CONSUMER.DELETE.EVENTS.…` and removes the consumer after its inactivity threshold.

| Config key | Default | Meaning |
|---|---|---|
| `filter` | `evt.>` | subject filter |
| `window_seconds` | `60` (15–3600) | aggregation window |
| `max_keys` | `5000` (10–50000) | distinct keys per window; further keys are counted as overflow and the window is marked sampled |

At the end of each window the tail:
1. adds the counts to `pulse_rollups` minute and hour buckets (used by the `pulse` telemetry adapter and traffic
   overlays, see [TELEMETRY.md](TELEMETRY.md)) and records a heartbeat for the source;
2. writes, through the normal ingest path, a `service` per publisher (`spec.pulse_source` = the ce-source, so it
   merges with a service that declares the same `pulse_source`), a `nats_subject` per template with
   `messages_per_minute`, an `event_contract` per type, and **observed** `publishes` and `carried_on` edges. The
   evidence carries the window, message and byte counts, rate, `sampled` flag and last seen time.

Every tail fact expires after three windows (at least 10 minutes), so edges disappear when traffic stops. A
message without a ce-source is counted under the publisher `unknown`. The tail sees every message on the filter,
including Deed's own events and ingest envelopes.

**Subject templates.** Tokens after the first that look like identifiers become `*`: ULIDs, UUIDs, numbers, hex
strings of 8+ characters containing a digit, prefixed IDs such as `ten_01jb9w2v3x4y5z6a`, and 24+ character
tokens containing a digit. At most 16 tokens are kept (the rest become `>`). For example
`evt.app.user.signed_in.550e8400-e29b-41d4-a716-446655440000` becomes `evt.app.user.signed_in.*`.

## Ingest consumer
External programs can feed a **push** source over Pulse instead of HTTP. The consumer subscribes to
`pulse.ingest_filter` as the durable `pulse.ingest_durable` (an ordered, only-new subscription when Deed has no
Pulse client identity) with at most 10 deliveries per message.

| Event type | Envelope fields | Equivalent API call |
|---|---|---|
| `deed.ingest.items.v1` | `source_id`, `entities`, `relations`, `atomic` | `POST /api/v1/sources/{id}/items` |
| `deed.ingest.observations.v1` | `source_id`, `observations` | `POST /api/v1/sources/{id}/observations` |
| `deed.ingest.heartbeat.v1` | `source_id` | `POST /api/v1/sources/{id}/heartbeat` |

Items use the same shapes and limits as the HTTP API ([INGEST_API.md](INGEST_API.md)): at most
`limits.ingest_max_items` (default 1000) items per event, and an event body of at most 8 MiB. Snapshots are not
available over Pulse. Observations need a source in the `observation` layer.

```json
{"source_id": "src_EXAMPLE", "atomic": false,
 "entities": [{"kind": "service", "name": "billing", "spec": {"pulse_source": "billing"}}]}
```

published on, for example, `evt.deed.ingest.billing.items` with type `deed.ingest.items.v1`.

**Authorization.** An event is applied only when the source named by `source_id` exists, is enabled, uses the
`push` provider, and its config `pulse_source` is set and equals both the event's ce-source and the subject token at
the first wildcard of `pulse.ingest_filter` (`billing` in `evt.deed.ingest.billing.items`). Pulse authenticates the
subject, through the publisher's grants, but not ce-source: any client may set any ce-source. Binding the subject
token is what keeps a client granted `publish:evt.deed.ingest.rogue.>` out of the `billing` source. `pulse_source`
must therefore be a subject token (`[a-z0-9_-]+`). Configure it when creating the source:

```sh
curl -s -X POST https://deed.example.com/api/v1/sources -H "Authorization: Bearer $DEED_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"project": "platform", "name": "billing-agent", "provider": "push", "config": {"pulse_source": "billing"}}'
```

A Pulse client can therefore write only into sources bound to a subject token its grants allow it to publish on, so
grant agents `publish:evt.deed.ingest.<pulse_source>.>` and never `evt.deed.ingest.>`. Events whose
ce-source equals `pulse.source` (Deed's own outbox events, such as `evt.deed.ingest.snapshot.committed`) are skipped.
Writes are audited with the source as actor.

**Failures.** Malformed envelopes, unknown sources, unsupported types, oversized events, validation errors and
publishers that are not allowed are **terminal**: logged (`ingest event rejected`), counted as
`deed_pulse_consumed_total{result="terminal"}` and never redelivered. Storage errors are returned to Pulse and
redelivered with backoff (`result="retry"`). Per-item errors in an items event do not fail the event; with
`atomic: true` the whole batch is rejected when any item fails.

## What is measured versus inferred

| Fact | Evidence | Basis |
|---|---|---|
| client `publishes` / `subscribes_to` subject | declared | a grant in the Pulse admin API (permission, not traffic) |
| consumer / client `subscribes_to` subject | declared | durable consumer filter subjects |
| pattern `contains` template | structural | wildcard match |
| contract `carried_on` subject (admin) | observed | last type in the subject index (one sample per subject) |
| publisher `publishes` subject (tail) | observed | messages counted in the window; `sampled: true` when keys overflowed |
| `subscribes_to` rate | inferred | the subject's publish rate; Pulse records no per-subscriber delivery |

A declared `publishes` or `subscribes_to` edge that the tail has not observed within 24 hours opens an
`expected_connection_unseen` finding, but only in projects with a fresh `pulse_tail` source.

## Metrics

| Metric | Meaning |
|---|---|
| `deed_outbox_events{state}` | pending and dead outbox rows |
| `deed_outbox_publish_total{result}` | publish attempts: `ok`, `error`, `dead` |
| `deed_outbox_oldest_pending_age_seconds` | age of the oldest unpublished event |
| `deed_pulse_connected{conn}` | 1 when the `publisher`, `tail` or `ingest` connection is up |
| `deed_pulse_consumed_total{result}` | ingest events: `ok`, `terminal`, `retry` |
| `deed_pulse_tail_messages_total{outcome}` | tail messages: `counted`, `overflow` |

`deploy/monitoring/deed-alerts.yaml` alerts on an outbox older than 15 minutes, dead-lettered events and a Pulse
connection down for 10 minutes.

## Validation status
`scripts/validate/pulse.sh` validates all four parts against a real pulsed built from source (`PULSE_DIR`, default
`../pulse`; no Docker): `pulsed init`, nats-server in operator/JWT mode with TLS from Pulse's internal CA and
`pulsed serve` on random loopback ports, Pulse clients created through the admin API (Deed, a publisher, a subscriber
with a durable, two push agents), a viewer PAT for `pulse_admin`, and Deed connecting with its own `.creds`. It
checks the declared grant and durable relationships, observed tail edges, ingest into a push source (and the refusal
of a spoofed ce-source), Deed's outbox events with their CloudEvents headers and de-duplication by message ID. Results
go to `$VAL_DIR/results.json`, with the admin API responses in `pulse-admin-responses.json`; secrets are generated in
a 0700 work directory and removed afterwards. Last run: go-pulse `3b2712f`, nats-server 2.15.1, 10/10 checks passed.

Unit and integration tests: `pulse_admin` against fixtures recorded from that run's response shapes
(`internal/pulseint/testdata`); the tail and ingest consumer against a local `nats-server` with JetStream (started
from `PATH`, or the server named by `DEED_TEST_NATS`; skipped when neither is available).
