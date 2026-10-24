# Deed

**Declarative infrastructure catalog and live relationship map, powered by Go, Terraform, and React Flow.**

Deed is a source-aware graph of infrastructure, software, identities and operational relationships. It answers what
you have, where it runs, who owns it, how it is connected, what depends on it, which services publish or consume
which Pulse/NATS events, and — for every value — where it came from and how fresh it is.

> Status: under active development. See [docs/ACCEPTANCE.md](docs/ACCEPTANCE.md) for the capability matrix and
> [docs/VALIDATION.md](docs/VALIDATION.md) for what has been validated and how.

## Concepts
- **Entities** of built-in or custom **kinds** (JSON Schema validated, versioned) in **projects**.
- **Relationships** of built-in or custom types, structural or observed, with evidence.
- **Sources** write **facets** in layers — declared, imported, discovered, observation, annotation/override — that
  are merged deterministically with field-level provenance and conflict reporting.
- Ingestion from **Terraform state** (S3, HTTP backend, push), the **Terraform provider**
  ([terraform-provider-deed](https://github.com/aurimasniekis/terraform-provider-deed), registry address
  `aurimasniekis/deed`), the **source protocol API**, **Pulse** events and built-in connectors.

## Documentation
- [Discovery and decisions](docs/DISCOVERY.md), [ADRs](docs/adr/README.md), [Threat model](docs/THREAT_MODEL.md)
- [Versioning](docs/VERSIONING.md), [Compatibility](docs/COMPATIBILITY.md)

## License
Apache License 2.0. Copyright 2026 Aurimas Niekis. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
