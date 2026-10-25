# Validation

This file records what was actually run, where, and what it proved. "Simulated" marks behaviour exercised against a
stand-in rather than the real external system.

## Spec §18 end-to-end scenario — `scripts/e2e.sh`

Run 2026-10-25T00:01:41Z on macOS 15 (Apple Silicon), Docker Desktop 29.8, Go 1.27.3, Terraform 1.11.4, Python 3.
go-deed at `2fb543c7b1a5` (plus the working-tree changes committed with this file), terraform-provider-deed at `fd234a24d26a`
built from source and installed with `dev_overrides`. Dependencies from `deploy/compose.dev.yaml`: NATS 2.15.1 (JetStream),
MinIO (Chainguard image), Prometheus 3.5.0, Loki 3.5.5, Grafana 12.2.0. Deed ran as a host process in development mode
(plain http on loopback, SQLite in a fresh data directory). **Result: 19/19 steps passed; planted-secret scan
of every database, WAL, log and JSON artefact: 0 hits.**

| # | Step | Result | Time |
|---|---|---|---|
| 1 | Deploy Deed locally with SQLite | PASS | 0.0 s |
| 2 | Authenticate (bootstrap link, local password + TOTP; API token) | PASS | 0.2 s |
| 3 | Register a custom kind | PASS | 0.0 s |
| 4 | Import a sanitized Terraform state (push source, CLI) | PASS | 0.1 s |
| 5 | Install the provider built from terraform-provider-deed (dev_overrides) | PASS | 0.3 s |
| 6 | Declare entities and relationships through Terraform; apply | PASS | 0.2 s |
| 7 | A subsequent plan is a no-op (-detailed-exitcode = 0) | PASS | 0.1 s |
| 8 | Update and import provider-managed resources | PASS | 0.8 s |
| 9 | Ownership, repository and Grafana links (incl. a Grafana API link binding) | PASS | 0.1 s |
| 10 | Ingest Pulse-compatible publisher/subscriber observations (tail + ingest consumer) | PASS | 45.4 s |
| 11 | Merge with the declared entity without losing declared ownership | PASS | 0.0 s |
| 12 | Dashboard finding -> inventory -> detail -> focused graph (API path the UI uses) | PASS | 0.0 s |
| 13 | Filter and group the graph; inspect provenance | PASS | 0.0 s |
| 14 | Operational summaries with freshness and failure states (telemetry, edge rates, sources) | PASS | 0.0 s |
| 15 | Conflict handling, interrupted snapshot and partial import safety | PASS | 0.1 s |
| 16 | Remove a Terraform declaration without deleting independent observations | PASS | 0.2 s |
| 17 | Audit records, metrics and alert delivery (Pulse event) | PASS | 0.0 s |
| 18 | Create an encrypted S3 backup (MinIO) | PASS | 1.0 s |
| 19 | Restore into a clean instance and verify catalog integrity | PASS | 1.2 s |

Evidence highlights (from `results.json`):
- **2 Authentication:** bootstrap link → local password; the session is limited until TOTP enrollment
  (`/projects` → 403); 10 recovery codes issued; a wrong TOTP on a fresh login → 403;
  an admin API token (`deed_at_…`) drives the rest of the run.
- **4 Terraform state import:** dry-run then import of `internal/tfimport/testdata/state-v4.json` through a push source
  with a source token (`deed import terraform`): 12 entities created, unsupported resources reported as
  diagnostics, planted secrets found: 0.
- **6–8 Provider:** apply of 6 resources; `plan -detailed-exitcode` = 0; display-name update applied; a declaration
  created outside the configuration imported with `terraform import deed_entity.notifier home/service/notifier`
  (first plan 2 = drops the unmanaged display name, then 0).
- **9 Links:** owner `platform`, `owned_by` relation, repository + dashboard links; a Grafana service-account token from
  the dev Grafana, a `grafana` integration (Grafana 12.2.0 reachable (database ok)) and a link binding returning
  ['http://127.0.0.1:33000/d/deed-overview/deed'].
- **10–11 Pulse:** a `pulse_tail` source observed `porch -> evt.porch.tenancy.tenant.updated.star` (observed); an agent
  published an ingest envelope consumed into a push source (`pulse_source` must match `ce-source`). The declared
  `service/porch` kept its owner (provenance: `terraform`/declared) while gaining
  a `declared/discovered` facet set.
- **14 Operations:** telemetry results {"broken-up": ["unavailable", null, null], "error-logs": ["measured", 10, null], "grafana-dashboards": ["measured", null, null], "prom-up": ["measured", 1, "ok"]}; edge rate {"status": "measured", "rate": 0.06666666666666667, "unit": "msg/s", "basis": "pulse_tail"};
  an unreachable integration produced 1 `integration_unreachable` finding; source freshness
  {"api": "fresh", "overrides": "fresh", "terraform": "fresh", "ui": "fresh", "agent": "fresh", "pulse-tail": "fresh", "tfstate": "fresh"}.
- **15 Safety:** two imported sources disagreeing on `spec.os` → 1 conflict; an aborted snapshot left
  9/9 entities; a partial import removed 0; a complete import of a reduced state was
  refused: `lict: commit would remove 8 of 12 live facts (67% > 25% guard); set allow_mass_removal to proceed (mass_removal_refused)`.
- **16 Removal:** after removing the Terraform declaration `service/porch` stayed live, undeclared, with its observed
  facet and publish edge; the declared owner went away with the declaration (by design).
- **17 Audit/metrics/alerts:** audit chain ok over 48 events; 52 `deed_*` metric families on the
  management listener; a critical certificate finding fired alert(s) ['critical Certificate expires within 7 days: certificate/id-example-com'] and Deed's outbox
  delivered `evt.deed.alert.alert.raised` to NATS (received evt.deed.alert.alert.raised type=deed.alert.alert_raised.v1 source=deed bytes=368).
- **18–19 Backup/restore:** encrypted backup to MinIO (54256 bytes, manifest signature verified);
  `deed backup verify` and `deed restore` into a clean data directory with the escrowed KEK; restored counts
  {"entities": 18, "relations": 6} equal the counts at backup time; restored audit chain ok; the owner's old
  session is refused (401); `backup.restored` recorded on first start.

What is simulated here:
- **Pulse:** the traffic is Pulse-compatible (go-pulse client, `EVENTS` stream created like pulsed does, CloudEvents
  headers) against plain NATS JetStream; a real pulsed deployment (accounts, grants, admin API) was not used.
  The Pulse admin adapter is validated only against fixtures shaped by pulsed's admin OpenAPI.
- **GitHub, HallPass, Porch connectors:** contract fixtures shaped by their published API descriptions; no live
  instances in this run.
- **OIDC:** not part of this run (covered by `internal/oidc` tests with a test OP); real Porch SSO pending.
- **OpenSearch, Mimir:** fixture tests only.
- **Browser UI:** this script exercises the API paths the UI uses for step 12–13; Playwright + axe browser tests are
  part of the web suite.

## Other validation
- `go test -race ./...` (CI, against NATS and MinIO containers) — see the CI badge / Actions history.
- `internal/backup` against a local MinIO binary: wrong key, corrupt object, foreign signature, S3 outage and an
  interrupted upload.
- `internal/pulseint` against a local nats-server: tail rollups, observed edges, ingest consumer authorization.
- `deploy/monitoring/check.sh`: Prometheus rules pass `promtool test rules`.
- Docker image build and `scripts/container-smoke.sh`: run in CI once the web UI is merged (the image embeds it).
- Real-service validation **pending**: production Pulse (pulsed), Porch SSO and API, HallPass API, GitHub API,
  Mimir, OpenSearch, Cloudflare in front of a VPS, S3 providers other than MinIO.
