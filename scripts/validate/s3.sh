#!/usr/bin/env bash
# Validates Deed's S3 use (encrypted backups and the Terraform state reader) against S3 implementations other than
# MinIO, each run locally in Docker: SeaweedFS (with and without an SSE-S3 key), Garage (its own region, "garage") and
# Versity S3 Gateway (POSIX backend). For each one, scripts/validate/s3_driver.py runs Deed's S3 integration tests
# (TestMinIO, TestS3Compat, TestS3Backend via DEED_TEST_S3) and an end-to-end backup → list → verify → restore with the
# deed CLI, including GFS pruning, an interrupted multipart upload, ListObjectsV2 paging and SSE-S3 handling.
#
# Usage: scripts/validate/s3.sh                       (every implementation)
#        scripts/validate/s3.sh garage versitygw      (a subset: seaweedfs seaweedfs-nosse garage versitygw)
#        KEEP=1 scripts/validate/s3.sh                (keep the work directory and containers' logs)
#
# Requirements: go, python3, curl, docker. Containers are named deed-s3val-<impl>-<random>, publish one port on a free
# 127.0.0.1 port and are removed on exit; nothing else (the dev compose stack's MinIO included) is touched. Every
# credential is generated at run time into a 0700 work directory. Results: PASS/FAIL lines and $VAL_DIR/results.json.
set -euo pipefail

# Images: releases at least a few weeks old, pinned.
SEAWEEDFS_IMAGE=${SEAWEEDFS_IMAGE:-chrislusf/seaweedfs:4.47}
GARAGE_IMAGE=${GARAGE_IMAGE:-dxflrs/garage:v2.4.1}
VERSITYGW_IMAGE=${VERSITYGW_IMAGE:-versity/versitygw:v1.8.0}

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
impls=("$@")
[[ ${#impls[@]} -gt 0 ]] || impls=(seaweedfs seaweedfs-nosse garage versitygw)
VAL_DIR=${VAL_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/deed-validate-s3.XXXXXX")}
mkdir -p "$VAL_DIR/bin" "$VAL_DIR/secrets" "$VAL_DIR/conf"
chmod 700 "$VAL_DIR" "$VAL_DIR/secrets" "$VAL_DIR/conf"
umask 077
run_id=$(head -c 4 /dev/urandom | od -An -tx1 | tr -d ' \n')
echo "work dir: $VAL_DIR (containers deed-s3val-*-$run_id)"

containers=()
cleanup() {
  for c in "${containers[@]}"; do
    if [[ "${KEEP:-0}" == 1 ]]; then docker logs "$c" >"$VAL_DIR/$c.log" 2>&1 || true; fi
    docker rm -f "$c" >/dev/null 2>&1 || true
  done
  if [[ "${KEEP:-0}" != 1 ]]; then
    rm -rf "${VAL_DIR:?}/secrets" "${VAL_DIR:?}/conf" "${VAL_DIR:?}/bin"
    for i in "${impls[@]}"; do rm -rf "${VAL_DIR:?}/$i/data" "${VAL_DIR:?}/$i/restored" "${VAL_DIR:?}/$i"/kek*; done
    echo "secrets, data and binaries removed; logs and results.json stay in $VAL_DIR (KEEP=1 keeps everything)"
  fi
}
trap cleanup EXIT
trap "exit 130" INT TERM

free_port() { python3 -I -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
hex() { head -c "$1" /dev/urandom | od -An -tx1 | tr -d ' \n'; }
wait_http() { # port what container
  for _ in $(seq 1 120); do
    curl -s -o /dev/null "http://127.0.0.1:$1/" && return 0
    sleep 0.5
  done
  echo "$2 did not answer on 127.0.0.1:$1:"; docker logs --tail 20 "$3"; exit 1
}
digest() { docker image inspect --format '{{index .RepoDigests 0}}' "$1" 2>/dev/null || echo unknown; }

echo "== build deed"
go build -o "$VAL_DIR/bin/deed" ./cmd/deed
"$VAL_DIR/bin/deed" backup keygen -out "$VAL_DIR/secrets/age-identity.txt" >"$VAL_DIR/secrets/age-recipient.txt" 2>/dev/null
export VAL_DIR VAL_ROOT=$root VAL_COMMIT
VAL_COMMIT=$(git -C "$root" rev-parse HEAD 2>/dev/null || echo unknown)
echo "deed: $VAL_COMMIT"

# start_<impl> starts the container and exports S3_ENDPOINT, S3_IMAGE, S3_VERSION and the region and SSE settings.
start_seaweedfs() { # [kek]
  local name="deed-s3val-$IMPL-$run_id" port
  port=$(free_port)
  printf 'sw%s' "$(hex 8)" >"$VAL_DIR/secrets/$IMPL-access"
  hex 24 >"$VAL_DIR/secrets/$IMPL-secret"
  python3 -I -c 'import json,sys; print(json.dumps({"identities": [{"name": "deed", "credentials": [{"accessKey": sys.argv[1], "secretKey": sys.argv[2]}],
    "actions": ["Admin", "Read", "Write", "List", "Tagging"]}]}))' "$(cat "$VAL_DIR/secrets/$IMPL-access")" "$(cat "$VAL_DIR/secrets/$IMPL-secret")" \
    >"$VAL_DIR/conf/$IMPL-s3.json"
  : >"$VAL_DIR/conf/$IMPL.env"
  # With a key SeaweedFS applies SSE-S3; without one it answers InternalError to SSE-S3 requests.
  if [[ "${1:-}" == kek ]]; then printf 'WEED_S3_SSE_KEK=%s\n' "$(hex 32)" >"$VAL_DIR/conf/$IMPL.env"; fi
  docker create --name "$name" -p "127.0.0.1:$port:8333" --env-file "$VAL_DIR/conf/$IMPL.env" "$SEAWEEDFS_IMAGE" \
    server -s3 -s3.config=/etc/deed-s3.json -s3.domainName=s3.seaweedfs.localhost -dir=/data -ip=127.0.0.1 -ip.bind=0.0.0.0 \
    -master.volumeSizeLimitMB=64 >/dev/null
  containers+=("$name")
  # SeaweedFS drops to its own user: the copy must be readable inside the container (the conf directory stays 0700).
  chmod 644 "$VAL_DIR/conf/$IMPL-s3.json"
  docker cp "$VAL_DIR/conf/$IMPL-s3.json" "$name:/etc/deed-s3.json" >/dev/null
  docker start "$name" >/dev/null
  wait_http "$port" SeaweedFS "$name"
  export S3_ENDPOINT="127.0.0.1:$port" S3_IMAGE="$SEAWEEDFS_IMAGE" S3_DIGEST S3_VERSION S3_SIGN_REGION=us-east-1 S3_CONFIG_REGION="" S3_TEST_REGION="" \
    S3_VHOST=s3.seaweedfs.localhost
  S3_DIGEST=$(digest "$SEAWEEDFS_IMAGE")
  S3_VERSION=$(docker exec "$name" weed version 2>/dev/null | head -1)
  if [[ "${1:-}" == kek ]]; then S3_SSE=on; else S3_SSE=off; fi
  export S3_SSE
}

start_garage() {
  local name="deed-s3val-$IMPL-$run_id" port node
  port=$(free_port)
  printf 'GK%s' "$(hex 12)" >"$VAL_DIR/secrets/$IMPL-access"
  hex 32 >"$VAL_DIR/secrets/$IMPL-secret"
  cat >"$VAL_DIR/conf/garage.toml" <<EOF
metadata_dir = "/var/lib/garage/meta"
data_dir = "/var/lib/garage/data"
db_engine = "sqlite"
replication_factor = 1
rpc_bind_addr = "127.0.0.1:3901"
rpc_public_addr = "127.0.0.1:3901"
rpc_secret = "$(hex 32)"

[s3_api]
s3_region = "garage"
api_bind_addr = "0.0.0.0:3900"
root_domain = ".s3.garage.localhost"

[admin]
api_bind_addr = "127.0.0.1:3903"
admin_token = "$(hex 16)"
EOF
  docker create --name "$name" -p "127.0.0.1:$port:3900" "$GARAGE_IMAGE" >/dev/null
  containers+=("$name")
  docker cp "$VAL_DIR/conf/garage.toml" "$name:/etc/garage.toml" >/dev/null
  docker start "$name" >/dev/null
  for _ in $(seq 1 60); do docker exec "$name" /garage status >/dev/null 2>&1 && break; sleep 0.5; done
  node=$(docker exec "$name" /garage node id -q 2>/dev/null | cut -d@ -f1)
  docker exec "$name" /garage layout assign -z dc1 -c 1G "$node" >/dev/null 2>&1
  docker exec "$name" /garage layout apply --version 1 >/dev/null 2>&1
  docker exec "$name" /garage key import --yes -n deed "$(cat "$VAL_DIR/secrets/$IMPL-access")" "$(cat "$VAL_DIR/secrets/$IMPL-secret")" >/dev/null 2>&1
  docker exec "$name" /garage key allow --create-bucket "$(cat "$VAL_DIR/secrets/$IMPL-access")" >/dev/null 2>&1
  wait_http "$port" Garage "$name"
  # Garage signs with its own region: the driver uses it; Deed and the tests discover it (region left empty).
  export S3_ENDPOINT="127.0.0.1:$port" S3_IMAGE="$GARAGE_IMAGE" S3_DIGEST S3_VERSION S3_SIGN_REGION=garage S3_CONFIG_REGION="" S3_TEST_REGION="" \
    S3_SSE=ignored S3_VHOST=s3.garage.localhost
  S3_DIGEST=$(digest "$GARAGE_IMAGE")
  S3_VERSION=$(docker exec "$name" /garage --version 2>/dev/null | head -1)
}

start_versitygw() {
  local name="deed-s3val-$IMPL-$run_id" port
  port=$(free_port)
  printf 'vgw%s' "$(hex 8)" >"$VAL_DIR/secrets/$IMPL-access"
  hex 24 >"$VAL_DIR/secrets/$IMPL-secret"
  printf 'ROOT_ACCESS_KEY_ID=%s\nROOT_SECRET_ACCESS_KEY=%s\n' "$(cat "$VAL_DIR/secrets/$IMPL-access")" "$(cat "$VAL_DIR/secrets/$IMPL-secret")" \
    >"$VAL_DIR/conf/$IMPL.env"
  docker run -d --name "$name" -p "127.0.0.1:$port:7070" --tmpfs /data:rw,mode=1777 --env-file "$VAL_DIR/conf/$IMPL.env" "$VERSITYGW_IMAGE" \
    --virtual-domain s3.versitygw.localhost posix /data >/dev/null
  containers+=("$name")
  wait_http "$port" "Versity S3 Gateway" "$name"
  # Deed is configured with us-east-1 here (the gateway's --region default), the tests discover it.
  export S3_ENDPOINT="127.0.0.1:$port" S3_IMAGE="$VERSITYGW_IMAGE" S3_DIGEST S3_VERSION S3_SIGN_REGION=us-east-1 S3_CONFIG_REGION=us-east-1 S3_TEST_REGION="" \
    S3_SSE=ignored S3_VHOST=s3.versitygw.localhost
  S3_DIGEST=$(digest "$VERSITYGW_IMAGE")
  S3_VERSION=$(docker exec "$name" versitygw --version 2>/dev/null | tr '\n' ' ')
}

for IMPL in "${impls[@]}"; do
  export IMPL S3_IMPL=$IMPL
  echo "== $IMPL"
  case "$IMPL" in
  seaweedfs) docker pull -q "$SEAWEEDFS_IMAGE" >/dev/null && start_seaweedfs kek ;;
  seaweedfs-nosse) docker pull -q "$SEAWEEDFS_IMAGE" >/dev/null && start_seaweedfs ;;
  garage) docker pull -q "$GARAGE_IMAGE" >/dev/null && start_garage ;;
  versitygw) docker pull -q "$VERSITYGW_IMAGE" >/dev/null && start_versitygw ;;
  *) echo "unknown implementation $IMPL (seaweedfs seaweedfs-nosse garage versitygw)"; exit 2 ;;
  esac
  echo "   $S3_IMAGE ($S3_VERSION) on $S3_ENDPOINT, SSE-S3 expected: $S3_SSE"
  python3 -I scripts/validate/s3_driver.py run
done
python3 -I scripts/validate/s3_driver.py summary
