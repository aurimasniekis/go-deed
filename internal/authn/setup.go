package authn

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Setup link purposes.
const (
	LinkBootstrap = "bootstrap" // first owner (CLI, only while no owner exists)
	LinkRecovery  = "recovery"  // break-glass login (CLI, needs the data-directory lock and KEK)
	LinkInvite    = "invite"    // new local user (admin)
	LinkReset     = "reset"     // password reset (admin)
)

// SetupTokenPrefix prefixes every setup link token.
const SetupTokenPrefix = "deed_setup_"

// ErrInvalidLink is returned for unknown, used or expired links.
var ErrInvalidLink = apperr.New(apperr.CodeUnauthorized, "this link is invalid, expired or already used")

func (svc *Service) linkMAC(token string) ([]byte, error) {
	return svc.kr.MAC(keyring.PurposeToken, []byte("setup|"+token))
}

// Link is an issued single-use link.
type Link struct {
	Token     string    `json:"token"`
	Purpose   string    `json:"purpose"`
	ExpiresAt time.Time `json:"expires_at"`
}

// URL renders the link for origin. The token travels in the fragment so it never reaches proxies or access logs.
func (l Link) URL(origin string) string {
	return strings.TrimSuffix(origin, "/") + "/setup#" + l.Purpose + "=" + l.Token
}

func (svc *Service) issueLinkTx(ctx context.Context, tx *store.Tx, purpose, userID, email, role, createdBy string, ttl time.Duration) (Link, error) {
	token := SetupTokenPrefix + base64.RawURLEncoding.EncodeToString(randomBytes(32))
	mac, err := svc.linkMAC(token)
	if err != nil {
		return Link{}, err
	}
	exp := tx.Now.Add(ttl)
	// One live link per purpose and user/email: issuing a new one supersedes the previous.
	if _, err := tx.Exec(ctx, `UPDATE setup_tokens SET used_at = ? WHERE purpose = ? AND used_at IS NULL AND (user_id IS ? OR email IS ?)`,
		tx.NowMicros(), purpose, nullStr(userID), nullStr(email)); err != nil {
		return Link{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO setup_tokens (id, token_mac, purpose, user_id, email, role, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, ids.New(ids.SetupToken), mac, purpose, nullStr(userID), nullStr(email), nullStr(role),
		nullStr(createdBy), tx.NowMicros(), exp.UnixMicro())
	return Link{Token: token, Purpose: purpose, ExpiresAt: exp}, err
}

type linkRow struct {
	id, purpose, userID, email, role string
}

func (svc *Service) takeLinkTx(ctx context.Context, tx *store.Tx, token, purpose string) (*linkRow, error) {
	if !strings.HasPrefix(token, SetupTokenPrefix) || len(token) > 128 {
		return nil, ErrInvalidLink
	}
	mac, err := svc.linkMAC(token)
	if err != nil {
		return nil, err
	}
	var r linkRow
	var exp int64
	var used sql.NullInt64
	err = tx.QueryRow(ctx, `SELECT id, purpose, COALESCE(user_id, ''), COALESCE(email, ''), COALESCE(role, ''), expires_at, used_at FROM setup_tokens
		WHERE token_mac = ?`, mac).Scan(&r.id, &r.purpose, &r.userID, &r.email, &r.role, &exp, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrInvalidLink
	}
	if err != nil {
		return nil, err
	}
	if r.purpose != purpose || used.Valid || !tx.Now.Before(clock.FromMicros(exp)) {
		return nil, ErrInvalidLink
	}
	if _, err := tx.Exec(ctx, `UPDATE setup_tokens SET used_at = ? WHERE id = ?`, tx.NowMicros(), r.id); err != nil {
		return nil, err
	}
	return &r, nil
}

// IssueBootstrap issues the first-owner link. It refuses when an owner already exists.
func (svc *Service) IssueBootstrap(ctx context.Context, ttl time.Duration) (Link, error) {
	var l Link
	err := svc.s.W().TxNamed(ctx, "authn.bootstrap_issue", func(tx *store.Tx) error {
		var owners int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE installation_role = 'owner'`).Scan(&owners); err != nil {
			return err
		}
		if owners > 0 {
			return apperr.New(apperr.CodeConflict, "an owner already exists; use `deed admin recover` to regain access")
		}
		var err error
		if l, err = svc.issueLinkTx(ctx, tx, LinkBootstrap, "", "", RoleOwner, "cli", ttl); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.CLI, Action: "auth.bootstrap_link_issued"})
		return err
	})
	return l, err
}

// LinkInfo describes a link before it is consumed (for the setup page).
type LinkInfo struct {
	Purpose     string `json:"purpose"`
	Email       string `json:"email,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	Role        string `json:"role,omitempty"`
}

// InspectLink validates a link without consuming it.
func (svc *Service) InspectLink(ctx context.Context, token string) (*LinkInfo, error) {
	if !strings.HasPrefix(token, SetupTokenPrefix) || len(token) > 128 {
		return nil, ErrInvalidLink
	}
	mac, err := svc.linkMAC(token)
	if err != nil {
		return nil, err
	}
	var info LinkInfo
	var userID string
	var exp int64
	var used sql.NullInt64
	err = svc.s.R().QueryRowContext(ctx, `SELECT purpose, COALESCE(email, ''), COALESCE(role, ''), COALESCE(user_id, ''), expires_at, used_at
		FROM setup_tokens WHERE token_mac = ?`, mac).Scan(&info.Purpose, &info.Email, &info.Role, &userID, &exp, &used)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (used.Valid || !svc.opts.Clock.Now().Before(clock.FromMicros(exp)))) {
		return nil, ErrInvalidLink
	}
	if err != nil {
		return nil, err
	}
	if userID != "" {
		if u, err := svc.GetUser(ctx, userID); err == nil {
			info.Email, info.DisplayName = u.Email, u.DisplayName
		}
	}
	return &info, nil
}

// ConsumeBootstrap creates the first owner with a password. The returned session must enroll TOTP before it is full.
func (svc *Service) ConsumeBootstrap(ctx context.Context, token, email, displayName, password string, meta SessionMeta) (*LoginResult, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return nil, err
	}
	name, err := validDisplayName(displayName)
	if err != nil {
		return nil, err
	}
	phc, err := svc.hash(ctx, password)
	if err != nil {
		return nil, err
	}
	id := ids.New(ids.User)
	var sess string
	err = svc.s.W().TxNamed(ctx, "authn.bootstrap", func(tx *store.Tx) error {
		if _, err := svc.takeLinkTx(ctx, tx, token, LinkBootstrap); err != nil {
			return err
		}
		var owners int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE installation_role = 'owner'`).Scan(&owners); err != nil {
			return err
		}
		if owners > 0 {
			return ErrInvalidLink
		}
		if err := insertUser(ctx, tx, id, email, name, RoleOwner); err != nil {
			return err
		}
		if err := setPasswordTx(ctx, tx, id, phc, svc.params().Version); err != nil {
			return err
		}
		needs := svc.MFARequired(&User{InstallationRole: RoleOwner})
		if sess, err = svc.createSession(ctx, tx, id, MethodSetup, tx.Now, nil, needs, needs, meta); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: id}, Action: "auth.bootstrap_completed",
			Target: audit.Target{Type: "user", ID: id}, SourceIP: meta.IP})
		return err
	})
	if store.IsConstraint(err) {
		return nil, apperr.New(apperr.CodeAlreadyExists, "a user with this email already exists")
	}
	if err != nil {
		return nil, err
	}
	p, err := svc.Authenticate(ctx, sess)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: sess, Principal: p, MustEnroll: p.NeedsEnroll}, nil
}

// IssueRecovery issues a break-glass login link for a user (CLI only). OnBreakGlass is notified when it is used.
func (svc *Service) IssueRecovery(ctx context.Context, email string) (Link, error) {
	norm, err := NormalizeEmail(email)
	if err != nil {
		return Link{}, err
	}
	var l Link
	err = svc.s.W().TxNamed(ctx, "authn.recovery_issue", func(tx *store.Tx) error {
		var userID string
		err := tx.QueryRow(ctx, `SELECT id FROM users WHERE email = ?`, norm).Scan(&userID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUserNotFound
		}
		if err != nil {
			return err
		}
		if l, err = svc.issueLinkTx(ctx, tx, LinkRecovery, userID, "", "", "cli", 15*time.Minute); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.CLI, Action: "auth.breakglass_link_issued", Target: audit.Target{Type: "user", ID: userID}})
		return err
	})
	return l, err
}

// ConsumeRecovery signs in with a break-glass link. The session counts as MFA-verified once; the user is re-enabled
// if it was disabled only when the link was issued for it explicitly (it always is). Audited and alerted.
func (svc *Service) ConsumeRecovery(ctx context.Context, token string, meta SessionMeta) (*LoginResult, error) {
	var sess, userID string
	err := svc.s.W().TxNamed(ctx, "authn.recovery", func(tx *store.Tx) error {
		r, err := svc.takeLinkTx(ctx, tx, token, LinkRecovery)
		if err != nil {
			return err
		}
		userID = r.userID
		if _, err := tx.Exec(ctx, `UPDATE users SET status = 'active', updated_at = ? WHERE id = ?`, tx.NowMicros(), userID); err != nil {
			return err
		}
		mfaAt := tx.Now
		if sess, err = svc.createSession(ctx, tx, userID, MethodBreakGlass, tx.Now, &mfaAt, false, false, meta); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: userID}, Action: "auth.breakglass_used",
			Target: audit.Target{Type: "user", ID: userID}, SourceIP: meta.IP, AuthMethod: MethodBreakGlass})
		if err != nil {
			return err
		}
		if svc.OnBreakGlass != nil {
			return svc.OnBreakGlass(ctx, tx, userID, meta.IP)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	p, err := svc.Authenticate(ctx, sess)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: sess, Principal: p}, nil
}

// IssueInvite invites a new local user (admin). The user chooses a display name and password from the link.
func (svc *Service) IssueInvite(ctx context.Context, actor audit.Actor, email, role string, ttl time.Duration) (Link, error) {
	norm, err := NormalizeEmail(email)
	if err != nil {
		return Link{}, err
	}
	if !ValidInstallationRole(role) {
		return Link{}, apperr.New(apperr.CodeInvalid, "role must be owner, admin, auditor or empty")
	}
	var l Link
	err = svc.s.W().TxNamed(ctx, "authn.invite_issue", func(tx *store.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE email = ?`, norm).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return apperr.New(apperr.CodeAlreadyExists, "a user with this email already exists")
		}
		var err error
		if l, err = svc.issueLinkTx(ctx, tx, LinkInvite, "", norm, role, actor.ID, ttl); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "user.invited", Details: map[string]any{"installation_role": role}})
		return err
	})
	return l, err
}

// ConsumeInvite creates the invited user and signs them in.
func (svc *Service) ConsumeInvite(ctx context.Context, token, displayName, password string, meta SessionMeta) (*LoginResult, error) {
	name, err := validDisplayName(displayName)
	if err != nil {
		return nil, err
	}
	phc, err := svc.hash(ctx, password)
	if err != nil {
		return nil, err
	}
	id := ids.New(ids.User)
	var sess string
	err = svc.s.W().TxNamed(ctx, "authn.invite_accept", func(tx *store.Tx) error {
		r, err := svc.takeLinkTx(ctx, tx, token, LinkInvite)
		if err != nil {
			return err
		}
		if err := insertUser(ctx, tx, id, r.email, name, r.role); err != nil {
			return err
		}
		if err := setPasswordTx(ctx, tx, id, phc, svc.params().Version); err != nil {
			return err
		}
		needs := svc.MFARequired(&User{InstallationRole: r.role})
		if sess, err = svc.createSession(ctx, tx, id, MethodSetup, tx.Now, nil, needs, needs, meta); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: id}, Action: "user.invite_accepted",
			Target: audit.Target{Type: "user", ID: id}, SourceIP: meta.IP})
		return err
	})
	if store.IsConstraint(err) {
		return nil, apperr.New(apperr.CodeAlreadyExists, "a user with this email already exists")
	}
	if err != nil {
		return nil, err
	}
	p, err := svc.Authenticate(ctx, sess)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: sess, Principal: p, MustEnroll: p.NeedsEnroll}, nil
}

// IssueReset issues a password reset link for a user (admin).
func (svc *Service) IssueReset(ctx context.Context, actor audit.Actor, userID string, ttl time.Duration) (Link, error) {
	var l Link
	err := svc.s.W().TxNamed(ctx, "authn.reset_issue", func(tx *store.Tx) error {
		u, err := getUserTx(ctx, tx, userID)
		if err != nil {
			return err
		}
		if u.Kind == KindService {
			return apperr.New(apperr.CodeConflict, "service accounts have no password; issue an API token instead")
		}
		if l, err = svc.issueLinkTx(ctx, tx, LinkReset, userID, "", "", actor.ID, ttl); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "user.password_reset_issued", Target: audit.Target{Type: "user", ID: userID}})
		return err
	})
	return l, err
}

// ConsumeReset sets a new password from a reset link and revokes every session. It does not sign in: the user then
// logs in normally (including their second factor).
func (svc *Service) ConsumeReset(ctx context.Context, token, password string, meta SessionMeta) error {
	phc, err := svc.hash(ctx, password)
	if err != nil {
		return err
	}
	return svc.s.W().TxNamed(ctx, "authn.reset", func(tx *store.Tx) error {
		r, err := svc.takeLinkTx(ctx, tx, token, LinkReset)
		if err != nil {
			return err
		}
		if err := setPasswordTx(ctx, tx, r.userID, phc, svc.params().Version); err != nil {
			return err
		}
		if err := revokeUserSessionsTx(ctx, tx, r.userID, nil, "password_reset"); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: r.userID}, Action: "user.password_reset",
			Target: audit.Target{Type: "user", ID: r.userID}, SourceIP: meta.IP})
		return err
	})
}
