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
`CF-Connecting-IP` is honoured only when the TCP peer (or the last trusted proxy hop) is inside a Cloudflare range;
from anywhere else the header is ignored, so a client that reaches the origin directly cannot spoof its address
for login throttling, rate limits or audit records. Refresh `cloudflare_ranges` from https://www.cloudflare.com/ips/
when Cloudflare announces changes.

## 2. TLS to the origin
Use **Full (strict)** SSL mode. Either terminate TLS in Deed:
```yaml
http:
  listen: 0.0.0.0:443
  tls_cert_file: /etc/deed/origin.pem        # Cloudflare Origin CA certificate (or any publicly trusted one)
  tls_key_file: /etc/deed/origin-key.pem
  client_ca_file: /etc/deed/cloudflare-origin-pull-ca.pem   # Authenticated Origin Pulls
```
or terminate in a local reverse proxy (Caddy, nginx) and list it in `proxy.trusted_proxies`.

**Authenticated Origin Pulls** (`client_ca_file`) makes Deed require Cloudflare's client certificate on every TLS
connection: even if an attacker finds the origin IP and the firewall is misconfigured, they cannot complete a
handshake. Enable it in the Cloudflare dashboard (SSL/TLS → Origin Server → Authenticated Origin Pulls) and download
the origin-pull CA. Zone-level AOP proves "some Cloudflare zone"; per-hostname AOP with your own CA proves "your
zone".

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
Docker publishes ports through its own chains, which bypass a host `input` policy: with the compose deployment,
publish Deed on `127.0.0.1` only (as `deploy/compose.yaml` does) and put the TLS proxy on the host, or add rules to
the `DOCKER-USER` chain.

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

### Cloudflare Tunnel
Alternatively run `cloudflared` on the host and point the tunnel at `http://127.0.0.1:8080`: the origin then needs
no inbound port at all. Set `proxy.trusted_proxies: [127.0.0.1/32]` and `proxy.cloudflare: true`; cloudflared sets
`CF-Connecting-IP` and the hop is trusted because it is the configured local proxy.

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
- `deed config check` shows `proxy.cloudflare: true` and the expected ranges.
- Audit records of a test login show your real IP, not a Cloudflare address.
