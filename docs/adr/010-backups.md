# ADR-010: Encrypted backups and restore

## Backup
1. `VACUUM INTO` a temp file in `<data>/backups/tmp` (consistent snapshot, WAL-safe).
2. `PRAGMA integrity_check` on the copy.
3. zstd compression (klauspost/compress).
4. **age** encryption to configured X25519 recipients (filippo.io/age). Identities are held offline.
5. Upload with minio-go (multipart for large objects, SSE-S3 requested, TLS required in production).
6. Upload the KEK encrypted to the same recipients (`<id>.kek.age`) unless `exclude_kek`.
7. Upload a **manifest** last (`<id>.manifest.json`, completeness marker): format `deed-backup/v1`, backup ID, schema
   version, app version, created_at, recipient fingerprints (SHA-256 of recipient strings), plaintext and ciphertext
   SHA-256 and sizes, KEK object presence, MAC over the manifest with the `backup-meta` key.
- Retention: GFS — keep 7 daily, 4 weekly, 6 monthly complete backups (manifest present); delete others and orphaned
  objects without a manifest older than 24 h; abort incomplete multipart uploads older than 24 h.
- Metrics: `deed_backup_last_success_timestamp_seconds`, `deed_backup_duration_seconds`, `deed_backup_size_bytes`,
  `deed_backup_runs_total{result}`.

## Restore (`deed restore`, offline)
Fetch manifest → verify format and schema compatibility → fetch object → verify ciphertext SHA-256 → decrypt with
the age identity → decompress → verify plaintext SHA-256 → `integrity_check` → check schema version ≤ binary →
write to `<data>/deed.db.restore` → atomic rename over `deed.db` keeping the previous file as `deed.db.pre-restore`
→ optionally restore the KEK file → post-restore validation (open, migrate, count tables, verify the audit chain).
Any failure leaves the existing database untouched. `deed backup verify` runs every step into a temp directory.

## Key rotation
Add a recipient to config; new backups encrypt to all recipients. Old identities stay valid for historical backups;
the manifest records recipient fingerprints so the operator knows which identity opens which backup.
