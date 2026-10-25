# Built-in API connectors

Deed's in-process source providers (ADR-013) read documented APIs with read-only credentials through the SSRF-safe
HTTP client (per-source host allowlist, dial-time IP checks, no redirects) and write through the same snapshot
protocol as every other source. A sync that cannot read everything (an endpoint failed, a cap or the rate budget was
reached) is committed as **partial**: new and changed facts are applied, nothing absent is removed.

Credentials are stored sealed (keyring purpose `field`) and are never returned by the API, logged or put in run
diagnostics. Every connector exposes **Test** (connectivity, writes nothing) and **Preview** (dry-run diff).

| Connector | Kinds | Relationships | Default schedule |
|---|---|---|---|
| `github` | repository, release, deployment, pull_request, issue | contains, deployed_from, references, tracks | 15 min |
| `hallpass` | service (instance), hallpass_agent, agent_box, hallpass_server, hallpass_role, hallpass_grant, agent_session | protected_by, has_access_to, contains, runs_on, references, connects_to | 5 min |
| `porch` | service (instance), porch_app, porch_service_account | authenticates_with, protected_by | 15 min |

Validation status: all three are tested against contract fixtures shaped by each API's published description
(`internal/connectors/testdata`). Validation against live GitHub, HallPass and Porch instances is **pending**.

## github
**Credential:** a fine-grained personal access token (or GitHub App installation token) limited to the repositories
you import, with *read-only* permissions: Metadata, Contents, Deployments, Pull requests, Issues. A token is optional
for public repositories (60 requests/hour).

**Config:**
```json
{
  "owners": ["example-org", "@me"],
  "repositories": ["example-org/infra"],
  "topics": ["infra"],
  "releases": 10,
  "closed_retention_days": 14
}
```
- `owners` lists organizations or users; `@me` is the token owner's own repositories (including private ones).
  Forks, archived repositories and repositories without one of `topics` are skipped unless enabled.
- `api_url` points at GitHub Enterprise Server (`https://ghe.example.com/api/v3`); add `allow_cidrs` when it resolves
  to a private address.
- Releases: the latest `releases` non-draft releases. Deployments: the latest deployment per environment with its
  latest status as health (`failure`/`error` → failed). A deployment whose ref equals a release tag is also
  `deployed_from` that release.
- Pull requests and issues: every open item (up to `max_items_per_repo`) plus items closed within
  `closed_retention_days`. Closed items carry an expiry, so they leave the catalog even if the source stops syncing.
- **Linking to the catalog:** write `deed:<kind>/<name>` (or `deed:ent_<id>`) in a pull request or issue body, or use
  it as a label. Each reference becomes a `tracks` relationship; references to entities that do not exist yet are kept
  as unresolved references and resolve when the entity appears.
- Rate limits: the connector stops early (partial run) when `X-RateLimit-Remaining` drops below
  `min_rate_remaining` (default 100).
- The default policy allows a snapshot to remove up to 90% of the source's facts, because pull requests and issues
  churn; tighten `policy.max_removal_percent` for repository-only sources.

## hallpass
**Credential:** a HallPass API token (`hallpass_at_…`) of a read-only role (auditor). A token carries one of its
creator's own roles, so an administrator who mints it must also hold the auditor role. Server addresses are returned
only when the role may read them (`inventory.address.read`; for auditors, HallPass's `rbac.auditor_address_read`);
without it, the connector test warns `address_redacted` and catalog servers are not matched by address.

**Config:** `{"url": "https://hallpass.example.com"}` plus optional `grants`, `sessions`, `agent_host_label`
(default `host`), `match_servers`, `max_grants`, `max_sessions`, `include_disabled`.

Reads `GET /api/v1/system/status`, `/agents`, `/servers`, `/servers/{id}/roles`, `/grants?state=active` and
`/access-sessions?state=open|closing` (cursor-paged). It models:
- the HallPass instance as a `service` whose health reflects storage, lockdown and unacknowledged break-glass use;
- agents (`hallpass_agent`) and, for agents with a `host` label, an `agent_box` the agent `runs_on`; a box whose
  hostname equals a catalog server's `spec.hostname` is linked to it (inferred);
- servers (`hallpass_server`, `protected_by` the instance) and their login roles (`hallpass_role`); a catalog
  server/VM/container whose `spec.ipv4` or `spec.hostname` equals the HallPass server address is `protected_by` it
  (inferred, with the matched value as evidence);
- each active grant as `hallpass_grant` (expiring with the grant) plus `agent has_access_to server`; an agent often
  holds several grants on one server (one per role), so that edge is emitted once, with every grant ID in its
  `grant_ids` evidence, and expires with the latest of them;
- open and closing access sessions as `agent_session` that `connects_to` the server. HallPass opens one access
  session per grant when the grant activates and closes it once the grant has ended and its channels are closed, so
  an open session means an active grant, not necessarily a live connection; the list carries no channel counts, so
  none are imported.

Deleted agents, servers and roles are never listed; disabled ones are skipped unless `include_disabled`. Labels that
are invalid Deed label keys or look like secrets are dropped. Grants and sessions are short-lived, so the default
policy allows up to 90% removal per snapshot; whatever HallPass no longer returns is tombstoned by the next sync.

Validated against a real HallPass run locally (development mode, agents requesting grants over the gateway's SSH,
an exec to a throwaway OpenSSH destination): `scripts/validate/hallpass.sh` (`HALLPASS_DIR` points at the HallPass
checkout, default `../hallpass`).

## porch
**Credential:** a Porch automation token (`porch_at_…`). Porch has no read-only permission for applications and
service accounts, so the narrowest working credential is a **tenant** automation token with `app:manage` and
`service_account:manage` (`POST /api/v1/tenants/{t}/automation-tokens`), used with `tenants: ["<that tenant>"]`; one
source per tenant. An installation `auditor` token is not enough: it lists tenants, but Porch answers 404 for every
read inside a tenant (Test then names the permission needed). An installation `admin` token reads every tenant but
can also change everything; avoid it. Deed only ever sends GET requests. Client secrets are never requested; secret
hints are not stored.

**Config:** `{"url": "https://id.example.com", "tenants": ["home"]}`. With `tenants` empty, every tenant the token
can list is imported (disabled tenants are skipped unless `include_disabled`); listing tenants needs an installation
token. With `tenants` set, `GET /tenants/{slug}` is tried for the tenant's status; a tenant token without
`tenant:manage` gets 404 there, and the tenant is then read as active.

Reads `GET /api/v1/tenants` (cursor-paged) or `/tenants/{slug}`, `/tenants/{slug}/applications` and
`/tenants/{slug}/service-accounts`. Applications become `porch_app` entities (OIDC client ID, client type, redirect
URIs, grant types, PKCE, signing algorithm, forward-auth hosts), service accounts become `porch_service_account`
entities; both `authenticates_with` the Porch instance `service`. With `match_hosts` (default on), a catalog DNS
record or domain whose `spec.fqdn` equals:
- a forward-auth application's protected host is `protected_by` that application;
- the host of an OIDC redirect URI or the launch URL `authenticates_with` that application.
Loopback redirect hosts are ignored. These links are inferred and carry the matched host as evidence.

**Validated** against a real Porch built from source (development mode, loopback) by `scripts/validate/porch.sh`:
a tenant token imports that tenant's enabled applications and service accounts with their `authenticates_with`
edges and a `protected_by` link to a matching DNS record; an installation admin token follows `next_cursor` across
107 tenants; an installation auditor token is refused with the permission it lacks.
