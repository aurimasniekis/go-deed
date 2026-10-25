#!/usr/bin/env bash
# Validate the github source connector against the live GitHub REST API (api.github.com), read-only.
#
# Deed runs from this repository in development mode on loopback; scripts/validate/github.py creates a github source
# for a few public repositories, runs its test and a sync, compares the catalog with what the API returns
# (repositories, latest releases, latest deployment per environment, open and recently closed pull requests and
# issues) and checks that a second sync is idempotent.
#
# Usage: scripts/validate/github.sh
#   GH_REPOS      space-separated owner/name list (default: "aurimasniekis/go-deed pressly/goose")
#   GITHUB_TOKEN  optional read-only token; without it the run is anonymous (60 requests per hour per address, which
#                 allows about one run per hour)
#   VALIDATE_DIR  work directory (default: a new 0700 temp dir); KEEP=1 keeps it on success
#
# Requires go, python3, curl and outbound HTTPS to api.github.com. Results: $VALIDATE_DIR/results.json.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
work=${VALIDATE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/deed-validate-github.XXXXXX")}
mkdir -p "$work"
chmod 700 "$work"
echo "work dir: $work"
export GH_REPOS=${GH_REPOS:-"aurimasniekis/go-deed pressly/goose"}

deed_pid=""
cleanup() {
  local rc=$?
  [[ -n $deed_pid ]] && { kill "$deed_pid" 2>/dev/null || true; wait "$deed_pid" 2>/dev/null || true; }
  if [[ $rc -ne 0 ]]; then
    echo "validate-github: FAIL (logs in $work)" >&2
    tail -n 20 "$work/deed.log" >&2 2>/dev/null || true
  elif [[ ${KEEP:-} != 1 ]]; then
    rm -rf "$work"
  fi
}
trap cleanup EXIT

remaining=$(curl -fsS ${GITHUB_TOKEN:+-H "Authorization: Bearer $GITHUB_TOKEN"} https://api.github.com/rate_limit |
  python3 -I -c 'import json, sys; print(json.load(sys.stdin)["rate"]["remaining"])')
echo "GitHub API requests remaining: $remaining"
if ((remaining < 45)); then echo "not enough GitHub API budget for a run; retry after the hourly reset or set GITHUB_TOKEN" >&2; exit 1; fi

free_port() { python3 -I -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()'; }
PORT_=$(free_port) MGMT_=$(free_port)

echo "== build deed"
mkdir -p "$work/bin" "$work/kek"
chmod 700 "$work/kek"
(cd "$root" && go build -o "$work/bin/deed" ./cmd/deed)
"$work/bin/deed" keys init -out "$work/kek/primary" >/dev/null
cat >"$work/deed.yaml" <<YAML
mode: development
data_dir: $work/deed-data
public_url: http://127.0.0.1:$PORT_
kek: {source: file, file: $work/kek/primary}
http: {listen: 127.0.0.1:$PORT_}
management: {listen: 127.0.0.1:$MGMT_}
log: {level: info, format: json}
YAML
"$work/bin/deed" migrate -config "$work/deed.yaml" >"$work/deed-migrate.log" 2>&1
"$work/bin/deed" admin bootstrap -config "$work/deed.yaml" 2>/dev/null |
  sed -n 's/.*\/setup#[a-z_]*=\([^ ]*\).*/\1/p' | head -1 >"$work/deed-setup-token"
[[ -s $work/deed-setup-token ]] || { echo "deed bootstrap link not found" >&2; exit 1; }
"$work/bin/deed" serve -config "$work/deed.yaml" >"$work/deed.log" 2>&1 &
deed_pid=$!
for _ in $(seq 1 150); do curl -fsS "127.0.0.1:$MGMT_/readyz" >/dev/null 2>&1 && break; sleep 0.2; done

# Not DEED_* names: deed refuses unknown DEED_* variables in its environment.
export VALIDATE_DIR=$work DEED_URL_="http://127.0.0.1:$PORT_"
python3 -I "$root/scripts/validate/github.py"
