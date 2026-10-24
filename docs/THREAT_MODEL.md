# Threat model

Scope: a single Deed installation on a VPS behind Cloudflare, its SQLite data directory, its outbound connectors,
the Terraform provider and the encrypted backups. Method: STRIDE per asset and trust boundary.

## Assets
| Asset | Sensitivity | Where |
|---|---|---|
| Catalog (entities, relations, provenance) | Medium — reveals topology | SQLite |
| Integration credentials (S3 state reader, GitHub, HallPass, Porch, Pulse PAT/creds) | High | sealed with keyring data keys |
| KEK | Critical | file 0400 outside the data dir / systemd credential; escrowed in backups encrypted to age recipients |
| User passwords, TOTP secrets, recovery codes | High | Argon2id hashes / sealed / MACs |
| Session, API and source tokens | High | keyed MACs only |
| Terraform state (transient) | Critical | in memory during import only; never persisted |
| Audit log | Medium | SQLite hash chain + optional ECS file sink |
| Backups | High | S3, age-encrypted |
| age identities | Critical | offline, owner custody |

## Trust boundaries
1. Internet → Cloudflare → origin listener. 2. Origin → management listener (loopback only). 3. Deed → outbound
integrations (S3, Pulse, GitHub, HallPass, Porch, telemetry backends). 4. Deed → OIDC provider. 5. Terraform/CI →
API (bearer tokens). 6. Pulse publishers → Deed ingest consumer. 7. Operator host shell → CLI (data-dir lock + KEK).

## Threats and mitigations
| # | Threat (STRIDE) | Mitigation | Residual |
|---|---|---|---|
| T1 | Spoofed client IP via forged `X-Forwarded-For`/`CF-Connecting-IP` (S) | Trust only configured proxy CIDRs; CF header only from Cloudflare ranges in CF mode; tests with forged headers | Misconfigured trusted CIDRs |
| T2 | Origin bypass of Cloudflare WAF (S/D) | Firewall to CF ranges; optional Authenticated Origin Pulls | Operator must apply firewall |
| T3 | Credential stuffing on local login (S) | Argon2id, per-account + per-/64 throttle, TOTP mandatory for admins, audit | Weak passwords for non-admin users (min 12 bytes + zxcvbn-style check) |
| T4 | Session theft / fixation (S) | `__Host-` cookie, HttpOnly, Secure, SameSite=Lax, rotation on login/privilege change, idle+absolute expiry, MAC storage | XSS (mitigated by CSP nonce, no untrusted HTML) |
| T5 | CSRF (T) | Origin/Sec-Fetch-Site + HMAC header | — |
| T6 | OIDC mix-up / code injection / replay (S) | PKCE S256, state bound to cookie, nonce, iss/aud/sig, RFC 9207 iss check, `(iss, sub)` matching | IdP compromise |
| T7 | Privilege escalation via group claims (E) | Explicit group→role mappings; JIT off; roles re-evaluated each login | Misconfigured mapping |
| T8 | Token leakage in logs (I) | Redaction by key, `redact.Secret`, tokens never in URLs, provider client never logs bodies | — |
| T9 | Terraform state secrets persisted or leaked (I) | Allowlist extraction, sensitive paths dropped, secret heuristics, no raw storage, no values in errors; leak tests over fixtures | New provider attribute shapes (allowlist is default-deny) |
| T10 | SSRF via connector URLs (I/E) | `safehttp`: allowlist, dial-time IP checks, metadata always blocked, redirect re-validation | Allowed private CIDRs reachable by admins by design |
| T11 | Malicious custom kind schema (D) | Size/depth/property limits, RE2 regexes, no remote refs, validation timeouts | — |
| T12 | Malicious SVG upload (T/I) | Allowlist sanitizer, sandbox CSP, `<img>` only, size cap | — |
| T13 | Stored XSS via descriptions/links (T) | No HTML rendering; markdown → React elements; URL scheme allowlist | — |
| T14 | Ingest flooding (D) | Batch limits, per-source rate limits, writer-queue backpressure (429), bounded staging | — |
| T15 | Stale/replayed ingest (T) | Revisions, tombstone revisions, idempotency keys, generations | — |
| T16 | Unauthorized graph traversal/SSE (I) | Project filtering in SQL, redacted stubs, per-subscriber event filtering | Installation admins see all by design |
| T17 | Audit tampering (R) | Hash chain detects edits that don't rewrite the chain; ECS file sink to external storage; `deed audit verify` | DB-level attacker can rewrite the whole chain — tamper-evident only, **not tamper-proof** |
| T18 | Backup theft (I) | age encryption, keys offline, SSE-S3 defense in depth, least-privilege bucket policy | Owner loses identity → backups unrecoverable |
| T19 | Backup tampering (T) | SHA-256 in manifest, manifest MAC, integrity_check before restore | Attacker with S3 write + backup-meta key |
| T20 | KEK loss (D) | KEK escrowed in backups encrypted to age recipients | — |
| T21 | Pulse outage causing memory growth (D) | Outbox on disk, bounded backlog, relay never blocks commits | — |
| T22 | Supply chain (T) | Pinned deps, govulncheck, gitleaks, Trivy, SBOM, SHA-pinned actions, distroless | — |
| T23 | Provider writes overwriting runtime data (T) | Provider writes only its declared facet; status computed-only | — |
| T24 | Metrics listener exposure (I) | Loopback by default; pprof refused off-loopback in production | — |
