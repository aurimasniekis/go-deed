package backup

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/authn"
	"github.com/aurimasniekis/go-deed/internal/authz"
	"github.com/aurimasniekis/go-deed/internal/httpapi"
)

// API registers the backup endpoints (installation roles only).
type API struct {
	Service *Service
	// Enabled reports whether scheduled backups are on.
	Enabled bool
}

type backupsInput struct {
	Remote bool `query:"remote" doc:"Also list the backups in storage (one LIST request plus a manifest read per backup)."`
	Limit  int  `query:"limit" minimum:"1" maximum:"200" default:"50"`
}

type backupsOutput struct {
	Body struct {
		Configured bool           `json:"configured"`
		Scheduled  bool           `json:"scheduled"`
		Running    bool           `json:"running"`
		VerifyKey  string         `json:"verify_key" doc:"Public key that verifies this installation's backup manifests; keep it with the age identity."`
		Runs       []BackupRun    `json:"runs"`
		Remote     []StoredBackup `json:"remote,omitempty"`
		RemoteErr  string         `json:"remote_error,omitempty"`
	}
}

type runBackupOutput struct {
	Body struct {
		Accepted bool `json:"accepted"`
	}
}

func readable(p *authn.Principal) bool {
	return authz.IsInstallation(p, authn.RoleOwner, authn.RoleAdmin, authn.RoleAuditor)
}

// Register adds the endpoints.
func (a *API) Register(api huma.API, d *httpapi.Deps) {
	sec := httpapi.Secured
	huma.Register(api, huma.Operation{OperationID: "list-backups", Method: http.MethodGet, Path: "/backups", Tags: []string{"backups"},
		Summary: "Backup runs and stored backups (owner, admin, auditor)", Security: sec},
		func(ctx context.Context, in *backupsInput) (*backupsOutput, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("list-backups", err)
			}
			if !readable(p) {
				return nil, d.Fail("list-backups", apperr.New(apperr.CodeForbidden, "installation role required"))
			}
			out := &backupsOutput{}
			out.Body.Configured, out.Body.Scheduled, out.Body.Running = a.Service.Configured(), a.Enabled, a.Service.Busy()
			if out.Body.VerifyKey, err = a.Service.VerifyKey(); err != nil {
				return nil, d.Fail("list-backups", err)
			}
			if out.Body.Runs, err = a.Service.Runs(ctx, in.Limit); err != nil {
				return nil, d.Fail("list-backups", err)
			}
			if in.Remote && a.Service.Configured() {
				k, _ := SigningKey(a.Service.o.Keyring)
				rs, err := ListVerified(ctx, a.Service.Objects(), []ed25519.PublicKey{k.Public().(ed25519.PublicKey)})
				if err != nil {
					out.Body.RemoteErr = "storage unavailable: " + err.Error()
				}
				out.Body.Remote = rs
			}
			return out, nil
		})
	huma.Register(api, huma.Operation{OperationID: "run-backup", Method: http.MethodPost, Path: "/backups", Tags: []string{"backups"},
		Summary: "Start a backup now (owner or admin)", Description: "The backup runs in the background; poll GET /backups for its result.",
		Security: sec, DefaultStatus: http.StatusAccepted},
		func(ctx context.Context, _ *struct{}) (*runBackupOutput, error) {
			p, err := httpapi.RequireUser(ctx)
			if err != nil {
				return nil, d.Fail("run-backup", err)
			}
			if !authz.CanAdminister(p) {
				return nil, d.Fail("run-backup", apperr.New(apperr.CodeForbidden, "installation owner or admin required"))
			}
			if !a.Service.Configured() {
				return nil, d.Fail("run-backup", apperr.New(apperr.CodeInvalid, "backups are not configured (backup.s3 and backup.recipients)"))
			}
			if a.Service.Busy() {
				return nil, d.Fail("run-backup", apperr.New(apperr.CodeConflict, "a backup is already running"))
			}
			actor := httpapi.Actor(ctx)
			go func() {
				if _, err := a.Service.Run(context.WithoutCancel(ctx), "manual", actor); err != nil && !errors.Is(err, ErrBusy) {
					a.Service.o.Log.Warn("manual backup failed", "error.message", err.Error())
				}
			}()
			out := &runBackupOutput{}
			out.Body.Accepted = true
			return out, nil
		})
}
