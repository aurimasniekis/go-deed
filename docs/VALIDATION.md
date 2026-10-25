# Validation

This file records what was actually run, where, and what it proved. "Simulated" marks behaviour exercised against a
stand-in rather than the real external system.

## Spec §18 end-to-end scenario — `scripts/e2e.sh`

Run 2026-10-25T05:08:48Z (finished) on macOS 15 (Apple Silicon), Docker Desktop 29.8, Go 1.27.3, Terraform 1.11.4,
Python 3. go-deed at `569e2db3228c`, terraform-provider-deed at `5c8537abd98a`
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
| 5 | Install the provider built from terraform-provider-deed (dev_overrides) | PASS | 0.1 s |
| 6 | Declare entities and relationships through Terraform; apply | PASS | 0.3 s |
| 7 | A subsequent plan is a no-op (-detailed-exitcode = 0) | PASS | 0.1 s |
| 8 | Update and import provider-managed resources | PASS | 0.9 s |
| 9 | Ownership, repository and Grafana links (incl. a Grafana API link binding) | PASS | 0.1 s |
| 10 | Ingest Pulse-compatible publisher/subscriber observations (tail + ingest consumer) | PASS | 45.4 s |
| 11 | Merge with the declared entity without losing declared ownership | PASS | 0.0 s |
| 12 | Dashboard finding -> inventory -> detail -> focused graph (API path the UI uses) | PASS | 0.0 s |
| 13 | Filter and group the graph; inspect provenance | PASS | 0.0 s |
| 14 | Operational summaries with freshness and failure states (telemetry, edge rates, sources) | PASS | 15.1 s |
| 15 | Conflict handling, interrupted snapshot and partial import safety | PASS | 0.1 s |
| 16 | Remove a Terraform declaration without deleting independent observations | PASS | 0.2 s |
| 17 | Audit records, metrics and alert delivery (Pulse event) | PASS | 0.0 s |
| 18 | Create an encrypted S3 backup (MinIO) | PASS | 1.0 s |
| 19 | Restore into a clean instance and verify catalog integrity | PASS | 1.2 s |

History: the first run (go-deed `2fb543c`, provider `fd234a2`) passed 19/19. A re-run after the UI merge (`5d026c3`)
failed step 14: an empty Loki answer queried before Loki had indexed the pushed lines was cached for the binding's
whole freshness period (300 s). Fixed in `0487244` (an empty answer is reused for at most 15 s, with a regression
test). The run above is the final one, after all later merges; step 14 includes one 15 s wait because Loki's first
answer was still empty.

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
- **14 Operations:** telemetry results {"broken-up": ["unavailable", null, null], "error-logs": ["measured", 2, null], "grafana-dashboards": ["measured", null, null], "prom-up": ["measured", 1, "ok"]}; edge rate {"status": "measured", "rate": 0.06666666666666667, "unit": "msg/s", "basis": "pulse_tail"};
  an unreachable integration produced 1 `integration_unreachable` finding; source freshness
  {"api": "fresh", "overrides": "fresh", "terraform": "fresh", "ui": "fresh", "agent": "fresh", "pulse-tail": "fresh", "tfstate": "fresh"}.
- **15 Safety:** two imported sources disagreeing on `spec.os` → 1 conflict; an aborted snapshot left
  9/9 entities; a partial import removed 0; a complete import of a reduced state was
  refused: `409 Conflict: commit would remove 8 of 12 live facts (67% > 25% guard); set allow_mass_removal to proceed (mass_removal_refused)`.
- **16 Removal:** after removing the Terraform declaration `service/porch` stayed live, undeclared, with its observed
  facet and publish edge; the declared owner went away with the declaration (by design).
- **17 Audit/metrics/alerts:** audit chain ok over 49 events; 53 `deed_*` metric families on the
  management listener; a critical certificate finding fired alert(s) ['critical Certificate expires within 7 days: certificate/id-example-com'] and Deed's outbox
  delivered `evt.deed.alert.alert.raised` to NATS (received evt.deed.alert.alert.raised type=deed.alert.alert_raised.v1 source=deed bytes=368).
- **18–19 Backup/restore:** encrypted backup to MinIO (61424 bytes, manifest signature verified);
  `deed backup verify` and `deed restore` into a clean data directory with the escrowed KEK; restored counts
  {"entities": 27, "relations": 15} equal the counts at backup time; restored audit chain ok; the owner's old
  session is refused (401); `backup.restored` recorded on first start.

What is simulated here:
- **Pulse:** in this run the traffic is Pulse-compatible (go-pulse client, `EVENTS` stream created like pulsed does,
  CloudEvents headers) against plain NATS JetStream; all three Pulse channels are validated separately against a
  real local pulsed (below).
- **GitHub, HallPass, Porch connectors:** contract fixtures in this run; HallPass and Porch are validated separately
  against real local instances and GitHub against the live API (below).
- **OIDC:** not part of this run; validated separately against a real local Porch (below) and by `internal/oidc`
  tests against an in-process test OP.
- **OpenSearch, Mimir:** fixture tests only.
- **Browser UI:** this script exercises the API paths the UI uses for step 12–13. The browser itself is covered by the
  Playwright + axe suite (`make web-e2e`, below).

## Real services run locally — `scripts/validate/`
Each script builds the real service from its source checkout (a sibling directory by default) and Deed from this
tree, runs both as development-mode processes on random loopback ports with generated credentials in a private
temporary directory, and tears everything down. Porch and HallPass ran 2026-10-25 against go-deed `3789a15`;
Pulse and GitHub against `2088e21`.

### Porch — `scripts/validate/porch.sh` (16/16 PASS)
Porch `99409626cc58` (main), development mode with its bundled test login provider.
- **OIDC SSO** with Porch as the provider (groups claim `porch.groups`, tenant claim `porch.tenant.slug`,
  `required_tenant: home`, JIT on, three group mappings): a full authorization-code flow (Deed start → Porch
  authorize → callback) provisions a user in a mapped group as installation `admin` and project editor and a second
  as `auditor`; a user only in an unmapped group is refused (`not_provisioned`); a user of another tenant reaching
  the client through a share is refused (`tenant_mismatch`); a second login matches on (iss, sub); removing a group
  in Porch removes the role at the next login; a callback with a wrong or missing RFC 9207 `iss` is refused; the
  authorization request carries PKCE S256 and a nonce.
- **Connector:** an installation auditor token is refused by Porch inside tenants and the source test names the
  permission needed; a tenant token (`app:manage` + `service_account:manage`) imports the enabled applications and
  service accounts with `authenticates_with` edges; a DNS record is matched to an application host
  (`protected_by`, inferred); an installation admin token pages across 107 tenants.
- **Found and fixed:** syncs with a tenant token always failed (the connector read `/tenants/{slug}`, which needs
  `tenant:manage`); Deed now logs when the provider grants fewer scopes than requested.
- **Porch behaviour to know** (Porch unchanged): new clients default to `allowed_scopes` without `groups`, so the
  client registration must allow it; Porch has no read-only permission for applications and service accounts.

### HallPass — `scripts/validate/hallpass.sh` (10/10 PASS)
HallPass `a41b41466a21` (main), development mode, with HallPass's `test/openssh` destination container when Docker
is available.
- Agents, servers, roles and an auto-approve policy created through the API; grants requested over the agents' SSH
  and an exec session held open through the destination.
- The first sync with an auditor `hallpass_at_` token matches HallPass exactly (18 entities, 28 edges, grant
  expiries); after deleting a server and an agent, revoking a grant and ending the session, the second sync
  tombstones exactly what HallPass no longer holds (9 entities, 17 edges); a wrong token fails with a clear
  diagnostic; a token allowed to read addresses matches catalog servers to HallPass servers and agent boxes.
- **Found and fixed:** the agent → server access edge was emitted once per grant under one external ID (it could
  expire while a longer grant was active); session channel counts were recorded as 0 although the list API omits
  them.
- Not exercised: interactive PTY sessions and recordings, git-kind servers, human approvals, lockdown states.

### Pulse — `scripts/validate/pulse.sh` (10/10 PASS)
go-pulse `3b2712f065ea` (main): `pulsed init` (operator/JWT mode, internal CA), nats-server v2.15.1 with the generated
configuration over TLS, `pulsed serve`; five Pulse clients with grants created through the admin API (deed, porch,
notifier, billing-agent, rogue-agent); Deed connected with its creds file and the Pulse CA.
- **Admin adapter** (`pulse_admin`, viewer PAT): declared publish/subscribe edges from grants, both durable consumers
  with their owners' `subscribes_to` edges, the stream, contracts and per-subject activity, all from the real admin
  API; the viewer PAT cannot write (403).
- **Tail** (`pulse_tail`): an observed porch → `evt.porch.tenancy.tenant.updated.*` publish edge with its rate.
- **Ingest consumer:** an agent's `deed.ingest.items.v1` envelope lands in its push source; another client's
  envelopes, including one claiming the agent's `ce-source`, are refused and write nothing.
- **Outbox:** Deed's events arrive through pulsed with `Nats-Msg-Id` = outbox ID, `ce-source: deed`, `deed.*.v1`
  types and RFC 3339 `ce-time`; republishing an ID is deduplicated by the stream.
- **Found and fixed:** pulsed authenticates the publish subject but not `ce-source`, which any client may set; the
  ingest consumer trusted `ce-source`, so one client could write into another client's push source. The subject
  token at the ingest filter's wildcard must now equal the push source's `pulse_source`. The admin fixtures were
  rewritten to the real response shapes.
- Not exercised: ACME certificates for NATS, the public listener behind Cloudflare, client rotation or revocation
  while Deed is connected.

### GitHub — `scripts/validate/github.sh` (5/5 PASS)
Live `api.github.com`, anonymous, read-only, repositories `aurimasniekis/go-deed` and `pressly/goose`: the source
test and a sync (120 entities: 2 repositories, 5 releases, 2 deployments, 38 pull requests, 14 issues; 59
`contains`/`deployed_from`/`references` relations) match exactly what the API returns to the same anonymous client,
and a second sync is idempotent (120 unchanged). **Found and fixed:** an explicit `min_rate_remaining: 0` (needed for
the 60-request anonymous budget) and `closed_retention_days: 0` were replaced by their defaults, so anonymous syncs
always stopped as partial runs.

### Grafana Mimir and OpenSearch — `scripts/validate/telemetry.sh` (14/14 PASS)
Docker, images pinned by digest: `grafana/mimir:3.2.1` (monolithic, filesystem storage, multitenancy on) and
`opensearchproject/opensearch:3.8.0` (single node, security plugin on: HTTPS with the demo CA, basic auth, a
least-privilege reader with `read` on two index patterns). Run at go-deed `521219e`.
- Mimir: samples remote-written for two tenants; Deed's scalar, health, `{{window}}`, timeseries (61 points) and table
  bindings equal direct PromQL answers for the configured tenant; another tenant sees only its own data; an unknown
  tenant is "no data"; a missing tenant is a 401 with an `X-Scope-OrgID` hint.
- OpenSearch: scalar counts and date-histogram timeseries equal direct `_count`/`_search` answers, including an index
  whose time field is `epoch_millis`; wrong password 401, foreign index 403, rejected query 400, unmatched index "no
  data".
- Stale: after rotating the reader's password and stopping Mimir, both bindings return the last good value as `stale`.
  No upstream body, credential or tenant detail appears in Deed's errors, integration list or log.
- **Found and fixed:** OpenSearch queries failed with 400 for time fields with a non-default date format; an
  unmatched index pattern was reported as a measured 0 / empty series; a least-privilege reader failed the
  connectivity test (403 on `GET /`) and raised an unreachable finding; a Mimir 401 without a tenant read as bad
  credentials.

### Cloudflare origin shape — `scripts/validate/origin.sh` (17/17 PASS)
The production container image (`deed:local` at go-deed `5174b97`), five hardened production-mode instances on one
Docker network behind an nginx edge standing in for Cloudflare, with throwaway certificates (origin root,
intermediate, `deed.example.com` leaf; an origin-pull CA and client certificate; a rogue CA).
- **Authenticated Origin Pulls:** no client certificate, a certificate from another CA, or one without client-auth
  usage are refused; the origin-pull certificate is accepted on TLS 1.2 and 1.3; TLS 1.0/1.1 refused; leaf +
  intermediate served; HSTS and security headers present; the edge with full verification succeeds and direct
  clients are refused.
- **Client IP:** a trusted edge's forwarded address is honoured and spoofed headers are not; an untrusted hop's
  headers are ignored; `CF-Connecting-IP` counts only from a hop in the Cloudflare ranges even with
  `proxy.cloudflare: true`; login throttling keys on the real client and cannot be evaded by spoofing.
- **Firewall docs:** the nftables ruleset passes `nft -c` and its sets equal Deed's built-in Cloudflare ranges; the
  UFW snippet runs under `ufw --dry-run`; the ruleset loaded into the origin's network namespace blocks direct
  access to 443 and 9464 while the edge still works.
- **Found and fixed (docs):** the minimal production config in DEPLOYMENT.md listened behind a local proxy without
  `trusted_proxies`, so every visitor resolved to 127.0.0.1 (one client's failed logins throttled everyone); the
  Tunnel section wrongly said cloudflared's `CF-Connecting-IP` is honoured; Compose deployments must trust the Docker
  network gateway.
- **Only a real Cloudflare zone can show:** requests from the real ranges, Cloudflare's own header handling and
  origin-pull certificate, Full (strict) with an Origin CA certificate, cloudflared's `X-Forwarded-For`, edge caching,
  SSE through the 100 s proxy timeout, and the firewall on a real host (listed in `docs/CLOUDFLARE.md`).

### S3 implementations other than MinIO — `scripts/validate/s3.sh` (36/36 PASS)
SeaweedFS 4.47 (with and without an SSE-S3 key), Garage v2.4.1 and Versity S3 Gateway v1.8.0 in Docker, images pinned
by digest, at go-deed `b72afe7`. Per implementation: the parameterized integration tests (`TestMinIO`, `TestS3Backend`,
`TestS3Compat`: multipart, SSE-S3, not-found mapping, ListObjectsV2 paging over 1,100 keys, deletes, abort of
incomplete uploads under the prefix only, virtual-host addressing, wrong region and secret), then the real CLI:
wrong-region refusal, a first backup with SSE-S3, a planted interrupted multipart upload and 1,001 keys sorted ahead
of the backups, retention pruning (interrupted upload aborted), `deed backup list` and `verify`, and `deed restore`
into a fresh data directory (escrowed KEK, audit chain and counts verified).
- **Found and fixed:** a wrong region gave a bare "400 Bad Request" (now the server's explanation and a hint); a
  rejected SSE-S3 upload read as an internal error (now says to set `sse: false`); a failed upload left incomplete
  multipart uploads behind until the next successful backup; the Terraform state S3 reader reported S3 error answers
  as a connection failure.
- Behaviour to know: Garage and Versity accept the SSE-S3 header without encrypting; backups are encrypted by Deed
  (age) before upload either way. AWS S3, Cloudflare R2 and Backblaze B2 themselves were not used.

### endoflife.date — `TestLiveEndOfLife` (PASS)
`DEED_TEST_EOL_URL=https://endoflife.date go test -run TestLiveEndOfLife ./internal/versions/` runs the version
checker against the live API: PostgreSQL 12.1 → end of life (cycle 12, latest 12.22), MySQL 5.7.30 → end of life
(latest 5.7.44), PostgreSQL 17.0 → update available (latest 17.11), an unknown product → unknown.

## Other validation
- `go test -race ./...` (CI, against NATS and MinIO containers) — see the CI badge / Actions history.
- `internal/backup` against a local MinIO binary: wrong key, corrupt object, foreign signature, S3 outage and an
  interrupted upload.
- `internal/pulseint` against a local nats-server: tail rollups, observed edges, ingest consumer authorization.
- `deploy/monitoring/check.sh`: Prometheus rules pass `promtool test rules`.
- Web UI: Vitest 86/86; Playwright (Chromium) + axe 73/73 against a fresh, seeded `deed serve` — dashboard →
  finding → inventory → detail → focused graph, admin flows, map interactions, axe scans of every view in light and
  dark, and the render-budget measurements in `docs/UI.md` (run locally 2026-10-25 after the level-of-detail merge;
  also a CI job).
- Docker image build and `scripts/container-smoke.sh` (non-root, KEK permission refusal, healthcheck, Docker health,
  graceful SIGTERM): PASS locally at `5d026c3` and in CI.
- Real-service validation **pending**: production deployments of Pulse, Porch, HallPass, Mimir and OpenSearch (only
  local runs above), GitHub Enterprise Server and private repositories, a real Cloudflare zone in front of a VPS (the origin shape is
  validated locally above), hosted S3 services (AWS S3, R2, B2; four other S3 implementations are validated above).
