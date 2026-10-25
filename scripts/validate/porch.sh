#!/usr/bin/env bash
# Validates Deed against a real Porch run locally from source: OIDC single sign-on (authorization code + PKCE, nonce,
# RFC 9207 iss, porch.groups → role mapping, JIT, tenant restriction) and the porch source connector (tenants,
# applications, service accounts, authenticates_with edges, cursor paging) with porch_at_ automation tokens: a tenant
# token (app:manage + service_account:manage, the narrowest that works), an installation admin token across 107
# tenants, and an installation auditor token, which Porch does not let read inside tenants.
#
# Usage: scripts/validate/porch.sh            (PORCH_DIR defaults to ../porch next to this repository)
#        PORCH_DIR=/path/to/go-porch KEEP=1 scripts/validate/porch.sh
#
# Requirements: go, python3. Everything listens on random free 127.0.0.1 ports; Porch signs users in through its own
# fake Google provider (test/fakegoogle, a test fixture shipped with Porch). Every name is example.com; every secret is
# generated at run time into a 0700 work directory that is removed afterwards unless KEEP=1. Results: PASS/FAIL lines
# on stdout and $VALIDATE_DIR/results.json.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
# Porch refuses unknown PORCH_* variables in its environment, so the setting is copied and removed from it.
porch_src=${PORCH_DIR:-$root/../porch}
if [[ -z "${PORCH_DIR:-}" && ! -d "$porch_src" ]]; then # in a git worktree: ../porch next to the main checkout
  porch_src=$(git -C "$root" rev-parse --path-format=absolute --git-common-dir)/../../porch
fi
porch_src=$(cd "$porch_src" && pwd)
unset PORCH_DIR
VALIDATE_DIR=${VALIDATE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/deed-validate-porch.XXXXXX")}
mkdir -p "$VALIDATE_DIR"
chmod 700 "$VALIDATE_DIR"
echo "work dir: $VALIDATE_DIR"
echo "porch: $porch_src @ $(git -C "$porch_src" rev-parse HEAD 2>/dev/null || echo unknown)"
echo "deed:  $root @ $(git -C "$root" rev-parse HEAD 2>/dev/null || echo unknown)"

pids=()
cleanup() {
  for p in "${pids[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
  if [[ "${KEEP:-}" != 1 ]]; then rm -rf "$VALIDATE_DIR"; fi
}
trap cleanup EXIT

free_port() { python3 -I -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1]); s.close()'; }
wait_ready() { # url what log
  for _ in $(seq 1 120); do curl -fsS "$1" >/dev/null 2>&1 && return 0; sleep 0.25; done
  echo "$2 did not become ready:"; tail -20 "$3"; exit 1
}

echo "== build porch, fakegoogle (from $porch_src) and deed"
mkdir -p "$VALIDATE_DIR/bin"
(cd "$porch_src" && CGO_ENABLED=0 go build -o "$VALIDATE_DIR/bin/porch" ./cmd/porch && go build -o "$VALIDATE_DIR/bin/fakegoogle" ./test/fakegoogle)
(cd "$root" && go build -o "$VALIDATE_DIR/bin/deed" ./cmd/deed)

# Not DEED_* names: deed refuses unknown DEED_* variables in its environment.
P_PORT=$(free_port); P_MGMT=$(free_port); IDP_PORT=$(free_port); D_PORT=$(free_port); D_MGMT=$(free_port)
export VALIDATE_DIR P_PORT P_MGMT IDP_PORT D_PORT D_MGMT
PORCH_URL="http://127.0.0.1:$P_PORT"

echo "== porch (development mode, own SQLite) and its fake Google on 127.0.0.1"
cat >"$VALIDATE_DIR/google-users.json" <<'JSON'
[
  {"subject": "ada", "email": "ada@example.com", "email_verified": true, "name": "Ada Example", "hd": "example.com"},
  {"subject": "bob", "email": "bob@example.com", "email_verified": true, "name": "Bob Example", "hd": "example.com"},
  {"subject": "dave", "email": "dave@example.com", "email_verified": true, "name": "Dave Example", "hd": "example.com"},
  {"subject": "carol", "email": "carol@lab.example.com", "email_verified": true, "name": "Carol Lab", "hd": "lab.example.com"}
]
JSON
head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n' >"$VALIDATE_DIR/google-secret"
chmod 600 "$VALIDATE_DIR/google-secret"
cat >"$VALIDATE_DIR/porch.yaml" <<YAML
mode: development
data_dir: $VALIDATE_DIR/porch-data
public_url: $PORCH_URL
http: {listen: 127.0.0.1:$P_PORT}
management: {listen: 127.0.0.1:$P_MGMT}
outbound: {allow_cidrs: [127.0.0.1/32]}
log: {format: text, level: info}
YAML
"$VALIDATE_DIR/bin/fakegoogle" -listen "127.0.0.1:$IDP_PORT" -issuer "http://127.0.0.1:$IDP_PORT" -client-id porch \
  -client-secret "$(cat "$VALIDATE_DIR/google-secret")" -redirect-uris "$PORCH_URL/login/google/callback" \
  -users "$VALIDATE_DIR/google-users.json" >"$VALIDATE_DIR/fakegoogle.log" 2>&1 &
pids+=($!)
"$VALIDATE_DIR/bin/porch" admin setup-token -config "$VALIDATE_DIR/porch.yaml" >"$VALIDATE_DIR/porch-setup.out" 2>&1 ||
  { cat "$VALIDATE_DIR/porch-setup.out"; exit 1; }
grep -o -m1 'porch_setup_[A-Za-z0-9_-]*' "$VALIDATE_DIR/porch-setup.out" >"$VALIDATE_DIR/porch-setup-token" || true
[[ -s "$VALIDATE_DIR/porch-setup-token" ]] || { echo "porch setup token not issued"; exit 1; }
"$VALIDATE_DIR/bin/porch" serve -config "$VALIDATE_DIR/porch.yaml" >"$VALIDATE_DIR/porch.log" 2>&1 &
pids+=($!)
wait_ready "http://127.0.0.1:$P_MGMT/readyz" porch "$VALIDATE_DIR/porch.log"
wait_ready "http://127.0.0.1:$IDP_PORT/.well-known/openid-configuration" fakegoogle "$VALIDATE_DIR/fakegoogle.log"

echo "== porch bootstrap through its management API"
python3 -I "$root/scripts/validate/porch_driver.py" porch

echo "== deed (development mode) with Porch as OIDC provider"
mkdir -p "$VALIDATE_DIR/kek"
chmod 700 "$VALIDATE_DIR/kek"
"$VALIDATE_DIR/bin/deed" keys init -out "$VALIDATE_DIR/kek/primary" >/dev/null
cat >"$VALIDATE_DIR/deed.yaml" <<YAML
mode: development
data_dir: $VALIDATE_DIR/deed-data
public_url: http://127.0.0.1:$D_PORT
kek: {source: file, file: $VALIDATE_DIR/kek/primary}
http: {listen: 127.0.0.1:$D_PORT}
management: {listen: 127.0.0.1:$D_MGMT}
log: {level: info, format: json}
outbound: {allow_cidrs: [127.0.0.1/32]}
auth:
  local: {enabled: true}
  oidc:
    - slug: porch
      display_name: Porch
      issuer: $PORCH_URL
      client_id: $(cat "$VALIDATE_DIR/oidc-client-id")
      client_secret_file: $VALIDATE_DIR/oidc-client-secret
      groups_claim: porch.groups
      tenant_claim: porch.tenant.slug
      required_tenant: home
      require_email_verified: true
      jit: true
      mappings:
        - {group: deed-admins, installation_role: admin}
        - {group: deed-auditors, installation_role: auditor}
        - {group: platform, project: infra, project_role: editor}
YAML
"$VALIDATE_DIR/bin/deed" config check -config "$VALIDATE_DIR/deed.yaml" >/dev/null
"$VALIDATE_DIR/bin/deed" migrate -config "$VALIDATE_DIR/deed.yaml" >"$VALIDATE_DIR/deed-migrate.log" 2>&1 ||
  { cat "$VALIDATE_DIR/deed-migrate.log"; exit 1; }
"$VALIDATE_DIR/bin/deed" admin bootstrap -config "$VALIDATE_DIR/deed.yaml" 2>/dev/null |
  sed -n 's/.*\/setup#[a-z_]*=\([^ ]*\).*/\1/p' | head -1 >"$VALIDATE_DIR/deed-setup-token"
[[ -s "$VALIDATE_DIR/deed-setup-token" ]] || { echo "deed bootstrap link not found"; exit 1; }
"$VALIDATE_DIR/bin/deed" serve -config "$VALIDATE_DIR/deed.yaml" >"$VALIDATE_DIR/deed.log" 2>&1 &
pids+=($!)
wait_ready "http://127.0.0.1:$D_MGMT/readyz" deed "$VALIDATE_DIR/deed.log"

status=0
python3 -I "$root/scripts/validate/porch_driver.py" deed || status=$?
if [[ $status -ne 0 ]]; then
  echo "--- deed log (tail)"; tail -20 "$VALIDATE_DIR/deed.log"
  echo "--- porch log (tail)"; tail -20 "$VALIDATE_DIR/porch.log"
fi
exit $status
