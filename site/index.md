---
layout: home
hero:
  name: Deed
  text: Know what you run, and why you know it.
  tagline: A source-aware graph of infrastructure, software, identities and operational relationships — declared with Terraform, imported from state, observed at runtime, with provenance and freshness for every value.
  image:
    src: /logo.svg
    alt: Deed
  actions:
    - theme: brand
      text: What is Deed?
      link: /guide/what-is-deed
    - theme: alt
      text: Try it locally
      link: /guide/try-locally
    - theme: alt
      text: GitHub
      link: https://github.com/aurimasniekis/go-deed
features:
  - title: Layered facts, not last-writer-wins
    details: Terraform declarations, imported state, runtime discovery and human overrides are kept as separate facets and merged deterministically, with field-level provenance and conflicts you can see.
  - title: Terraform both ways
    details: Declare entities and relationships with the aurimasniekis/deed provider; import read-only from Terraform state (push, S3, HTTP backend) with allowlist-only extraction that never stores secrets.
  - title: Event flows from Pulse
    details: Declared publish/subscribe grants from the Pulse admin API, observed publishers from a read-only evt.> tail, and measured rates on the map — labelled measured, inferred or unavailable.
  - title: A map that stays readable
    details: Server-side neighbourhoods with depth and node caps, fan clustering, impact and path queries, grouping by kind, location, team or any label, show / hide, telemetry inside node cards, saved views, live updates over SSE, and an accessible table for every relationship.
  - title: Operations built in
    details: Findings and alerts (certificates, drift, stale sources, end-of-life versions), named telemetry bindings for Prometheus, Loki, NATS, OpenSearch and Grafana, Prometheus metrics and alert rules.
  - title: Safe by default
    details: Argon2id + TOTP, OIDC SSO (Porch first), project-scoped roles and tokens, service accounts, SSRF-guarded outbound calls, a hash-chained audit log, and age-encrypted backups with signed manifests.
---

<div class="home-shots">

<h2>The map</h2>
<img class="ss-light" src="docs/assets/screenshots/map-metrics-light.png" alt="Map: the neighbourhood of a service, edges coloured by relationship type, measured traffic rates and telemetry values inside the node cards"><img class="ss-dark" src="docs/assets/screenshots/map-metrics-dark.png" alt="Map: the neighbourhood of a service, edges coloured by relationship type, measured traffic rates and telemetry values inside the node cards">
<p>A focused neighbourhood: relationship types by colour, evidence by line style, measured rates only where a measurement exists, and the telemetry you choose inside each card.</p>

<div class="grid">
<div>
<h2>Dashboard</h2>
<img class="ss-light" src="docs/assets/screenshots/dashboard-light.png" alt="Dashboard: entities, relationships, findings, alerts, health and freshness"><img class="ss-dark" src="docs/assets/screenshots/dashboard-dark.png" alt="Dashboard: entities, relationships, findings, alerts, health and freshness">
</div>
<div>
<h2>Why does this value exist?</h2>
<img class="ss-light" src="docs/assets/screenshots/entity-provenance-light.png" alt="Entity provenance: winning source, layer and revision per field, and the losing candidates"><img class="ss-dark" src="docs/assets/screenshots/entity-provenance-dark.png" alt="Entity provenance: winning source, layer and revision per field, and the losing candidates">
</div>
<div>
<h2>Colour by source</h2>
<img class="ss-light" src="docs/assets/screenshots/map-source-light.png" alt="Map coloured by asserting source, with the edge inspector"><img class="ss-dark" src="docs/assets/screenshots/map-source-dark.png" alt="Map coloured by asserting source, with the edge inspector">
</div>
<div>
<h2>Findings</h2>
<img class="ss-light" src="docs/assets/screenshots/findings-light.png" alt="Findings: certificates, drift, conflicts, missing targets and available updates"><img class="ss-dark" src="docs/assets/screenshots/findings-dark.png" alt="Findings: certificates, drift, conflicts, missing targets and available updates">
</div>
<div>
<h2>Group by anything</h2>
<img class="ss-light" src="docs/assets/screenshots/map-grouped-light.png" alt="Map overview grouped by a label, two groups expanded in place into containers of their members"><img class="ss-dark" src="docs/assets/screenshots/map-grouped-dark.png" alt="Map overview grouped by a label, two groups expanded in place into containers of their members">
</div>
<div>
<h2>Show / hide</h2>
<img class="ss-light" src="docs/assets/screenshots/map-show-hide-light.png" alt="Map with the show / hide panel open: kinds, label values and relationship types with counts"><img class="ss-dark" src="docs/assets/screenshots/map-show-hide-dark.png" alt="Map with the show / hide panel open: kinds, label values and relationship types with counts">
</div>
<div>
<h2>Quick actions</h2>
<img class="ss-light" src="docs/assets/screenshots/map-quick-actions-light.png" alt="Map with the quick-actions menu of a service open"><img class="ss-dark" src="docs/assets/screenshots/map-quick-actions-dark.png" alt="Map with the quick-actions menu of a service open">
</div>
<div>
<h2>Label rules</h2>
<img class="ss-light" src="docs/assets/screenshots/project-label-rules-light.png" alt="Project settings: the label rules that add labels to entities other sources own"><img class="ss-dark" src="docs/assets/screenshots/project-label-rules-dark.png" alt="Project settings: the label rules that add labels to entities other sources own">
</div>
</div>

<p>Screenshots show clearly labelled synthetic data. More in the <a href="/go-deed/guide/ui">web UI tour</a>.</p>

</div>
