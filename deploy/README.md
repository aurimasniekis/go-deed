# Deployment assets (see docs/DEPLOYMENT.md)

Deed is a single process with a SQLite database: **one instance per data directory**, no replicas, no network
filesystems (NFS/SMB/S3 mounts are refused). Scale up, not out. The recommended setup is a small VPS behind
Cloudflare (docs/CLOUDFLARE.md) with encrypted backups to S3-compatible storage (docs/BACKUP.md).

| Option | Files |
|---|---|
| Container (Docker/Podman Compose) | `Dockerfile`, `deploy/compose.yaml`, `deploy/deed.container.yaml` |
| Binary + systemd | `deploy/systemd/deed.service`, `deploy/systemd/README.md` |
| Development / validation stack | `deploy/compose.dev.yaml` (NATS, MinIO, Prometheus, Loki, Grafana; `--profile app` adds Deed) |

## Minimal production configuration
```yaml
mode: production
data_dir: /var/lib/deed
public_url: https://deed.example.com
kek: {source: file, file: /etc/deed/kek}       # `deed keys init -out /etc/deed/kek`; never inside data_dir
http: {listen: 127.0.0.1:8080}                 # behind a local TLS proxy or cloudflared, or terminate TLS (CLOUDFLARE.md)
management: {listen: 127.0.0.1:9464}           # /metrics, /healthz, /readyz — keep private
proxy: {cloudflare: true}
auth:
  local: {enabled: true, mfa_required_roles: [owner, admin]}
  oidc: []                                      # docs/SSO.md (Porch is the primary documented provider)
backup:
  enabled: true
  recipients: ["age1…"]
  s3: {endpoint: s3.example.com, bucket: deed-backups, prefix: deed/, access_key_id: …, secret_access_key_file: /etc/deed/s3-secret}
```
Validate with `deed config check -config /etc/deed/deed.yaml`: unknown keys and unknown `DEED_*` variables are errors,
and the effective configuration is printed with secrets redacted.

## First start
1. `deed keys init -out /etc/deed/kek` (mode 0400, owned by the service user). Keep an offline copy, or rely on the
   KEK escrow in encrypted backups.
2. Start the service. Migrations run automatically after a pre-migration snapshot.
3. `deed admin bootstrap` prints a single-use setup link (30 minutes) that creates the first owner, who must enroll
   TOTP. Recovery: `deed admin recover -email …` (break-glass, audited, raises a critical finding).

## Upgrades
Replace the binary or image and restart. Deed refuses to start on a database whose schema is newer than the binary;
roll back by restoring the pre-migration snapshot (`data_dir/backups/pre-migrate-*.db`) or a backup. `/api/v1` changes are additive
(docs/VERSIONING.md), so the Terraform provider and agents keep working across upgrades.

## Monitoring
Scrape `management.listen` `/metrics`. `deploy/monitoring/` holds a Grafana dashboard and Prometheus alert rules
built from the exported metrics (`check.sh` runs `promtool test rules`). `deed healthcheck` probes `/readyz` for
container health checks.

## Resource notes
See docs/PERFORMANCE.md for measured latency and memory at 10k entities / 50k relations.
