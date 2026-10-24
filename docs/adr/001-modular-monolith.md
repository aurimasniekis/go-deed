# ADR-001: Modular monolith, SQLite, hand-written SQL

## Context
Deed is a single-VPS product for a small number of operators. The spec prefers a modular monolith. The sibling
projects (Porch, HallPass, Pulse) run one Go binary on SQLite and share proven storage code.

## Decision
- One binary `deed` (`cmd/deed`) with subcommands `serve`, `migrate`, `openapi`, `healthcheck`, `config check`,
  `keys …`, `admin bootstrap|recover|…`, `backup run|list|verify|keygen`, `restore`, `import terraform`,
  `audit verify`, `demo seed`, `version`. A second binary `deed-agent` (`cmd/deed-agent`) is a reference external
  source runner.
- Domain packages under `internal/`: `catalog` (projects, kinds, relationship types, entities, relations, facets and
  the reconcile engine), `graph` (traversal), `sources` (registry, scheduler, providers), `tfimport`, `ingest`
  (source protocol), `pulseint`, `telemetry/*`, `findings`, `alerts`, `versions`, `live` (SSE), `backup`, `authn`,
  `authz`, `audit`, `httpapi`, `webui`. Infrastructure packages (`store`, `config`, `httpx`, `keyring`, `ids`,
  `apperr`, `obs/*`, `outbox`, `events`, `safehttp`) are adapted from Porch (Apache-2.0).
- SQLite via `modernc.org/sqlite` (pure Go): one writer with `BEGIN IMMEDIATE`, a read-only pool, WAL,
  `synchronous=FULL`, verified pragmas, flock, local-filesystem check, embedded forward-only goose migrations with a
  pre-migration `VACUUM INTO` snapshot and refusal of newer schemas.
- **Hand-written SQL** instead of sqlc: sqlc is not part of this toolchain and Deed's read paths are dominated by
  dynamic filters (inventory, graph, search). A tiny helper (`store.Q`) keeps scanning uniform. A `txscope`
  convention (no network I/O inside write transactions) is enforced by code review and the `storetest` duration
  guard (100 ms per write transaction in tests).
- Normalized tables for entities, facets, relations, assertions and sources; validated JSON columns for properties
  (`spec`, `status`, facet fields). FTS5 for search.

## Consequences
- + Single deployable, simple backups, transactional consistency across catalog, audit and outbox.
- + Proven storage semantics copied rather than reinvented.
- − Vertical scaling only; one writer serializes mutations (bounded by short transactions and batch limits).
- − JSON columns trade schema-level typing for custom-kind flexibility; validation happens in Go with JSON Schema.
