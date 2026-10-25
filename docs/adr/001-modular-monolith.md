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
  dynamic filters (inventory, graph, search). A tiny helper (`store.Q`) keeps scanning uniform. The `txscope`
  rule (no network I/O, NATS/Pulse, S3, outbound HTTP, connector calls, waits, channel operations or fsyncs inside
  write transactions) is enforced statically by `tools/analyzers/txscope` (`make txscope`, run in CI) and at run
  time by the `storetest` duration guard (100 ms per write transaction in tests).
- **Exceptions to the 100 ms budget:** a snapshot commit is one atomic transaction by design (ADR-003), so it holds
  the writer for a time proportional to the snapshot (bounded by `MaxSnapshotItems`; measured in PERFORMANCE.md).
  Incremental writes, batches and every request-path transaction stay within the budget. Start-up work (keyring load,
  built-in seeding) and offline CLI commands are also exempt.
- Normalized tables for entities, facets, relations, assertions and sources; validated JSON columns for properties
  (`spec`, `status`, facet fields). FTS5 for search.

## Consequences
- + Single deployable, simple backups, transactional consistency across catalog, audit and outbox.
- + Proven storage semantics copied rather than reinvented.
- − Vertical scaling only; one writer serializes mutations (bounded by short transactions and batch limits).
- − JSON columns trade schema-level typing for custom-kind flexibility; validation happens in Go with JSON Schema.
