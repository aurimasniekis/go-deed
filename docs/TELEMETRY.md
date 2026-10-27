# Telemetry

Deed shows operational data next to the catalog without becoming a metrics or logs backend (ADR-008). It queries
the systems you already run, caches bounded summaries in memory for the binding's freshness period, and never stores
raw samples, log lines or documents. Pulse activity is the one exception: the `pulse_tail` source keeps per-minute
and per-hour **counts** (never messages) in SQLite, pruned after `retention.telemetry_minute` / `telemetry_hour`.

## Integrations
An integration is an installation-wide connection to one system: an adapter type, a base URL and sealed
credentials (never returned by the API, logged or audited). Owners and admins manage them; auditors can read them.

| Adapter | Shapes | Queries | Credentials |
|---|---|---|---|
| `prometheus` | scalar, timeseries, table | PromQL (Prometheus, Mimir with `tenant`, Thanos, VictoriaMetrics, or a Grafana data-source proxy URL) | bearer token or basic auth |
| `loki` | scalar, timeseries, table | LogQL **metric** queries only — log lines are never fetched | bearer token or basic auth |
| `natsmon` | scalar, table | `[rate ]<endpoint>[?query] <path>` against the NATS monitoring port | none (keep it private) |
| `opensearch` | scalar, timeseries | `<index-pattern> <query DSL>`; counts and date histograms only, scripts refused | basic, API key or bearer |
| `grafana` | link | `search <terms>`, `search tag:<tag>`, `dashboard <uid>?var-x=…`; a search that matches nothing returns a link to Grafana's own search for the same terms (inferred) | service-account token (Viewer) |
| `pulse` | scalar, timeseries, table | `rate|messages|bytes publisher=… subject=… type=…` over Deed's own tail rollups | none (built-in) |

Every adapter goes through the SSRF-safe client: only the configured origin is reachable, addresses are checked at
dial time (DNS rebinding cannot redirect a query), cloud metadata addresses are always blocked, loopback and private
ranges only when listed in `allow_cidrs`, redirects are refused, and responses are capped (4 MiB for queries).
`POST /integrations/{id}/test` checks connectivity and updates `deed_telemetry_integration_up`.

**Mimir / Cortex.** Use the query URL including the `/prometheus` prefix and set `tenant`; Deed sends it as
`X-Scope-OrgID`. Without it a multi-tenant Mimir answers 401 ("no org id"), which Deed reports as a missing tenant
rather than bad credentials. A tenant with no data is valid: queries simply return no data.

**OpenSearch.** A user with the `read` action on the bound index patterns is enough. Without the
`cluster:monitor/main` permission the connectivity test still succeeds (the version is not shown). For a private CA
(for example the demo certificates of the security plugin) set `outbound.ca_file`. `time_field` may use any date
mapping format (`date`, `date_nanos`, `epoch_millis`, custom patterns): the window bounds are sent as ISO 8601 with an
explicit `format`. An index pattern that matches no index is *no data*, not a count of zero.

Grafana is never treated as the metrics store: a `grafana` integration produces links. To chart data that lives
behind Grafana, either query the data source directly or point a `prometheus` integration at
`https://GRAFANA/api/datasources/proxy/uid/<uid>` with a Grafana service-account token.

## Bindings
Users never write queries. An admin authors a **binding**: a named query template, the kinds it applies to, a
shape, a unit and typed parameters filled from the entity:

```json
{
  "name": "http-rps", "title": "Requests/s", "integration": "mimir", "kinds": ["service"],
  "query": "sum(rate(http_requests_total{service=\"{{svc}}\"}[{{window}}]))",
  "params": [{"name": "svc", "type": "label", "from": "/metadata/name"}],
  "shape": "scalar", "unit": "req/s", "freshness_seconds": 60
}
```

- `from` is a JSON pointer into the entity envelope (`/spec/hostname`, `/metadata/labels/app`, …); edge-rate
  bindings use `from:/…` and `to:/…` for the relationship's endpoints. `default` fills missing values.
- Types constrain values before they reach a query: `label` (`[A-Za-z0-9_.:@/+-]`), `int`, `duration`, `subject`
  (NATS) and `string`. Every value except `int` and `duration` is then escaped for the adapter's language (PromQL /
  LogQL string, JSON string, URL component). A value that fails validation makes the binding *not applicable* for
  that entity; it is never sent.
- Built-in placeholders: `{{window}}` (the requested window as a Prometheus duration) and `{{step}}`.
- Roles: `chart` (default), `health` (scalar + `thresholds` → ok/warning/critical, kept separate from structural
  health), `link`, and `edge_rate` (scalar rate for a `relationship_type`, used by traffic overlays).
- `guard` (optional): a query with the same placeholders; the binding applies to an entity only when it returns data.
  Otherwise the result is "not applicable" (no value, no health). Use it for "this host has node_exporter":
  `up{job="node",instance="{{instance}}"}`. The guard runs with a multi-series shape (table where the adapter has it)
  and is cached like the query.
- `when_empty` (optional, scalar bindings): the value of an empty result after the guard passed, e.g. `0` for an
  error rate whose counter has no series yet. Together they replace `… or vector(0)`, which made every entity of the
  kind look measured (and healthy) even when nothing about it was scraped. On update, omit to keep, `{}` to remove,
  `{"value": 0}` to set; an empty `guard` string removes the guard.
- `on_map` (optional, scalar `chart` or `health` bindings): show the value on the entity's map node card (see
  [Node metrics](#node-metrics)).
- Bindings are validated on save: unknown placeholders, unbalanced queries, log (non-metric) LogQL, OpenSearch
  scripts and unsupported shapes are refused.

Viewers see binding names, titles, kinds and parameters but not query text.

## Results
`GET /entities/{id}/telemetry?window=1h` runs up to 20 applicable bindings (4 at a time) and returns, per binding:

| status | meaning |
|---|---|
| `measured` | fresh value from the system of record, with `measured_at` |
| `inferred` | derived: a counter delta (`natsmon rate`), a sampled Pulse rollup, or a link built without asking Grafana |
| `stale` | the latest query failed; the last good value (≤ 10 × freshness old) is shown with the error |
| `unavailable` | no value: adapter down, timeout, no data, or the binding is not applicable to this entity |

Errors never include upstream response bodies. Each integration allows `limits.telemetry_concurrency` queries at a
time (default 4); each query times out after `limits.telemetry_timeout` (10 s); identical concurrent queries are
coalesced; results are cached (LRU, `limits.telemetry_cache_entries`) for the binding's `freshness_seconds`, with
range queries aligned to their step so viewers share entries. An empty answer ("no data") is reused for at most 15 s,
so data a backend has only just ingested appears without waiting out the freshness period.

## Traffic overlays
`POST /telemetry/edge-rates {"relation_ids": [...], "window": "15m"}` returns a rate per relationship whose
endpoints you can read:
- `publishes` edges from a service with `spec.pulse_source` to a `nats_subject`: messages per second seen by the
  `evt.>` tail for that publisher and subject pattern (**measured**; inferred when the tail was sampling);
- `subscribes_to` edges: the subject's publish rate, **inferred** and labelled "delivery to this subscriber is not
  measured" — Pulse records no per-subscriber delivery;
- any type with an `edge_rate` binding: the binding's value;
- everything else: `unavailable`, and the UI does not animate it.

## Node metrics
`POST /telemetry/map-metrics {"entity_ids": [...], "window": "15m"}` runs the bindings marked `on_map` that apply to
each entity (up to 200 entities and 6 bindings per entity per request, 8 at a time) and returns, per entity you can
read, the binding, title, value, unit, status, health (for health bindings) and `measured_at`. Unavailable and
not-applicable results are left out, so an entity without data has no entry. Results share the binding cache with the
entity page. The map draws them inside detailed node cards, up to three per row under the name — e.g. CPU, memory
and disk usage from node_exporter bindings guarded by `up{job="node",instance="{{instance}}"}` — refreshed every
minute, coloured by health and greyed when stale.

## Live updates
`GET /api/v1/stream/telemetry?entity=<id>&relation=<id>…&interval=30&window=1h` is a separate SSE stream from the
topology stream (`/api/v1/stream`), so metric ticks never trigger a graph re-layout. It sends `telemetry` and
`edge_rates` events every `interval` seconds (15–300), re-checks credentials and access each tick without extending
the session's idle timer, and closes with a `closed` event when access ends. At most `limits.telemetry_streams`
streams run at once.

## Metrics
`deed_telemetry_queries_total{adapter,result}`, `deed_telemetry_query_duration_seconds{adapter}`,
`deed_telemetry_integration_up{adapter,integration}` and `deed_cache_requests_total{cache="telemetry",result}`.

## Validation status
Adapters are tested against API-shaped fixtures (`internal/telemetry/telemetry_test.go`). Prometheus, Loki and
Grafana are additionally exercised against the docker-compose stack by `scripts/e2e.sh`.

`scripts/validate/telemetry.sh` validates the `prometheus` adapter against **Grafana Mimir 3.2.1** (monolithic,
multitenancy on, samples pushed through remote write for two tenants) and the `opensearch` adapter against
**OpenSearch 3.8.0** with the security plugin (HTTPS with the demo CA, basic auth, a least-privilege reader), both in
Docker on loopback. It compares every supported shape (scalar, health, `{{window}}`, timeseries and table for Mimir;
scalar and timeseries for OpenSearch) with the same queries sent directly to the backend, and checks tenant
isolation, a missing or unknown tenant, wrong credentials, a forbidden index, rejected queries, an unreachable
endpoint, stale values after credential rotation and an outage, and that no upstream body or secret reaches Deed's
answers or log. Production Grafana validation is **pending**.
