package catalog

import (
	"context"
	"database/sql"
	"errors"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// MergeRulesVersion names the rules of Merge (precedence, what counts as a conflict). Entities only re-merge when
// their facts change, so a release that changes the rules bumps it and every entity is re-merged once
// (RemergeIfRulesChanged): otherwise conflicts recorded under the old rules would stay until each entity changes.
//
//	2 (0.5.0): icons, terraform/* labels and annotations, and values a source policy ranks lower are not conflicts.
//	3 (0.7.0): annotations and overrides alone no longer keep an entity alive.
const MergeRulesVersion = "3"

const mergeRulesKey = "catalog.merge_rules_version"

// RemergeIfRulesChanged re-merges every live entity when the stored merge rules version differs from
// MergeRulesVersion, in batches of short transactions so ingestion keeps going, then records the version. It returns
// the number of entities re-merged (0 when the rules are current). Re-merging an unchanged entity only refreshes its
// conflicts and provenance; it emits no change events.
func (s *Service) RemergeIfRulesChanged(ctx context.Context) (int, error) {
	var stored string
	err := s.Store.R().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, mergeRulesKey).Scan(&stored)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if stored == MergeRulesVersion {
		return 0, nil
	}
	n, after := 0, ""
	for {
		var ids []string
		if err := s.Store.W().TxNamed(ctx, "catalog.remerge", func(tx *store.Tx) error {
			ids, err = stringsQuery(ctx, tx, `SELECT id FROM entities WHERE tombstoned_at IS NULL AND id > ? ORDER BY id LIMIT 200`, after)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if _, err := s.recomputeTx(ctx, tx, id, recomputeOpts{actor: audit.System}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return n, err
		}
		n += len(ids)
		if len(ids) < 200 {
			break
		}
		after = ids[len(ids)-1]
	}
	return n, s.Store.W().TxNamed(ctx, "catalog.remerge_done", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`, mergeRulesKey, MergeRulesVersion, tx.NowMicros())
		return err
	})
}
