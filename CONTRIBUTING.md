# Contributing

Thanks for your interest. Issues and pull requests are welcome; please open an issue first for larger changes.

## Development
```sh
make build          # bin/deed, bin/deed-agent
make check          # gofmt, tidy, vet, txscope analyzer, golangci-lint, race tests, OpenAPI drift, promtool rule tests, web checks
make e2e            # spec §18 scenario against deploy/compose.dev.yaml (needs Docker, Terraform, Python 3)
```
Integration tests use a local `nats-server` and `minio` when installed, or `DEED_TEST_NATS` /
`DEED_TEST_S3` (see docs/TESTING.md). The web UI lives in `web/` (pnpm); `make web-gen` regenerates the typed API
client after API changes, and `make openapi` regenerates `api/openapi.json`.

## Rules of the road
- `/api/v1` changes are additive only (docs/VERSIONING.md); the Terraform provider and agents depend on it.
- Write transactions stay short (ADR-001): no network I/O inside them, no password hashing, no large loops.
- Never log, store or return secrets; connectors and importers keep allowlisted fields only.
- Public repository: examples use `example.com` and RFC 5737 / RFC 1918 addresses; no real hostnames, IPs, state
  files or credentials in code, tests or docs.
- Commit messages follow Conventional Commits; user-visible changes get a CHANGELOG.md entry.

By contributing you agree that your contributions are licensed under the Apache License 2.0.
