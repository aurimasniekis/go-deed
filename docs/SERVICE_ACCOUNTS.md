# Service accounts

A service account is a non-human user for automation: the Terraform provider, CI jobs, scripts. It has no password,
SSO identity or session and cannot sign in to the UI; it authenticates only with API tokens (`deed_at_…`). Its
**installation role** (`admin`, `auditor` or none) and **project memberships** bound every one of its tokens exactly as
a person's roles bound theirs, so automation does not depend on a person's account or token.

Source tokens (`deed_st_…`, docs/INGEST_API.md) are different: they can only push data into one source.

## Getting the first token

Pick one:

- **UI:** *Settings → Users → Service accounts → New service account*, then *Create token* (shown once). Needs an
  installation owner or admin with a recent sign-in.
- **Host command** (no person's token involved; the server must be stopped because the data-directory lock is
  exclusive, like `deed admin recover`):

  ```sh
  deed admin token -config /etc/deed/deed.yaml -service-account terraform -create -role admin -days 366 > token.txt
  ```

  Only the token is written to stdout, so it can be piped straight into a secret manager; the summary (token ID, role,
  expiry) goes to stderr. Without `-create` the service account must exist. `-role` is the token's installation role
  (`admin`, `auditor` or `none`) and, with `-create`, the new account's role; a token never exceeds its account's role.
  The action is audited as `service_account.created` and `token.created` with the CLI as actor.

## Project roles

A service account without an installation role sees only the projects it is a member of. Grant roles like for people:
on the project's page (*Settings → Projects → project → Members*, where service accounts are listed as
"name (service account)"), with `PUT /api/v1/projects/{project}/members/{user}` or the provider's
`deed_project_member` resource.

## API

| Method and path | Who | |
|---|---|---|
| `GET /api/v1/service-accounts` | installation owner, admin, auditor | list |
| `POST /api/v1/service-accounts` | owner, admin | `{name, display_name?, description?, installation_role?}` |
| `GET /api/v1/service-accounts/{id or name}` | owner, admin, auditor | |
| `PATCH /api/v1/service-accounts/{id or name}` | owner, admin | `display_name`, `description`, `status` (`disabled` revokes every token), `installation_role` |
| `DELETE /api/v1/service-accounts/{id or name}` | owner, admin | removes the account, its tokens and memberships |
| `GET /api/v1/service-accounts/{id or name}/tokens` | owner, admin, auditor | |
| `POST /api/v1/service-accounts/{id or name}/tokens` | owner, admin | `{name, expires_in_days?, installation_role?, project_role?, projects?}`; the token is returned once |
| `DELETE /api/v1/service-accounts/{id or name}/tokens/{token}` | owner, admin | revoke |

Names are 1–63 characters of lowercase letters, digits, `-` and `_`. A service account cannot be an owner. When
`installation_role` is omitted on token creation the token inherits the account's role; `""` creates a token limited to
project memberships. `project_role` and `projects` narrow a token further, as for personal tokens.

Managing service accounts and their tokens works from a session (with a recent sign-in) **or with a token that has the
installation `admin` role**, so automation can rotate its own credential: create the new token, store it, then revoke
the old one. `GET /api/v1/me` with a service account token returns the account.

## Terraform

The provider manages service accounts and their tokens with `deed_service_account` and `deed_service_account_token`
(the token value is a sensitive computed attribute and therefore in state; keep state encrypted). A typical layout:
bootstrap one `terraform` service account with the host command, store its token in the secret manager, configure the
provider with it, and let Terraform manage everything else, including further service accounts.

## Lifetime and rotation

Tokens expire after `expires_in_days` (default 90, at most `auth.max_token_lifetime`, 366 days by default). Disabling a
service account revokes all its tokens; deleting it removes them. Every token records `last_used_at`, shown in the UI.
