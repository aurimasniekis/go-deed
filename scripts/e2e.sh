#!/usr/bin/env bash
# End-to-end acceptance scenario (spec §18) against real processes: a freshly built deed with SQLite, the
# terraform-provider-deed binary built from its repository, the docker-compose dev stack (NATS JetStream, MinIO,
# Prometheus, Loki, Grafana) and the deed CLI for import, backup and restore. Results: $E2E_DIR/results.json and the
# console log; docs/VALIDATION.md records a run.
#
# Requirements: go, python3, terraform, docker (compose). The provider repository defaults to
# ../terraform-provider-deed (PROVIDER_DIR). Nothing here touches real infrastructure: every name is example.com and
# every fixture is synthetic.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
E2E_DIR=${E2E_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/deed-e2e.XXXXXX")}
PROVIDER_DIR=${PROVIDER_DIR:-$root/../terraform-provider-deed}
# Not DEED_* names: deed refuses unknown DEED_* variables in its environment.
PORT=${E2E_PORT:-57871}
MGMT=${E2E_MGMT:-57872}
RPORT=${E2E_RESTORE_PORT:-57873}
RMGMT=${E2E_RESTORE_MGMT:-57874}
compose=(docker compose -f deploy/compose.dev.yaml)
mkdir -p "$E2E_DIR"
chmod 700 "$E2E_DIR"
echo "e2e work dir: $E2E_DIR"

pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT

echo "== build deed, deed-agent, pulsepub and the provider"
mkdir -p "$E2E_DIR/bin"
go build -o "$E2E_DIR/bin/deed" ./cmd/deed
go build -o "$E2E_DIR/bin/pulsepub" ./scripts/e2e/pulsepub
(cd "$PROVIDER_DIR" && go build -o "$E2E_DIR/bin/terraform-provider-deed" .)
echo "provider commit: $(git -C "$PROVIDER_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)"

echo "== dev stack"
"${compose[@]}" up -d nats minio minio-init prometheus loki grafana >/dev/null
for _ in $(seq 1 60); do
  curl -fsS 127.0.0.1:39190/-/ready >/dev/null 2>&1 && curl -fsS 127.0.0.1:33000/api/health >/dev/null 2>&1 &&
    curl -fsS 127.0.0.1:33100/ready >/dev/null 2>&1 && break
  sleep 2
done
"$E2E_DIR/bin/pulsepub" stream -url nats://127.0.0.1:34222

echo "== configuration (development mode: plain http on loopback)"
"$E2E_DIR/bin/deed" backup keygen -out "$E2E_DIR/age-identity.txt" >"$E2E_DIR/age-recipient.txt" 2>/dev/null
printf 'deed-dev-secret' >"$E2E_DIR/s3-secret" && chmod 600 "$E2E_DIR/s3-secret"
write_config() { # data_dir port mgmt kek
  cat <<EOF
mode: development
data_dir: $1
public_url: http://127.0.0.1:$2
kek: {source: file, file: $4}
http: {listen: 127.0.0.1:$2}
management: {listen: 127.0.0.1:$3}
log: {level: info, format: json}
outbound: {allow_cidrs: [127.0.0.1/32]}
pulse: {enabled: true, url: "nats://127.0.0.1:34222", source: deed}
backup:
  enabled: false
  recipients: ["$(cat "$E2E_DIR/age-recipient.txt")"]
  s3: {endpoint: "127.0.0.1:39000", bucket: deed-backups, prefix: "e2e-$(basename "$E2E_DIR")/", access_key_id: deed-dev,
       secret_access_key_file: $E2E_DIR/s3-secret, insecure: true, lookup: path, sse: false}
versions: {enabled: true}
EOF
}
mkdir -p "$E2E_DIR/kek"
chmod 700 "$E2E_DIR/kek"
"$E2E_DIR/bin/deed" keys init -out "$E2E_DIR/kek/primary" >/dev/null
write_config "$E2E_DIR/data" "$PORT" "$MGMT" "$E2E_DIR/kek/primary" >"$E2E_DIR/deed.yaml"
write_config "$E2E_DIR/restored" "$RPORT" "$RMGMT" "$E2E_DIR/kek/restored" >"$E2E_DIR/restore.yaml"
"$E2E_DIR/bin/deed" config check -config "$E2E_DIR/deed.yaml" >/dev/null

echo "== start deed"
"$E2E_DIR/bin/deed" migrate -config "$E2E_DIR/deed.yaml" >/dev/null
"$E2E_DIR/bin/deed" admin bootstrap -config "$E2E_DIR/deed.yaml" 2>/dev/null | sed -n 's/.*\/setup#[a-z_]*=\([^ ]*\).*/\1/p' | head -1 >"$E2E_DIR/setup-token"
[[ -s "$E2E_DIR/setup-token" ]] || { echo "bootstrap link not found"; exit 1; }
"$E2E_DIR/bin/deed" serve -config "$E2E_DIR/deed.yaml" >"$E2E_DIR/deed.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 60); do curl -fsS "127.0.0.1:$MGMT/readyz" >/dev/null 2>&1 && break; sleep 0.5; done
curl -fsS "127.0.0.1:$MGMT/readyz" >/dev/null || { echo "deed did not become ready:"; tail -5 "$E2E_DIR/deed.log"; exit 1; }

export E2E_DIR PORT MGMT RPORT RMGMT PROVIDER_DIR
python3 scripts/e2e/driver.py
