package authn

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/obs/metrics"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Installation roles (ADR-004).
const (
	RoleOwner   = "owner"
	RoleAdmin   = "admin"
	RoleAuditor = "auditor"
)

// Project roles (ADR-004).
const (
	ProjectViewer = "viewer"
	ProjectEditor = "editor"
	ProjectAdmin  = "admin"
)

// Authentication methods recorded on sessions and audit events.
const (
	MethodPassword   = "password"
	MethodOIDC       = "oidc"
	MethodBreakGlass = "breakglass"
	MethodSetup      = "setup"
	MethodToken      = "token"
	MethodSource     = "source_token"
)

// Options configure the service.
type Options struct {
	Clock            clock.Clock
	Metrics          *metrics.Metrics
	Issuer           string // TOTP issuer label
	SessionIdle      time.Duration
	SessionAbsolute  time.Duration
	ReauthWindow     time.Duration
	MinPasswordBytes int
	MFARequiredRoles []string
	LocalEnabled     bool
	MaxTokenLifetime time.Duration
	// Params overrides Argon2id parameters (tests only).
	Params *Params
}

// Service implements authentication.
type Service struct {
	s     *store.Store
	kr    *keyring.Keyring
	audit *audit.Writer
	opts  Options
	sem   chan struct{} // bounds concurrent Argon2id work (64 MiB each)

	// OnBreakGlass runs inside the transaction that consumes a break-glass link (raises the alert).
	OnBreakGlass func(ctx context.Context, tx *store.Tx, userID, ip string) error
}

// New builds the service.
func New(s *store.Store, kr *keyring.Keyring, aw *audit.Writer, opts Options) *Service {
	if opts.Clock == nil {
		opts.Clock = clock.System{}
	}
	if opts.Issuer == "" {
		opts.Issuer = "Deed"
	}
	if opts.SessionIdle == 0 {
		opts.SessionIdle = 30 * time.Minute
	}
	if opts.SessionAbsolute == 0 {
		opts.SessionAbsolute = 12 * time.Hour
	}
	if opts.ReauthWindow == 0 {
		opts.ReauthWindow = 5 * time.Minute
	}
	if opts.MinPasswordBytes == 0 {
		opts.MinPasswordBytes = 12
	}
	if opts.MaxTokenLifetime == 0 {
		opts.MaxTokenLifetime = 366 * 24 * time.Hour
	}
	return &Service{s: s, kr: kr, audit: aw, opts: opts, sem: make(chan struct{}, 4)}
}

func (svc *Service) params() Params {
	if svc.opts.Params != nil {
		return *svc.opts.Params
	}
	return CurrentParams
}

func (svc *Service) acquire(ctx context.Context) error {
	select {
	case svc.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (svc *Service) release() { <-svc.sem }

// User is an account.
type User struct {
	ID               string     `json:"id"`
	Email            string     `json:"email,omitempty"`
	DisplayName      string     `json:"display_name"`
	Status           string     `json:"status"`
	InstallationRole string     `json:"installation_role,omitempty"`
	RoleSource       string     `json:"role_source"`
	HasPassword      bool       `json:"has_password"`
	MFAEnrolled      bool       `json:"mfa_enrolled"`
	CreatedAt        time.Time  `json:"created_at"`
	LastLoginAt      *time.Time `json:"last_login_at,omitempty"`
}

// MFARequired reports whether policy requires a second factor for u.
func (svc *Service) MFARequired(u *User) bool {
	return u.InstallationRole != "" && slices.Contains(svc.opts.MFARequiredRoles, u.InstallationRole)
}

const userCols = `u.id, COALESCE(u.email, ''), u.display_name, u.status, COALESCE(u.installation_role, ''), u.role_source, u.created_at,
	u.last_login_at, EXISTS (SELECT 1 FROM local_credentials c WHERE c.user_id = u.id), EXISTS (SELECT 1 FROM mfa_totp t WHERE t.user_id = u.id)`

type rowScanner interface{ Scan(dest ...any) error }

func scanUser(r rowScanner) (*User, error) {
	var u User
	var created int64
	var last sql.NullInt64
	if err := r.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Status, &u.InstallationRole, &u.RoleSource, &created, &last, &u.HasPassword, &u.MFAEnrolled); err != nil {
		return nil, err
	}
	u.CreatedAt = clock.FromMicros(created)
	if last.Valid {
		t := clock.FromMicros(last.Int64)
		u.LastLoginAt = &t
	}
	return &u, nil
}

// ErrUserNotFound is returned for unknown users.
var ErrUserNotFound = apperr.New(apperr.CodeNotFound, "user not found")

// GetUser returns a user by ID.
func (svc *Service) GetUser(ctx context.Context, id string) (*User, error) {
	u, err := scanUser(svc.s.R().QueryRowContext(ctx, `SELECT `+userCols+` FROM users u WHERE u.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return u, err
}

func getUserTx(ctx context.Context, tx *store.Tx, id string) (*User, error) {
	u, err := scanUser(tx.QueryRow(ctx, `SELECT `+userCols+` FROM users u WHERE u.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	return u, err
}

// ListUsers returns every user ordered by display name.
func (svc *Service) ListUsers(ctx context.Context) ([]*User, error) {
	rows, err := svc.s.R().QueryContext(ctx, `SELECT `+userCols+` FROM users u ORDER BY u.display_name, u.id`)
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

// NormalizeEmail lowercases and validates an address.
func NormalizeEmail(e string) (string, error) {
	e = strings.ToLower(strings.TrimSpace(e))
	if len(e) > 254 {
		return "", apperr.New(apperr.CodeInvalid, "email is too long")
	}
	a, err := mail.ParseAddress(e)
	if err != nil || a.Address != e || !strings.Contains(e, "@") {
		return "", apperr.New(apperr.CodeInvalid, "email is not a valid address")
	}
	return e, nil
}

func validDisplayName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || len(n) > 128 || strings.ContainsAny(n, "\x00\r\n") {
		return "", apperr.New(apperr.CodeInvalid, "display name must be 1–128 characters on one line")
	}
	return n, nil
}

// ValidInstallationRole reports whether r is "", owner, admin or auditor.
func ValidInstallationRole(r string) bool {
	return r == "" || r == RoleOwner || r == RoleAdmin || r == RoleAuditor
}

// ValidProjectRole reports whether r is viewer, editor or admin.
func ValidProjectRole(r string) bool {
	return r == ProjectViewer || r == ProjectEditor || r == ProjectAdmin
}

// CreateUserInput creates a local user.
type CreateUserInput struct {
	Email            string
	DisplayName      string
	Password         string // optional: users may be invited to set one, or sign in only through OIDC
	InstallationRole string
}

// CreateUser creates a user (installation owner/admin only; checked by the caller).
func (svc *Service) CreateUser(ctx context.Context, actor audit.Actor, in CreateUserInput) (*User, error) {
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		return nil, err
	}
	name, err := validDisplayName(in.DisplayName)
	if err != nil {
		return nil, err
	}
	if !ValidInstallationRole(in.InstallationRole) {
		return nil, apperr.New(apperr.CodeInvalid, "installation_role must be owner, admin or auditor")
	}
	var phc string
	if in.Password != "" {
		if phc, err = svc.hash(ctx, in.Password); err != nil {
			return nil, err
		}
	}
	id := ids.New(ids.User)
	err = svc.s.W().TxNamed(ctx, "authn.user_create", func(tx *store.Tx) error {
		if err := insertUser(ctx, tx, id, email, name, in.InstallationRole); err != nil {
			return err
		}
		if phc != "" {
			if err := setPasswordTx(ctx, tx, id, phc, svc.params().Version); err != nil {
				return err
			}
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "user.created", Target: audit.Target{Type: "user", ID: id},
			Details: map[string]any{"installation_role": in.InstallationRole, "local_login": phc != ""}})
		return err
	})
	if store.IsConstraint(err) {
		return nil, apperr.New(apperr.CodeAlreadyExists, "a user with this email already exists")
	}
	if err != nil {
		return nil, err
	}
	return svc.GetUser(ctx, id)
}

func insertUser(ctx context.Context, tx *store.Tx, id, email, name, role string) error {
	_, err := tx.Exec(ctx, `INSERT INTO users (id, email, display_name, installation_role, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, nullStr(email), name, nullStr(role), tx.NowMicros(), tx.NowMicros())
	return err
}

func setPasswordTx(ctx context.Context, tx *store.Tx, userID, phc string, version uint32) error {
	_, err := tx.Exec(ctx, `INSERT INTO local_credentials (user_id, password_phc, params_version, changed_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id) DO UPDATE SET password_phc = excluded.password_phc, params_version = excluded.params_version,
		changed_at = excluded.changed_at`, userID, phc, int64(version), tx.NowMicros())
	return err
}

func (svc *Service) hash(ctx context.Context, password string) (string, error) {
	pw, err := NormalizePassword(password, svc.opts.MinPasswordBytes)
	if err != nil {
		return "", err
	}
	if err := checkStrength(string(pw)); err != nil {
		return "", err
	}
	if err := svc.acquire(ctx); err != nil {
		return "", err
	}
	defer svc.release()
	return HashPassword(pw, svc.params())
}

// checkStrength rejects trivially guessable passwords (one repeated character, common sequences).
func checkStrength(pw string) error {
	lower := strings.ToLower(pw)
	distinct := map[rune]bool{}
	for _, r := range lower {
		distinct[r] = true
	}
	if len(distinct) < 5 {
		return apperr.New(apperr.CodeInvalid, "password is too repetitive; use a passphrase")
	}
	for _, bad := range []string{"password", "123456", "qwerty", "letmein", "deed"} {
		if strings.Contains(lower, bad) && len(lower) < len(bad)+8 {
			return apperr.New(apperr.CodeInvalid, "password is too common; use a passphrase")
		}
	}
	return nil
}

// UpdateUserInput changes a user (nil fields stay).
type UpdateUserInput struct {
	DisplayName      *string
	Status           *string
	InstallationRole *string
}

// UpdateUser changes a user's profile, status or installation role. Granting or removing owner requires an owner
// actor (checked by the caller); the last active owner can never be demoted or disabled.
func (svc *Service) UpdateUser(ctx context.Context, actor audit.Actor, id string, in UpdateUserInput) (*User, error) {
	err := svc.s.W().TxNamed(ctx, "authn.user_update", func(tx *store.Tx) error {
		u, err := getUserTx(ctx, tx, id)
		if err != nil {
			return err
		}
		details := map[string]any{}
		if in.DisplayName != nil {
			n, err := validDisplayName(*in.DisplayName)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE users SET display_name = ?, updated_at = ? WHERE id = ?`, n, tx.NowMicros(), id); err != nil {
				return err
			}
			details["display_name_changed"] = true
		}
		demotesOwner := u.InstallationRole == RoleOwner &&
			((in.InstallationRole != nil && *in.InstallationRole != RoleOwner) || (in.Status != nil && *in.Status == "disabled"))
		if demotesOwner {
			var owners int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE installation_role = 'owner' AND status = 'active'`).Scan(&owners); err != nil {
				return err
			}
			if owners <= 1 {
				return apperr.New(apperr.CodeConflict, "the last owner cannot be demoted or disabled")
			}
		}
		if in.InstallationRole != nil {
			if !ValidInstallationRole(*in.InstallationRole) {
				return apperr.New(apperr.CodeInvalid, "installation_role must be owner, admin, auditor or empty")
			}
			if _, err := tx.Exec(ctx, `UPDATE users SET installation_role = ?, role_source = 'manual', updated_at = ? WHERE id = ?`,
				nullStr(*in.InstallationRole), tx.NowMicros(), id); err != nil {
				return err
			}
			details["installation_role"] = *in.InstallationRole
			details["previous_role"] = u.InstallationRole
			// Privilege change: existing sessions must re-authenticate.
			if err := revokeUserSessionsTx(ctx, tx, id, nil, "privilege_changed"); err != nil {
				return err
			}
		}
		if in.Status != nil {
			if *in.Status != "active" && *in.Status != "disabled" {
				return apperr.New(apperr.CodeInvalid, "status must be active or disabled")
			}
			if _, err := tx.Exec(ctx, `UPDATE users SET status = ?, updated_at = ? WHERE id = ?`, *in.Status, tx.NowMicros(), id); err != nil {
				return err
			}
			details["status"] = *in.Status
			if *in.Status == "disabled" {
				if err := revokeUserSessionsTx(ctx, tx, id, nil, "user_disabled"); err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE user_id = ? AND revoked_at IS NULL`, tx.NowMicros(), id); err != nil {
					return err
				}
			}
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "user.updated", Target: audit.Target{Type: "user", ID: id}, Details: details})
		return err
	})
	if err != nil {
		return nil, err
	}
	return svc.GetUser(ctx, id)
}

// DeleteUser removes a user (never the last owner).
func (svc *Service) DeleteUser(ctx context.Context, actor audit.Actor, id string) error {
	return svc.s.W().TxNamed(ctx, "authn.user_delete", func(tx *store.Tx) error {
		u, err := getUserTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if u.InstallationRole == RoleOwner {
			var owners int
			if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE installation_role = 'owner' AND status = 'active'`).Scan(&owners); err != nil {
				return err
			}
			if owners <= 1 {
				return apperr.New(apperr.CodeConflict, "the last owner cannot be deleted")
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "user.deleted", Target: audit.Target{Type: "user", ID: id}})
		return err
	})
}

// SetPassword sets the password of p's own account (requires recent authentication when one already exists).
func (svc *Service) SetPassword(ctx context.Context, p *Principal, current, next string) error {
	if !svc.opts.LocalEnabled {
		return ErrLocalDisabled
	}
	u, err := svc.GetUser(ctx, p.UserID)
	if err != nil {
		return err
	}
	if u.HasPassword {
		var phc string
		if err := svc.s.R().QueryRowContext(ctx, `SELECT password_phc FROM local_credentials WHERE user_id = ?`, u.ID).Scan(&phc); err != nil {
			return err
		}
		pw, _ := NormalizePassword(current, 1)
		if err := svc.acquire(ctx); err != nil {
			return err
		}
		ok, verr := VerifyPassword(pw, phc)
		svc.release()
		if verr != nil || !ok {
			return apperr.New(apperr.CodeUnauthorized, "current password is wrong")
		}
	}
	phc, err := svc.hash(ctx, next)
	if err != nil {
		return err
	}
	return svc.s.W().TxNamed(ctx, "authn.password_set", func(tx *store.Tx) error {
		if err := setPasswordTx(ctx, tx, u.ID, phc, svc.params().Version); err != nil {
			return err
		}
		if err := revokeUserSessionsTx(ctx, tx, u.ID, p.sessionMAC, "password_changed"); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "user.password_changed", Target: audit.Target{Type: "user", ID: u.ID}})
		return err
	})
}

// HasOwner reports whether an active owner exists.
func (svc *Service) HasOwner(ctx context.Context) (bool, error) {
	var n int
	err := svc.s.R().QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE installation_role = 'owner' AND status = 'active'`).Scan(&n)
	return n > 0, err
}

// ProjectMembership is a user's role in one project.
type ProjectMembership struct {
	ProjectID   string `json:"project_id"`
	ProjectName string `json:"project_name"`
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
	Role        string `json:"role"`
	RoleSource  string `json:"role_source"`
}

// ListMembers returns the members of a project.
func (svc *Service) ListMembers(ctx context.Context, projectID string) ([]ProjectMembership, error) {
	return svc.members(ctx, `WHERE m.project_id = ?`, projectID)
}

// UserMemberships returns the projects a user belongs to.
func (svc *Service) UserMemberships(ctx context.Context, userID string) ([]ProjectMembership, error) {
	return svc.members(ctx, `WHERE m.user_id = ?`, userID)
}

func (svc *Service) members(ctx context.Context, where string, arg string) ([]ProjectMembership, error) {
	rows, err := svc.s.R().QueryContext(ctx, `SELECT m.project_id, p.name, m.user_id, u.display_name, COALESCE(u.email, ''), m.role, m.role_source
		FROM project_members m JOIN projects p ON p.id = m.project_id JOIN users u ON u.id = m.user_id `+where+` ORDER BY p.name, u.display_name`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectMembership{}
	for rows.Next() {
		var m ProjectMembership
		if err := rows.Scan(&m.ProjectID, &m.ProjectName, &m.UserID, &m.DisplayName, &m.Email, &m.Role, &m.RoleSource); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SetMember grants (role != "") or removes (role == "") a project role.
func (svc *Service) SetMember(ctx context.Context, actor audit.Actor, projectID, userID, role string) error {
	if role != "" && !ValidProjectRole(role) {
		return apperr.New(apperr.CodeInvalid, "role must be viewer, editor or admin")
	}
	return svc.s.W().TxNamed(ctx, "authn.member_set", func(tx *store.Tx) error {
		if _, err := getUserTx(ctx, tx, userID); err != nil {
			return err
		}
		var err error
		if role == "" {
			_, err = tx.Exec(ctx, `DELETE FROM project_members WHERE project_id = ? AND user_id = ?`, projectID, userID)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role, role_source, created_at) VALUES (?, ?, ?, 'manual', ?)
				ON CONFLICT (project_id, user_id) DO UPDATE SET role = excluded.role, role_source = 'manual'`, projectID, userID, role, tx.NowMicros())
		}
		if store.IsConstraint(err) {
			return apperr.New(apperr.CodeNotFound, "project not found")
		}
		if err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, ProjectID: projectID, Action: "project.member_set",
			Target: audit.Target{Type: "user", ID: userID}, Details: map[string]any{"role": role}})
		return err
	})
}

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand: %v", err))
	}
	return b
}
