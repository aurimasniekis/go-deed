# Deed — discovery and decision log

This document records the reconnaissance of the existing ecosystem and every architectural, product, operational
and security question that shaped Deed. Each question lists why it matters, the evidence found in existing
repositories, the default adopted, trade-offs, whether the decision is reversible and whether it blocked
implementation. Consequential answers are promoted to ADRs in [`docs/adr/`](adr/).

Status legend: **Resolved** (default adopted and implemented), **Owner decision** (answered by the project owner
during planning), **Open** (non-blocking; tracked in [ACCEPTANCE.md](ACCEPTANCE.md)).

---

## Part A — Reconnaissance

### A.1 References inspected

| Reference | Location | What it is | Used for |
|---|---|---|---|
| Porch (`go-porch`) | `~/Projects/Golang/porch` (public: `github.com/aurimasniekis/go-porch`) | Identity provider / OIDC OP + SSO portal, Go + React, SQLite | Primary code template: store, config, httpx, audit, keyring, outbox relay, webui, Huma API, backup |
| HallPass (`go-hallpass`) | `~/Projects/Golang/hallpass` (public) | SSH access broker with operator console | Operator auth model (Argon2id + TOTP + recovery codes), OIDC RP flow, CSRF, throttling, JSON Schema validation library choice |
| Pulse (`go-pulse`) | `~/Projects/Golang/pulse` (public) | Event bus on NATS JetStream with admin API | Go client `pkg/pulse`, envelope + subject grammar, admin API for clients/grants/consumers/subjects, conventions for metrics listener, CI, Dockerfile |
| terraform-provider-porch / -hallpass / -pulse | `~/Projects/Golang/terraform-provider-*` | Plugin Framework providers | Provider template: client, helpers, acceptance harness booting the real app binary, docs via tfplugindocs |
| Pulse deployment repository | private | Ansible/Terraform deploying Pulse on one VPS | Only general conventions (loopback admin listener, metrics on 127.0.0.1:9464, Cloudflare proxy). No details are copied into Deed's public repositories. |

### A.2 Relevant patterns found

- **SQLite discipline** (Porch/HallPass `internal/store`): modernc (pure Go, `CGO_ENABLED=0`); one writer connection
  with `_txlock=immediate`; `query_only` reader pool; WAL; `synchronous=FULL`; `foreign_keys=1`; `trusted_schema=0`;
  verified pragmas on every pooled connection; flock on the data directory; refusal on NFS/SMB/FUSE; embedded
  forward-only goose migrations; a `VACUUM INTO` snapshot before migrating; refusal to run against a schema newer
  than the binary; busy retry with jittered backoff; health tracking (busy/full/IO/corrupt).
- **Configuration** (Porch `internal/config`): YAML with `KnownFields(true)`, environment overrides derived from the
  YAML path (`DEED_HTTP_LISTEN`), unknown prefixed environment variables are an error, `mode: production|development`
  with production refusing insecure settings.
- **HTTP edge** (`internal/httpx`): client IP from trusted proxies with a right-to-left X-Forwarded-For walk,
  `CF-Connecting-IP` only when the hop is in Cloudflare ranges *and* Cloudflare mode is on; per-request CSP nonce
  injected into the SPA's `index.html`; `no-store` for API paths; server-generated request IDs.
- **Audit** (`internal/audit`): SHA-256 hash chain appended in the same transaction as the change; refuses
  secret-looking keys in plaintext details; sealed sensitive details; ECS JSON mirror sink. Porch correctly calls
  this tamper-*evident*, not tamper-proof.
- **Keyring** (`internal/keyring`): a KEK (file, systemd credential or env) wraps per-purpose AES-256-GCM data keys
  with versions (active/decrypt-only/retired); MACs for token lookup; a canary detects a wrong KEK.
- **Outbox → Pulse** (Porch `internal/outbox`, `internal/events/relay.go`): rows written in the domain transaction;
  relay publishes with `pulse.WithID(outboxID)`; backoff with jitter; dead-letter after 50 attempts or 24 h.
- **API** (Porch `internal/mgmtapi`): Huma v2 code-first, OpenAPI 3.1 exported and drift-checked, RFC 9457 problem
  documents with stable `code`, ETag/If-Match, `Idempotency-Key` stored as a keyed MAC and replayed for 24 h.
- **Operator authentication** (HallPass `internal/opauth`, Pulse ADR-0038): local accounts with Argon2id
  (m=64 MiB, t=3, p=2), TOTP with step-reuse prevention, 10 recovery codes stored as MACs, throttling (3 free
  failures, then doubling up to 15 min, decays after 24 h), idle 30 min / absolute 12 h sessions, server-side session
  tokens stored hashed, CSRF header, re-authentication window 5 min, bootstrap setup link (30 min) and break-glass
  `admin recover`.
- **OIDC RP** (HallPass `internal/oidc/flow.go`, Pulse `internal/oidc`): coreos/go-oidc + x/oauth2, PKCE S256, nonce,
  server-side state, identities matched on `(iss, sub)` never on email, group→role mapping, JIT rules.
- **Porch as OP** (Porch `internal/oidcop`): `sub` = `u_<id>`; groups are emitted under the nested claim
  `porch.groups` only when the `groups` scope is requested and disclosure is enabled; `porch.roles` is always `[]`;
  the tenant is `porch.tenant.slug`; PKCE S256 required; ES256 signing by default; RFC 9207 `iss` response parameter.
- **Pulse client** (`github.com/aurimasniekis/go-pulse/pkg/pulse`): `Connect(url, WithCredsFile, WithCAFile,
  WithSource, WithName)`, `Publish(ctx, subject, type, data, WithID)`, `Subscribe(ctx, filter, Handle[T],
  Durable(name), MaxDeliver(n))`, `Terminal(err)`; envelope headers `ce-type`, `ce-source`, `ce-time`; subject grammar
  `evt.<domain>.<entity>.<action>[.<key>…]`; dedup window 2 minutes on `Nats-Msg-Id`.
- **Pulse admin API** (`/api/admin/v1`, viewer PAT, loopback listener by default): `/clients` (with grants),
  `/streams/{name}/consumers` (durables named `<clientID>_<name>`, filter subjects), `/subjects/tree` and
  `/subjects/info` (msgs, bytes, first/last seen, **no publisher attribution**).
- **Packaging**: management listener on `127.0.0.1:9464` (`/metrics`, `/healthz`, `/readyz`); `healthcheck`
  subcommand because distroless images have no curl; images pinned by digest; CI with SHA-pinned actions,
  govulncheck, gitleaks, Trivy, SBOM, provenance attestation; Keep a Changelog; `docs/VERSIONING.md`.
- **Frontend** (Pulse/Porch `web/`): React 19, Vite 8, TypeScript, Mantine 8, TanStack Router/Query/Store/Table/
  Virtual, ECharts 6, `openapi-fetch` with generated types, Tabler icons, Inter + JetBrains Mono, vitest, Playwright,
  pnpm; built into `internal/webui/dist` and embedded.
- **Providers**: Plugin Framework v1.19, protocol 6; hand-written client with `call[T]`, RFC 9457 `APIError`,
  retries with jittered backoff honoring `Retry-After`, reuse of one `Idempotency-Key` across retries, no redirects,
  8 MiB body cap, never logs bodies; acceptance tests boot the real app binary built from a pinned commit.

### A.3 Reusable packages and how they are reused

Code is **copied and adapted** (not imported) because the source packages are `internal/`. Every adapted file
starts with `// Adapted from Porch <path> (Apache-2.0).` and NOTICE names the origin. Imported as a dependency:
`github.com/aurimasniekis/go-pulse/pkg/pulse` (pinned pseudo-version; Pulse has no tagged releases yet).

### A.4 Compatibility requirements

- Pulse subjects must satisfy `evt.<domain>.<entity>.<action>…`, tokens `[a-z0-9_-]+`, ≤ 256 bytes; types
  `<domain>.<entity>.<action>.v1`. Deed publishes `evt.deed.<area>.<entity>.<action>` with type
  `deed.<area>.<entity>_<action>.v1` (same shape as Porch's `evt.porch.*`).
- Deed's durable consumer is named by Pulse as `<clientID>_<durable>`; Deed's client needs grants for its
  publish subjects (`evt.deed.>`), its ingest filter (`evt.deed.ingest.>`) and, for the tail aggregator, `evt.>`.
- Pulse admin PATs work only on the loopback admin listener; Deed therefore takes a configured URL (an SSH tunnel or
  an operator-published endpoint) and a viewer PAT.
- Porch groups arrive at the JSON path `porch.groups`; Deed's OIDC client takes a configurable groups claim path.

### A.5 Practices deliberately not copied

| Practice | Where | Why not |
|---|---|---|
| Top-level-only groups claim | HallPass OIDC | Porch nests groups under `porch.groups`; Deed resolves a JSON path. |
| Comment implying Porch emits roles | Porch docs | `porch.roles` is always `[]`; Deed maps groups only. |
| `//go:generate` tfplugindocs without a version | provider templates | Fails with plain `go generate`; Deed pins `tfplugindocs@v0.25.0` in the Makefile. |
| CSP without nonce | HallPass console | Deed uses Porch's per-request nonce. |
| "Tamper-proof" wording for audit | various | The hash chain is tamper-evident against edits that do not rewrite the whole chain. |
| Missing release tooling | all providers | Deed's provider ships goreleaser + GPG signing workflow + registry manifest. |
| sqlc code generation | Porch | `sqlc` is not available in this toolchain and Deed's queries are dominated by dynamic filters; Deed follows Pulse's hand-written SQL with a small query helper. Documented in ADR-001. |

### A.6 Licensing constraints

Porch, HallPass and Pulse are Apache-2.0 by the same author; adapting them requires keeping the license and a NOTICE
entry, both done. Icon sets: Simple Icons (CC0), Tabler (MIT), theSVG (collection MIT, per-icon licenses — icons
with `Custom`, unknown or no-derivatives terms are excluded unless explicitly allowlisted for unmodified use; see
ADR-011b and `docs/ICONS.md`). No GPL code is used.

### A.7 Unavailable references and how they are isolated

| Missing | Effect | Isolation |
|---|---|---|
| A production Porch, Pulse, Mimir or Grafana reachable from this environment | Real-service validation of those integrations cannot be performed here | Each integration sits behind an explicit interface (`sources.Provider`, `telemetry.Adapter`, `pulseint.AdminClient`) with contract tests against recorded fixtures and local services (nats-server, docker-compose Prometheus/Loki/Grafana/MinIO). [VALIDATION.md](VALIDATION.md) lists every integration as *code complete*, *locally validated* or *real-service validated*. |
| Terraform Registry signing key and namespace registration | Provider cannot be published | Release workflow expects `GPG_PRIVATE_KEY`/`PASSPHRASE`; publication is an owner step documented in the provider's `docs/RELEASING.md`. |
| Production telemetry rates | Budgets are estimates | Budgets are configurable and measured in [PERFORMANCE.md](PERFORMANCE.md). |

---

## Part B — Questions

### B.1 Scope and scale

#### Q-01. Single organization or multiple tenants?
- **Why it matters:** Drives every table's keys, authorization checks and the UI's navigation.
- **Evidence:** Porch is multi-tenant (it is an IdP); Pulse and HallPass are single-installation. The owner manages a
  few distinct networks (home, family members' networks).
- **Default:** *Owner decision* — one installation with **projects**. Every entity belongs to exactly one project;
  roles are granted per project; cross-project edges are allowed only through declared **bridges** (ADR-004).
- **Trade-offs:** Projects are cheaper than full tenancy (shared kinds, shared identity, one database) but do not
  isolate administrators: installation admins see everything.
- **Reversible:** Partly — splitting an installation later is an export/import, not a migration.
- **Blocking:** No (resolved).

#### Q-02. Environments, projects, namespaces and ownership?
- **Why it matters:** Users filter and group by these constantly; ambiguity creates duplicate concepts.
- **Evidence:** Porch has tenants + groups; no existing system models environments.
- **Default:** *Project* is the security boundary. *Environment* is a built-in kind (`environment`) plus a
  first-class `metadata.environment` field indexed for filtering. *Owner* is a reference to a `group`/`team` entity
  (`spec.owner`) plus the `owned_by` relationship; missing owners raise a finding.
- **Trade-offs:** Modeling environment as both a field and an entity is redundant, but the field makes filters fast
  and the entity lets environments carry links and owners.
- **Reversible:** Yes. **Blocking:** No.

#### Q-03. Expected entity, edge, source and concurrent-user counts?
- **Why it matters:** Sets index design, graph query limits and render budgets.
- **Evidence:** A personal/home-lab estate is hundreds to low thousands of entities; Terraform states of the owner's
  scale contain < 2,000 resources.
- **Default:** Design target **10,000 entities / 50,000 relationships / 50 sources / 20 concurrent users**,
  benchmarked in [PERFORMANCE.md](PERFORMANCE.md). Hard caps: 200,000 entities per installation (configurable).
- **Trade-offs:** SQLite comfortably handles this; beyond ~1M edges a graph database would be warranted.
- **Reversible:** Yes. **Blocking:** No.

#### Q-04. Telemetry and event rates?
- **Why it matters:** Determines whether Deed may persist anything per event.
- **Evidence:** Pulse's EVENTS stream is sized for a personal bus; subject rollups exist per minute.
- **Default:** Deed never stores raw events or samples. The `evt.>` tail aggregates per minute per
  `(ce-source, subject template, ce-type)` and keeps 7 days of per-minute buckets plus 90 days of hourly rollups.
  Tail ingestion is bounded at 2,000 msg/s (configurable); above that it samples and labels results as sampled.
- **Trade-offs:** Sampling loses exact counts at high rates; labelled as such in the UI.
- **Reversible:** Yes. **Blocking:** No.

#### Q-05. History and audit retention?
- **Why it matters:** Unbounded history grows the database; too little loses forensic value.
- **Evidence:** Porch keeps audit forever; Pulse prunes rollups.
- **Default:** Audit: kept indefinitely (it is small); optional export via the ECS file sink. Entity change log:
  365 days. Tombstones: 30 days then compaction. Outbox: 7 days after publication. Telemetry rollups: 7 d minute,
  90 d hour. SSE change log replay window: 24 h / 10,000 events. All configurable.
- **Reversible:** Yes. **Blocking:** No.

#### Q-06. Which integrations require native connectors?
- **Why it matters:** Prevents implying that an icon or schema is a working integration.
- **Evidence:** The spec lists many kinds; only some have APIs Deed can read.
- **Default:** Four explicit support levels per kind: *modeling* (schema), *Terraform mapping*, *runtime ingestion*
  (push protocol / Pulse), *native integration* (in-process provider). Native providers: `terraform_state`,
  `pulse_admin`, `pulse_tail`, `github`, `hallpass`, `porch`; `docker` via the external `deed-agent`. The support
  matrix is generated from the provider registry and shown in the UI and [INTEGRATIONS.md](INTEGRATIONS.md).
- **Reversible:** Yes. **Blocking:** No.

#### Q-07. What is deliberately out of scope?
- **Default:** Provisioning or mutating upstream infrastructure; storing metrics/logs; acting as a CMDB of record for
  finance; running user-supplied code; multi-node HA; Kubernetes-style controllers. Home Assistant has modeling and
  Terraform/runtime-ingestion support but no native connector in this release.
- **Reversible:** Yes. **Blocking:** No.

### B.2 Identity and ownership

#### Q-08. How are stable IDs assigned?
- **Why it matters:** Terraform state, links and history must survive renames.
- **Evidence:** Porch `internal/ids`: prefixed, time-sortable UUIDv7 in 26 Crockford base32 characters.
- **Default:** Server-assigned `ent_<26>` (and `rel_`, `src_`, `prj_`, `knd_`). `(project, kind, name)` is the unique
  natural key; `name` is a DNS-label-like slug and may be renamed (the old name is kept as an alias).
- **Trade-offs:** Clients cannot choose IDs; they upsert by natural key or external ID instead.
- **Reversible:** No (IDs are persisted externally). **Blocking:** No.

#### Q-09. How do Terraform-managed, imported and runtime-discovered entities converge?
- **Why it matters:** The core promise: one entity, many sources.
- **Default:** Every source writes a **facet** for an entity; entities are matched by (1) explicit entity ID,
  (2) the source's own external ID, (3) natural key `(project, kind, name)`, (4) declared **identity keys** of the
  kind (e.g. `server` matches on `spec.hostname` or `spec.ipv4`; `repository` on `spec.url`). Identity-key matches
  across sources are automatic only when unique; otherwise a **duplicate candidate** finding is raised (ADR-003).
- **Reversible:** Yes (matching rules are data). **Blocking:** No.

#### Q-10. What happens after renames, moves, recreation or source replacement?
- **Default:** Rename → alias kept, ID stable. Move to another project → explicit `move` operation (admin on both
  projects), relations crossing projects must go through a bridge or are flagged. Recreation upstream (new external
  ID) → if identity keys match the tombstoned entity within the tombstone retention window, the entity is
  **revived** with the same ID and history notes the new external ID; otherwise a new entity. Source replacement →
  `sources/{id}/handover` re-keys external IDs from the old source to the new one in one transaction.
- **Reversible:** Yes. **Blocking:** No.

#### Q-11. How are duplicate candidates detected?
- **Default:** On every facet write, identity keys are looked up; more than one non-tombstoned match produces a
  `duplicate_candidate` finding with both IDs and the matching key. Merge is an explicit admin action that moves
  facets, relations, aliases and external IDs onto the surviving entity and tombstones the other (audited).
- **Reversible:** Merge is not reversible automatically (history records it). **Blocking:** No.

#### Q-12. Who owns each field and relationship?
- **Default:** Field-level ownership by facet layer and source policy (ADR-003). Relationships are owned per
  assertion: each source asserts independently; a relation lives while any assertion lives.
- **Blocking:** No.

#### Q-13. How are conflicts surfaced and resolved?
- **Default:** Two sources in the **same layer** disagreeing on a field produce a `conflict` row and a finding; the
  winner is deterministic (earliest `first_claimed_at`, then source ID). Resolution options: set a source priority
  in the source policy, add an `override` annotation (editor+), or fix the upstream. The entity detail "Why" view
  shows all candidate values.
- **Reversible:** Yes. **Blocking:** No.

### B.3 Declarative lifecycle

#### Q-14. Which data is authoritative?
- **Default:** Per field: `override` > `declared` > `imported` > `discovered`; observations never override spec
  fields — they land in `status`. Precedence is configurable per source policy (e.g. a Terraform import may be
  preferred over a declaration for `spec.ipv4`).
- **Blocking:** No.

#### Q-15. Which data is an overlay?
- **Default:** `annotation` facets (labels, links, owner, description) and `override` facets authored by humans in
  the UI or API; they never remove fields from other facets.
- **Blocking:** No.

#### Q-16. Does declaration removal delete, orphan or tombstone?
- **Default:** Removing a Terraform resource deletes only that source's `declared` facet. If other facets remain the
  entity stays (marked "undeclared" in the UI); if none remain the entity is tombstoned for 30 days. Per-source
  snapshot policy `on_absent: tombstone|orphan|keep` controls absence from snapshots.
- **Reversible:** Tombstones are revivable within retention. **Blocking:** No.

#### Q-17. How do partial imports differ from complete snapshots?
- **Default:** A snapshot is `begin(generation, complete=true|false)` → batches → `commit`. Only `complete=true`
  commits tombstone missing facets, and only within the source's scope. Partial imports upsert only.
- **Blocking:** No.

#### Q-18. How are malformed batches handled?
- **Default:** Each item is validated independently; the response lists per-item errors with JSON pointers. Valid
  items in an incremental batch are applied unless `atomic: true`, in which case any error rejects the batch. In a
  snapshot, an item error marks the snapshot as having errors; `commit` refuses to tombstone when any batch had
  errors unless `allow_partial_errors: true`.
- **Blocking:** No.

#### Q-19. How are out-of-order updates rejected?
- **Default:** Every incremental item carries a per-source monotonic `revision` (integer) or `observed_at`. Items with
  a revision ≤ the stored facet revision, or ≤ the tombstone revision, are rejected as `stale_revision` (409 per item),
  so a delayed upsert can never resurrect a deleted fact.
- **Blocking:** No.

#### Q-20. How are stale sources represented?
- **Default:** Each source has an expected interval; freshness = `fresh` (< 1×), `aging` (1–3×), `stale` (> 3×) and
  `unknown` (never synced). Facets can carry `expires_at`; expired discovered/observation facets stop contributing
  and the entity shows "stale". Stale sources raise a finding.
- **Blocking:** No.

### B.4 Terraform

#### Q-21. Which initial state sources are supported?
- **Default:** *Owner decision* — (1) push from CLI/CI (`deed import terraform` or `POST
  /api/v1/sources/{id}/terraform/state`), (2) S3-compatible backend reader (read-only, minio-go), (3) Terraform HTTP
  backend reader (GET only). Local files are supported through push.
- **Reversible:** Yes. **Blocking:** No.

#### Q-22. How does "core" Terraform publish its state?
- **Evidence:** The owner's infrastructure Terraform uses an S3-compatible remote backend (details are private and
  not recorded here).
- **Default:** Deed reads the same backend with a separate read-only credential, or CI pushes `terraform show -json`
  after apply. Both are documented in [TERRAFORM_IMPORT.md](TERRAFORM_IMPORT.md).
- **Blocking:** No.

#### Q-23. Which state format versions are supported?
- **Default:** Raw state `version: 4` (Terraform ≥ 0.12 through 1.x) and `terraform show -json` output with
  `format_version` `1.x`. Other versions are refused with a diagnostic.
- **Blocking:** No.

#### Q-24. Which mappings are automatic or configurable?
- **Default:** Built-in mapping catalog (embedded YAML) for common providers; per-source custom mappings in the same
  format (resource type → kind, name template, identity keys, allowlisted attribute paths → spec fields, relation
  inference rules, icon). Unmapped resource types are counted in diagnostics and never persisted.
- **Blocking:** No.

#### Q-25. How are sensitive fields excluded?
- **Default:** Allowlist-only extraction: only attribute paths named in a mapping are read; `sensitive_attributes`
  paths are additionally dropped even when allowlisted; values matching secret heuristics (PEM blocks, high-entropy
  tokens) are refused. Raw state is parsed in memory with a 64 MiB cap, never stored, never logged; errors never
  echo values. Fuzz tests assert no secret from fixtures reaches the database, logs, events or audit.
- **Blocking:** No.

#### Q-26. How are state access and provider access authenticated?
- **Default:** S3 reader: dedicated read-only key stored sealed in the keyring. HTTP backend: basic or bearer
  credentials, sealed. Push: a **source token** (`deed_st_…`) scoped to a single source. Provider: a **user API
  token** (`deed_at_…`) with an editor role on the target projects.
- **Blocking:** No.

#### Q-27. How are provider-managed declarations separated from runtime observations?
- **Default:** Every provider-created object is written as a `declared` facet of the provider's per-project
  `terraform` source; status, observations and other facets are read-only computed attributes. Deleting a resource
  removes only that facet (ADR-003).
- **Blocking:** No.

#### Q-28. How are application/provider compatibility and release versions managed?
- **Default:** Independent SemVer. The API path is the contract (`/api/v1`, additive only). The provider declares the
  minimum Deed version it needs and checks `GET /api/v1/system/info` at configure time with a warning (not an error)
  when older. [COMPATIBILITY.md](COMPATIBILITY.md) holds the matrix.
- **Blocking:** No.

### B.5 Graph and telemetry

#### Q-29. Which relationships are structural versus observed?
- **Default:** Each relationship type declares `default_evidence: structural|observed`. Structural: `runs_on`,
  `hosted_on`, `contains`, `depends_on`, `resolves_to`, `stored_in`, `deployed_from`, `owned_by`,
  `secured_by_certificate`, `protected_by`, `authenticates_with`, `monitored_by`. Observed-capable: `connects_to`,
  `routes_to`, `publishes`, `subscribes_to`. Every assertion records its actual evidence class (declared grant,
  observed message, consumer config, Terraform dependency, attribute join, manual).
- **Blocking:** No.

#### Q-30. How are NATS wildcards, subjects, streams and consumers represented?
- **Default:** `nats_system` contains `nats_stream`; `nats_stream` contains `nats_consumer`; `nats_subject` entities
  represent **subject templates** (concrete tokens replaced by `*` where they are IDs: ULIDs, UUIDs, numbers, hex),
  and grants with wildcards become `nats_subject` entities whose name is the wildcard pattern with a `pattern: true`
  flag. Services `publishes`/`subscribes_to` subjects; consumers `subscribes_to` subjects through `filter_subjects`.
- **Blocking:** No.

#### Q-31. How are event contracts different from transport subjects?
- **Default:** `event_contract` = a CloudEvents `type` (e.g. `porch.tenancy.tenant_created.v1`) with optional JSON
  Schema and owner; `nats_subject` = where it travels. A contract `carried_on` subjects (custom-capable relationship
  type). Observed tail data links `(ce-source, ce-type, subject template)` triples.
- **Blocking:** No.

#### Q-32. What evidence supports each traffic visualization?
- **Default:** Traffic animation only for edges with a **measured** rate from the tail aggregator or a telemetry
  binding within the freshness window; labelled "sampled, N-min window". Grants alone draw a solid "declared" edge
  without animation. Rates are per `(publisher source, subject template)`, never claimed as end-to-end delivery.
- **Blocking:** No.

#### Q-33. How do unknown, stale, zero traffic and unhealthy differ?
- **Default:** Four distinct states with distinct icon + text + color: **unknown** (no measurement exists), **stale**
  (last measurement older than the freshness window), **zero** (fresh measurement of 0), **unhealthy** (a health
  binding or finding reports failure). Never inferred from each other.
- **Blocking:** No.

#### Q-34. How are cycles and high-degree nodes handled?
- **Default:** Traversals use visited sets and depth caps (≤ 6 default, ≤ 10 max) and node caps (≤ 2,000).
  High-degree nodes (> 50 neighbors of one type) are collapsed into a cluster node "42 × dns_record" that expands on
  demand. Impact analysis reports cycles explicitly.
- **Blocking:** No.

### B.6 Authentication and deployment

#### Q-35. What exact login methods do HallPass and Pulse support?
- **Evidence:** Both: local accounts with Argon2id password + TOTP (mandatory for privileged roles) + 10 recovery
  codes; generic OIDC SSO with group→role mapping and JIT; CLI bootstrap link; break-glass `admin recover`. Neither
  supports passkeys or a password-less "backup login" (that is a Porch-specific mechanism for its own users).
- **Default:** *Owner decision* — Deed implements the same: local accounts + TOTP + recovery codes, generic OIDC SSO
  with Porch documented as primary, bootstrap and break-glass (ADR-005).
- **Blocking:** No.

#### Q-36. What identity semantics does Porch provide?
- **Evidence:** Porch `internal/oidcop`: `sub` = stable `u_<id>`; `email`, `email_verified`; nested `porch.groups`
  (only with `groups` scope + disclosure), `porch.tenant.slug`, `amr`; PKCE S256 required; ES256; RFC 9207 `iss`
  response parameter.
- **Default:** Identities keyed on `(iss, sub)`; groups claim path configurable (default `porch.groups`); optional
  `required_tenant`; optional `require_email_verified`; RFC 9207 `iss` validated when present.
- **Blocking:** No.

#### Q-37. Which roles and service credentials are required?
- **Default:** Installation roles: `owner`, `admin`, `auditor`. Project roles: `viewer`, `editor`, `admin`. Machine
  credentials: user API tokens (`deed_at_`, subset of the user's roles, optionally project-scoped, expiry ≤ 366 days)
  and source tokens (`deed_st_`, ingest into one source only). Outbound: Pulse creds file (publisher + subscriber
  grants), Pulse admin viewer PAT, GitHub fine-grained read-only token, HallPass and Porch auditor tokens.
- **Blocking:** No.

#### Q-38. Which proxy headers can be trusted?
- **Default:** Only from configured `proxy.trusted_proxies` CIDRs (X-Forwarded-For, right-to-left walk) and, with
  `proxy.cloudflare: true`, `CF-Connecting-IP` only when the resolved hop is inside Cloudflare's published ranges.
  `X-Forwarded-Proto` is never needed: `public_url` defines the scheme.
- **Blocking:** No.

#### Q-39. How is Cloudflare origin bypass prevented?
- **Default:** Documented firewall allowing 80/443 only from Cloudflare ranges (nftables and UFW examples with a
  refresh timer) plus optional Authenticated Origin Pulls (`http.client_ca_file`). Without either, Deed still
  resolves client IPs safely (it never trusts `CF-Connecting-IP` from a non-Cloudflare peer).
- **Blocking:** No.

#### Q-40. What are the SQLite deployment constraints?
- **Default:** Local filesystem only (refuses NFS/SMB/FUSE/Ceph/9p…); one process (flock); one instance; data
  directory 0700 owned by the service user; never on S3-mounted volumes; backups via `VACUUM INTO`.
- **Blocking:** No.

### B.7 Backups and operations

#### Q-41. What are the RPO and RTO targets?
- **Default:** RPO 6 h (default backup interval, configurable down to 15 min); RTO 30 min for a 1 GB database
  (download + decrypt + verify + start). Measured in VALIDATION.md for the test dataset.
- **Blocking:** No.

#### Q-42. Where are encryption keys stored?
- **Default:** Integration secrets: sealed with keyring data keys wrapped by the KEK (file outside the data
  directory, 0400, or a systemd credential). Backups: age X25519 **recipients** in config; **identities kept offline**
  by the owner. Each backup also embeds the KEK *encrypted to the same age recipients*, so disaster recovery needs
  only the age identity and the bucket. The server can write backups but cannot read them.
- **Blocking:** No.

#### Q-43. How are restores tested?
- **Default:** Integration tests against MinIO restore into a fresh directory and compare counts and the audit
  chain; failure tests cover wrong key, corrupt object, missing manifest, S3 outage and interrupted multipart upload.
  `deed backup verify` performs a full restore-to-temp check on demand; the runbook recommends a monthly verify.
- **Blocking:** No.

#### Q-44. How are secrets rotated?
- **Default:** KEK: `deed keys rotate-kek` (offline, rewraps data keys). Purpose keys: `deed keys rotate`. Source
  credentials: replace in the UI/API (sealed). API/source tokens: create new, revoke old; expiry enforced. Age
  recipients: add a new recipient; old identities remain valid for historical backups.
- **Blocking:** No.

#### Q-45. What happens during Pulse, S3, OIDC or telemetry outages?
- **Default:** Pulse down: outbox accumulates (bounded at 100,000 rows; beyond that oldest *unpublished* events are
  dead-lettered with a metric and alert), consumers reconnect with backoff, catalog writes are unaffected. S3 down:
  backup fails, metric + finding + alert; no data loss. OIDC down: SSO login fails with a clear error; local accounts
  and break-glass still work. Telemetry down: per-adapter state "unavailable" with last good value marked stale.
- **Blocking:** No.

### B.8 Frontend

#### Q-46. Which design system meets the requested visual standard?
- **Default:** *Owner decision* — Mantine 8 (as Pulse/Porch) with restrained radii (4 px), Inter and JetBrains Mono,
  a neutral gray scale and a single accent; ECharts 6 for charts; React Flow 12 for the map.
- **Blocking:** No.

#### Q-47. How are large graphs navigated?
- **Default:** Never render everything: server-side neighborhood/path/impact queries with depth and node caps;
  aggregation into cluster nodes above 500 nodes; progressive expansion; ELK layout in a Web Worker; saved views and
  layouts; search-and-focus; breadcrumbs of the exploration path.
- **Blocking:** No.

#### Q-48. Which views are shareable?
- **Default:** Every view encodes its state in the URL (filters, sort, columns, graph focus/depth/filters). Saved
  views are server-side objects with stable IDs and project-level visibility.
- **Blocking:** No.

#### Q-49. How are accessibility and reduced motion supported?
- **Default:** WCAG 2.2 AA contrast in both themes (checked by axe in Playwright), visible focus rings, keyboard
  navigation of the graph (arrow keys between neighbors, Enter to expand), every graph view has a relations-table
  equivalent, status uses icon + text + color, animations off under `prefers-reduced-motion`.
- **Blocking:** No.

#### Q-50. How is performance measured?
- **Default:** Go benchmarks (graph traversal, reconcile, search) on the 10k/50k synthetic dataset; Playwright
  measurement of time-to-first-neighborhood and frame budget for 2,000 nodes; results with hardware in
  PERFORMANCE.md.
- **Blocking:** No.

### B.9 Security and data handling

#### Q-51. How are user-defined links validated?
- **Default:** Only `https:` (and `http:` when an admin allows it per project) URLs; no `javascript:`, `data:`,
  `file:`; max 2,048 chars; rendered with `rel="noopener noreferrer"` and never as HTML.
- **Blocking:** No.

#### Q-52. Is any untrusted HTML rendered?
- **Default:** No. Markdown descriptions render through a sanitizer that outputs React elements (no
  `dangerouslySetInnerHTML`); custom SVG icons are sanitized server-side, served with `Content-Security-Policy:
  sandbox` and displayed via `<img>` only.
- **Blocking:** No.

#### Q-53. How are custom-kind schemas bounded?
- **Default:** JSON Schema 2020-12 only; no remote `$ref`; ≤ 64 KiB; depth ≤ 16; ≤ 512 properties; `pattern`
  regexes compiled with Go RE2 (linear time) and limited to 512 chars; `format` assertions limited to a safe set.
- **Blocking:** No.

#### Q-54. How are secrets referenced instead of embedded?
- **Default:** A `secret_ref` value type (`{"secretRef": {"store": "vault|1password|env|file", "key": "…"}}`) is
  stored and displayed as a reference; Deed never dereferences it. Spec fields whose names look secret are rejected
  unless they hold a `secretRef`.
- **Blocking:** No.

#### Q-55. How are machine credentials revoked and what happens to in-flight work?
- **Default:** Revocation is immediate (token lookup per request); open SSE streams re-validate every 60 s and close
  when the credential is revoked; in-flight snapshots of a revoked source token fail at the next batch or commit.
- **Blocking:** No.

#### Q-56. How is SSRF prevented for connectors?
- **Default:** All outbound calls use `safehttp`: per-integration allowlist of `scheme://host:port`; IP check at dial
  time (defeats DNS rebinding); loopback, link-local, metadata (169.254.169.254, fd00:ec2::254), unspecified,
  multicast and special ranges blocked unless a configured CIDR allows them; redirects re-validated with a 3-hop cap;
  response size and time budgets.
- **Blocking:** No.

### B.10 Delivery

#### Q-57. How do the two repositories share the API contract?
- **Default:** `api/openapi.json` is generated and committed in go-deed; the provider's client is hand-written (as in
  the existing providers) and contract-tested in the provider's acceptance suite against the real `deed` binary built
  from a pinned commit; go-deed's CI also builds the provider at a pinned commit and runs its acceptance tests
  against the current tree (contract test in both directions).
- **Blocking:** No.

#### Q-58. How is the demo dataset labelled?
- **Default:** `deed demo seed` creates a project named `demo-synthetic` whose every entity carries the label
  `deed.dev/synthetic=true`; the UI shows a persistent "Synthetic demo data" banner on any view that includes them.
- **Blocking:** No.

#### Q-59. Which external actions are deferred?
- **Default:** Creating/pushing the provider GitHub repository (owner instruction: develop locally only); Terraform
  Registry publication (needs GPG key and registry namespace); real-service validation against production Porch,
  Pulse and Mimir. All listed in VALIDATION.md.
- **Blocking:** No (continuing locally).

#### Q-60. Which Go and Node toolchains are targeted?
- **Evidence:** Sibling repos declare `go 1.27.3`; the 1.27.3 toolchain is available locally; Node 24 + pnpm 10.
- **Default:** `go 1.27.3`, Node 24, pnpm 10, Terraform ≥ 1.11 for the provider tests.
- **Blocking:** No.
