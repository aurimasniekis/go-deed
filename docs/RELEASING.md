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
