# Security policy

Deed is under active development and has not been externally security reviewed. Do not rely on it for anything
important yet.

## Reporting a vulnerability
Please report privately via GitHub Security Advisories on `aurimasniekis/go-deed` ("Report a vulnerability").
Do not open public issues for security problems. You will get an acknowledgement within 7 days.

## Scope
In scope: authentication (local accounts, TOTP, OIDC), sessions and CSRF, API and source tokens, project isolation and
bridges, authorization in graph queries, search and live streams, the ingest protocol, Terraform state handling
(secrets must never be stored), outbound requests from connectors and telemetry (SSRF), custom icon sanitization,
key management, backups and restore, and audit integrity.

The threat model, its assumptions and its explicit non-goals are in [docs/THREAT_MODEL.md](docs/THREAT_MODEL.md).
The audit hash chain is tamper-evident against casual edits, not tamper-proof against an attacker with write access to
the database and the keyring.

## Supported versions
No version has been released yet. Once releases exist, the latest minor release receives security fixes.
