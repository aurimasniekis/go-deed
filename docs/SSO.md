# Single sign-on (OIDC)

Deed is an OpenID Connect relying party (ADR-005, `internal/oidc`). Any OpenID Connect Core 1.0 provider that
supports the authorization code flow with PKCE S256 works; Porch is the primary documented provider. SSO and local
accounts can run side by side, and a host-side break-glass path always exists.

## Configuration
Providers are a YAML list under `auth.oidc`. Lists of objects cannot be set through `DEED_*` environment variables.

| Key | Default | Meaning |
|---|---|---|
| `slug` | — | lowercase slug (`^[a-z0-9][a-z0-9-]{0,62}$`), unique; used in `/auth/oidc/{slug}/…` |
| `display_name` | slug | label on the login page |
| `issuer` | — | absolute `https://` URL; must equal the `issuer` in the provider's discovery document. `http://` only for a loopback host in development mode |
| `client_id` | — | required |
| `client_secret_file` | — | file holding the client secret (trimmed). Preferred |
| `client_secret` | — | inline secret; **refused in production mode** |
| `scopes` | `openid profile email groups` | `openid` is always added if missing |
| `groups_claim` | `porch.groups` | dotted JSON path to the user's groups |
| `tenant_claim` | — | dotted JSON path to a tenant identifier (Porch: `porch.tenant.slug`) |
| `required_tenant` | — | reject logins whose `tenant_claim` value differs; needs `tenant_claim` |
| `require_email_verified` | `false` | reject logins without `email_verified: true` |
| `allowed_amr` | — | if set, the `amr` claim must contain at least one of these values |
| `jit` | `false` | create a user on first login when at least one mapping matches |
| `mappings` | — | group → role mappings (below) |

Each mapping names a `group` and grants an `installation_role` (`owner`, `admin`, `auditor`), a project role
(`project` slug plus `project_role`: `viewer`, `editor`, `admin`), or both. `project` and `project_role` must be set
together, and a mapping that grants nothing is a configuration error. `deed config check` validates all of this and
prints the effective configuration with `client_secret` redacted.

Register this redirect URI with the provider:

```
<public_url>/auth/oidc/<slug>/callback
```

Deed reaches the provider (discovery, token, userinfo, JWKS) through the SSRF-safe client with a 20 s timeout. If
the issuer resolves to a private address, list its range in `outbound.allow_cidrs`; add a private CA with
`outbound.ca_file`.

## Porch example
Create a confidential OIDC application in Porch with the redirect URI above and enable group disclosure (Porch emits
`porch.groups` only when the `groups` scope is requested and disclosure is on).

```yaml
public_url: https://deed.example.com
outbound:
  allow_cidrs: ["10.0.20.0/24"]          # only if id.example.com resolves to a private address
auth:
  local: {enabled: true}                 # keep local sign-in until SSO works, then decide
  oidc:
    - slug: porch
      display_name: Porch
      issuer: https://id.example.com      # exactly as published by Porch's discovery document
      client_id: deed
      client_secret_file: /etc/deed/oidc-porch-secret
      groups_claim: porch.groups          # the default; shown for clarity
      tenant_claim: porch.tenant.slug
      required_tenant: home
      require_email_verified: true
      jit: true
      mappings:
        - {group: deed-owners, installation_role: owner}
        - {group: sre, installation_role: admin}
        - {group: security, installation_role: auditor}
        - {group: platform, project: infra, project_role: editor}
        - {group: developers, project: infra, project_role: viewer}
```

## Flow and protections
`GET /auth/oidc/{slug}/start?return_to=/inventory` begins a sign-in (these browser routes are outside `/api/v1`;
`GET /api/v1/system/info` lists the providers as `sso_providers` and reports `local_auth`).

- **State:** a random `state` whose keyed MAC is the primary key of a server-side row; the row holds the nonce, the
  PKCE verifier and a binding value, sealed with the keyring `field` key. It expires after 10 minutes and is
  deleted on first use.
- **Browser binding:** the binding value is set in a `__Host-deed_oidc` cookie (HttpOnly, Secure, SameSite=Lax;
  `deed_oidc` on an `http://` development origin) and must match on callback. The cookie is cleared either way.
- **PKCE S256** and **nonce** on every request; the nonce in the ID token is compared in constant time.
- **RFC 9207:** if the callback carries `iss`, it must equal the configured issuer; if the provider advertises
  `authorization_response_iss_parameter_supported`, `iss` must be present.
- **ID token:** signature, issuer, audience and expiry are checked by coreos/go-oidc. Userinfo claims fill in claims
  the ID token lacks (for example groups) only when the userinfo `sub` matches; ID token values win.
- **Discovery** is cached for one hour; during a provider outage the last good metadata is reused.
- `return_to` must be a same-origin absolute path (`/…`); anything else becomes `/`.

On failure the browser is redirected to `/login?error=<code>` (for example `validation_failed`, `forbidden`,
`unauthorized`, `unavailable`). Provider error bodies are never shown.

## Identities and JIT
Identities are matched on `(iss, sub)`, never on email. On first login of an unknown identity:

- with `jit: false` (default), or when no mapping matches, the login fails with "this identity is not provisioned in
  Deed; ask an administrator";
- with `jit: true` and at least one matching mapping, Deed creates the user (audited as `user.provisioned`). The
  email is stored only if it is verified **and** no other user has it: an existing account is never taken over or
  merged by email.

Disabled users are rejected. Signed-in users can list their identities (`GET /api/v1/me/identities`), link another
one (`/auth/oidc/{slug}/start?purpose=link`, from a browser session) and unlink one
(`DELETE /api/v1/me/identities/{id}`, recent sign-in required; refused when it is the last way to sign in).

## Groups and role mapping
`groups_claim` is a dotted path. A literal top-level key containing dots is tried first, then the path is walked
through nested objects. The value may be a string, an array of strings, or an array of objects, in which case the
first of `slug`, `name`, `id` is used. At most 256 distinct groups of up to 256 bytes are kept.

Mappings are re-evaluated on every login (only when the provider has mappings):
- the highest matching installation role wins (`owner` > `admin` > `auditor`); for each project, the highest
  matching project role (`admin` > `editor` > `viewer`);
- roles granted by a provider are recorded with source `oidc:<slug>`. A role from that source is lowered or removed
  when the groups no longer grant it; a manually granted role is raised by a mapping but never lowered;
- the last active owner is never demoted;
- a mapping naming a project that does not exist is skipped with a warning in the log.

Changes are audited as `user.role_synced`.

## Policy checks
After token validation, in order: `required_tenant`, `require_email_verified`, `allowed_amr`. A failing check
rejects the login ("sign-in was rejected by policy") and records a denied `auth.login`. Deed's own TOTP is not
asked for SSO sessions; to require multi-factor authentication, enforce it at the provider and list the
corresponding `amr` values in `allowed_amr`.

## Re-authentication
Sensitive operations need a sign-in within `auth.session.reauth_window` (default 5 minutes). For an SSO session the
API answers `403 reauth_required` with the action `/auth/oidc/<slug>/start?purpose=reauth`. Deed then sends
`prompt=login` and `max_age=0`, requires the same `(iss, sub)` as the current user, and requires an `auth_time` claim
no older than 10 minutes; a provider that omits `auth_time` cannot complete re-authentication.

## Local accounts and break-glass
- `auth.local.enabled: false` disables password sign-in. Configuration validation refuses it when no OIDC provider
  is configured ("nobody could sign in").
- `deed admin recover -email user@example.com` (server stopped: it needs the data-directory lock and the KEK) prints
  a single-use 15-minute link that signs in as that user without the second factor once. It works even with local
  sign-in disabled, re-enables the user, is audited (`auth.breakglass_link_issued`, `auth.breakglass_used`) and opens
  a critical `breakglass_used` finding. It needs an account email: SSO users provisioned without a verified email
  cannot be recovered this way, so keep at least one owner with an email address.
- `deed admin bootstrap` creates the first owner while none exists.

## Audit and metrics
Successful logins record `auth.login` with the provider and group count. Denials record `auth.login` with outcome
`denied` and a reason code: `provider_error`, `issuer_mismatch`, `token_exchange_failed`, `id_token_invalid`,
`nonce_mismatch`, `tenant_mismatch`, `email_not_verified`, `amr_not_allowed`, `stale_auth_time`, or `forbidden`
(not provisioned or disabled). Denials carry a short hash of `iss|sub`, not the subject. Other actions:
`auth.reauth`, `user.identity_linked`, `user.identity_unlinked`.

`deed_auth_logins_total{method="oidc",outcome}` counts `success`, `failure` and `error` (provider unavailable).

## Validation status
There are no automated tests of the OIDC flow in this checkout (`internal/oidc` has no `_test.go` files), and SSO
against a real Porch instance is **pending**.
