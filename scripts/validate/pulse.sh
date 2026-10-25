#!/usr/bin/env bash
# Validates Deed's Pulse integration against a real pulsed built from source (no Docker): pulsed init + nats-server
# (operator/JWT mode, TLS with Pulse's internal CA) + pulsed serve on random loopback ports, Pulse clients with grants
# created through the admin API, a viewer PAT for the pulse_admin source, and a fresh deed connecting with its own
# .creds. scripts/validate/pulse_driver.py runs the checks and writes $VAL_DIR/results.json.
#
# Requirements: go, python3, openssl, curl and nats-server (PATH, or NATS_SERVER=/path). PULSE_DIR is the go-pulse
# checkout (default ../pulse); it is only read and built into $VAL_DIR. Every name is example.com; every secret (master
# key, PATs, .creds, KEK) is generated at run time in a 0700 work directory and removed on exit unless KEEP=1.
set -euo pipefail
[[ -n "${TRACE:-}" ]] && set -x

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
PULSE_DIR=$(cd "${PULSE_DIR:-$root/../pulse}" && pwd)
NATS_SERVER=${NATS_SERVER:-$(command -v nats-server || true)}
[[ -x "$NATS_SERVER" ]] || { echo "nats-server not found (PATH or NATS_SERVER)"; exit 2; }
VAL_DIR=${VAL_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/deed-pulse-validate.XXXXXX")}
mkdir -p "$VAL_DIR/bin" "$VAL_DIR/secrets"
chmod 700 "$VAL_DIR" "$VAL_DIR/secrets"
umask 077
echo "work dir: $VAL_DIR"

pids=()
cleanup() {
  # Reverse start order: deed shuts down against a live pulsed, then pulsed, then nats-server.
  for ((i = ${#pids[@]} - 1; i >= 0; i--)); do
    kill "${pids[$i]}" 2>/dev/null || true
    wait "${pids[$i]}" 2>/dev/null || true
  done
  if [[ "${KEEP:-0}" != 1 ]]; then
    rm -rf "$VAL_DIR/secrets" "$VAL_DIR/pulse" "$VAL_DIR/deed-data" "$VAL_DIR/bin"
    echo "secrets, data and binaries removed; logs and results.json stay in $VAL_DIR (KEEP=1 keeps everything)"
  fi
}
trap cleanup EXIT
trap "exit 130" INT TERM

echo "== build pulsed (from $PULSE_DIR), deed and pulsepub"
PULSE_COMMIT=$(git -C "$PULSE_DIR" rev-parse HEAD)
PULSE_DIRTY=$(git -C "$PULSE_DIR" status --porcelain --untracked-files=no | wc -l | tr -d ' ')
(cd "$PULSE_DIR" && CGO_ENABLED=0 go build -trimpath -ldflags "-X github.com/aurimasniekis/go-pulse/internal/app.Version=validate-${PULSE_COMMIT:0:12}" \
  -o "$VAL_DIR/bin/pulsed" ./cmd/pulsed)
go build -o "$VAL_DIR/bin/deed" ./cmd/deed
go build -o "$VAL_DIR/bin/pulsepub" ./scripts/e2e/pulsepub
echo "pulse commit: $PULSE_COMMIT (modified tracked files: $PULSE_DIRTY); deed commit: $(git rev-parse HEAD); $("$NATS_SERVER" --version)"

# Eight free loopback ports: NATS client, monitor, websocket; pulsed admin, metrics; deed http, management.
read -r NATS_PORT MON_PORT WS_PORT ADMIN_PORT METRICS_PORT PORT MGMT _ < <(python3 -I -c '
import socket
ss = [socket.socket() for _ in range(8)]
for s in ss: s.bind(("127.0.0.1", 0))
print(" ".join(str(s.getsockname()[1]) for s in ss))')

echo "== pulsed init (operator mode, internal CA) on 127.0.0.1:$NATS_PORT"
openssl rand -hex 32 >"$VAL_DIR/secrets/pulse-master.key"
mkdir -p "$VAL_DIR/pulse/data" "$VAL_DIR/pulse/nats"
export PULSE_PROFILE=dev PULSE_LOG_LEVEL=info PULSE_MASTER_KEY_FILE="$VAL_DIR/secrets/pulse-master.key"
export PULSE_DATA_DIR="$VAL_DIR/pulse/data" PULSE_NATS_DIR="$VAL_DIR/pulse/nats"
export PULSE_NATS_CLIENT_LISTEN=127.0.0.1:$NATS_PORT PULSE_NATS_MONITOR_LISTEN=127.0.0.1:$MON_PORT PULSE_NATS_WEBSOCKET_LISTEN=127.0.0.1:$WS_PORT
export PULSE_NATS_URL=tls://127.0.0.1:$NATS_PORT PULSE_NATS_TLS_NAME=localhost PULSE_NATS_WEBSOCKET_URL=http://127.0.0.1:$WS_PORT
export PULSE_RAW_HOST=localhost PULSE_PUBLIC_NATS_URL=tls://127.0.0.1:$NATS_PORT PULSE_PUBLIC_ADDR=off PULSE_CF_RANGES_REFRESH=false
export PULSE_ADMIN_ADDR=127.0.0.1:$ADMIN_PORT PULSE_ADMIN_URL=http://127.0.0.1:$ADMIN_PORT PULSE_EVENTS_MAX_BYTES=256MiB
export PULSE_METRICS_ADDR=127.0.0.1:$METRICS_PORT PULSE_HEALTHCHECK_URL=http://127.0.0.1:$METRICS_PORT/readyz
"$VAL_DIR/bin/pulsed" init >"$VAL_DIR/pulsed-init.log" 2>&1

echo "== nats-server and pulsed serve"
"$NATS_SERVER" -c "$VAL_DIR/pulse/nats/nats.conf" >"$VAL_DIR/nats.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$MON_PORT/healthz?js-enabled-only=true" >/dev/null 2>&1 && break; sleep 0.2; done
"$VAL_DIR/bin/pulsed" serve >"$VAL_DIR/pulsed.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 100); do curl -fsS "$PULSE_HEALTHCHECK_URL" >/dev/null 2>&1 && break; sleep 0.3; done
curl -fsS "$PULSE_HEALTHCHECK_URL" >/dev/null || { echo "pulsed did not become ready:"; tail -5 "$VAL_DIR/pulsed.log"; exit 1; }

echo "== personal access tokens (operator for setup, viewer for deed's pulse_admin source)"
"$VAL_DIR/bin/pulsed" admin token --email validate-operator@example.com --create --role operator --days 1 --name validate \
  >"$VAL_DIR/secrets/operator.pat" 2>>"$VAL_DIR/pulsed-admin.log"
"$VAL_DIR/bin/pulsed" admin token --email deed-viewer@example.com --create --role viewer --days 1 --name deed \
  >"$VAL_DIR/secrets/viewer.pat" 2>>"$VAL_DIR/pulsed-admin.log"

export VAL_DIR PULSE_COMMIT PULSE_DIRTY NATS_PORT ADMIN_PORT PORT MGMT
echo "== Pulse clients and grants"
python3 -I scripts/validate/pulse_driver.py clients

echo "== deed (development mode) with its Pulse client credentials"
"$VAL_DIR/bin/deed" keys init -out "$VAL_DIR/secrets/deed-kek" >/dev/null
cat >"$VAL_DIR/deed.yaml" <<EOF
mode: development
data_dir: $VAL_DIR/deed-data
public_url: http://127.0.0.1:$PORT
kek: {source: file, file: $VAL_DIR/secrets/deed-kek}
http: {listen: 127.0.0.1:$PORT}
management: {listen: 127.0.0.1:$MGMT}
log: {level: debug, format: json}
outbound: {allow_cidrs: [127.0.0.1/32]}
pulse:
  enabled: true
  url: tls://127.0.0.1:$NATS_PORT
  creds_file: $VAL_DIR/secrets/deed.creds
  ca_file: $VAL_DIR/secrets/pulse-ca.pem
  source: deed
EOF
"$VAL_DIR/bin/deed" config check -config "$VAL_DIR/deed.yaml" >/dev/null
"$VAL_DIR/bin/deed" migrate -config "$VAL_DIR/deed.yaml" >/dev/null
"$VAL_DIR/bin/deed" admin bootstrap -config "$VAL_DIR/deed.yaml" >"$VAL_DIR/secrets/bootstrap.txt" 2>&1
sed -n 's/.*\/setup#[a-z_]*=\([^ ]*\).*/\1/p' "$VAL_DIR/secrets/bootstrap.txt" | sed -n 1p >"$VAL_DIR/secrets/setup-token"
[[ -s "$VAL_DIR/secrets/setup-token" ]] || { echo "bootstrap link not found"; exit 1; }
"$VAL_DIR/bin/deed" serve -config "$VAL_DIR/deed.yaml" >"$VAL_DIR/deed.log" 2>&1 &
pids+=($!)
for _ in $(seq 1 60); do curl -fsS "127.0.0.1:$MGMT/readyz" >/dev/null 2>&1 && break; sleep 0.5; done
curl -fsS "127.0.0.1:$MGMT/readyz" >/dev/null || { echo "deed did not become ready:"; tail -5 "$VAL_DIR/deed.log"; exit 1; }

status=0
python3 -I scripts/validate/pulse_driver.py run || status=$?
if [[ "${HOLD:-0}" == 1 ]]; then
  echo "HOLD=1: pulsed admin $PULSE_ADMIN_URL, NATS $PULSE_NATS_URL, deed http://127.0.0.1:$PORT; stop with Ctrl-C"
  echo "VAL_DIR=$VAL_DIR NATS_PORT=$NATS_PORT ADMIN_PORT=$ADMIN_PORT PORT=$PORT MGMT=$MGMT" >"$VAL_DIR/hold.env"
  while kill -0 "${pids[@]}" 2>/dev/null; do sleep 5; done
fi
exit "$status"
