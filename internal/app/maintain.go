package app

import (
	"context"
	"time"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// initMaintenance registers storage and catalog housekeeping jobs.
func (a *App) initMaintenance() {
	r := a.Cfg.Retention
	lastExpiry := a.Clock.Now().Add(-r.Tombstones.D())
	a.every("expire_facts", 30*time.Second, func(ctx context.Context) error {
		now := a.Clock.Now()
		err := a.Store.W().TxNamed(ctx, "maintain.expire", func(tx *store.Tx) error {
			_, err := a.Catalog.ExpireTx(ctx, tx, lastExpiry, 500)
			return err
		})
		if err == nil {
			lastExpiry = now
		}
		return err
	})
	a.every("freshness", 5*time.Minute, a.Catalog.RefreshFreshness)
	a.every("sessions_prune", time.Hour, a.Authn.PruneSessions)
	a.every("compact", 6*time.Hour, func(ctx context.Context) error {
		n, err := a.Catalog.Compact(ctx, a.Clock.Now().Add(-r.Tombstones.D()))
		if err != nil {
			return err
		}
		if n > 0 {
			a.Log.Info("compacted tombstones", "event.action", "catalog.compact", "count", n)
		}
		if err := a.Catalog.PruneChangeLog(ctx, a.Clock.Now().Add(-r.ChangeLog.D())); err != nil {
			return err
		}
		return a.Store.W().TxNamed(ctx, "maintain.prune", func(tx *store.Tx) error {
			if _, err := tx.Exec(ctx, `DELETE FROM idempotency_keys WHERE expires_at < ?`, tx.NowMicros()); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `DELETE FROM source_runs WHERE started_at < ?`, tx.Now.Add(-r.SourceRuns.D()).UnixMicro())
			return err
		})
	})
	a.every("checkpoint", 10*time.Minute, func(ctx context.Context) error {
		if err := a.Store.Checkpoint(ctx, "PASSIVE"); err != nil {
			return err
		}
		return a.Store.IncrementalVacuum(ctx, 2000)
	})
	a.every("catalog_metrics", time.Minute, func(ctx context.Context) error {
		c, err := a.Catalog.CountEntities(ctx, nil)
		if err != nil {
			return err
		}
		a.Metrics.SetCatalog(int64(c.Entities-c.Custom), int64(c.Custom), int64(c.Relations))
		srcs, err := a.Catalog.Sources(ctx, nil)
		if err != nil {
			return err
		}
		for _, s := range srcs {
			if !s.System {
				a.Metrics.SourceFreshness(s.Name, s.Freshness)
			}
		}
		return nil
	})
}

var _ = audit.System
