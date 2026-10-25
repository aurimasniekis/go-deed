# Try it locally

Requirements: Go 1.27, Docker (for the optional dev stack), and for the UI, Node 24 with pnpm.

```sh
git clone https://github.com/aurimasniekis/go-deed && cd go-deed
make build                                          # bin/deed, bin/deed-agent
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
