# ADR-003: Layered facts and deterministic reconciliation

## Layers
| Layer | Written by | Example |
|---|---|---|
| `declared` | Terraform provider, API declarations | owner, intended version, links |
| `imported` | Terraform state import, connector snapshots | IPv4 of a VM from `hcloud_server` |
| `discovered` | runtime ingestion, Pulse, agents | a container seen on a host |
| `observation` | telemetry summaries (time-bounded, `expires_at` required) | request rate, last event seen |
| `annotation` / `override` | humans (editor+) | description, override of a wrong imported value |

## Facets
Each `(entity, source)` pair owns one **facet**: `layer`, `generation`, `revision`, `fields` (JSON object of
envelope + spec paths), `observed_at`, `expires_at`, `tombstoned_at`, `tombstone_revision`, `first_claimed_at`.
Facets are never merged destructively: a source can only replace its own facet.

## Effective view
`entities.effective_json` and `effective_provenance_json` are recomputed **inside the same transaction** after any
facet change by a deterministic, field-level merge:

1. Collect live facets (not tombstoned, not expired).
2. Flatten each facet's fields to JSON pointer → value (objects recurse; arrays and scalars are leaves; `labels`,
   `annotations` and `links` merge per key/entry).
3. For each pointer pick the winner by: layer precedence (`override` > `declared` > `imported` > `discovered`;
   per-source policy may reorder declared/imported/discovered) → source priority (policy, lower wins) →
   earliest `first_claimed_at` → source ID.
4. `observation` facets write only under `status.observed`, never into `spec`.
5. When two facets **in the same layer** hold different values for a pointer, a `conflicts` row is upserted (pointer,
   winning and losing source, values' hashes) and an open `conflict` finding is kept in sync; it closes when they
   agree or one disappears.
6. Provenance records, per pointer, `{source, layer, revision, observed_at, facet_id, candidates}`.

Spec validation runs on the **effective** spec against the kind's storage version. Invalid effective spec does not
reject the source write (sources must not block each other); it raises an `invalid_schema` finding. Declarations
through the API/provider, however, are validated before writing and rejected with 422.

## Source protocol
- Incremental `upsert`/`delete` items carry a per-source monotonic `revision` (int64) or `observed_at`. An item whose
  revision ≤ stored facet revision or ≤ the facet's `tombstone_revision` is rejected `stale_revision`. Deletes record
  `tombstone_revision`, so a late upsert cannot resurrect a deleted fact.
- **Snapshots**: `begin(generation, complete)` → batches (`seq`, idempotency key, ≤ 1,000 items, ≤ 4 MiB) →
  `commit`. Items are staged in `snapshot_items`; nothing touches the catalog until commit. Commit is a single
  transaction that applies all staged items and, for `complete=true`, handles facets of this source absent from the
  snapshot according to `on_absent: tombstone|orphan|keep`.
- **Mass-removal guard**: commit refuses when it would tombstone more than `max_removal_percent` (default 25 %) of the
  source's live facets, unless the request sets `allow_mass_removal: true` (audited).
- Abort, timeout (default 1 h) or failure discards staging and leaves the previous generation intact.
- **Dry-run**: the commit path runs inside a transaction that is rolled back and returns the diff (created, updated,
  tombstoned, conflicts, validation errors).
- Generations are monotonic per source; a `begin` with a generation ≤ the last committed one is rejected.

## Deletion and retention
- Deleting a provider resource removes only that source's facet. When no live facet remains the entity is tombstoned
  (`entities.tombstoned_at`). Tombstoned entities are hidden by default, revivable within 30 days, then compacted.
- Relations: `relations(id, type, from, to, project)` with per-source `relation_assertions` (layer, inferred flag,
  evidence JSON, confidence 0–1, first/last seen, expires_at, revision, tombstone). A relation is live while any
  non-tombstoned, non-expired assertion exists.

## Concurrency and idempotency
- One writer serializes all mutations; each facet and kind definition has an integer `version` exposed as an ETag;
  `If-Match` mismatches return 412.
- Idempotency keys on POST (24 h, keyed MAC, request hash) as in Porch; snapshot batches are idempotent by `(snapshot,
  seq)` + key.

## Invariants (property-tested)
1. Applying the same batch twice yields the same state.
2. For any permutation of a set of revisioned items for one facet, the final state equals applying them in revision
   order.
3. A stale revision never changes state; a delete is never undone by an older upsert.
4. An aborted or failed snapshot leaves the catalog byte-identical.
5. A partial (`complete=false`) snapshot never tombstones.
6. The effective view is a pure function of the live facets and policies (recomputing from scratch equals the
   incremental result).
7. Removing a source's facet never changes fields owned by other sources.
