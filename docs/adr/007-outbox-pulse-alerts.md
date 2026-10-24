# ADR-007: Transactional outbox to Pulse and alert lifecycle

## Outbox
- Domain mutations insert `outbox` rows in the same transaction. A relay publishes
  `evt.deed.<area>.<entity>.<action>` with type `deed.<area>.<entity>_<action>.v1`, `pulse.WithID(outboxID)` (the
  2-minute JetStream dedup window absorbs relay retries; consumers dedup on the event ID beyond that) and
  `pulse.WithTime(created_at)`.
- Retries: exponential backoff with jitter capped at 5 min; dead-letter after 50 attempts or 24 h; published rows
  retained 7 days; backlog bounded at 100,000 unpublished rows (oldest dead-lettered first, metric + alert).
- Pulse downtime never blocks commits. Metrics: `deed_outbox_backlog`, `deed_outbox_published_total{result}`,
  `deed_outbox_oldest_age_seconds`.
- Payloads never contain secrets (keys checked by `redact.IsSensitiveKey`) and never contain raw spec values of
  custom kinds — only IDs, names, kinds, projects and change summaries.

## Alerts (Deed-owned)
Pulse has no application alert API (`_sys.alert.*` is pulsed-internal). Deed therefore owns alerts:
- Findings (open/closed, severity `critical|warning|info`, class `confirmed|warning|unknown|stale|not_applicable`)
  feed **alert rules** (by finding type, severity, project, label selector).
- Lifecycle: `pending` (for `for:` duration) → `firing` → `resolved`; dedup key = rule + finding key; cooldown
  between re-notifications (default 1 h); suppression windows (silences) by matcher and expiry; evidence = the
  finding's evidence JSON and entity links.
- Delivery: outbox events `evt.deed.alert.alert.raised|resolved` (type `deed.alert.alert_raised.v1` …) with
  retries via the relay; Pulse threshold rules or outbound webhooks fan them out. Delivery state per alert is visible.
