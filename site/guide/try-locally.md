# Try it locally

Requirements: Go 1.27, Node 24 with pnpm (the web UI is built first and embedded in the binary; `make build` alone
serves a "web UI is not included" page), and Docker for the optional dev stack. To run a release instead, see
[Deployment](/operators/deployment).

```sh
git clone https://github.com/aurimasniekis/go-deed && cd go-deed
make web build                                      # the UI, then bin/deed and bin/deed-agent with it embedded
bin/deed migrate -config deploy/dev/deed.yaml
bin/deed admin bootstrap -config deploy/dev/deed.yaml   # prints a single-use setup link
bin/deed serve -config deploy/dev/deed.yaml
```

Open the link, create the owner account and enroll TOTP. Then either seed the clearly labelled synthetic demo
project:

```sh
bin/deed demo seed -config deploy/dev/deed.yaml     # stop the server first: the CLI needs the data-directory lock
```

or point real sources at it: create a project, a Terraform-state push source and push a state file with
`deed import terraform`, or declare entities with the [Terraform provider](/guide/terraform).

## The full stack
`deploy/compose.dev.yaml` runs NATS (JetStream), MinIO, Prometheus (with Deed's alert rules), Loki and Grafana
(with the Deed dashboard) on loopback ports. `scripts/e2e.sh` uses it to run the complete acceptance scenario —
Terraform import and provider, Pulse traffic, telemetry, alerts, backup to MinIO and restore — and prints evidence for
every step.

```sh
docker compose -f deploy/compose.dev.yaml up -d
scripts/e2e.sh
```
