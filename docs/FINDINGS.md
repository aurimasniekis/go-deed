# Findings and alerts

A **finding** is a fact about the catalog or about Deed itself that someone may need to act on: an expiring
certificate, a failing source, drift between a declaration and what was observed. An **alert** is a finding
selected by an alert rule and moved through a lifecycle with notifications (ADR-007). Deed owns both; it delivers
alert notifications as Pulse events and does not send e-mail or chat messages itself.

## Severity and class
Every finding has a **severity** (`critical`, `warning`, `info`) and a **class** that says how strong the evidence
is:

| Class | Meaning |
|---|---|
| `confirmed` | the condition is known to be true (an expired certificate, a failed run) |
| `warning` | a risk that has not materialized yet (expires soon, possible duplicate) |
| `stale` | the data is too old to trust |
| `unknown` | no data to decide |
| `not_applicable` | the check does not apply |

`unknown` and `not_applicable` are defined and accepted by filters, but no current check produces them.

## Finding types

| Type | Severity | Class | Produced when |
|---|---|---|---|
| `certificate_expired` | critical | confirmed | a `certificate` entity's `spec.not_after` is in the past |
| `certificate_expiring` | critical within 7 days, else warning within 30 days | warning | `spec.not_after` is near |
| `domain_expiring` | critical within 7 days, else warning within 30 days | warning | a `domain` entity's `spec.expires_at` is near (evidence includes `auto_renew`) |
| `invalid_schema` | warning | confirmed | an entity does not validate against its kind's schema (evidence lists the errors) |
| `stale_observation` | info | stale | an entity's freshness is `stale` |
| `missing_owner` | info | confirmed | a `service`, `application`, `repository`, `server`, `vm`, `database` or `domain` has no `metadata.owner` and no `owned_by` relationship |
| `declared_observed_drift` | warning | confirmed | declared and observed versions differ (ignoring a leading `v`), or a declared `/spec/…` value differs from an imported or discovered candidate (evidence lists pointer, both values and source) |
| `source_conflict` | warning | confirmed | sources in the same layer assert different values for an entity's fields |
| `duplicate_candidate` | info | warning | several entities of one kind in one project share an identity key |
| `missing_relationship_target` | warning | confirmed | a source referenced an entity that does not exist (an unresolved reference) |
| `stale_source` | warning | stale | an enabled source had no successful sync or heartbeat within 3× its expected interval |
| `ingestion_failed` | warning | confirmed | an enabled source's latest run failed (message = the source's last error) |
| `absent_from_source` | info | stale | an entity is no longer in its source's latest complete snapshot and the source's `on_absent` policy is `orphan` (at most 1000 per evaluation) |
| `expected_connection_unseen` | warning | warning | a declared `publishes` or `subscribes_to` edge has no observed assertion in the last 24 hours, in a project with a fresh `pulse_tail` source ([PULSE.md](PULSE.md)) |
| `version_update_available` | info | confirmed | the version checker found a newer release |
| `version_end_of_life` | critical after the EOL date, else warning | confirmed | the version checker found the release is (or will be) end of life |
| `integration_unreachable` | warning | confirmed | an enabled telemetry integration failed its last connectivity test |
| `backup_stale` | critical | confirmed | backups are enabled and none succeeded within two `backup.interval`s |
| `events_dead_lettered` | warning | confirmed | at least one outbox event was dead-lettered in the last 24 hours |
| `breakglass_used` | critical | confirmed | a single-use recovery link signed in without the second factor; kept open for 24 hours |
| `backup_restored` | warning | confirmed | the first start after `deed restore`; kept open for 7 days |

The certificate and domain thresholds (30 and 7 days), the list of kinds that need an owner and the 24-hour window
for expected connections are built-in defaults, not configuration keys. Version findings need the version checker
(`versions.enabled`, default on, every `versions.interval`, default 12 h); end-of-life data is fetched only with
`versions.eol: true`, from https://endoflife.date (or a compatible API). `TestRun` covers the checker against a
local fixture server; `DEED_TEST_EOL_URL=https://endoflife.date go test -run TestLiveEndOfLife ./internal/versions/`
checks it against the live API (last run 2026-10-25: PostgreSQL 12 and MySQL 5.7 end of life, PostgreSQL 17 with an
update available).

## Evaluation
The findings engine recomputes every finding every two minutes and reconciles the result with the stored set:
- a new finding is inserted as `open` and emits `evt.deed.findings.finding.opened`;
- a finding still present has its severity, class, title, message, evidence and `last_seen_at` updated (a closed
  one is reopened with a new `first_seen_at`);
- a finding no longer present is set to `closed` with `closed_at` and emits `evt.deed.findings.finding.closed`.
  Evaluation never deletes findings; closed findings are pruned daily after `retention.closed_findings`
  (default 90 days).

Each finding has a `dedup_key` (type plus subject, e.g. `certificate_expiring|ent_…`), so the same condition keeps
the same finding across evaluations. The alert engine runs after every evaluation.

`POST /api/v1/findings/evaluate` runs an evaluation (and the alert engine) immediately. It needs an installation
owner or admin and returns `204`.

## Findings API

| Endpoint | Who | Notes |
|---|---|---|
| `GET /api/v1/findings` | users | filters: `project`, `entity`, `type`, `severity`, `class`, `state` (`open` default, `closed`, `all`), `limit` (1–1000, default 200); ordered by severity, then most recently seen |
| `GET /api/v1/findings/{id}` | users | |
| `POST /api/v1/findings/{id}/acknowledge` | project editor; installation owner/admin for installation findings | records `acknowledged_at` / `acknowledged_by` and audits `finding.acknowledged` |

Findings are filtered by the projects the caller can read. Installation-level findings (no project, such as
`backup_stale` or `integration_unreachable`) are shown only to installation owners, admins and auditors.
Acknowledging is informational: it does not close the finding or affect alerts.

## Alert rules
A rule selects open findings with a **matcher**; every field is optional and all given fields must match:

| Matcher field | Matches |
|---|---|
| `types` | finding types |
| `min_severity` | `info` < `warning` < `critical` |
| `classes` | finding classes |
| `projects` | project IDs |

Rule fields: `name` (required, unique, up to 100 characters; not changed by updates), `description`, `enabled`
(default true), `matcher`, `for_seconds` (0–86400, default 0), `cooldown_seconds` (0–604800, default 3600; 0 notifies on every transition;
on update an omitted field keeps its value) and
`severity` (overrides the finding's severity).

Built-in rules are created on first start. They can be edited or disabled, not deleted:

| Rule | Matcher | For | Cooldown |
|---|---|---|---|
| `critical-findings` | `min_severity: critical` | 0 | 6 h |
| `certificates` | `certificate_expiring`, `certificate_expired`, `domain_expiring` | 0 | 24 h |
| `ingestion` | `ingestion_failed`, `stale_source`, `integration_unreachable` | 10 min | 6 h |
| `security` | `breakglass_used` | 0 | 1 h |
| `event-flow` | `expected_connection_unseen` | 30 min | 12 h |

```sh
curl -s -X POST https://deed.example.com/api/v1/alert-rules -H "Authorization: Bearer $DEED_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name": "prod-drift", "matcher": {"types": ["declared_observed_drift"], "projects": ["prj_EXAMPLE"]},
       "for_seconds": 900, "cooldown_seconds": 86400}'
```

| Endpoint | Who |
|---|---|
| `GET /api/v1/alert-rules` | users |
| `POST /api/v1/alert-rules` | installation owner/admin |
| `PUT /api/v1/alert-rules/{id}` (`If-Match` supported) | installation owner/admin |
| `DELETE /api/v1/alert-rules/{id}` | installation owner/admin; custom rules only |

## Alert lifecycle
One alert exists per rule and finding (dedup key = rule ID + finding key). On each evaluation:

1. A matching finding without an alert creates one in `pending`.
2. A `pending` alert becomes `firing` once it has been pending for the rule's `for_seconds`.
3. A `firing` alert that is not silenced is notified (`raised`) when it has never been notified or the last
   notification is at least `cooldown_seconds` old. While the finding stays open, it is re-notified after every
   cooldown.
4. When the finding closes, stops matching, or the rule is disabled, the alert becomes `resolved`. A `resolved`
   notification is sent only if the alert was firing and had been notified; a pending alert resolves silently.
5. If the finding comes back, the resolved alert returns to `pending` and the cycle restarts.

The alert copies the finding's title and evidence and takes the rule's severity override if set.

## Silences
A silence suppresses notifications of matching alerts for a time; the alert still advances through its states.
A firing alert under a silence is reported with state `suppressed` and `silenced_by` set. Silences use the same
matcher (severity is compared after any rule override).

```sh
curl -s -X POST https://deed.example.com/api/v1/silences -H "Authorization: Bearer $DEED_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"matcher": {"types": ["stale_source"]}, "comment": "planned maintenance", "hours": 4}'
```

`hours` is 1–2160 (90 days); the silence starts immediately. `GET /api/v1/silences` lists them (users);
`POST /api/v1/silences` and `DELETE /api/v1/silences/{id}` need an installation owner or admin. Creating and
deleting silences and saving or deleting rules are audited (`alert.silence_created`, `alert.silence_deleted`,
`alert.rule_saved`, `alert.rule_deleted`).

## Delivery
Notifications are outbox events, published to Pulse by the relay with its retries and dead-lettering
([PULSE.md](PULSE.md)):

| Subject | Type |
|---|---|
| `evt.deed.alert.alert.raised` | `deed.alert.alert_raised.v1` |
| `evt.deed.alert.alert.resolved` | `deed.alert.alert_resolved.v1` |

Payload: `alert_id`, `rule`, `finding_type`, `severity`, `title`, `evidence`, `at`, and `project_id` for project
findings. Fan-out to people (Pulse rules, webhooks) happens outside Deed.

`GET /api/v1/alerts?state=pending|firing|suppressed|resolved&limit=…` (users; default 200, max 500) returns each
alert with `notifications`, `last_notified_at` and `delivery`: the outbox state of the last notification
(`pending`, `retrying`, `published` or `dead`; empty before the first notification). Project alerts follow
project read access; installation-level alerts are shown to owners, admins and auditors.

## Dashboard summary
`GET /api/v1/dashboard` (optionally `?project=…`, repeatable) returns:

| Field | Content |
|---|---|
| `counts` | entity counts for the readable projects |
| `finding_counts`, `findings_by_type` | open findings by severity and by type |
| `expiring` | up to 20 certificate and domain findings |
| `unresolved` | up to 20 `missing_relationship_target` findings |
| `versions` | up to 20 version findings |
| `drift` | up to 20 `declared_observed_drift` and `source_conflict` findings |
| `sources` | freshness, last success and last error of every non-system source |
| `alerts` | up to 50 pending, firing and suppressed alerts |
| `recent_changes` | the 25 latest catalog changes |
| `outbox` | pending, dead, published in 24 h, oldest pending, last error (owners, admins, auditors only) |
| `synthetic_data` | true when a readable project holds demo data |

The finding counts are computed from at most 1000 open findings.

## Metrics
`deed_findings_open{type,severity}`, `deed_alerts_firing{severity}` (firing and not silenced) and
`deed_alerts_transitions_total{to}` (`firing`, `resolved`).
