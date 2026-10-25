# Running Deed behind Cloudflare

Deed runs on its own VPS with Cloudflare in front (ADR-012). Three things matter: Deed must see the real client IP,
nothing may reach the origin except Cloudflare, and the management listener must stay private.

## 1. Client IPs
```yaml
proxy:
  cloudflare: true          # trust CF-Connecting-IP …
  # cloudflare_ranges: […]  # … only from these peers (defaults to Cloudflare's published ranges, retrieved 2026-10)
  trusted_proxies: []       # add a local reverse proxy (e.g. 127.0.0.1/32) if one sits between Cloudflare and Deed
```
Deed walks `X-Forwarded-For` from the right only while the current hop is in `trusted_proxies`; the first untrusted
hop is the client. `CF-Connecting-IP` replaces that hop only when it is inside a Cloudflare range (the TCP peer, or
the address a trusted local proxy put in `X-Forwarded-For`); from anywhere else the header is ignored, so a client
that reaches the origin directly cannot spoof its address for login throttling, rate limits or audit records. Refresh
`cloudflare_ranges` from https://www.cloudflare.com/ips/ when Cloudflare announces changes.

Every reverse proxy between Cloudflare and Deed must be listed in `trusted_proxies`, by the address Deed sees it
connect from. Otherwise every visitor resolves to the proxy's address: audit records show the proxy, and one client's
failed logins throttle everyone. With the container deployment (`deploy/compose.yaml`) a proxy on the host reaches
the published `127.0.0.1:8080` from the compose network's gateway, not from 127.0.0.1: trust that address
(`docker network inspect deed_default -f '{{(index .IPAM.Config 0).Gateway}}'`, passed as `DEED_TRUSTED_PROXIES`) and
pin the network's subnet in a compose override so the gateway does not change when the network is recreated.

## 2. TLS to the origin
Use **Full (strict)** SSL mode. Either terminate TLS in Deed:
```yaml
http:
  listen: 0.0.0.0:443
  tls_cert_file: /etc/deed/origin.pem        # Cloudflare Origin CA certificate (or any publicly trusted one)
  tls_key_file: /etc/deed/origin-key.pem
  client_ca_file: /etc/deed/cloudflare-origin-pull-ca.pem   # Authenticated Origin Pulls
```
`tls_cert_file` may hold the certificate followed by its intermediates; Deed serves the whole file. TLS 1.2 is the
minimum. Binding 443 as the unprivileged `deed` user needs `CAP_NET_BIND_SERVICE` with the systemd unit
(deploy/systemd/README.md); in the container image it works under Docker, which sets
`net.ipv4.ip_unprivileged_port_start=0`. Alternatively listen on an unprivileged port such as 8443 and point
Cloudflare at it with an Origin Rule (destination port override).

Or terminate in a local reverse proxy (Caddy, nginx) and list it in `proxy.trusted_proxies`. `client_ca_file` only
applies to Deed's own TLS listener, so Authenticated Origin Pulls must then be enforced by that proxy (nginx:
`ssl_client_certificate` + `ssl_verify_client on`; Caddy: `tls { client_auth { mode require_and_verify … } }`).

**Authenticated Origin Pulls** (`client_ca_file`) makes Deed require Cloudflare's client certificate on every TLS
connection: even if an attacker finds the origin IP and the firewall is misconfigured, they cannot complete a
handshake. Enable it in the Cloudflare dashboard (SSL/TLS → Origin Server → Authenticated Origin Pulls) and use
Cloudflare's origin-pull CA (https://developers.cloudflare.com/ssl/static/authenticated_origin_pull_ca.pem) as
`client_ca_file`. With Cloudflare's shared certificate, AOP proves "some Cloudflare zone": anyone who points their
own zone at your origin IP passes it. Upload your own client certificate (zone-level or per-hostname AOP) and use its
CA instead to prove "your zone".

## 3. Origin firewall
Allow 443 only from Cloudflare; keep SSH restricted; never expose 9464 (metrics) or the plain HTTP port.

### nftables
```nft
#!/usr/sbin/nft -f
# /etc/nftables.d/deed.nft — refresh the sets from https://www.cloudflare.com/ips-v4 and /ips-v6
table inet deed {
  set cloudflare4 { type ipv4_addr; flags interval; elements = {
    173.245.48.0/20, 103.21.244.0/22, 103.22.200.0/22, 103.31.4.0/22, 141.101.64.0/18, 108.162.192.0/18,
    190.93.240.0/20, 188.114.96.0/20, 197.234.240.0/22, 198.41.128.0/17, 162.158.0.0/15, 104.16.0.0/13,
    104.24.0.0/14, 172.64.0.0/13, 131.0.72.0/22 } }
  set cloudflare6 { type ipv6_addr; flags interval; elements = {
    2400:cb00::/32, 2606:4700::/32, 2803:f800::/32, 2405:b500::/32, 2405:8100::/32, 2a06:98c0::/29, 2c0f:f248::/32 } }
  set admin4 { type ipv4_addr; flags interval; elements = { 192.0.2.0/24 } }   # your admin network (example)

  chain input {
    type filter hook input priority 0; policy drop;
    ct state established,related accept
    iif lo accept
    ip protocol icmp accept
    ip6 nexthdr ipv6-icmp accept
    tcp dport 22 ip saddr @admin4 accept
    tcp dport 443 ip saddr @cloudflare4 accept
    tcp dport 443 ip6 saddr @cloudflare6 accept
    # 8080 (plain HTTP) and 9464 (metrics) are never reachable from outside.
  }
}
```
### UFW
```sh
ufw default deny incoming
ufw default allow outgoing
ufw allow from 192.0.2.0/24 to any port 22 proto tcp          # admin network (example)
for r in $(curl -fsS https://www.cloudflare.com/ips-v4) $(curl -fsS https://www.cloudflare.com/ips-v6); do
  ufw allow from "$r" to any port 443 proto tcp comment cloudflare
done
ufw enable
```
If either download fails, no 443 rule is added and the origin is closed rather than open; check `ufw status` before
logging out.

### Docker
Docker publishes ports through its own chains, which bypass both a host nftables `input` policy and UFW: with the
compose deployment, publish Deed on `127.0.0.1` only (as `deploy/compose.yaml` does) and put the TLS proxy on the
host, or add rules to the `DOCKER-USER` chain.

### Cloudflare Tunnel
Alternatively run `cloudflared` on the host and point the tunnel at `http://127.0.0.1:8080`: the origin then needs
no inbound port at all. Set `proxy.trusted_proxies: [127.0.0.1/32]`. Cloudflare puts the visitor's address in
`X-Forwarded-For` (appending it to anything the visitor sent) and cloudflared passes the header on, so Deed takes the
client from there. `CF-Connecting-IP` is not used on this path: the hop that sends it, 127.0.0.1, is not a Cloudflare
address, and being a trusted proxy does not make it one. A request without `X-Forwarded-For` resolves to 127.0.0.1.

## 4. Cloudflare settings
- **Cache:** bypass `/api/*` (Deed sends `Cache-Control: no-store` on API responses; static assets are immutable and
  safe to cache).
- **SSE:** `/api/v1/stream*` are long-lived event streams. Cloudflare passes them through; Deed sends a keep-alive
  comment every 20 s so idle streams are not cut at Cloudflare's 100 s proxy read timeout.
- **WAF:** managed rules are compatible; exclude `/api/v1/sources/*/terraform/state` from body inspection if it
  flags large state uploads.
- **Access (optional):** Cloudflare Access in front of the UI adds a second gate; leave `/api/v1/*` reachable with
  service tokens if Terraform or agents run outside your network.

## 5. Checklist
- `curl -sI https://deed.example.com/api/v1/system/info` works through Cloudflare.
- `curl -sk --resolve deed.example.com:443:<origin-ip> https://deed.example.com/` from outside Cloudflare times out
  (firewall) or fails the TLS handshake (AOP).
- The management port (9464) and any plain HTTP port time out from outside.
- `deed config check` shows `proxy.cloudflare: true` and the expected ranges.
- Audit records of a test login show your real IP, not a Cloudflare address or your proxy's.

`scripts/validate/origin.sh` checks the origin side of this locally with the container image (TLS with Authenticated
Origin Pulls, client-IP resolution behind an nginx edge, login throttling under spoofed headers, the nftables and UFW
examples above); what only a real Cloudflare zone can show is listed in its output.
