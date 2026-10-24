# ADR-009: Live updates over SSE

- SSE chosen over WebSockets: one-directional updates, works through Cloudflare, automatic browser reconnect with
  `Last-Event-ID`, no extra protocol.
- `GET /api/v1/stream?project=…&topics=topology,findings` (session or token auth). Every topology event's `id` is the
  `change_log.seq`. On reconnect with `Last-Event-ID`, the server replays from `change_log` when the gap is within
  24 h and 10,000 events; otherwise it sends `event: resync` and the client refetches.
- Per-client bounded channel (256 events). On overflow the server drops the queue, sends `resync`, and counts
  `deed_live_dropped_total`. Heartbeat comment every 20 s. Credentials are re-validated every 60 s; revoked → stream
  closes.
- Authorization: events are filtered per subscriber by readable projects; events for unreadable projects are never
  sent (not even redacted).
- Telemetry ticks are a separate stream (`/api/v1/stream/telemetry?view=…`) so topology changes and metric updates
  never contend; the UI never re-runs layout on a telemetry tick.
- `write_timeout` does not apply to the stream handler (it uses `http.ResponseController` to extend deadlines).
