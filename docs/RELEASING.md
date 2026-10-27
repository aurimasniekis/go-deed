# Releasing Deed

A release is cut from `main` by pushing a `vX.Y.Z` tag (docs/VERSIONING.md). The release workflow
(`.github/workflows/release.yaml`, adapted from Porch) stops at a **draft** GitHub release that a maintainer reviews
and publishes. Nothing has been released yet.

> **Not externally reviewed.** Deed has had no external security review. Every release keeps this note in its
> CHANGELOG section until one has been done and its findings are resolved.

| Job | What it does | Fails the release when |
|---|---|---|
| `meta` | Validates the tag and the `## [X.Y.Z] - YYYY-MM-DD` CHANGELOG section; derives version, commit time and image name (`ghcr.io/aurimasniekis/go-deed`) | bad tag, missing changelog section |
| `test` | `make vet race` with `DEED_TEST_TX_MAX=500ms` | any failure |
| `web` | `pnpm install --frozen-lockfile && pnpm build` (typecheck, Vite build into `internal/webui/dist`, `check-dist`) | build or dist check fails |
| `binaries` | `scripts/release-build.sh`: static `deed` and `deed-agent` for linux/amd64, linux/arm64, darwin/arm64 with the UI embedded; smoke-runs `deed version --json`; syft SPDX SBOMs (binaries, source tree); `SHA256SUMS` | build, version mismatch, dynamic binary |
| `image-scan` | Builds the image per architecture; Trivy fails on fixable HIGH/CRITICAL; `scripts/container-smoke.sh` on amd64 | fixable HIGH/CRITICAL, smoke failure |
| `image-publish` | Tag runs only: multi-arch manifest with provenance and SBOM to GHCR (and Docker Hub when `DOCKERHUB_TOKEN` is set), Trivy on the pushed digest, build provenance attestation | push or scan failure |
| `attest-binaries` | Tag runs only: provenance attestation over `SHA256SUMS` | — |
| `github-release` | Tag runs only: draft release with the CHANGELOG section, image digest, archives, SBOMs, checksums | — |

## Procedure
1. `make check` and `scripts/e2e.sh` green on `main`; CI green.
2. Move `## [Unreleased]` entries into `## [X.Y.Z] - YYYY-MM-DD` in CHANGELOG.md; commit.
3. `git tag -s vX.Y.Z -m vX.Y.Z && git push origin vX.Y.Z`.
4. Review the draft release (notes, SBOMs, Trivy output in the run log) and publish it.

## Dry run (nothing published)
Dispatch the workflow from a branch: `gh workflow run release.yaml --ref main`. It runs the release gate (race
tests), builds the web UI, the static binaries, checksums and SBOMs, and builds and Trivy-scans the amd64 and arm64
images without pushing; attestation, the draft release and image publication are skipped because the ref is not a
`v*` tag. Last dry run: 2026-10-25, run 40158630244 at `2d0d0e1` — every build and scan job succeeded; the three
publishing jobs were skipped.

## Release record
- **v0.7.0** (2026-10-27, `4275583`, tag checked against the release commit): release run 40563603655 succeeded.
  Checked before publishing: checksums, provenance of the archive and the image (GHCR and Docker Hub), and an upgrade
  of a v0.6.0 database by the release binary: snapshot, migration 10 → 11, the one-time re-merge (13 entities, rules
  version 3) tombstoning an entity that only a UI annotation kept alive under 0.6.0, a new label rule labelling the
  project's DNS zones and the overview grouping by it, audit chain verified.
- **v0.6.0** (2026-10-27, `cacf828`, tag checked against the release commit): release run 40529323045 succeeded.
  Checked before publishing: checksums, provenance of the archive and the image (GHCR and Docker Hub), and a v0.5.1
  database served by the release binary (no migration, no re-merge: rules version already current; entities, the
  map with `hideKind`/`hideType` filters and the audit chain intact).
- **v0.5.1** (2026-10-27, `dd83b72`, tag checked against the release commit): release run 40521323785 succeeded.
  Checked before publishing: checksums, provenance of the archive and the image (GHCR and Docker Hub), and a v0.5.0
  database with a conflict row recorded under the old rules (a `terraform/address` annotation) started by the release
  binary: no migration, 12 entities re-merged once in the background, the stale conflict gone, rules version 2
  recorded, entities and audit chain intact.
- **v0.5.0** (2026-10-27, `3a4921e`, tag checked against the release commit): release run 40517045515 succeeded.
  Checked before publishing: checksums, provenance of the archive and the image (GHCR and Docker Hub), a v0.4.0
  database served by the release binary (no migration; entities, bindings and the audit chain intact), and infra's
  certificate collision replayed: three pushed states whose origin certificates v0.4.0 merged into
  `certificate/origin-origin` (with a non-RFC 3339 `not_after`) re-pushed to the release binary gave three
  certificates named after their hosts with RFC 3339 dates, the old name kept as an alias.
- **v0.4.0** (2026-10-27, `e8f12b4`, tag checked against the release commit): release run 40505500552 succeeded.
  Checked before publishing: checksums, provenance of the archive and the image (GHCR and Docker Hub), and an upgrade
  of a v0.3.2 database (a Terraform-imported project, a Prometheus integration and a binding) by the release binary:
  snapshot, migration 9 → 10, entities and the binding intact, `guard`, `when_empty` and `on_map` set on the old
  binding, audit chain verified.
- **v0.3.2** (2026-10-27, `4f1ef6f`, tag checked against the release commit): release run 40483001616 succeeded.
  Checked before publishing: checksums, provenance of the archive and the image, and infra's failing case replayed: a
  v0.3.0 database whose Workspace import had bound `domain` entities (one adopted by a hand-declared hub domain),
  re-synced by the release binary with the 0.3.2 mappings: run `ok`, 0 item errors, the records-less zone derived, the
  hub domain containing its zone and its Workspace service.
- **v0.3.1** (2026-10-26, `89d8a3f`, tag checked against the release commit before pushing): release run 40401862832
  succeeded. Checked before publishing: checksums, SLSA provenance of the archive and the image, the 0.3.0 database
  served without migration, and the zone-module fixture pushed to the release binary: zones derived from records, and a
  hand-declared `example.com` domain containing its DNS zone and its Google Workspace domain.
- **v0.3.0** (2026-10-26, `0aeba06`): release run 40396208532 succeeded in every job. Checked before publishing:
  checksums, SLSA provenance of the linux/amd64 archive and the image, and an upgrade of a v0.2.0 database (demo
  data and a service account) by the release binary: snapshot, migration 8 → 9, data and token intact, the `region`
  grouping served.
- **v0.2.0** (2026-10-26, `1304838`): release run 40377515163 succeeded in every job; images on GHCR and Docker Hub.
  Checked before publishing: checksum and SLSA provenance of the linux/amd64 archive and of the image, and an upgrade
  of a v0.1.0 database (demo data) by the release binary: pre-migration snapshot, migration 7 → 8, data intact,
  `deed admin token` works.
- **v0.1.0** (2026-10-26, `3f45d31`): release run 40315870370 succeeded in every job. Image
  `ghcr.io/aurimasniekis/go-deed:0.1.0` = `docker.io/aurimasniekis/deed:0.1.0`
  (`sha256:df73365dd54d3483bd9eb7063d6bf757fa4f46433465fb18dbd507786bc740c9`), both pullable anonymously. Checked
  before publishing the draft: `SHA256SUMS` matches the linux/amd64 archive, and `gh attestation verify` accepts the
  SLSA provenance of that archive and of the image.

## Verify what was published
```sh
sha256sum --check SHA256SUMS
gh attestation verify deed_X.Y.Z_linux_amd64.tar.gz --repo aurimasniekis/go-deed
gh attestation verify oci://ghcr.io/aurimasniekis/go-deed:X.Y.Z --repo aurimasniekis/go-deed
```

## Terraform provider
The provider (`aurimasniekis/terraform-provider-deed`) is released separately with goreleaser and GPG signing; see
its `docs/RELEASING.md`. Registry publication needs the signing key and namespace setup described there.
Dry run (2026-10-25, provider `b2645ab`): `goreleaser release --snapshot --clean --skip=publish` with a throwaway
GPG key in a temporary keyring built the nine platform archives, the `SHA256SUMS` file listing them and the
`_manifest.json` (protocol 6.0), and a detached signature that `gpg --verify` accepts; Terraform installed the
darwin/arm64 archive from a filesystem mirror and loaded all six resources and six data sources. Provider v0.1.0 was
published on 2026-10-26 and is on the Registry (see the provider's `docs/RELEASING.md`).
