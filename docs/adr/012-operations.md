# ADR-012: Operations

- Public listener `:8080` (behind Cloudflare or a reverse proxy); management listener `127.0.0.1:9464` with
  `/metrics`, `/healthz`, `/readyz` (and opt-in pprof, refused on non-loopback in production).
- Trusted proxies only from config; `CF-Connecting-IP` honored only when the hop is in Cloudflare ranges and
  `proxy.cloudflare: true`. HSTS when `public_url` is https.
- Optional origin TLS (`http.tls_cert_file/key_file`) with Authenticated Origin Pulls (`http.client_ca_file`).
  Origin bypass prevention: firewall allowing only Cloudflare ranges (nftables and UFW examples in
  `docs/CLOUDFLARE.md`).
- Single writer; no NFS/SMB/S3-mounted database; one instance per data directory (flock).
- Graceful shutdown: readiness false → HTTP `Shutdown` (15 s) → stop schedulers, consumers and relay → close the
  store (WAL checkpoint) → release the lock.
- Container: distroless static nonroot (uid 65532), data volume `/var/lib/deed` (0700), `HEALTHCHECK` via
  `deed healthcheck`. systemd unit with `LoadCredential=` for the KEK and hardening options.
