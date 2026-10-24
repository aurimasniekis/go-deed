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

## Verify what was published
```sh
sha256sum --check SHA256SUMS
gh attestation verify deed_X.Y.Z_linux_amd64.tar.gz --repo aurimasniekis/go-deed
gh attestation verify oci://ghcr.io/aurimasniekis/go-deed:X.Y.Z --repo aurimasniekis/go-deed
```

## Terraform provider
The provider (`aurimasniekis/terraform-provider-deed`) is released separately with goreleaser and GPG signing; see
its `docs/RELEASING.md`. Registry publication needs the signing key and namespace setup described there.
