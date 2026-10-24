#!/usr/bin/env bash
# Container smoke test for the Deed image.
#
# Usage: scripts/container-smoke.sh IMAGE
#
# Checks, without pushing anything (deploy/README.md describes the layout):
#   1. image metadata: user 65532:65532, EXPOSE 8080/tcp only, VOLUME /var/lib/deed, HEALTHCHECK, no shell
#   2. `deed version --json` runs
#   3. `deed keys init --out` writes the KEK into a separate named volume (0400, 65532:65532)
#   4. `deed config check` and `deed migrate` with the image's production config (deploy/deed.container.yaml),
#      a read-only root filesystem, no capabilities and the KEK volume mounted read-only at /run/secrets
#   5. a KEK inside data_dir and a KEK with group/other permission bits are refused
#   6. `deed serve` passes `deed healthcheck` and the Docker HEALTHCHECK, serves /api/v1/system/info on the published
#      port, and exits 0 on SIGTERM
#
# The helper image (stat/cp/chmod on the volumes) defaults to the Dockerfile's pinned NODE_IMAGE, so no extra
# unpinned image is pulled; override with HELPER_IMAGE.
#
# Adapted from HallPass scripts/container-smoke.sh (Apache-2.0, same author).
set -euo pipefail

image=${1:?usage: $0 IMAGE}
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
helper=${HELPER_IMAGE:-$(sed -n 's/^ARG NODE_IMAGE=//p' "$root/Dockerfile")}
[[ -n $helper ]] || { echo "container smoke: no helper image (set HELPER_IMAGE)" >&2; exit 2; }
id="deed-smoke-$$"
data_vol="$id-data"
kek_vol="$id-kek"
tmp=$(mktemp -d)
container=""
public_url=https://deed.example.test

cleanup() {
  local rc=$?
  if [[ -n $container ]]; then
    if [[ $rc -ne 0 ]]; then docker logs "$container" 2>&1 | tail -n 50 >&2 || true; fi
    docker rm -f "$container" >/dev/null 2>&1 || true
  fi
  docker volume rm -f "$data_vol" "$kek_vol" >/dev/null 2>&1 || true
  rm -rf "$tmp"
  if [[ $rc -eq 0 ]]; then echo "container smoke: PASS"; else echo "container smoke: FAIL" >&2; fi
}
trap cleanup EXIT

step() { printf '== %s\n' "$*"; }
fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

# Hardened run options shared by every invocation (mirrors deploy/compose.yaml).
hardening=(--read-only --cap-drop ALL --security-opt no-new-privileges:true --user 65532:65532
  --tmpfs "/tmp:size=16m,mode=1777")
run_opts=(--rm "${hardening[@]}")

step "image metadata"
user=$(docker image inspect -f '{{.Config.User}}' "$image")
[[ $user == "65532:65532" ]] || fail "image user is '$user', want 65532:65532"
ports=$(docker image inspect -f '{{json .Config.ExposedPorts}}' "$image")
[[ $ports == '{"8080/tcp":{}}' ]] || fail "exposed ports $ports, want only 8080/tcp"
vols=$(docker image inspect -f '{{json .Config.Volumes}}' "$image")
[[ $vols == '{"/var/lib/deed":{}}' ]] || fail "volumes $vols, want only /var/lib/deed"
hc=$(docker image inspect -f '{{json .Config.Healthcheck.Test}}' "$image")
[[ $hc == *'"healthcheck"'* ]] || fail "HEALTHCHECK $hc"
for sh in /bin/sh /bin/bash /busybox/sh; do
  if docker run --rm --entrypoint "$sh" "$image" -c true >/dev/null 2>&1; then fail "image contains a shell at $sh"; fi
done
echo "user=$user ports=$ports volumes=$vols healthcheck=$hc, no shell"

step "version"
docker run "${run_opts[@]}" "$image" version --json | tee "$tmp/version.json"
grep -q '"version"' "$tmp/version.json" || fail "version output"

step "provision KEK in a separate volume"
docker volume create "$kek_vol" >/dev/null
docker volume create "$data_vol" >/dev/null
# /var/lib/deed-kek is 0700 65532:65532 in the image, so a fresh named volume mounted there inherits that ownership
# (the distroless image has no shell to chown it).
docker run "${run_opts[@]}" --network none -v "$kek_vol:/var/lib/deed-kek" "$image" \
  keys init --out /var/lib/deed-kek/deed_kek
mode=$(docker run --rm -v "$kek_vol:/kek:ro" "$helper" stat -c '%a %u:%g' /kek /kek/deed_kek | tr '\n' ' ')
[[ $mode == "700 65532:65532 400 65532:65532 " ]] || fail "KEK dir/file mode/owner is '$mode', want 700/400 65532:65532"
echo "KEK dir, file mode/owner: $mode"
if docker run "${run_opts[@]}" --network none -v "$kek_vol:/var/lib/deed-kek" "$image" \
  keys init --out /var/lib/deed-kek/deed_kek >/dev/null 2>&1; then
  fail "keys init overwrote an existing KEK"
fi
echo "keys init refuses to overwrite an existing KEK"

# The image's baked-in production config (/etc/deed/deed.yaml from deploy/deed.container.yaml): mode production,
# kek.file /run/secrets/deed_kek, http.listen 0.0.0.0:8080, management on container loopback. public_url has no
# default and comes from the environment, as in deploy/compose.yaml.
mounts=(-v "$data_vol:/var/lib/deed" -v "$kek_vol:/run/secrets:ro" -e "DEED_PUBLIC_URL=$public_url")

step "config check (production config)"
docker run "${run_opts[@]}" "${mounts[@]}" "$image" config check >"$tmp/effective.yaml"
grep -q '^mode: production$' "$tmp/effective.yaml" || fail "effective config is not production mode"
grep -q "^public_url: $public_url\$" "$tmp/effective.yaml" || fail "effective public_url"
echo "configuration OK (mode: production, public_url: $public_url)"

step "missing public_url is refused"
if out=$(docker run "${run_opts[@]}" -v "$data_vol:/var/lib/deed" -v "$kek_vol:/run/secrets:ro" "$image" \
  config check 2>&1); then
  fail "config check accepted a config without public_url"
fi
[[ $out == *"public_url is required"* ]] || fail "unexpected refusal message: $out"
echo "refused: public_url is required"

step "KEK inside the data volume is refused"
if out=$(docker run "${run_opts[@]}" "${mounts[@]}" -e DEED_KEK_FILE=/var/lib/deed/deed_kek "$image" \
  config check 2>&1); then
  fail "config check accepted kek.file inside data_dir: $out"
fi
[[ $out == *"kek.file must be stored outside data_dir"* ]] || fail "unexpected refusal message: $out"
echo "refused: kek.file must be stored outside data_dir"

step "migrate"
docker run "${run_opts[@]}" "${mounts[@]}" --network none "$image" migrate | tee "$tmp/migrate.txt"
grep -Eq '^schema version ([0-9]+) \(latest \1\)$' "$tmp/migrate.txt" || fail "migrate output"
perm=$(docker run --rm -v "$data_vol:/data:ro" "$helper" stat -c '%a %u:%g' /data)
[[ $perm == "700 65532:65532" ]] || fail "data volume is '$perm', want '700 65532:65532'"
echo "data volume mode/owner: $perm"

step "KEK with group/other permissions is refused"
docker run --rm -v "$kek_vol:/kek" "$helper" sh -c 'cp -p /kek/deed_kek /kek/deed_kek-loose && chmod 0440 /kek/deed_kek-loose'
# `keys status` loads the KEK (config check and migrate do not).
if out=$(docker run "${run_opts[@]}" "${mounts[@]}" --network none -e DEED_KEK_FILE=/run/secrets/deed_kek-loose \
  "$image" keys status 2>&1); then
  fail "keys status accepted a 0440 KEK: $out"
fi
[[ $out == *"must not be accessible by group or others"* ]] || fail "unexpected refusal message: $out"
echo "refused: mode 0440 KEK (must not be accessible by group or others)"
docker run --rm -v "$kek_vol:/kek" "$helper" rm -f /kek/deed_kek-loose
docker run "${run_opts[@]}" "${mounts[@]}" --network none "$image" keys status >"$tmp/keys.json"
grep -q '"kek_id"' "$tmp/keys.json" || fail "keys status with the 0400 KEK"
echo "keys status OK with the 0400 KEK"

step "serve: healthcheck, Docker health, discovery, graceful SIGTERM"
container=$(docker run -d "${hardening[@]}" "${mounts[@]}" --health-interval 2s -p 127.0.0.1::8080 "$image" serve)
ready=false
for _ in $(seq 1 60); do
  if docker exec "$container" /usr/local/bin/deed healthcheck >/dev/null 2>&1; then ready=true; break; fi
  [[ $(docker inspect -f '{{.State.Running}}' "$container") == true ]] || fail "serve exited early"
  sleep 0.5
done
$ready || fail "deed healthcheck did not succeed within 30s"
echo "deed healthcheck: $(docker exec "$container" /usr/local/bin/deed healthcheck)"
health=""
for _ in $(seq 1 60); do
  health=$(docker inspect -f '{{.State.Health.Status}}' "$container")
  [[ $health == healthy ]] && break
  sleep 0.5
done
[[ $health == healthy ]] || fail "Docker health status is '$health', want healthy"
echo "Docker health status: $health"
port=$(docker port "$container" 8080/tcp | head -n1 | sed 's/.*://')
curl -fsS --max-time 5 "http://127.0.0.1:$port/api/v1/system/info" >"$tmp/info.json" ||
  fail "system info on 127.0.0.1:$port"
grep -q '"api_version":"v1"' "$tmp/info.json" || fail "system info: $(head -c 300 "$tmp/info.json")"
echo "system info: $(head -c 200 "$tmp/info.json")"
docker stop -t 30 "$container" >/dev/null
code=$(docker inspect -f '{{.State.ExitCode}}' "$container")
[[ $code == 0 ]] || fail "serve exited $code after SIGTERM"
echo "serve exited 0 after SIGTERM"
