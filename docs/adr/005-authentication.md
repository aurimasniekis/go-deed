# ADR-005: Authentication

## Local accounts (as HallPass/Pulse)
- Argon2id (m=64 MiB, t=3, p=2, 16-byte salt, 32-byte key, PHC string, versioned parameters, rehash on login),
  NFKC normalization, 12–1024 bytes; unknown users verify a dummy hash (timing).
- TOTP (RFC 6238, SHA-1, 6 digits, 30 s, ±1 step) with step-reuse prevention; **mandatory** for installation
  `owner`/`admin` (login completes only after enrollment), optional otherwise.
- 10 recovery codes, single use, stored as keyed MACs.
- Throttling per account and per source /64: 3 free failures, then 1 s doubling up to 15 min, decaying after 24 h;
  no permanent lockout.
- `deed admin bootstrap` (only when no owner exists) prints a single-use setup link valid 30 minutes.
  `deed admin recover --email …` is break-glass: it needs the data-directory lock and the KEK, prints a single-use
  15-minute login link that bypasses TOTP once, is audited, and raises a `breakglass_used` alert.
- Local login can be disabled installation-wide (`auth.local.enabled: false`); OIDC policy is never weakened by it.

## OIDC SSO
- Generic providers configured in YAML (`auth.oidc` is the list of providers): issuer, client ID, client secret (file),
  scopes, groups claim JSON path (default `porch.groups`), group→role mappings (installation and project roles),
  `required_tenant` (path `porch.tenant.slug`), `require_email_verified`, `jit: false` by default, `allowed_amr`.
- Authorization code flow with PKCE S256, `state` (server-side row, hashed) + `__Host-deed_oidc` cookie binding,
  `nonce`, signature/issuer/audience/expiry checks via coreos/go-oidc, RFC 9207 `iss` parameter check when present,
  identities matched on `(iss, sub)`. Group mappings re-evaluated on every login; removed groups revoke roles.

## Sessions and CSRF
- `__Host-deed_session` (HttpOnly, Secure, SameSite=Lax, Path=/) in production; `deed_session` over http://localhost
  in development. 256-bit random token; only its keyed MAC is stored. Idle 30 min, absolute 12 h, rotation on login
  and privilege change, revocation list, "sign out everywhere".
- Unsafe methods require Origin/Sec-Fetch-Site same-origin **and** `X-Deed-CSRF` = HMAC(session) (two layers).
- Re-authentication within 5 minutes for sensitive operations (token creation, credential changes, role grants,
  backup and restore operations).

## Tokens
- `deed_at_<43 base62>`: user API tokens (Terraform, CLI). Role ≤ creator, optional project list, expiry ≤ 366 days.
- `deed_st_<43 base62>`: source tokens, ingest into exactly one source.
- Stored as keyed MACs (`keyring` purpose `token`), shown once, last-used tracked, revocable. Bearer tokens are
  never accepted from query strings or cookies.
- No tokens in browser storage.
