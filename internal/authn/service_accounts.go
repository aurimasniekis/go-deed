package authn

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// User kinds. A service account is a non-human user for automation (Terraform, CI): it has no password, SSO identity
// or session and authenticates only with API tokens. Its installation role and project memberships bound its tokens
// exactly as a person's do.
const (
	KindHuman   = "human"
	KindService = "service"
)

var (
	serviceAccountName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	errServiceOwner    = apperr.New(apperr.CodeInvalid, "a service account cannot be an owner")
	errNotService      = apperr.New(apperr.CodeNotFound, "service account not found")
)

// ServiceAccountInput creates a service account.
type ServiceAccountInput struct {
	Name        string
	DisplayName string
	Description string
	// InstallationRole is "", admin or auditor.
	InstallationRole string
}

// UpdateServiceAccountInput changes a service account; nil fields are kept.
type UpdateServiceAccountInput struct {
	DisplayName      *string
	Description      *string
	InstallationRole *string
	Status           *string
}

func validServiceRole(r string) error {
	switch r {
	case "", RoleAdmin, RoleAuditor:
		return nil
	case RoleOwner:
		return errServiceOwner
	}
	return apperr.New(apperr.CodeInvalid, "installation_role must be admin, auditor or empty")
}

func validDescription(d string) (string, error) {
	d = strings.TrimSpace(d)
	if len(d) > 500 {
		return "", apperr.New(apperr.CodeInvalid, "description must be at most 500 characters")
	}
	return d, nil
}

// CreateServiceAccount creates a service account.
func (svc *Service) CreateServiceAccount(ctx context.Context, actor audit.Actor, in ServiceAccountInput) (*User, error) {
	var id string
	err := svc.s.W().TxNamed(ctx, "authn.service_account_create", func(tx *store.Tx) error {
		var err error
		id, err = createServiceAccountTx(ctx, tx, svc.audit, actor, in)
		return err
	})
	if err != nil {
		return nil, err
	}
	return svc.GetUser(ctx, id)
}

func createServiceAccountTx(ctx context.Context, tx *store.Tx, aw *audit.Writer, actor audit.Actor, in ServiceAccountInput) (string, error) {
	if !serviceAccountName.MatchString(in.Name) {
		return "", apperr.New(apperr.CodeInvalid, "name must be 1–63 lowercase letters, digits, '-' or '_', starting with a letter or digit")
	}
	if strings.TrimSpace(in.DisplayName) == "" {
		in.DisplayName = in.Name
	}
	display, err := validDisplayName(in.DisplayName)
	if err != nil {
		return "", err
	}
	desc, err := validDescription(in.Description)
	if err != nil {
		return "", err
	}
	if err := validServiceRole(in.InstallationRole); err != nil {
		return "", err
	}
	id := ids.New(ids.User)
	_, err = tx.Exec(ctx, `INSERT INTO users (id, kind, name, description, display_name, installation_role, created_at, updated_at)
		VALUES (?, 'service', ?, ?, ?, ?, ?, ?)`, id, in.Name, desc, display, nullStr(in.InstallationRole), tx.NowMicros(), tx.NowMicros())
	if store.IsConstraint(err) {
		return "", apperr.New(apperr.CodeAlreadyExists, "a service account named %s already exists", in.Name)
	}
	if err != nil {
		return "", err
	}
	_, err = aw.Append(ctx, tx, audit.Event{Actor: actor, Action: "service_account.created", Target: audit.Target{Type: "user", ID: id},
		Details: map[string]any{"name": in.Name, "installation_role": in.InstallationRole}})
	return id, err
}

// ServiceAccount returns a service account by ID or name.
func (svc *Service) ServiceAccount(ctx context.Context, ref string) (*User, error) {
	u, err := scanUser(svc.s.R().QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.kind = 'service' AND (u.id = ? OR u.name = ?)`, ref, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotService
	}
	return u, err
}

func serviceAccountTx(ctx context.Context, tx *store.Tx, ref string) (*User, error) {
	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE u.kind = 'service' AND (u.id = ? OR u.name = ?)`, ref, ref))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotService
	}
	return u, err
}

// ListServiceAccounts returns every service account ordered by name.
func (svc *Service) ListServiceAccounts(ctx context.Context) ([]*User, error) {
	rows, err := svc.s.R().QueryContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.kind = 'service' ORDER BY u.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateServiceAccount changes a service account. Disabling it revokes its tokens (as for people).
func (svc *Service) UpdateServiceAccount(ctx context.Context, actor audit.Actor, ref string, in UpdateServiceAccountInput) (*User, error) {
	sa, err := svc.ServiceAccount(ctx, ref)
	if err != nil {
		return nil, err
	}
	if in.InstallationRole != nil {
		if err := validServiceRole(*in.InstallationRole); err != nil {
			return nil, err
		}
	}
	return svc.UpdateUser(ctx, actor, sa.ID, UpdateUserInput{DisplayName: in.DisplayName, Description: in.Description, Status: in.Status,
		InstallationRole: in.InstallationRole})
}

// DeleteServiceAccount deletes a service account with its tokens and memberships.
func (svc *Service) DeleteServiceAccount(ctx context.Context, actor audit.Actor, ref string) error {
	sa, err := svc.ServiceAccount(ctx, ref)
	if err != nil {
		return err
	}
	return svc.DeleteUser(ctx, actor, sa.ID)
}

// ServiceTokenInput issues a service account token.
type ServiceTokenInput struct {
	Name string
	// InstallationRole is the token's installation role; nil inherits the service account's.
	InstallationRole *string
	ProjectRole      string
	Projects         []string
	ExpiresIn        time.Duration
}

// CreateServiceAccountToken issues an API token for a service account. Installation owners and admins may do this from
// a session (with a recent sign-in) or with an admin token, so automation can rotate its own credentials. The raw token
// is returned once.
func (svc *Service) CreateServiceAccountToken(ctx context.Context, p *Principal, ref string, in ServiceTokenInput) (*Token, string, error) {
	if p.InstallationRole != RoleOwner && p.InstallationRole != RoleAdmin {
		return nil, "", apperr.New(apperr.CodeForbidden, "installation owner or admin role required")
	}
	if err := svc.RequireRecentAuth(p); err != nil {
		return nil, "", err
	}
	var t *Token
	var raw string
	err := svc.s.W().TxNamed(ctx, "authn.service_token_create", func(tx *store.Tx) error {
		sa, err := serviceAccountTx(ctx, tx, ref)
		if err != nil {
			return err
		}
		t, raw, err = svc.issueServiceTokenTx(ctx, tx, p.Actor(), sa, in)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return t, raw, nil
}

func (svc *Service) issueServiceTokenTx(ctx context.Context, tx *store.Tx, actor audit.Actor, sa *User, in ServiceTokenInput) (*Token, string, error) {
	if sa.Status != "active" {
		return nil, "", apperr.New(apperr.CodeConflict, "service account %s is disabled", sa.Name)
	}
	if in.Name = strings.TrimSpace(in.Name); in.Name == "" || len(in.Name) > 100 {
		return nil, "", apperr.New(apperr.CodeInvalid, "name must be 1–100 characters")
	}
	role := sa.InstallationRole
	if in.InstallationRole != nil {
		role = *in.InstallationRole
	}
	if err := validServiceRole(role); err != nil {
		return nil, "", err
	}
	if installationRank[role] > installationRank[sa.InstallationRole] {
		return nil, "", apperr.New(apperr.CodeInvalid, "a token cannot exceed the service account's installation role (%s)",
			cmp.Or(sa.InstallationRole, "none"))
	}
	if in.ProjectRole != "" && !ValidProjectRole(in.ProjectRole) {
		return nil, "", apperr.New(apperr.CodeInvalid, "project_role must be viewer, editor or admin")
	}
	if in.Projects == nil {
		in.Projects = []string{}
	}
	exp, err := svc.expiry(in.ExpiresIn)
	if err != nil {
		return nil, "", err
	}
	raw := APITokenPrefix + newTokenSecret()
	t := &Token{ID: ids.New(ids.APIToken), Kind: "user", Name: in.Name, UserID: sa.ID, InstallationRole: role, ProjectRole: in.ProjectRole,
		Projects: in.Projects, CreatedBy: actor.ID, CreatedAt: tx.Now, ExpiresAt: tx.Now.Add(exp)}
	if err := svc.insertToken(ctx, tx, t, raw); err != nil {
		return nil, "", err
	}
	_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "token.created", Target: audit.Target{Type: "api_token", ID: t.ID},
		Details: map[string]any{"service_account": sa.Name, "name": t.Name, "installation_role": t.InstallationRole, "project_role": t.ProjectRole,
			"projects": t.Projects, "expires_at": t.ExpiresAt.Format(time.RFC3339)}})
	return t, raw, err
}

// IssueServiceTokenOnHost issues a service account token from the host (`deed admin token`, server stopped), creating
// the account with role when create is set. It is how automation gets its first credential without a person's token.
func (svc *Service) IssueServiceTokenOnHost(ctx context.Context, name string, create bool, role, tokenName string, expiresIn time.Duration) (*User, *Token, string, error) {
	var sa *User
	var t *Token
	var raw string
	err := svc.s.W().TxNamed(ctx, "authn.service_token_host", func(tx *store.Tx) error {
		var err error
		sa, err = serviceAccountTx(ctx, tx, name)
		if errors.Is(err, errNotService) && create {
			var id string
			if id, err = createServiceAccountTx(ctx, tx, svc.audit, audit.CLI, ServiceAccountInput{Name: name, InstallationRole: role}); err != nil {
				return err
			}
			sa, err = getUserTx(ctx, tx, id)
		}
		if errors.Is(err, errNotService) {
			return apperr.New(apperr.CodeNotFound, "no service account named %s (add -create to create it)", name)
		}
		if err != nil {
			return err
		}
		t, raw, err = svc.issueServiceTokenTx(ctx, tx, audit.CLI, sa, ServiceTokenInput{Name: tokenName, InstallationRole: &role, ExpiresIn: expiresIn})
		return err
	})
	if err != nil {
		return nil, nil, "", err
	}
	return sa, t, raw, nil
}
