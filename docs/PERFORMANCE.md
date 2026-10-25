# Performance

Measured with the opt-in harness `internal/perf` (`DEED_PERF=1 DEED_TEST_TX_MAX=10m go test -run TestPerformance -v
./internal/perf/`), which builds the catalog through the same catalog/ingest services the API uses and times each
operation in-process (no HTTP). Numbers below are from one run on an **Apple M1 Max (10 cores, 64 GiB), macOS 15,
go1.27.3 darwin/arm64**, SQLite (modernc) on the internal SSD, WAL mode, `synchronous=FULL`, while other
development work was running on the machine.

## Dataset
- **10,000 entities**: 2,000 servers (3 environments, 40 racks) with four containers each.
- **50,000 relations**: every container `runs_on` its server, plus random `depends_on` edges between containers.
- Database file after the run: 139 MiB (entities with effective JSON, provenance and FTS
  index; per-source facets; relation assertions; change log; audit chain).
- Building it: entities 3.2 s, relations 11.9 s (250 items per transaction).

## Latency (milliseconds)
| Operation | Samples | p50 | p95 | p99 |
|---|---|---|---|---|
| Inventory filter (kind + environment), page of 50 | 100 | 2.0 | 2.5 | 2.9 |
| Inventory full-text search, first page of 50 | 100 | 0.2 | 0.2 | 0.6 |
| Entity detail | 200 | 0.1 | 0.1 | 0.1 |
| Impact analysis, downstream, depth 4 | 100 | 77.3 | 84.9 | 90.4 |
| Graph neighborhood, depth 2, ≤ 500 nodes (first map load) | 200 | 25.9 | 28.6 | 29.7 |
| Graph neighborhood, depth 3, ≤ 2,000 nodes | 50 | 61.1 | 79.3 | 82.8 |
| Whole-catalog overview grouped by kind | 20 | 182.6 | 218.9 | 218.9 |
| Shortest path between two random entities, ≤ 6 hops | 50 | 51.1 | 78.6 | 87.1 |
| Incremental ingest batch of 100 items | 20 | 36.7 | 40.0 | 40.0 |
| Declare/update one entity (merge, provenance, change log, audit) — one write transaction | 200 | 0.5 | 1.0 | 6.8 |

| Snapshot (complete, through `RunSnapshot`) | Total |
|---|---|
| 1,000 items | 0.40 s |
| 10,000 items | 3.70 s |

## Against the budgets
- **First neighborhood under 500 ms p95 at 10k/50k (ADR-011):** met — 29 ms (depth 2), 79 ms (depth 3, 2,000-node cap).
  The median depth-2 neighborhood had 118 nodes; high-degree fans collapse into cluster nodes.
- **Write transactions within 100 ms (ADR-001):** single-entity declarations p99 6.8 ms;
  a 100-item ingest batch 40 ms p95. The documented exception is the atomic snapshot
  commit, which holds the writer for a time proportional to the snapshot — about 3.7 s for 10,000 items here
  (begin, staging and commit together). Other writers queue behind it (WAL readers are unaffected); keep very large
  sources on incremental items, or split them into several sources.
- **Path search:** bidirectional breadth-first search with a visited-node budget; before that change the same
  benchmark took 1.07 s at p95, now 79 ms.
- **Overview:** 219 ms p95 to aggregate the whole catalog; it is computed on demand for the map's
  zoomed-out level, not on every interaction.

## Not measured here
- Browser rendering budget (≤ 2,000 nodes / 4,000 edges at once) — measured by the web suite once the UI is merged
  (see docs/UI.md).
- HTTP overhead, TLS and Cloudflare latency; resident memory of a long-running server; concurrent load from many
  users. The numbers above are single-client, in-process.
- Hardware other than the development laptop.

Reproduce, or change the size with `DEED_PERF_ENTITIES` / `DEED_PERF_RELATIONS`; `DEED_PERF_OUT=perf.json` writes the
raw results.
