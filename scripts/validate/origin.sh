#!/usr/bin/env bash
# Validates Deed's origin deployment shape for Cloudflare (docs/CLOUDFLARE.md, ADR-012) with the real container image
# in production mode, without a Cloudflare account:
#   - origin TLS with Authenticated Origin Pulls (client certificates from a configured CA), TLS >= 1.2, the served
#     chain, HSTS and security headers;
#   - client-IP resolution behind a real reverse proxy (nginx as the edge) for trusted, untrusted and Cloudflare-mode
#     hops, and login throttling keyed on the real client address whatever X-Forwarded-For / CF-Connecting-IP say;
#   - the documented nftables ruleset (nft -c, then loaded into the origin's network namespace) and UFW snippet
#     (ufw --dry-run), and the documented Cloudflare ranges against the built-in defaults.
# scripts/validate/origin_driver.py runs the checks and writes $VAL_DIR/results.json.
#
# Usage: scripts/validate/origin.sh                 (IMAGE=deed:local by default; BUILD=1 builds it from this tree)
#        IMAGE=deed:validate BUILD=1 KEEP=1 scripts/validate/origin.sh
#
# Requirements: docker, python3, openssl. Everything runs on one private docker network (a random 10.x.y.0/24) plus one
# published 127.0.0.1 port; the edge is EDGE_IMAGE (nginx, also the curl client), the firewall checks use FW_IMAGE
# (built once from FW_BASE with nftables + ufw when missing; that build needs the distribution mirrors). Names are
# example.com, foreign addresses RFC 5737. All keys and certificates (throwaway CAs, server, origin-pull client) and the
# KEK are generated at run time in a 0700 work directory and removed on exit with every container, volume and the
# network, unless KEEP=1 (containers and volumes are always removed; KEEP keeps the files).
set -euo pipefail
[[ -n "${TRACE:-}" ]] && set -x

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$root"
IMAGE=${IMAGE:-deed:local}
EDGE_IMAGE=${EDGE_IMAGE:-nginx:1.27.3}
FW_IMAGE=${FW_IMAGE:-deed-validate-firewall:local}
FW_BASE=${FW_BASE:-debian:trixie}
for t in docker python3 openssl; do command -v "$t" >/dev/null || { echo "$t not found"; exit 2; }; done

umask 077
VAL_DIR=${VAL_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/deed-origin-validate.XXXXXX")}
mkdir -p "$VAL_DIR/secrets" "$VAL_DIR/stage/deed" "$VAL_DIR/stage/edge" "$VAL_DIR/logs"
chmod 700 "$VAL_DIR"
echo "work dir: $VAL_DIR"

P="deed-origin-$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
containers=()
volumes=("$P-etc" "$P-kek")
network=""
# shellcheck disable=SC2329 # invoked by the EXIT trap
cleanup() {
  local rc=$?
  for c in ${containers[@]+"${containers[@]}"}; do
    docker logs "$c" >"$VAL_DIR/logs/${c#"$P"-}.log" 2>&1 || true
    docker rm -fv "$c" >/dev/null 2>&1 || true
  done
  docker volume rm -f "${volumes[@]}" >/dev/null 2>&1 || true
  [[ -n $network ]] && docker network rm "$network" >/dev/null 2>&1
  if [[ "${KEEP:-0}" != 1 ]]; then
    rm -rf "${VAL_DIR:?}/secrets" "${VAL_DIR:?}/stage"
    echo "keys, certificates and configs removed; logs and results.json stay in $VAL_DIR (KEEP=1 keeps everything)"
  fi
  return "$rc"
}
trap cleanup EXIT
trap "exit 130" INT TERM

if [[ "${BUILD:-0}" == 1 ]]; then
  echo "== build $IMAGE from $root"
  docker build --build-arg VERSION="validate-$(git rev-parse --short=12 HEAD)" --build-arg COMMIT="$(git rev-parse HEAD)" \
    -t "$IMAGE" . >"$VAL_DIR/logs/build.log" 2>&1 || { tail -20 "$VAL_DIR/logs/build.log"; exit 1; }
fi
docker image inspect "$IMAGE" >/dev/null || { echo "image $IMAGE not found (make docker, or BUILD=1)"; exit 2; }
docker image inspect "$EDGE_IMAGE" >/dev/null 2>&1 || docker pull -q "$EDGE_IMAGE" >/dev/null
if ! docker image inspect "$FW_IMAGE" >/dev/null 2>&1; then
  echo "== build $FW_IMAGE (nftables, ufw) from $FW_BASE"
  mkdir -p "$VAL_DIR/fwimage"
  printf 'FROM %s\nRUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nftables ufw iptables && rm -rf /var/lib/apt/lists/*\n' \
    "$FW_BASE" >"$VAL_DIR/fwimage/Dockerfile"
  docker build -q -t "$FW_IMAGE" "$VAL_DIR/fwimage" >/dev/null
fi
echo "deed image: $IMAGE ($(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$IMAGE")); tree: $(git rev-parse HEAD)"

echo "== throwaway PKI (origin CA chain for deed.example.com, origin-pull CA + client, rogue CA + client)"
S=$VAL_DIR/secrets
cat >"$S/x509.cnf" <<'EOF'
[root]
basicConstraints=critical,CA:TRUE
keyUsage=critical,keyCertSign,cRLSign
subjectKeyIdentifier=hash
[intermediate]
basicConstraints=critical,CA:TRUE,pathlen:0
keyUsage=critical,keyCertSign,cRLSign
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid:always
[server]
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=DNS:deed.example.com
authorityKeyIdentifier=keyid:always
[client]
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=clientAuth
authorityKeyIdentifier=keyid:always
[serveronly]
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
authorityKeyIdentifier=keyid:always
EOF
# cert NAME CN EXTENSIONS [ISSUER]: key + certificate valid for two days; self-signed without ISSUER.
cert() {
  openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out "$S/$1-key.pem" 2>/dev/null
  openssl req -new -key "$S/$1-key.pem" -subj "/O=Deed validation/CN=$2" -out "$S/$1.csr"
  if [[ -z "${4:-}" ]]; then
    openssl x509 -req -in "$S/$1.csr" -signkey "$S/$1-key.pem" -days 2 -set_serial "0x$(openssl rand -hex 8)" \
      -extfile "$S/x509.cnf" -extensions "$3" -out "$S/$1.pem" 2>/dev/null
  else
    openssl x509 -req -in "$S/$1.csr" -CA "$S/$4.pem" -CAkey "$S/$4-key.pem" -days 2 -set_serial "0x$(openssl rand -hex 8)" \
      -extfile "$S/x509.cnf" -extensions "$3" -out "$S/$1.pem" 2>/dev/null
  fi
  rm -f "$S/$1.csr"
}
cert origin-root "Validation Origin Root CA" root
cert origin-int "Validation Origin Intermediate CA" intermediate origin-root
cert origin deed.example.com server origin-int
cert pull-ca "Validation Origin Pull CA" root                       # stands in for Cloudflare's origin-pull CA
cert pull origin-pull.example.com client pull-ca                     # stands in for Cloudflare's origin-pull certificate
cert pull-serveronly serveronly.example.com serveronly pull-ca       # right CA, but no clientAuth extended key usage
cert rogue-ca "Validation Rogue CA" root
cert rogue origin-pull.example.com client rogue-ca                   # same name, other CA
cat "$S/origin.pem" "$S/origin-int.pem" >"$S/origin-chain.pem"

# Network and fixed addresses: .10 edge (nginx; the "Cloudflare edge" stand-in in Cloudflare mode), .11-.15 Deed
# instances, .21/.22 clients. The subnet is random 10.x.y.0/24; retried when it collides with an existing network.
for _ in 1 2 3 4 5 6 7 8; do
  SUB="10.$((RANDOM % 150 + 100)).$((RANDOM % 254 + 1))"
  if docker network create --driver bridge --subnet "$SUB.0/24" --label deed-validate=origin "$P" >/dev/null 2>&1; then
    network=$P
    break
  fi
done
[[ -n $network ]] || { echo "could not create a docker network"; exit 1; }
EDGE=$SUB.10
echo "network $network $SUB.0/24 (edge $EDGE)"

echo "== configs: five production Deed instances"
# deed CONFIG_NAME PROXY_YAML [HTTP_YAML]: the image's production settings with access and audit logs on stdout.
deedcfg() {
  local http=${3:-"{listen: 0.0.0.0:8080}"}
  cat >"$VAL_DIR/stage/deed/$1.yaml" <<EOF
mode: production
data_dir: /var/lib/deed
public_url: https://deed.example.com
kek: {source: file, file: /run/secrets/deed_kek}
http: $http
management: {listen: 0.0.0.0:9464}
proxy: $2
log: {level: info, format: json, access_log: true, audit_file: "-"}
versions: {enabled: false}
EOF
}
deedcfg trusted "{trusted_proxies: [$EDGE/32], cloudflare: false}"
deedcfg untrusted "{trusted_proxies: [], cloudflare: false}"
deedcfg cloudflare "{trusted_proxies: [], cloudflare: true}"
deedcfg localproxy "{trusted_proxies: [$EDGE/32], cloudflare: true}"
# The origin: TLS + Authenticated Origin Pulls on 443 as in docs/CLOUDFLARE.md; the edge's address is configured as
# the only "Cloudflare range" (proxy.cloudflare_ranges is the documented override of the built-in list).
deedcfg origin "{trusted_proxies: [], cloudflare: true, cloudflare_ranges: [$EDGE/32]}" \
  "{listen: 0.0.0.0:443, tls_cert_file: /etc/deed-validate/deed/origin.pem, tls_key_file: /etc/deed-validate/deed/origin-key.pem, client_ca_file: /etc/deed-validate/deed/origin-pull-ca.pem}"
cp "$S/origin-chain.pem" "$VAL_DIR/stage/deed/origin.pem"
cp "$S/origin-key.pem" "$VAL_DIR/stage/deed/origin-key.pem"
cp "$S/pull-ca.pem" "$VAL_DIR/stage/deed/origin-pull-ca.pem"
cp "$S/origin-root.pem" "$VAL_DIR/stage/edge/origin-root.pem"
cp "$S/pull.pem" "$VAL_DIR/stage/edge/origin-pull.pem"
cp "$S/pull-key.pem" "$VAL_DIR/stage/edge/origin-pull-key.pem"

# Edge (nginx) listeners. 8001/8002 pass client headers through and append X-Forwarded-For (a plain reverse proxy);
# 8003/8004/8006 behave like Cloudflare (CF-Connecting-IP := peer, X-Forwarded-For appended); 8005 is a local proxy
# that sets CF-Connecting-IP but no X-Forwarded-For; 8006/8007 reach the TLS origin with Full (strict) verification,
# 8006 presenting the origin-pull client certificate and 8007 none. Both proxy_pass lines name the same address, so
# nginx shares one implicit upstream and would resume 8006's TLS session (authenticated with the client certificate) on
# 8007: 8007 disables session reuse to test a fresh handshake.
edge_server() { # PORT UPSTREAM EXTRA...
  local port=$1 up=$2
  shift 2
  printf '  server {\n    listen %s;\n    location / {\n      proxy_pass %s;\n      proxy_http_version 1.1;\n' "$port" "$up"
  printf '      proxy_set_header Host deed.example.com;\n      proxy_set_header Connection "";\n      proxy_set_header X-Forwarded-Proto https;\n'
  for l in "$@"; do printf '      %s\n' "$l"; done
  printf '    }\n  }\n'
}
# shellcheck disable=SC2016 # nginx variables
xff='proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;'
# shellcheck disable=SC2016
cfip='proxy_set_header CF-Connecting-IP $remote_addr;'
tls=('proxy_ssl_server_name on;' 'proxy_ssl_name deed.example.com;' 'proxy_ssl_verify on;' 'proxy_ssl_verify_depth 3;'
  'proxy_ssl_trusted_certificate /etc/deed-validate/edge/origin-root.pem;' 'proxy_ssl_protocols TLSv1.2 TLSv1.3;')
aop=('proxy_ssl_certificate /etc/deed-validate/edge/origin-pull.pem;' 'proxy_ssl_certificate_key /etc/deed-validate/edge/origin-pull-key.pem;')
{
  printf 'worker_processes 1;\npid /tmp/nginx.pid;\nerror_log /dev/stderr info;\nevents { worker_connections 256; }\nhttp {\n'
  # shellcheck disable=SC2016 # nginx variables
  printf '  log_format edge '\''$remote_addr port=$server_port xff="$http_x_forwarded_for" cf="$http_cf_connecting_ip" status=$status upstream=$upstream_addr'\'';\n'
  printf '  access_log /dev/stdout edge;\n'
  edge_server 8001 "http://$SUB.11:8080" "$xff"
  edge_server 8002 "http://$SUB.12:8080" "$xff"
  edge_server 8003 "http://$SUB.13:8080" "$xff" "$cfip"
  edge_server 8004 "http://$SUB.14:8080" "$xff" "$cfip"
  edge_server 8005 "http://$SUB.14:8080" 'proxy_set_header X-Forwarded-For "";' "$cfip"
  edge_server 8006 "https://$SUB.15:443" "$xff" "$cfip" "${tls[@]}" "${aop[@]}"
  edge_server 8007 "https://$SUB.15:443" "$xff" "$cfip" "${tls[@]}" 'proxy_ssl_session_reuse off;'
  printf '}\n'
} >"$VAL_DIR/stage/edge/nginx.conf"

echo "== volumes: configs/certificates (deed: 65532 0400, edge: root 0400) and the KEK"
docker volume create "$P-etc" >/dev/null
docker volume create "$P-kek" >/dev/null
docker container create --name "$P-loader" --network none -v "$P-etc:/v" "$EDGE_IMAGE" true >/dev/null
containers+=("$P-loader")
docker cp -q "$VAL_DIR/stage/." "$P-loader:/v/"
docker run --rm --network none -v "$P-etc:/v" --entrypoint sh "$EDGE_IMAGE" -c \
  'chown -R 0:0 /v && chmod 755 /v && chown -R 65532:65532 /v/deed && chmod 500 /v/deed && chmod 400 /v/deed/* && chmod 700 /v/edge && chmod 400 /v/edge/*'
hardening=(--read-only --cap-drop ALL --security-opt no-new-privileges:true --user 65532:65532 --tmpfs "/tmp:size=16m,mode=1777")
docker run --rm "${hardening[@]}" --network none -v "$P-kek:/var/lib/deed-kek" "$IMAGE" keys init --out /var/lib/deed-kek/deed_kek >/dev/null

echo "== start Deed instances"
i=11
for name in trusted untrusted cloudflare localproxy origin; do
  publish=(--label deed-validate=origin)
  [[ $name == origin ]] && publish+=(-p 127.0.0.1::443)
  docker run -d --name "$P-$name" --network "$network" --ip "$SUB.$i" "${hardening[@]}" "${publish[@]}" \
    -v "$P-kek:/run/secrets:ro" -v "$P-etc:/etc/deed-validate:ro" -e "DEED_CONFIG=/etc/deed-validate/deed/$name.yaml" \
    "$IMAGE" serve >/dev/null
  containers+=("$P-$name")
  i=$((i + 1))
done
for name in trusted untrusted cloudflare localproxy origin; do
  ok=false
  for _ in $(seq 1 60); do
    if docker exec "$P-$name" /usr/local/bin/deed healthcheck >/dev/null 2>&1; then ok=true; break; fi
    [[ $(docker inspect -f '{{.State.Running}}' "$P-$name") == true ]] || break
    sleep 0.5
  done
  $ok || { echo "deed $name did not become ready:"; docker logs "$P-$name" 2>&1 | tail -5; exit 1; }
done
ORIGIN_PORT=$(docker port "$P-origin" 443/tcp | head -n1 | sed 's/.*://')
echo "Deed instances ready; origin TLS published on 127.0.0.1:$ORIGIN_PORT"

echo "== edge (nginx) and clients"
docker run -d --name "$P-edge" --network "$network" --ip "$EDGE" -v "$P-etc:/etc/deed-validate:ro" --entrypoint nginx \
  "$EDGE_IMAGE" -c /etc/deed-validate/edge/nginx.conf -g 'daemon off;' >/dev/null
containers+=("$P-edge")
for n in 1 2; do
  docker run -d --name "$P-client$n" --network "$network" --ip "$SUB.2$n" --entrypoint sleep "$EDGE_IMAGE" infinity >/dev/null
  containers+=("$P-client$n")
done
for _ in $(seq 1 30); do
  docker exec "$P-client1" curl -fsS -o /dev/null --max-time 2 "http://$EDGE:8001/api/v1/system/info" 2>/dev/null && break
  sleep 0.5
done

export VAL_DIR P SUB ORIGIN_PORT IMAGE EDGE_IMAGE FW_IMAGE
status=0
python3 -I scripts/validate/origin_driver.py || status=$?
exit "$status"
