package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/backup"
	"github.com/aurimasniekis/go-deed/internal/buildinfo"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/config"
	"github.com/aurimasniekis/go-deed/internal/findings"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// NewObjectStore builds the S3 store of a backup configuration (nil when no endpoint is set).
func NewObjectStore(c config.BackupS3Config) (*backup.S3, error) {
	if c.Endpoint == "" {
		return nil, nil
	}
	secret := c.SecretAccessKey
	if c.SecretAccessKeyFile != "" {
		b, err := os.ReadFile(c.SecretAccessKeyFile)
		if err != nil {
			return nil, fmt.Errorf("backup.s3.secret_access_key_file: %w", err)
		}
		secret = strings.TrimSpace(string(b))
	}
	var ca []byte
	if c.CAFile != "" {
		var err error
		if ca, err = os.ReadFile(c.CAFile); err != nil {
			return nil, fmt.Errorf("backup.s3.ca_file: %w", err)
		}
	}
	return backup.NewS3(backup.S3Options{Endpoint: c.Endpoint, Bucket: c.Bucket, Prefix: c.Prefix, Region: c.Region, AccessKey: c.AccessKeyID,
		SecretKey: secret, Insecure: c.Insecure, Lookup: c.Lookup, CAPEM: ca, SSE: c.SSE})
}

// BackupService builds the backup service from the configuration (also for CLI commands that skip the modules).
func (a *App) BackupService() (*backup.Service, error) {
	if a.Backups != nil {
		return a.Backups, nil
	}
	cfg := a.Cfg.Backup
	o := backup.Options{Store: a.Store, Keyring: a.Keyring, Audit: a.Audit, Metrics: a.Metrics, Clock: a.Clock, Log: a.Log.With("component", "backup"),
		Retention: backup.Retention{Daily: cfg.KeepDaily, Weekly: cfg.KeepWeekly, Monthly: cfg.KeepMonthly}, AppVersion: buildinfo.Get().Version}
	if objs, err := NewObjectStore(cfg.S3); err != nil {
		return nil, err
	} else if objs != nil {
		o.Objects = objs
	}
	if len(cfg.Recipients) > 0 {
		rs, err := backup.ParseRecipients(cfg.Recipients)
		if err != nil {
			return nil, err
		}
		o.Recipients = rs
	}
	if !cfg.ExcludeKEK && a.KEK != nil {
		o.KEK = a.KEK.Encoded
	}
	a.Backups = backup.New(o)
	return a.Backups, nil
}

// initBackups wires the backup service, its API and schedule, and records a restore that happened while offline.
func (a *App) initBackups(ctx context.Context) error {
	cfg := a.Cfg.Backup
	if _, err := a.BackupService(); err != nil {
		return err
	}
	a.apiExt = append(a.apiExt, (&backup.API{Service: a.Backups, Enabled: cfg.Enabled}).Register)
	if err := a.recordRestore(ctx); err != nil {
		return err
	}
	if cfg.Enabled {
		a.addLoop("backup", func(ctx context.Context) { a.backupLoop(ctx, cfg.Interval.D()) })
	}
	return nil
}

// backupLoop runs a backup when the last successful one is older than the interval (checked every minute), so
// restarts neither skip nor duplicate backups.
func (a *App) backupLoop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		var last sql.NullInt64
		_ = a.Store.R().QueryRowContext(ctx, `SELECT MAX(started_at) FROM backup_runs WHERE result IN ('ok', 'running')`).Scan(&last)
		var lastErr sql.NullInt64
		_ = a.Store.R().QueryRowContext(ctx, `SELECT MAX(started_at) FROM backup_runs WHERE result = 'error'`).Scan(&lastErr)
		now := a.Clock.Now()
		due := !last.Valid || now.Sub(clock.FromMicros(last.Int64)) >= interval
		// After a failure, retry no more than every 15 minutes.
		if due && lastErr.Valid && now.Sub(clock.FromMicros(lastErr.Int64)) < 15*time.Minute {
			due = false
		}
		if due {
			_, err := a.Backups.Run(ctx, "schedule", audit.System)
			switch {
			case err == nil:
				a.Metrics.JobRun("backup", "ok")
			case ctx.Err() != nil:
				return
			default:
				a.Metrics.JobRun("backup", "error")
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// recordRestore appends the audit event and finding for a restore performed by `deed restore`, then clears its marker.
func (a *App) recordRestore(ctx context.Context) error {
	m, err := backup.ReadMarker(a.Cfg.DataDir)
	if err != nil || m == nil {
		return err
	}
	err = a.Store.W().TxNamed(ctx, "backup.restore_recorded", func(tx *store.Tx) error {
		if _, err := a.Audit.Append(ctx, tx, audit.Event{Actor: audit.System, Action: "backup.restored", Target: audit.Target{Type: "backup", ID: m.BackupID},
			Details: map[string]any{"backup_created": m.BackupCreated.Format(time.RFC3339), "restored_at": m.RestoredAt.Format(time.RFC3339),
				"verified": m.Verified, "revoked_tokens": m.RevokedTokens}}); err != nil {
			return err
		}
		return findings.RaiseTx(ctx, tx, findings.Finding{DedupKey: findings.TypeRestored + "|" + m.BackupID, Type: findings.TypeRestored,
			Severity: findings.Warning, Class: findings.ClassConfirmed, Title: "Database restored from a backup",
			Message:  "Changes made after " + m.BackupCreated.Format(time.RFC3339) + " are gone; every session was signed out. Re-run source syncs and review tokens.",
			Evidence: map[string]any{"backup_id": m.BackupID, "verified": m.Verified, "revoked_tokens": m.RevokedTokens}}, 7*24*time.Hour)
	})
	if err != nil {
		return err
	}
	a.Log.Warn("database was restored from a backup", "event.action", "backup.restored", "backup.id", m.BackupID)
	return backup.ClearMarker(a.Cfg.DataDir)
}
