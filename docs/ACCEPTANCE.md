# Acceptance matrix

Maps every required capability from the product specification to its implementation, tests and documentation.
Status: **done** (implemented and tested), **partial** (implemented; a listed part is missing), **pending**
(not yet implemented), **external** (needs an owner action or unavailable service). This file is updated with every
slice; nothing is removed to make completion easier.

| # | Capability (spec §) | Implementation | Tests | Docs | Status |
|---|---|---|---|---|---|
| 1 | Discovery doc + ADRs (§3) | `docs/DISCOVERY.md`, `docs/adr/` | — | same | done |
| 2 | Versioned resource model, envelope (§4) | `internal/catalog` | `catalog` unit tests | ADR-002, `docs/CUSTOM_KINDS.md` | pending |
| 3 | Custom kinds with versioned JSON Schemas, evolution rules, limits (§4) | `internal/catalog/kinds.go`, `schema.go` | schema tests, fuzz | `docs/CUSTOM_KINDS.md` | pending |
| 4 | Custom relationship types, endpoint constraints (§4, §7) | `internal/catalog/reltypes.go` | unit | `docs/CUSTOM_KINDS.md` | pending |
| 5 | Layered facts, facets, deterministic merge, provenance (§4) | `internal/catalog/reconcile*.go` | property tests | ADR-003 | pending |
| 6 | Conflicts surfaced (§4) | `conflicts` table, findings | unit | ADR-003 | pending |
| 7 | Snapshots, generations, partial imports, removal guard, dry-run (§4) | `internal/ingest` | property + integration | `docs/INGEST_API.md` | pending |
| 8 | Stale updates cannot resurrect deletions (§4) | revision/tombstone checks | property tests | ADR-003 | pending |
| 9 | "Why does this value exist?" (§4) | provenance API + UI | API + e2e | user guide | pending |
| 10 | Health, freshness, findings, versions modeled separately (§4, §13) | `status` dimensions | unit | ADR-002 | pending |
| 11 | Terraform state import: v4 + show-json, mappings, inference, allowlist, diagnostics, preview (§5A) | `internal/tfimport` | fixtures, fuzz, leak tests | `docs/TERRAFORM_IMPORT.md` | pending |
| 12 | State sources: push, S3, HTTP backend (§5A) | `internal/sources/tfstate` | MinIO + httptest | same | pending |
| 13 | External ingestion API with source tokens, limits, idempotency, backpressure (§5C) | `internal/ingest`, `httpapi` | integration | `docs/INGEST_API.md` | pending |
| 14 | Pulse ingest consumer, admin adapter, tail aggregator (§5C, §7) | `internal/pulseint` | nats-server contract tests | `docs/PULSE.md` | pending |
| 15 | Standalone Terraform provider: resources, data sources, import, timeouts, retries (§6) | `terraform-provider-deed` | unit + acceptance vs real deed | provider docs | pending |
| 16 | Provider: no perpetual diffs, deletion preserves observations, conflicting ownership, schema changes (§6) | provider + ADR-003 | acceptance scenarios | provider docs | pending |
| 17 | Provider release engineering: goreleaser, GPG, registry manifest (§6) | provider `.goreleaser.yml`, workflows | `goreleaser check` | `RELEASING.md` | pending |
| 18 | Relationship types incl. structural vs observed, inferred evidence (§7) | builtin reltypes | unit | ADR-002 | pending |
| 19 | Telemetry adapters: Prometheus/Mimir, Loki, NATS, Pulse, Grafana, OpenSearch (§7) | `internal/telemetry/*` | httptest + compose | `docs/TELEMETRY.md` | pending |
| 20 | Named bindings, validated params, budgets (§7) | `internal/telemetry/bindings.go` | unit | same | pending |
| 21 | Live updates via SSE with resume/resync and bounded buffers (§7) | `internal/live` | integration | ADR-009 | pending |
| 22 | Dashboard view (§8) | `web/src/routes/index.tsx` | Playwright | user guide | pending |
| 23 | Inventory: search, sort, filter, columns, saved views, bulk, URL state, virtualization (§8) | `web/src/routes/inventory` | Playwright | user guide | pending |
| 24 | Entity details: all tabs (§8) | `web/src/routes/entities.$id` | Playwright | user guide | pending |
| 25 | Sources & integrations UI (§8) | `web/src/routes/sources` | Playwright | user guide | pending |
| 26 | Global map: custom React Flow, filters, grouping, layouts, saved views, minimap, breadcrumbs, impact, overlays, legend, keyboard, reduced motion, table equivalent (§8) | `web/src/map` | Playwright + axe | user guide | pending |
| 27 | Light/dark, responsive, accessible, consistent states, synthetic demo banner, no devtools in prod (§8) | `web/` | axe, check-dist | — | pending |
| 28 | Modular monolith, SQLite constraints, migrations, FTS, query plans, retention (§9) | `internal/store` | store tests, plan tests | ADR-001 | pending |
| 29 | Versioned API, pagination, filters, limits, errors, ETags, timeouts, shutdown, health (§9) | `internal/httpapi` | API tests | `api/openapi.json` | pending |
| 30 | Transactional outbox to Pulse (§9) | `internal/outbox`, `internal/events` | relay tests, nats-server | ADR-007 | pending |
| 31 | Porch OIDC + local login + bootstrap + break-glass (§10) | `internal/authn`, `internal/oidc` | OIDC + session tests | ADR-005 | pending |
| 32 | RBAC: viewer/editor/admin, machine clients, sensitive ops (§10) | `internal/authz` | authz matrix tests | ADR-004 | pending |
| 33 | Security hardening: cookies, CSRF, throttling, headers/CSP, link validation, bounds (§10) | `httpx`, `authn` | security tests | THREAT_MODEL | pending |
| 34 | SSRF-safe connectors (§10) | `internal/safehttp` | SSRF tests | ADR-008 | pending |
| 35 | Cloudflare: trusted proxies, origin TLS, bypass prevention docs (§10) | `httpx`, `docs/CLOUDFLARE.md` | forged-header tests | same | pending |
| 36 | Audit of auth, denials, admin, sources, mutations, imports, backups (§10) | `internal/audit` | audit tests | THREAT_MODEL | pending |
| 37 | Prometheus metrics, bounded labels (§11) | `internal/obs/metrics` | metrics tests | `docs/OPERATIONS.md` | pending |
| 38 | Structured ECS-style logs with redaction (§11) | `internal/obs/log` | redaction tests | same | pending |
| 39 | Alert lifecycle → Pulse (§11) | `internal/alerts` | unit + nats | ADR-007 | pending |
| 40 | Sample Grafana dashboards + alert rules (§11) | `deploy/monitoring` | promtool check | same | pending |
| 41 | Encrypted S3 backups, retention, metrics (§12) | `internal/backup` | MinIO integration | `docs/BACKUP_RESTORE.md` | pending |
| 42 | Restore command + runbook, failure cases (§12) | `deed restore` | restore tests (wrong key, corrupt, outage, interrupted) | same | pending |
| 43 | Versions: declared/observed/available, policy, channel, EOL (§13) | `internal/versions` | unit | `docs/FINDINGS.md` | pending |
| 44 | Findings: certificates, missing targets, stale, drift, failed ingestion, unreachable, invalid schema, missing owner, expected-unseen (§13) | `internal/findings` | unit | same | pending |
| 45 | Impact analysis as evidence-based (§13) | `internal/graph/impact.go` | unit | user guide | pending |
| 46 | Test suite incl. race, fuzz, e2e, axe, benchmarks, scanning (§14) | all | CI | `docs/TESTING.md` | pending |
| 47 | Deployment: Docker, compose, systemd, permissions, Cloudflare, metrics docs (§15) | `deploy/`, `Dockerfile` | image smoke test | `docs/DEPLOYMENT.md` | pending |
| 48 | CI + release workflows, SBOM, provenance (§15) | `.github/workflows` | CI | `docs/RELEASING.md` | pending |
| 49 | Docs set: README, architecture, threat model, API, integration authoring, custom kinds, backup runbook, troubleshooting, CONTRIBUTING, SECURITY (§15) | `docs/` | — | — | pending |
| 50 | Demo data clearly labelled (§15) | `deed demo seed` | unit | user guide | pending |
| 51 | End-to-end scenario §18 | `scripts/e2e.sh` | executed | `docs/VALIDATION.md` | pending |
| 52 | Source providers: github, hallpass, porch, docker agent (ADR-013) | `internal/sources/*`, `cmd/deed-agent` | contract fixtures | `docs/INTEGRATIONS.md` | pending |
| 53 | Icon packs + sanitized custom icons (ADR-011b) | `web/scripts/build-icons.mjs`, `internal/icons` | sanitizer tests | `docs/ICONS.md` | pending |
| 54 | Edge styling system + options drawer + legend (ADR-011c) | `web/src/map/styles` | unit + Playwright | user guide | pending |
