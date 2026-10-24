# ADR-013: Pluggable source providers

- Every source has a `provider` type implementing `sources.Provider`:
  `Describe() Descriptor` (config JSON Schema, secret fields, capabilities snapshot/incremental/pull/push, emitted
  kinds and relationship types, support level per kind), `Validate(cfg) error`, `Test(ctx, cfg, secrets)
  Diagnostics` (safe connectivity checks, no data written), `Sync(ctx, cfg, secrets, Emitter) error`.
- The `Emitter` is the same snapshot/incremental reconcile protocol used by the external API, so in-process and
  out-of-process providers behave identically.
- Registry + scheduler (interval, ±10 % jitter, per-provider concurrency, timeout, backoff on failure); run history
  (`source_runs`) with diagnostics and mapping errors; credentials sealed with the keyring.
- Every provider uses `safehttp` with the source's allowlist.
- No arbitrary code execution: in-process providers are compiled in; out-of-process providers are any program using
  the push protocol with a source token. `pkg/deedclient` is an importable Go SDK; `cmd/deed-agent` is a reference
  runner (Docker containers and host facts).
- Built-in providers: `terraform_state` (S3 / HTTP backend / push), `pulse_admin`, `pulse_tail`, `github`
  (repositories, releases, deployments, open + recently closed pull requests and issues), `hallpass` (agents,
  servers, roles, grants, access sessions), `porch` (applications, service accounts), `manual` (API/Terraform
  declarations; the provider target), `push` (generic external source).
- The Sources UI is generic: a form rendered from the provider's JSON Schema, secret fields write-only, test
  connection, preview (dry-run), sync history, mapping errors, policy (precedence, on_absent, removal guard).
