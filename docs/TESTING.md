# Testing

Deed's tests are plain `go test` packages that run against real SQLite files, plus a handful of opt-in suites that
need external processes (NATS, MinIO, Docker, Terraform). `make check` runs everything CI checks except the browser
and container jobs. `docs/VALIDATION.md` records what was actually run against real services and what was simulated.

## Quick reference
| Command | What it runs |
|---|---|
| `make test` | `go test ./...` |
| `make race` | `go test -race -count=1 ./...` |
| `make fuzz-smoke` | the two fuzz targets for `FUZZTIME` each (default `10s`) |
| `make lint` | `golangci-lint run --timeout 10m ./...` (CI pins v2.15.0) |
| `make openapi-check` | fails when `api/openapi.json` differs from `deed openapi` (fix with `make openapi`) |
| `make monitoring-check` | `deploy/monitoring/check.sh`: promtool rule check and unit tests, dashboard JSON check |
| `make txscope` | `go vet` with `tools/analyzers/txscope`: no network I/O, NATS/S3/outbound-HTTP calls, waits or fsyncs inside store write transactions (`make tools-test` tests the analyzer) |
| `make check` | `fmt-check vet txscope tools-test lint race openapi-check monitoring-check web-check web-build` (the web build runs `scripts/check-dist.mjs`) |
| `make e2e` | `scripts/e2e.sh`, the spec §18 end-to-end scenario |
| `make container-smoke` | builds `deed:local` and runs `scripts/container-smoke.sh` against it |
| `make secrets` | gitleaks over the git history with `.gitleaks.toml` |
| `make vuln` | `govulncheck@v1.9.0 ./...` |

`web-check` runs the web typecheck, lint and Vitest suites; `make web-e2e` runs Playwright + axe against a fresh,
seeded `deed serve` (build the UI first with `make web-build`). See `docs/UI.md` for the browser suites.

## Unit and integration tests
Most packages test through real dependencies rather than mocks:
- **Stores.** `internal/store/storetest.Open` creates a fresh data directory under `t.TempDir()` and closes the store
  at cleanup. `internal/testenv.New` builds an in-process core on top of it (store, keyring with a generated KEK,
  audit writer, catalog with built-in kinds seeded, fake clock at 2026-10-01 12:00 UTC).
- **HTTP API.** `internal/httpapi` tests run the full API in-process (`harness_test.go`): declaration lifecycle,
  authorization boundaries, login throttling and MFA, the push-source protocol, Terraform state push, telemetry
  authorization, `pkg/deedclient` against the real API, and provider regressions (deleting kinds and relationship
  types after their users are destroyed, cross-project assertions).
- **Domain packages.** Catalog merge and reconciliation (order independence, precedence, stale revisions never
  resurrect, identity keys, schema compatibility and limits), ingest snapshots (complete vs partial, mass-removal
  guard, interrupted snapshots, batch replay, dry-run, out-of-order updates), graph (fan collapsing, impact over
  cycles, path search, overview), alerts, versions, telemetry adapters against API-shaped fixtures, connectors
  against contract fixtures (`internal/connectors/testdata`), `internal/httpx` security behaviour (client IP spoofing,
  security headers, same-origin, body limits, rate limiter) and live-update stream resume.
- **Secret leak checks.** Fixtures carry a `PLANTED-SECRET` marker; the Terraform import tests
  (`internal/tfimport`, `internal/httpapi/tfstate_test.go`) and telemetry tests fail if it reaches extracted output,
  the database, or an error message.

There are currently no `_test.go` files in `internal/oidc`, `internal/store`, `internal/audit`, `internal/keyring`,
`internal/findings` or `internal/sources`; those packages are exercised only indirectly through the packages above.

### Transaction guard
ADR-001 requires write transactions to stay short. Every store opened through `storetest.Open` installs a
transaction observer: a successful write transaction slower than the limit fails the test with
`write transaction "<name>" took <d> (> <limit>, ADR-001)`.

| Setting | Limit |
|---|---|
| default | 100 ms |
| built with `-race` | 500 ms (the race detector slows SQLite-heavy code; the guard scales by 5) |
| `DEED_TEST_TX_MAX=<duration>` | overrides both (CI uses `2s`: shared runners stall on fsync under the race detector, and the static txscope analyzer catches I/O inside transactions; the performance harness needs `10m`) |

Exempt transactions: unnamed ones (test fixtures and offline CLI commands; serving code always names its
transactions), the start-up transactions `keyring.load`, `catalog.seed_builtins` and `installation.ensure`, the atomic
snapshot commit `ingest.snapshot_commit` (bounded by snapshot size instead, see `docs/PERFORMANCE.md`), and the
offline demo seed (`demo.entities`, `demo.relations`).

### External services
| Variable | Used by | Without it |
|---|---|---|
| `DEED_TEST_NATS=nats://host:port` | `internal/pulseint` (tail rollups, observed edges, ingest consumer authorization) | starts a local `nats-server -js` from `PATH`, or skips |
| `DEED_TEST_S3=http://user:password@host:port` | `internal/backup` `TestMinIO` | starts a local `minio` binary from `PATH` (and stops it to simulate an outage), or skips |

These variables (and `DEED_TEST_TX_MAX`) are on `internal/config`'s list of non-configuration variables, so a
server started from the same shell does not refuse them as `unknown environment override(s)`.

```sh
docker run -d --name nats -p 4222:4222 nats:2.15.1-alpine -js
DEED_TEST_NATS=nats://127.0.0.1:4222 go test -race -count=1 ./internal/pulseint/
```

## Fuzzing
| Target | Package | Property |
|---|---|---|
| `FuzzSanitize` | `internal/icons` | sanitized SVG is accepted again unchanged (idempotent) and never contains `<script`, `javascript:`, `foreignObject`, event handlers or `<style` |
| `FuzzParse` | `internal/tfimport` | parsing arbitrary input with a 1 MiB cap and extracting it with the built-in mappings never panics; seeded with `testdata/state-v4.json` and `testdata/show.json` |

```sh
make fuzz-smoke FUZZTIME=1m
go test -run '^$' -fuzz FuzzParse -fuzztime 5m ./internal/tfimport/
```

CI runs 10 s per target on pushes and pull requests and 5 minutes per target in the nightly scheduled run.

## Lint and static checks
`.golangci.yml` (golangci-lint v2) enables the standard set plus `bodyclose`, `contextcheck`, `copyloopvar`,
`depguard`, `durationcheck`, `errorlint`, `forbidigo`, `gosec`, `misspell`, `nilerr`, `noctx`, `revive`,
`rowserrcheck`, `unconvert` and `usestdlibvars`, with `gofmt` and `goimports` as formatters. Project rules:
- `math/rand` and `math/rand/v2` are denied outside tests (use `crypto/rand`);
- the standard `log` package is denied outside `cmd/` and tests (use `log/slog` through `internal/obs`);
- `fmt.Print*`, `print` and `println` are forbidden outside `cmd/`, `test/`, `examples/` and `scripts/`.

## Monitoring rules
`deploy/monitoring/check.sh` runs `promtool check rules deed-alerts.yaml` and `promtool test rules
deed-alerts.test.yaml` (a local `promtool`, or the pinned `prom/prometheus:v3.5.0` image), then checks that
`deed-dashboard.json` parses and has the `deed-overview` UID. The rule tests cover `DeedDown`, `DeedNotReady`,
`DeedSourceStale`, `DeedOutboxBacklog` and `DeedBackupMissing`.

## End-to-end scenario
`scripts/e2e.sh` (`make e2e`) runs the spec §18 scenario against real processes. Requirements: Go, Python 3,
Terraform, Docker with compose, and a checkout of terraform-provider-deed (`PROVIDER_DIR`, default
`../terraform-provider-deed`). It:
1. builds `deed`, the `pulsepub` helper (`scripts/e2e/pulsepub`) and the provider;
2. starts NATS, MinIO, Prometheus, Loki and Grafana from `deploy/compose.dev.yaml`;
3. writes a development-mode config (plain http on loopback), runs `deed migrate` and `deed admin bootstrap`, and
   starts `deed serve`;
4. runs `scripts/e2e/driver.py` (standard library only), which prints PASS/FAIL per step with evidence and finishes
   with a scan of every database, WAL, log and JSON artefact for the planted secret.

Results go to `$E2E_DIR/results.json` (default a fresh `mktemp` directory). Ports can be changed with
`DEED_E2E_PORT`, `DEED_E2E_MGMT`, `DEED_E2E_RESTORE_PORT` and `DEED_E2E_RESTORE_MGMT`. Fixtures are synthetic; the
Pulse traffic is Pulse-compatible but runs against plain NATS JetStream, not a pulsed deployment.

## Performance harness
`internal/perf` is opt-in and builds a 10,000-entity / 50,000-relation catalog in a real SQLite file:

```sh
DEED_PERF=1 DEED_TEST_TX_MAX=10m go test -run TestPerformance -v -timeout 30m ./internal/perf/
```

`DEED_PERF_ENTITIES` / `DEED_PERF_RELATIONS` change the size and `DEED_PERF_OUT=perf.json` writes the raw results.
`docs/PERFORMANCE.md` has a recorded run and the budgets it is compared against. `make bench` runs `go test -bench`
over `internal/graph` and `internal/catalog`, but neither package currently defines a `Benchmark` function.

## Container smoke test
`scripts/container-smoke.sh IMAGE` checks a built image without pushing it: user `65532:65532`, only `8080/tcp`
exposed, only `/var/lib/deed` as a volume, a `HEALTHCHECK`, no shell; `deed version --json`; `deed keys init` into a
separate volume (refuses to overwrite); `deed config check` and `deed migrate` with the production config on a
read-only root filesystem with all capabilities dropped; refusal of a missing `public_url`, a KEK inside `data_dir`
and a KEK readable by group or others; and finally `deed serve` passing `deed healthcheck`, the Docker health check,
`GET /api/v1/system/info`, and exiting 0 on SIGTERM.

## CI
`.github/workflows/ci.yaml` runs on pushes to `main`, pull requests and nightly:

| Job | Steps |
|---|---|
| `detect` | sets `web` / `e2e` outputs from the presence of `web/package.json` and `web/playwright.config.ts` |
| `go` | golangci-lint v2.15.0; `make fmt-check vet txscope tools-test openapi-check monitoring-check`; NATS 2.15.1 (JetStream) and MinIO containers; `make race` with `DEED_TEST_TX_MAX=2s`, `DEED_TEST_NATS`, `DEED_TEST_S3`; `make fuzz-smoke` |
| `web` | only with `web/package.json`: install, `web-check`, `web-build`, and a check that the generated API client is current |
| `e2e` | only with `web/playwright.config.ts`: Playwright (Chromium) with axe |
| `container` | only with `web/package.json`: build the image, `scripts/container-smoke.sh deed:ci` |
| `security` | govulncheck v1.9.0 and gitleaks v8.30.1 over the full history |

The release workflow (`.github/workflows/release.yaml`) gates on `make vet race`, smoke-tests the static
linux/amd64 binary (`deed version --json`, not dynamically linked), scans images with Trivy and runs the container
smoke test on amd64; see `docs/RELEASING.md`. The §18 scenario and the performance harness are not part of CI.
