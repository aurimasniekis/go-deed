# Performance

Measured with the opt-in harness `internal/perf` (`DEED_PERF=1 DEED_TEST_TX_MAX=10m go test -run TestPerformance -v
./internal/perf/`), which builds the catalog through the same catalog/ingest services the API uses and times each
operation in-process (no HTTP). Numbers below are from one run on an **Apple M1 Max (10 cores, 64 GiB), macOS 15,
go1.27.3 darwin/arm64**, SQLite (modernc) on the internal SSD, WAL mode, `synchronous=FULL`, on an otherwise idle
machine (go-deed `c3a52c3` and later, after the indexed both-direction traversal).

## Dataset
- **10,000 entities**: 2,000 servers (3 environments, 40 racks) with four containers each.
- **50,000 relations**: every container `runs_on` its server, plus random `depends_on` edges between containers.
- Database file after the run: 139 MiB (entities with effective JSON, provenance and FTS
  index; per-source facets; relation assertions; change log; audit chain).
- Building it: entities 3.6 s, relations 12.0 s (250 items per transaction).

## Latency (milliseconds)
| Operation | Samples | p50 | p95 | p99 |
|---|---|---|---|---|
| Inventory filter (kind + environment), page of 50 | 100 | 2.1 | 2.5 | 2.6 |
| Inventory full-text search, first page of 50 | 100 | 0.2 | 0.2 | 0.5 |
| Entity detail | 200 | 0.1 | 0.1 | 0.1 |
| Impact analysis, downstream, depth 4 | 100 | 13.5 | 18.8 | 22.2 |
| Graph neighborhood, depth 2, ≤ 500 nodes (first map load) | 200 | 1.6 | 2.6 | 3.8 |
| Graph neighborhood, depth 3, ≤ 2,000 nodes | 50 | 14.4 | 27.7 | 29.2 |
| Whole-catalog overview grouped by kind | 20 | 186.9 | 194.0 | 194.0 |
| Shortest path between two random entities, ≤ 6 hops | 50 | 1.9 | 8.9 | 11.3 |
| Incremental ingest batch of 100 items | 20 | 35.4 | 44.9 | 44.9 |
| Declare/update one entity (merge, provenance, change log, audit) — one write transaction | 200 | 0.5 | 0.9 | 7.1 |

Before the both-direction traversal used two indexed lookups (`TestQueryPlans` now guards it), SQLite scanned every
relation per frontier step: depth-2 neighborhood p95 28.6 ms, depth 3 79.3 ms, impact 84.9 ms, path 78.6 ms.

| Snapshot (complete, through `RunSnapshot`) | Total |
|---|---|
| 1,000 items | 0.44 s |
| 10,000 items | 4.09 s |

## Against the budgets
- **First neighborhood under 500 ms p95 at 10k/50k (ADR-011):** met — 2.6 ms (depth 2), 28 ms (depth 3, 2,000-node cap).
  The median depth-2 neighborhood had 118 nodes; high-degree fans collapse into cluster nodes.
- **Write transactions within 100 ms (ADR-001):** single-entity declarations p99 7.1 ms;
  a 100-item ingest batch 45 ms p95. The documented exception is the atomic snapshot
  commit, which holds the writer for a time proportional to the snapshot — about 4.1 s for 10,000 items here
  (begin, staging and commit together). Other writers queue behind it (WAL readers are unaffected); keep very large
  sources on incremental items, or split them into several sources.
- **Path search:** bidirectional breadth-first search with a visited-node budget; before that change the same
  benchmark took 1.07 s at p95, now 8.9 ms.
- **Overview:** 194 ms p95 to aggregate the whole catalog; it is computed on demand for the map's
  zoomed-out level, not on every interaction. With `sources=true` (only the map's colour-by-source mode asks for it)
  it also aggregates the asserting sources of every bundle, a second pass over the catalog's assertions: 486 ms p50 /
  498 ms p95 against 200 / 237 ms without it, in a later run of the same harness on a busier machine
  (`graph_overview_by_kind_with_sources`).
- **Edge sources** on neighbourhoods, expansions, paths and impact cost one indexed lookup per 400 relationships;
  an A/B run (sources on and off, same machine) measured +0.6 ms p50 for the depth-2 neighbourhood and +5 ms p50 for
  depth 3 at the 2,000-node cap.

## Browser rendering
Measured by the Playwright suite (`web/e2e/perf.spec.ts`, headless Chromium at 1440×900 on the same laptop, six runs
each); method and the full table in `docs/UI.md` § Performance budgets and measurements:
- typical neighbourhoods (26 nodes): 187–248 ms from fetch to first frame, 60 fps panning and zooming;
- ~500 nodes: first frame 129–357 ms in all three layouts, 60 fps panning and zooming, and no frame over 33 ms when
  zooming across the level-of-detail threshold (before: one 83–133 ms frame per crossing); a revisit is served from
  the layout cache in ~80 ms;
- 2,000 nodes: first frame layered 295–458 ms (before 591–615 ms; the layered layout above 600 nodes is now Deed's own,
  17–20 ms instead of ELK's ~350 ms), force 318–338 ms, radial 234–308 ms; panning and zooming 58–60 fps; the longest
  frame when crossing the level-of-detail threshold is 17–33 ms in force and radial and 50–67 ms in layered (before
  367–533 ms), as cards and edges are swapped in over several frames.
- On a 10k/50k catalog (`DEED_E2E_DEMO_SCALE=2500`: 10,098 entities, 55,151 relations), typical first neighbourhoods
  (depth 1–2, 5–39 nodes) take 196 ms p50 and 260 ms p95 from fetch to first frame over 24 loads.
- **Met:** every first-frame and 500-node frame budget. **Still above ~50 ms:** one frame of up to 67 ms per threshold
  crossing in the 2,000-node layered drawing (beyond the 500-node pan/zoom budget).

## Not measured here
- HTTP overhead, TLS and Cloudflare latency; resident memory of a long-running server; concurrent load from many
  users. The numbers above are single-client, in-process.
- Hardware other than the development laptop.

Reproduce, or change the size with `DEED_PERF_ENTITIES` / `DEED_PERF_RELATIONS`; `DEED_PERF_OUT=perf.json` writes the
raw results.
