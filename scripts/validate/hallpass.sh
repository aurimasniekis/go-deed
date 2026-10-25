#!/usr/bin/env bash
# Validate the hallpass source connector against a real HallPass built from source and run as a local process.
#
# HallPass runs in development mode (SQLite, generated KEK, plain HTTP and SSH on loopback); Deed runs from this
# repository the same way. scripts/validate/hallpass.py seeds HallPass through its operator API and agent SSH commands
# (agents, servers, roles, auto-approved grants, an open access session), mints an auditor hallpass_at_ token, syncs a
# Deed hallpass source and compares the catalog with what HallPass returns, then removes data in HallPass, syncs again
# and checks absence handling, and runs the connector test with a wrong token.
#
# Usage: scripts/validate/hallpass.sh
#   HALLPASS_DIR   HallPass source checkout (default ../hallpass, relative to this repository)
#   VALIDATE_DIR   work directory (default: a new 0700 temp dir); generated credentials live only here
#   VALIDATE_DEST  1 = start a throwaway OpenSSH destination container (HallPass's test/openssh image, published on
#                  127.0.0.1) so an access session can be opened; 0 = skip sessions; default: 1 when docker answers
#   KEEP=1         keep the work directory on success
#
# Requires go, python3, ssh, ssh-keygen, curl; docker for the destination. Loopback only; every name is example.com.
# Results: $VALIDATE_DIR/results.json and the console log.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
hp_src=$(cd "${HALLPASS_DIR:-$root/../hallpass}" && pwd)
# hallpass refuses unknown HALLPASS_* variables in its environment.
unset HALLPASS_DIR
if [[ -n ${VALIDATE_DIR:-} ]]; then
  # Never share (or later delete) a directory that holds anything else.
  if [[ -e $VALIDATE_DIR && -n $(ls -A "$VALIDATE_DIR") ]]; then echo "VALIDATE_DIR $VALIDATE_DIR is not empty" >&2; exit 1; fi
  work=$VALIDATE_DIR
  mkdir -p "$work"
else
  work=$(mktemp -d "${TMPDIR:-/tmp}/deed-validate-hallpass.XXXXXX")
fi
chmod 700 "$work"
echo "work dir: $work"

if [[ -z ${VALIDATE_DEST:-} ]]; then
  if docker info >/dev/null 2>&1; then VALIDATE_DEST=1; else VALIDATE_DEST=0; fi
fi

hp_pid="" deed_pid="" dest=""
cleanup() {
  local rc=$?
  for p in $deed_pid $hp_pid; do kill "$p" 2>/dev/null || true; done
  for p in $deed_pid $hp_pid; do wait "$p" 2>/dev/null || true; done
  if [[ -n $dest ]]; then docker rm -f "$dest" >/dev/null 2>&1 || true; fi
  if [[ $rc -ne 0 ]]; then
    echo "validate-hallpass: FAIL (logs in $work)" >&2
    tail -n 20 "$work/hallpass.log" "$work/deed.log" >&2 2>/dev/null || true
  elif [[ ${KEEP:-} != 1 ]]; then
    rm -rf "$work"
  fi
}
trap cleanup EXIT

free_port() { python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'; }
wait_http() { # url pid what
  for _ in $(seq 1 150); do
    curl -fsS "$1" >/dev/null 2>&1 && return 0
    kill -0 "$2" 2>/dev/null || { echo "$3 exited during startup" >&2; return 1; }
    sleep 0.2
  done
  echo "$3 did not become ready" >&2
  return 1
}

HP_HTTP=$(free_port) HP_SSH=$(free_port) DEED_PORT_=$(free_port) DEED_MGMT_=$(free_port)
HP_URL="http://127.0.0.1:$HP_HTTP"

echo "== build HallPass ($hp_src) and deed"
mkdir -p "$work/bin"
HP_COMMIT=$(python3 -I "$root/scripts/validate/headrev.py" "$hp_src")
echo "HallPass commit: $HP_COMMIT"
(cd "$hp_src" && CGO_ENABLED=0 go build -trimpath -o "$work/bin/hallpass" ./cmd/hallpass)
(cd "$root" && go build -o "$work/bin/deed" ./cmd/deed)

if [[ $VALIDATE_DEST == 1 ]]; then
  echo "== destination (HallPass test/openssh image on 127.0.0.1)"
  ctx=$(mktemp -d)
  cp "$hp_src/test/openssh/Dockerfile.sshd" "$hp_src/test/openssh/sshd_config" "$hp_src/test/openssh/entrypoint.sh" "$ctx/"
  docker build -q -t deed-validate-hallpass-dest:local -f "$ctx/Dockerfile.sshd" "$ctx" >/dev/null
  rm -rf "$ctx"
  dest=deed-validate-hallpass-dest-$$
  docker run -d --name "$dest" -p 127.0.0.1::22 deed-validate-hallpass-dest:local >/dev/null
  for _ in $(seq 1 50); do
    docker exec "$dest" sh -c 'test -s /etc/ssh/ssh_host_ed25519_key.pub && pgrep sshd' >/dev/null 2>&1 && break
    sleep 0.2
  done
  HP_DEST_PORT=$(docker port "$dest" 22/tcp | head -n 1 | sed 's/.*://')
  export HP_DEST_CONTAINER=$dest HP_DEST_PORT
fi

echo "== HallPass (development mode, http 127.0.0.1:$HP_HTTP, ssh 127.0.0.1:$HP_SSH)"
cat >"$work/hallpass.yaml" <<EOF
mode: development
data_dir: $work/hp-data
public_url: $HP_URL
kek:
  file: ""
ssh:
  listen: 127.0.0.1:$HP_SSH
http:
  listen: 127.0.0.1:$HP_HTTP
egress:
  allow_cidrs: [127.0.0.0/8]
  allow_special_cidrs: [127.0.0.0/8]
EOF
"$work/bin/hallpass" migrate -config "$work/hallpass.yaml" >"$work/hallpass-migrate.log" 2>&1 ||
  { tail -n 5 "$work/hallpass-migrate.log" >&2; exit 1; }
boot=$("$work/bin/hallpass" admin bootstrap -config "$work/hallpass.yaml" 2>&1)
printf '%s\n' "$boot" | awk '/^  token: /{print $2}' >"$work/hp-setup-token"
[[ -s $work/hp-setup-token ]] || { echo "HallPass bootstrap printed no setup token" >&2; exit 1; }
# The gateway host key, pinned for the agents (hostkey show takes the data-directory lock: run it before serving).
"$work/bin/hallpass" hostkey show -config "$work/hallpass.yaml" 2>/dev/null |
  awk -v h="[127.0.0.1]:$HP_SSH" '$1 == "known_hosts:" {print h, $3, $4}' >"$work/known_hosts"
[[ -s $work/known_hosts ]] || { echo "could not read the HallPass gateway host key" >&2; exit 1; }
"$work/bin/hallpass" serve -config "$work/hallpass.yaml" >"$work/hallpass.log" 2>&1 &
hp_pid=$!
wait_http "$HP_URL/readyz" "$hp_pid" HallPass
for a in ci-runner review-bot temp-bot; do ssh-keygen -q -t ed25519 -N "" -C "$a@example.com" -f "$work/agent-$a"; done

echo "== Deed (development mode, http 127.0.0.1:$DEED_PORT_)"
mkdir -p "$work/kek"
chmod 700 "$work/kek"
"$work/bin/deed" keys init -out "$work/kek/primary" >/dev/null
cat >"$work/deed.yaml" <<EOF
mode: development
data_dir: $work/deed-data
public_url: http://127.0.0.1:$DEED_PORT_
kek: {source: file, file: $work/kek/primary}
http: {listen: 127.0.0.1:$DEED_PORT_}
management: {listen: 127.0.0.1:$DEED_MGMT_}
log: {level: info, format: json}
outbound: {allow_cidrs: [127.0.0.1/32]}
EOF
"$work/bin/deed" config check -config "$work/deed.yaml" >/dev/null 2>&1
"$work/bin/deed" migrate -config "$work/deed.yaml" >"$work/deed-migrate.log" 2>&1
"$work/bin/deed" admin bootstrap -config "$work/deed.yaml" 2>/dev/null |
  sed -n 's/.*\/setup#[a-z_]*=\([^ ]*\).*/\1/p' | head -1 >"$work/deed-setup-token"
[[ -s $work/deed-setup-token ]] || { echo "deed bootstrap link not found" >&2; exit 1; }
"$work/bin/deed" serve -config "$work/deed.yaml" >"$work/deed.log" 2>&1 &
deed_pid=$!
wait_http "http://127.0.0.1:$DEED_MGMT_/readyz" "$deed_pid" deed

# Not DEED_* names: deed refuses unknown DEED_* variables in its environment.
export VALIDATE_DIR=$work HP_URL HP_SSH HP_COMMIT VALIDATE_DEST DEED_URL_="http://127.0.0.1:$DEED_PORT_"
python3 -I "$root/scripts/validate/hallpass.py"
