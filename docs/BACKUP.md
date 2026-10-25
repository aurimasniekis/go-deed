# Backups and restore (ADR-010)

Deed writes encrypted backups of its SQLite database (and, by default, its KEK) to S3-compatible storage. Deed can
**write** backups but cannot **read** them: they are encrypted with [age](https://age-encryption.org) to X25519
recipients whose identities you keep offline.

## What a backup contains
`backups/<bkp_id>/` under `backup.s3.prefix`:

| Object | Content |
|---|---|
| `deed.db.zst.age` | `VACUUM INTO` snapshot that passed `PRAGMA integrity_check`, zstd-compressed, age-encrypted |
| `kek.age` | the KEK, age-encrypted to the same recipients (omitted with `backup.exclude_kek: true`) |
| `manifest.json` | written **last** (the completeness marker): format, backup ID, creation time, app and schema version, installation ID, KEK ID, recipient fingerprints, row counts, and plaintext/ciphertext SHA-256 and sizes of every object — signed with Ed25519 |

The manifest holds no secrets. It is signed with a key derived from the keyring (`backup-meta` purpose) because age
recipients are public: without the signature, anyone able to write to the bucket could plant a forged
"installation" encrypted to your recipients. Restores require the **verification key**
(`deed-backup-ed25519:…`, from `deed backup verify-key` or `GET /api/v1/backups`).

Uploads stream (multipart, 16 MiB parts) and request SSE-S3 (`backup.s3.sse`, default on) as defense in depth.
Interrupted multipart uploads are aborted (a failed upload right away, anything left over under `backups/` by the
next successful backup); backups without a manifest are deleted after 24 hours.

## Setting up
1. Generate an identity on a trusted machine and keep it offline (password manager, hardware token, printed copy):
   ```sh
   deed backup keygen -out deed-backup-identity.txt   # prints the recipient age1…
   ```
   Several recipients (e.g. two operators, or a new key during rotation) can be listed; any one identity decrypts.
2. Configure:
   ```yaml
   backup:
     enabled: true
     interval: 6h
     keep_daily: 7
     keep_weekly: 4
     keep_monthly: 6
     recipients: ["age1…"]
     s3:
       endpoint: s3.example.com        # host[:port]
       bucket: deed-backups
       prefix: deed/
       region: eu-central-1
       access_key_id: …
       secret_access_key_file: /run/credentials/deed/s3-secret
   ```
   Give the S3 credential **write-only plus list/get** on the prefix where possible, and enable object lock /
   versioning on the bucket so a compromised server cannot erase history (pruning then fails loudly instead).
3. Record the verification key next to the identity:
   ```sh
   curl -s -H "Authorization: Bearer $DEED_TOKEN" https://deed.example.com/api/v1/backups | jq -r .verify_key
   ```
   Rotating the `backup-meta` keyring purpose changes it; keep old keys for old backups.

## S3 implementations
Backups need: HEAD bucket, single and multipart PUT, GET, ListObjectsV2 with continuation, DELETE object, and
ListMultipartUploads / AbortMultipartUpload. `region` may stay empty: the client then asks the bucket for its region
(GetBucketLocation). If you set it, it must be the region the server signs with.

| Implementation | Validated | Settings and behaviour |
|---|---|---|
| MinIO | CI service container, local binary (`TestMinIO`), `scripts/e2e.sh` | SSE-S3 needs a KMS configured in MinIO; the e2e run uses `sse: false` |
| SeaweedFS `4.47` | `scripts/validate/s3.sh` | SSE-S3 is applied only when SeaweedFS has an SSE-S3 key (`s3.sse.kek`); without one it answers `InternalError` to every SSE-S3 upload: set `sse: false`. Ignores the signature region |
| Garage `v2.4.1` | `scripts/validate/s3.sh` | Signs with its own `s3_region` (default `garage`): leave `region` empty or set it to that value; any other region is refused. Accepts the SSE-S3 header but **does not encrypt** (no `x-amz-server-side-encryption` on the object) |
| Versity S3 Gateway `v1.8.0` (POSIX) | `scripts/validate/s3.sh` | Checks the region (`us-east-1` unless started with `--region`). Accepts the SSE-S3 header but does not encrypt |
| AWS S3, Cloudflare R2, Backblaze B2 | not validated | |

All of them work with `lookup: path` and, given DNS for `<bucket>.<endpoint>`, `lookup: virtual` (the server needs
its virtual-host domain configured: SeaweedFS `-s3.domainName`, Garage `root_domain`, Versity `--virtual-domain`).
Backups are age-encrypted before upload, so SSE-S3 is defense in depth only; where the server ignores it nothing is
lost, but do not count on it.

## Operating
- Scheduled backups run when the last successful one is older than `backup.interval` (failures retry after 15
  minutes). `POST /api/v1/backups` (owner/admin) or `deed backup run` (server stopped) take one now.
- Retention is grandfather-father-son over this installation's **verified** backups: the newest backup, plus the
  newest of each of the last `keep_daily` days, `keep_weekly` ISO weeks and `keep_monthly` months. Manifests this
  installation did not sign are ignored (never counted, never deleted).
- Metrics: `deed_backup_runs_total{result}`, `deed_backup_last_success_timestamp_seconds`,
  `deed_backup_duration_seconds`, `deed_backup_size_bytes`. A `backup_stale` finding opens when no backup succeeded
  for two intervals.
- `deed backup list -verify-key …` shows stored backups; `deed backup verify -id latest -identity … -verify-key …`
  downloads, decrypts and checks a backup (hashes, sizes, integrity check, row counts) **without** restoring — run
  it regularly from the machine that holds the identity.

## Restore runbook
Restore is offline. You need: the age identity, the verification key, the S3 settings (a `deed.yaml`), and either
the backup's escrowed KEK (default) or the original KEK.

1. Stop Deed (`systemctl stop deed`). Restore refuses while the data-directory lock is held.
2. Verify first:
   ```sh
   deed backup verify -config deed.yaml -identity id.txt -verify-key deed-backup-ed25519:… -id latest
   ```
3. Restore:
   ```sh
   deed restore -config deed.yaml -identity id.txt -verify-key deed-backup-ed25519:… -id latest \
     -kek-out /etc/deed/kek -yes          # omit -kek-out to keep the configured KEK (it must match the backup's KEK ID)
   ```
   Restore downloads and checks every object (signature, ciphertext and plaintext SHA-256 and size), runs
   `PRAGMA integrity_check` and compares row counts, refuses a backup whose schema is newer than the binary, keeps the
   current database as `deed.db.pre-restore-<time>`, swaps the restored one in atomically, **revokes every session**
   (and with `-revoke-tokens` every API/source token) and verifies the audit hash chain.
4. Point `kek.file` at the restored KEK if you used `-kek-out`, then start Deed. The first start records
   `backup.restored` in the audit log and opens a `backup_restored` finding.
5. Afterwards: everything after the backup's creation time is gone. Re-run pull sources, ask push sources to send
   full snapshots, and review API tokens created or revoked since the backup.

Failure modes and what they mean:

| Message | Cause |
|---|---|
| `cannot decrypt (wrong identity or damaged object)` | the identity does not match any recipient |
| `… was modified or truncated in storage` | ciphertext hash or size differs from the manifest |
| `manifest signature does not verify` | wrong verification key, or a planted manifest |
| `schema version N but this binary supports up to M` | restore with a newer `deed` |
| `holds a different KEK` | `-kek-out` points at an existing, different KEK file |
| `restore needs the server stopped` | the data directory is locked by a running server |

Backup runs fail with these S3 messages (in `backup_runs.error`, the log and `deed backup run`):

| Message | Cause |
|---|---|
| `… (SSE-S3 was requested and not every S3 implementation supports it …)` | the server rejected an SSE-S3 upload (SeaweedFS without a key answers `InternalError`): set `backup.s3.sse: false` |
| `… (check backup.s3.region …)` | the server signs with another region (Garage: `unexpected scope …, expected: '…/garage/…'`) |
| `bucket … does not exist` | wrong `bucket`, or a credential that cannot see it |
| `Access Denied` / `The request signature we calculated does not match …` | wrong access key or secret |

## Validation status
Covered by `internal/backup` tests: round trip, wrong identity, foreign signature, corrupted object, S3 outage,
interrupted upload, newer schema, lock held, KEK mismatch, GFS retention and planted manifests — in memory and
against a real MinIO server (when the `minio` binary is installed). A full CLI backup/verify/restore against local
MinIO was run during development.

`scripts/validate/s3.sh` runs SeaweedFS (with and without an SSE-S3 key), Garage and Versity S3 Gateway in Docker
and, for each, `TestMinIO`, `TestS3Backend` and `TestS3Compat` (bucket check, single and multipart puts, SSE-S3,
not-found mapping, ListObjectsV2 paging over 1100 keys, deletes of present and missing keys, aborting incomplete
multipart uploads under the prefix only, path and virtual-host addressing, wrong region and wrong secret) through
`DEED_TEST_S3=http://key:secret@host:port[?region=…&sse=on|off&vhost=…]`, then the CLI end to end: a wrong region,
the first backup with SSE-S3 (or its rejection), an interrupted multipart upload, an incomplete backup and 1001 keys
sorted ahead of the backups, two more backups with `keep_daily: 1` (older backups pruned, the upload aborted, the
fresh incomplete backup kept), `deed backup list`, `deed backup verify` and `deed restore` into a fresh data
directory. Backups to AWS S3, R2 or B2 are **not yet validated**.
