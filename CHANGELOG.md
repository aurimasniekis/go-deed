# Changelog

All notable changes are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/)
and the project uses [Semantic Versioning](https://semver.org/) (docs/VERSIONING.md).

## [Unreleased]

### Changed
- Map: layered layouts place every connected component in its own region instead of interleaving unrelated chains;
  the members of an expanded group are drawn together in a container (packed in a grid when they are not related to
  each other) that collapses back into the group; an overview group of a single entity is shown as the entity.

### Fixed
- Map: expanding an overview group of more than 50 entities failed with "expected array length <= 50" (the member
  neighbourhood is now fetched in batches).
- Map: the right-click menu did not close when clicking elsewhere on the map.

## [0.2.0] - 2026-10-26

Upgrading from 0.1.0: schema migration 8 (service accounts) runs at start-up; Deed takes a `VACUUM INTO` snapshot of
the database first, as for every migration.

### Added
- **Link** buttons on *Settings → Account & security* to link a single sign-on identity to the signed-in account
  (previously only reachable by URL). A failed link returns to the account page with the reason instead of the
  sign-in page.
- Service accounts: non-human users for automation that authenticate only with API tokens, bounded by their
  installation role and project memberships. `/api/v1/service-accounts` (with token issue, list and revoke; an admin
  token may manage them so automation can rotate its own credential), a *Service accounts* section on the Users page,
  service accounts in the project member picker, and the host command `deed admin token` for the first credential
  ([docs/SERVICE_ACCOUNTS.md](docs/SERVICE_ACCOUNTS.md)).

### Fixed
- The findings page crashed ("Cannot read properties of undefined (reading 'length')") when there were no open
  findings and no filters.

## [0.1.0] - 2026-10-26

First release. It has not been externally reviewed; see [SECURITY.md](SECURITY.md) and the [threat model](docs/THREAT_MODEL.md).

### Added
- Source-aware catalog: projects and bridges, built-in and custom kinds (JSON Schema 2020-12 with compatibility
  checks), relationship types, layered facets with deterministic field-level merge, provenance, conflicts, change
  log and history.
- Authentication (local Argon2id + TOTP + recovery codes, OIDC SSO with JSON-path group claims, bootstrap and
  break-glass recovery), project and installation roles, user and source tokens, CSRF, audit hash chain.
- Ingest protocol (incremental items, snapshots with mass-removal guard, dry-run, observations), pluggable source
  providers: Terraform state (push, S3, HTTP backend), Pulse admin, Pulse `evt.>` tail, GitHub, HallPass, Porch.
- Pulse integration: outbox relay, ingest consumer, tail rollups; SSE live updates.
- Graph API (neighborhood, expand, path, impact, overview), saved views, custom icons. Graph edges list the sources
  asserting them (primary first); the map can colour relationships by source, and installation admins can change
  (and reset) the colour of built-in relationship types.
- Telemetry integrations and named bindings (Prometheus/Mimir, Loki, NATS monitoring, OpenSearch, Grafana, Pulse
  rollups), traffic overlays, telemetry stream.
- Findings, alerts, version intelligence, dashboard summary.
- Web UI: dashboard, inventory with saved views and bulk edits, entity detail (declared vs observed, provenance,
  relations, history, audit), sources, and the map: layered layout with level-of-detail clustering, edge styles
  encoding evidence, traffic overlays, options drawer, keyboard navigation and right-click quick actions.
- Encrypted backups (age, zstd, signed manifests, GFS retention) and offline restore.
- Go SDK (`pkg/deedclient`), `deed-agent` (Docker), synthetic demo seed.
- Deployment assets (distroless image, compose, systemd), Grafana dashboard and alert rules, Cloudflare guide.

[Unreleased]: https://github.com/aurimasniekis/go-deed/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/aurimasniekis/go-deed/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/aurimasniekis/go-deed/releases/tag/v0.1.0
