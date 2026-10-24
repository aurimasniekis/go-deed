# ADR-011c: Edge styling and the map options panel

- **Color** by relationship type (palette per type, editable in the options drawer); alternative modes: health,
  freshness, source.
- **Stroke** encodes evidence: solid = declared/structural; dashed = inferred (hover shows evidence); dotted =
  stale; double/thick = bridge (cross-project); faded = unknown.
- **Width** = log-scaled measured rate, only when a fresh measurement exists.
- Arrowheads always show direction; optional mid-edge type labels.
- **Animation**: *Traffic flow* (dash-offset, speed ∝ measured rate, only on fresh measured edges, labelled
  "sampled, N-min window") and *Direction hint* (marching dash, off by default, labelled "not traffic"). Both off
  under `prefers-reduced-motion` or the user toggle.
- **Routing**: bezier, smoothstep or straight; high-degree nodes bundle edges by type.
- **Options drawer** (persisted per saved view): filters (kind, relationship type, project, source, environment,
  owner, health, freshness, evidence class), grouping (project, environment, kind, host, none), layout (ELK layered,
  force, radial-around-focus; direction), edge color mode, routing, animation modes, labels, icon size, compact or
  detailed node cards.
- The **legend** is generated from the active styles; every color, stroke and animation on screen is explained.
