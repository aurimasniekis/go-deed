# ADR-008: Telemetry adapters, named bindings and the SSRF-safe client

- `telemetry.Adapter` interface: `Kind()`, `Test(ctx)`, `Query(ctx, Binding, Params) (Result, error)`.
  Implementations: `prometheus` (Prometheus HTTP API incl. Mimir with `X-Scope-OrgID`), `loki` (query_range,
  counts only), `natsmon` (NATS monitoring `/varz`, `/connz`, `/jsz`), `pulse` (subject info from the admin API),
  `grafana` (search dashboards, resolve links; never a metrics store), `opensearch` (`_count`/`_search` aggregations).
- **Named bindings**: admin-authored templates (`binding_definitions`): adapter integration, query template with
  typed parameters (`string` with regex, `enum`, `duration` with bounds), parameter sources bound to entity fields
  (`{{ .spec.hostname }}`), result shape (`scalar|timeseries|table`), unit, thresholds, freshness window. Users with
  read access can *execute* bindings for entities they can read; they cannot author queries. Values are escaped for
  the query language (PromQL label matcher escaping, LogQL, Lucene).
- Limits per adapter: TTL cache (default 30 s), singleflight, concurrency semaphore (default 4), timeout (10 s),
  response size budget (2 MiB), max series (500), max points (11,000).
- Results carry `quality: measured|inferred|stale|unavailable`, `observed_at`, and partial-failure details.
- **safehttp**: per-integration allowlist of `scheme://host:port`; IP check at dial time (rebinding-safe); blocked by
  default: loopback, RFC 1918/ULA (unless in configured `allow_cidrs`), link-local, metadata `169.254.169.254` and
  `fd00:ec2::254` (always blocked), unspecified, multicast, CGNAT, 6to4/Teredo/NAT64; redirects re-validated with a
  3-hop cap; no environment proxies; TLS ≥ 1.2.
