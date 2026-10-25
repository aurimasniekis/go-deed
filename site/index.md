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
    details: Server-side neighborhoods with depth and node caps, fan clustering, impact and path queries, saved views, live updates over SSE, and an accessible table for every relationship.
  - title: Operations built in
    details: Findings and alerts (certificates, drift, stale sources, end-of-life versions), named telemetry bindings for Prometheus, Loki, NATS, OpenSearch and Grafana, Prometheus metrics and alert rules.
  - title: Safe by default
    details: Argon2id + TOTP, OIDC SSO (Porch first), project-scoped roles and tokens, SSRF-guarded outbound calls, a hash-chained audit log, and age-encrypted backups with signed manifests.
---
