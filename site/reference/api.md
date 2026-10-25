# HTTP API

Every endpoint lives under `/api/v1` and is described by an OpenAPI 3.1 document:
[`api/openapi.json`](https://github.com/aurimasniekis/go-deed/blob/main/api/openapi.json), also printed by
`deed openapi`. Changes to `/api/v1` are additive only ([Versioning](/reference/versioning)).

- **Authentication:** browser sessions (`__Host-deed_session` cookie plus the `X-Deed-CSRF` header on unsafe
  methods) or bearer tokens: `deed_at_…` user API tokens and `deed_st_…` source tokens.
- **Errors:** RFC 9457 problem documents with a stable `code` (`validation_failed`, `not_found`, `conflict`,
  `precondition_failed`, `rate_limited`, …) and per-field errors; 429 and 503 carry `Retry-After`.
- **Concurrency:** `ETag`/`If-Match` on mutable resources; `Idempotency-Key` makes POST retries safe.
- **Live updates:** `GET /api/v1/stream` (catalog changes, resumable with `Last-Event-ID`) and
  `GET /api/v1/stream/telemetry` (telemetry snapshots), both Server-Sent Events.

The source push protocol has its own page: [Ingest API](/reference/ingest-api).
