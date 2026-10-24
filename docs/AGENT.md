# deed-agent and the Go SDK

Anything that can speak HTTP can feed Deed through a **push source** and its source token (`deed_st_…`), which can
write only into that one source. Deed never runs third-party code (ADR-013): external collectors are separate
programs.

## Go SDK — `pkg/deedclient`
```go
c, err := deedclient.New("https://deed.example.com", os.Getenv("DEED_SOURCE_TOKEN"), "src_…")
res, err := c.Sync(ctx, deedclient.Batch{
	Entities: []deedclient.Entity{{Kind: "server", Name: "nas-1", ExternalID: "nas-1", Spec: map[string]any{"ipv4": "192.0.2.20"}}},
	Relations: []deedclient.Relation{{Type: "located_in", From: deedclient.Ref{ExternalID: "nas-1"}, To: deedclient.Ref{Kind: "location", Name: "home-rack"}}},
}, deedclient.CommitOptions{})
```
- `Sync` sends a **complete snapshot** (begin → batches → commit) and aborts it on any failure, so an interrupted
  upload never removes data. Facts this source no longer reports are tombstoned according to the source policy, with
  the mass-removal guard (`AllowMassRemoval` to override deliberately). `DryRun` previews the change.
- `Items` writes incrementally (`atomic` rejects the whole batch on any error); `Observations` writes time-bounded
  status; `Heartbeat` keeps the source fresh without data.
- Refs: `ExternalID` (this source's own IDs), `ID` (Deed IDs) or `Kind`+`Name`. References to entities that do not
  exist yet are kept as unresolved and resolve later.
- Transient failures (connection errors, 429, 502–504) are retried with jittered backoff and `Retry-After`; one
  `Idempotency-Key` is reused across the retries of a request; redirects are never followed.

## deed-agent (Docker)
Collects one Docker Engine: the `docker_host`, its `container`s (image, tag, digest, state, published ports,
Compose project/service, health from the container status) and `network`s, with `runs_on`, `member_of` and
`hosted_on` relationships, and pushes them as a snapshot every `-interval` (default 1 minute).

```sh
# Create a push source and a token (project admin), then:
export DEED_ENDPOINT=https://deed.example.com DEED_SOURCE_ID=src_… DEED_SOURCE_TOKEN=deed_st_…
deed-agent -dry-run          # print what would be sent
deed-agent -preview          # send as a dry-run commit and print the summary
deed-agent                   # run
```

It needs read access to the Docker socket only. Environment variables, commands and container labels are never
sent, except the allowlisted labels `com.docker.compose.project`, `com.docker.compose.service`,
`org.opencontainers.image.version` (plus keys added with `-labels`; make sure their values are not secrets). The
image `org.opencontainers.image.source` label becomes a repository link when it is an https URL.

Read access to the Docker socket is root-equivalent on most hosts; prefer a read-only socket proxy that allows only
`GET /info`, `GET /containers/json` and `GET /networks` (e.g. `tecnativa/docker-socket-proxy` with `CONTAINERS=1
NETWORKS=1 INFO=1`) and point `-docker` at it.

Validation: the collector is tested against Docker API fixtures and was dry-run against Docker Desktop 29 during
development; the SDK is tested against the real Deed API (`internal/httpapi/deedclient_test.go`).
