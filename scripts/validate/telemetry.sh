#!/usr/bin/env bash
# Validate Deed's telemetry adapters against real Grafana Mimir and OpenSearch instances (Docker, loopback only).
#
# - Mimir runs monolithic (-target=all) with filesystem storage and multitenancy enabled, so X-Scope-OrgID decides
#   which data a query sees. scripts/validate/telemetry.py pushes deterministic samples for two tenants through the
#   remote-write API (protobuf + snappy, encoded with the standard library).
# - OpenSearch runs single-node with the security plugin on: HTTPS with the image's demo certificates (Deed trusts
#   the demo root CA through outbound.ca_file) and basic auth. The driver creates a least-privilege user that may
#   only read logs-deed-* and metrics-deed-*, and indexes log-like documents with timestamps.
# - Deed runs from this repository in development mode. The driver creates prometheus and opensearch integrations,
#   bindings of every supported shape, a service entity, and compares /entities/{id}/telemetry with the same queries
#   sent directly to Mimir and OpenSearch, then checks failure states (wrong tenant, missing tenant, wrong
#   credentials, missing index permission, rejected query, unreachable, stale after an outage) for leaked upstream
#   bodies.
#
# Usage: scripts/validate/telemetry.sh
#   VALIDATE_DIR  work directory (default: a new 0700 temp dir); KEEP=1 keeps secrets, data and containers' state
#                 files on exit (containers are always removed)
#
# Requires docker, go, python3 and curl. Results: $VALIDATE_DIR/results.json.
set -euo pipefail

# Releases at least a few weeks old, pinned by digest (multi-arch index).
MIMIR_IMAGE=${MIMIR_IMAGE:-grafana/mimir:3.2.1@sha256:92838f113ba54230014e79bc812e57ca90bb9ebc06e665f59e4f700098c2dd04}
OPENSEARCH_IMAGE=${OPENSEARCH_IMAGE:-opensearchproject/opensearch:3.8.0@sha256:fafe3fc3587088674669235575aa166228c48bdb940294a8cdbbc1da75236a40}

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work=${VALIDATE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/deed-validate-telemetry.XXXXXX")}
mkdir -p "$work/secrets" "$work/bin"
chmod 700 "$work" "$work/secrets"
umask 077
echo "work dir: $work"

suffix="$$-$RANDOM"
mimir_ctr="deed-validate-mimir-$suffix"
os_ctr="deed-validate-opensearch-$suffix"
deed_pid=""
cleanup() {
  local rc=$?
  [[ -n $deed_pid ]] && { kill "$deed_pid" 2>/dev/null || true; wait "$deed_pid" 2>/dev/null || true; }
  docker logs "$mimir_ctr" >"$work/mimir.log" 2>&1 || true
  docker logs "$os_ctr" >"$work/opensearch.log" 2>&1 || true
  docker rm -f "$mimir_ctr" "$os_ctr" >/dev/null 2>&1 || true
  if [[ $rc -ne 0 ]]; then
    echo "validate-telemetry: FAIL (logs in $work)" >&2
    tail -n 20 "$work/deed.log" >&2 2>/dev/null || true
  fi
  if [[ ${KEEP:-} != 1 ]]; then
    rm -rf "${work:?}/secrets" "${work:?}/deed-data" "${work:?}/bin"
    echo "containers, secrets, data and binaries removed; logs and results.json stay in $work (KEEP=1 keeps everything)"
  fi
}
trap cleanup EXIT
trap "exit 130" INT TERM

free_port() { python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'; }
host_port() { docker port "$1" "$2" | sed -n 's/^127\.0\.0\.1:\([0-9]*\)$/\1/p' | head -1; }
rand_secret() { python3 -I -c 'import secrets; print("Dv-" + secrets.token_urlsafe(18) + "-9a")'; }

echo "== pull $MIMIR_IMAGE and $OPENSEARCH_IMAGE"
docker pull -q "$MIMIR_IMAGE" >/dev/null
docker pull -q "$OPENSEARCH_IMAGE" >/dev/null

echo "== Grafana Mimir (monolithic, filesystem storage, multitenancy on)"
cat >"$work/mimir.yaml" <<'YAML'
multitenancy_enabled: true
target: all
server: {http_listen_port: 9009, grpc_listen_port: 9095, log_level: warn}
common:
  storage:
    backend: filesystem
    filesystem: {dir: /data/common}
blocks_storage:
  storage_prefix: blocks
  tsdb: {dir: /data/tsdb}
  bucket_store: {sync_dir: /data/tsdb-sync}
ingester:
  ring: {replication_factor: 1, kvstore: {store: inmemory}, instance_addr: 127.0.0.1}
distributor:
  ring: {kvstore: {store: inmemory}, instance_addr: 127.0.0.1}
store_gateway:
  sharding_ring: {replication_factor: 1, kvstore: {store: inmemory}, instance_addr: 127.0.0.1}
compactor:
  data_dir: /data/compactor
  sharding_ring: {kvstore: {store: inmemory}, instance_addr: 127.0.0.1}
ruler_storage: {storage_prefix: rules}
alertmanager_storage: {storage_prefix: alertmanager}
# The classic (ingester) write path; Mimir 3 defaults to the Kafka-based ingest storage.
ingest_storage: {enabled: false}
limits:
  out_of_order_time_window: 3h
YAML
# Copied in rather than bind-mounted: the work dir is 0700.
docker create --name "$mimir_ctr" -p 127.0.0.1::9009 --tmpfs /data "$MIMIR_IMAGE" -config.file=/etc/mimir-validate.yaml >/dev/null
chmod 644 "$work/mimir.yaml"
docker cp "$work/mimir.yaml" "$mimir_ctr:/etc/mimir-validate.yaml"
docker start "$mimir_ctr" >/dev/null

echo "== OpenSearch (single node, security plugin with demo TLS certificates)"
rand_secret >"$work/secrets/opensearch-admin"
rand_secret >"$work/secrets/opensearch-reader"
rand_secret >"$work/secrets/opensearch-reader-rotated"
docker run -d --name "$os_ctr" -p 127.0.0.1::9200 -e discovery.type=single-node -e "OPENSEARCH_JAVA_OPTS=-Xms512m -Xmx512m" \
  -e "OPENSEARCH_INITIAL_ADMIN_PASSWORD=$(cat "$work/secrets/opensearch-admin")" "$OPENSEARCH_IMAGE" >/dev/null

MIMIR_PORT=$(host_port "$mimir_ctr" 9009)
OS_PORT=$(host_port "$os_ctr" 9200)
for _ in $(seq 1 150); do [[ $(curl -s "127.0.0.1:$MIMIR_PORT/ready" 2>/dev/null) == ready ]] && break; sleep 1; done
[[ $(curl -s "127.0.0.1:$MIMIR_PORT/ready") == ready ]] || { echo "mimir did not become ready" >&2; exit 1; }
for _ in $(seq 1 180); do docker cp "$os_ctr:/usr/share/opensearch/config/root-ca.pem" "$work/secrets/opensearch-root-ca.pem" >/dev/null 2>&1 && break; sleep 1; done
for _ in $(seq 1 180); do
  curl -fsS --cacert "$work/secrets/opensearch-root-ca.pem" -u "admin:$(cat "$work/secrets/opensearch-admin")" \
    "https://127.0.0.1:$OS_PORT/_cluster/health?wait_for_status=yellow&timeout=1s" >/dev/null 2>&1 && break
  sleep 1
done
curl -fsS --cacert "$work/secrets/opensearch-root-ca.pem" -u "admin:$(cat "$work/secrets/opensearch-admin")" "https://127.0.0.1:$OS_PORT/" >/dev/null ||
  { echo "opensearch did not become ready" >&2; exit 1; }
echo "mimir on 127.0.0.1:$MIMIR_PORT, opensearch on https://127.0.0.1:$OS_PORT"

echo "== build deed"
PORT_=$(free_port) MGMT_=$(free_port) DEAD_PORT=$(free_port)
(cd "$root" && go build -o "$work/bin/deed" ./cmd/deed)
"$work/bin/deed" keys init -out "$work/secrets/kek" >/dev/null
cat >"$work/deed.yaml" <<YAML
mode: development
data_dir: $work/deed-data
public_url: http://127.0.0.1:$PORT_
kek: {source: file, file: $work/secrets/kek}
http: {listen: 127.0.0.1:$PORT_}
management: {listen: 127.0.0.1:$MGMT_}
log: {level: info, format: json}
outbound: {ca_file: $work/secrets/opensearch-root-ca.pem}
limits: {telemetry_timeout: 5s}
YAML
"$work/bin/deed" migrate -config "$work/deed.yaml" >"$work/deed-migrate.log" 2>&1
"$work/bin/deed" admin bootstrap -config "$work/deed.yaml" 2>/dev/null |
  sed -n 's/.*\/setup#[a-z_]*=\([^ ]*\).*/\1/p' | head -1 >"$work/secrets/deed-setup-token"
[[ -s $work/secrets/deed-setup-token ]] || { echo "deed bootstrap link not found" >&2; exit 1; }
"$work/bin/deed" serve -config "$work/deed.yaml" >"$work/deed.log" 2>&1 &
deed_pid=$!
for _ in $(seq 1 150); do curl -fsS "127.0.0.1:$MGMT_/readyz" >/dev/null 2>&1 && break; sleep 0.2; done
curl -fsS "127.0.0.1:$MGMT_/readyz" >/dev/null || { echo "deed did not become ready" >&2; exit 1; }

# Not DEED_* names: deed refuses unknown DEED_* variables in its environment.
export VALIDATE_DIR=$work DEED_URL_="http://127.0.0.1:$PORT_" DEED_MGMT_="http://127.0.0.1:$MGMT_" MIMIR_PORT OS_PORT DEAD_PORT
export MIMIR_CTR=$mimir_ctr MIMIR_IMAGE OPENSEARCH_IMAGE
python3 -I "$root/scripts/validate/telemetry.py"
