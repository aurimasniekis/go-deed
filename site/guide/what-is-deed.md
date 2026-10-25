# What is Deed?

Deed answers questions that usually need five tabs and a memory: *what do we have, where does it run, who owns it,
how is it connected, what breaks if this goes down, which services publish or consume which events — and where did
each of those facts come from, and how fresh is it?*

It is a single Go binary with an embedded web UI and a SQLite database, designed for homelabs and small
infrastructure teams that already describe parts of their world in Terraform and run an event bus like
[Pulse](https://github.com/aurimasniekis/go-pulse) (NATS JetStream).

## The model
- **Entities** of built-in kinds (server, VM, container, service, DNS record, certificate, repository, NATS subject,
  Porch application, HallPass agent, …) or **custom kinds** you define with JSON Schema.
- **Relationships** (`runs_on`, `depends_on`, `publishes`, `authenticates_with`, `protected_by`, …) with evidence:
  declared, structural, observed or inferred.
- **Projects** separate unrelated estates (home, parents' network, a client). Cross-project edges need an explicit
  **bridge**.
- **Sources** write **facets** — never directly into the entity. The effective entity is a deterministic merge with
  field-level provenance; disagreements in the same layer become visible conflicts.

## Where facts come from
| Source | Layer | How |
|---|---|---|
| Terraform provider | declared | `deed_entity`, `deed_relationship`, `deed_kind` resources |
| API / UI | declared, annotation, override | REST API, inline edits |
| Terraform state | imported | push from CI, S3 or HTTP backends; allowlisted attributes only |
| GitHub, HallPass, Porch, Pulse admin | imported | built-in connectors with read-only tokens |
| Pulse tail, agents, ingest API | discovered | observed publishers, `deed-agent` (Docker), any program with a source token |
| Observations API, agents | observation | time-bounded health and observed values; never overwrite declared spec |

Telemetry (Prometheus/Mimir, Loki, NATS monitoring, OpenSearch, Grafana links) is not a source: Deed queries it
through admin-defined bindings, caches summaries briefly and shows them next to entities and edges.

## What it is not
Deed is not a metrics or log store (it queries and caches summaries), not a CMDB that forces one source of truth,
and not a deployment tool: it never changes your infrastructure.

## Status
Under active development, not released, not externally security reviewed. The
[validation record](/reference/validation) lists what has been exercised end to end and what is still pending.
