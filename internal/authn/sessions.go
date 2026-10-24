// Adapted from HallPass internal/opauth/sessions.go (Apache-2.0).

package authn

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Cookie names: the __Host- prefix requires Secure, Path=/ and no Domain (production, https).
const (
	CookieName    = "__Host-deed_session"
	DevCookieName = "deed_session"
)

// CookieFor returns the session cookie name for a deployment served over https (secure) or http://localhost.
func CookieFor(secure bool) string {
	if secure {
		return CookieName
	}
	return DevCookieName
}

// TokenFromRequest returns the session cookie value ("" when absent).
func TokenFromRequest(r *http.Request) string {
	for _, n := range []string{CookieName, DevCookieName} {
		if c, err := r.Cookie(n); err == nil {
			return c.Value
		}
	}
	return ""
}

// SessionCookie builds the session cookie; maxAge < 0 deletes it.
func SessionCookie(secure bool, token string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: CookieFor(secure), Value: token, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge}
}

// Principal is the authenticated caller.
type Principal struct {
	UserID           string
	Email            string
	DisplayName      string
	InstallationRole string
	// ProjectRoles maps project ID → role. For tokens it is already capped by the token's grant.
	ProjectRoles map[string]string
	// AllProjects is set when the token or role grants every project (installation owner/admin/auditor).
	Method      string
	Provider    string
	SessionID   string // public session ID, safe to show
	AuthAt      time.Time
	MFAAt       *time.Time
	ExpiresAt   time.Time
	Limited     bool // MFA verification or enrollment still pending
	NeedsEnroll bool
	// TokenID is set for API and source tokens; SourceID only for source tokens.
	TokenID  string
	SourceID string
	// TokenProjectRole caps every project role of an API token ("" = no cap); TokenProjects restricts it to a list
	// of project IDs (nil = every project the user can access).
	TokenProjectRole string
	TokenProjects    []string

	csrfSecret []byte
	sessionMAC []byte
}

// Actor returns the audit actor of the principal.
func (p *Principal) Actor() audit.Actor {
	switch {
	case p == nil:
		return audit.Actor{Type: audit.ActorSystem}
	case p.SourceID != "":
		return audit.Actor{Type: audit.ActorSource, ID: p.TokenID}
	case p.TokenID != "":
		return audit.Actor{Type: audit.ActorToken, ID: p.TokenID}
	default:
		return audit.Actor{Type: audit.ActorUser, ID: p.UserID}
	}
}

// IsSession reports whether the principal is a browser session (CSRF applies).
func (p *Principal) IsSession() bool { return p.TokenID == "" && p.sessionMAC != nil }

// CSRFToken derives the CSRF header value from the session's secret.
func (p *Principal) CSRFToken() string {
	if len(p.csrfSecret) == 0 {
		return ""
	}
	h := hmac.New(sha256.New, p.csrfSecret)
	h.Write([]byte("deed/csrf/v1"))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// VerifyCSRF checks the header value in constant time.
func (p *Principal) VerifyCSRF(v string) bool {
	want := p.CSRFToken()
	return want != "" && hmac.Equal([]byte(v), []byte(want))
}

// SessionMeta describes the client.
type SessionMeta struct {
	IP        string
	UserAgent string
	Provider  string
}

func (svc *Service) sessionMAC(token string) ([]byte, error) {
	return svc.kr.MAC(keyring.PurposeToken, []byte("session|"+token))
}

// createSession inserts a session inside tx and returns the bearer token (cookie value).
func (svc *Service) createSession(ctx context.Context, tx *store.Tx, userID, method string, authAt time.Time, mfaAt *time.Time, limited, needsEnroll bool, meta SessionMeta) (string, error) {
	token := base64.RawURLEncoding.EncodeToString(randomBytes(32))
	mac, err := svc.sessionMAC(token)
	if err != nil {
		return "", err
	}
	var mfa sql.NullInt64
	if mfaAt != nil {
		mfa = sql.NullInt64{Int64: mfaAt.UnixMicro(), Valid: true}
	}
	if len(meta.UserAgent) > 256 {
		meta.UserAgent = meta.UserAgent[:256]
	}
	_, err = tx.Exec(ctx, `INSERT INTO sessions (id_mac, public_id, user_id, csrf_secret, auth_method, provider_slug, auth_at, mfa_at,
		limited, needs_enroll, created_at, last_seen_at, absolute_expires_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		mac, ids.New(ids.SessionPublic), userID, randomBytes(32), method, meta.Provider, authAt.UnixMicro(), mfa, b2i(limited), b2i(needsEnroll),
		tx.NowMicros(), tx.NowMicros(), tx.Now.Add(svc.opts.SessionAbsolute).UnixMicro(), meta.IP, meta.UserAgent)
	if err != nil {
		return "", err
	}
	if !limited {
		if _, err := tx.Exec(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, tx.NowMicros(), userID); err != nil {
			return "", err
		}
	}
	return token, nil
}

// rotateTx replaces the session behind p with a fresh one (login completion, privilege change) and returns its token.
func (svc *Service) rotateTx(ctx context.Context, tx *store.Tx, p *Principal, authAt time.Time, mfaAt *time.Time, meta SessionMeta) (string, error) {
	if p.sessionMAC != nil {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = ?, revoke_reason = 'rotated' WHERE id_mac = ?`, tx.NowMicros(), p.sessionMAC); err != nil {
			return "", err
		}
	}
	if meta.Provider == "" {
		meta.Provider = p.Provider
	}
	return svc.createSession(ctx, tx, p.UserID, p.Method, authAt, mfaAt, false, false, meta)
}

// ErrUnauthorized means no valid credential.
var ErrUnauthorized = apperr.New(apperr.CodeUnauthorized, "authentication required")

// Authenticate resolves a session token, enforcing revocation, idle and absolute expiry, and marks it active.
func (svc *Service) Authenticate(ctx context.Context, token string) (*Principal, error) {
	return svc.authenticate(ctx, token, true)
}

// Validate is Authenticate without touching the idle timer (long-lived SSE re-checks).
func (svc *Service) Validate(ctx context.Context, token string) (*Principal, error) {
	return svc.authenticate(ctx, token, false)
}

func (svc *Service) authenticate(ctx context.Context, token string, touch bool) (*Principal, error) {
	if token == "" || len(token) > 128 {
		return nil, ErrUnauthorized
	}
	mac, err := svc.sessionMAC(token)
	if err != nil {
		return nil, err
	}
	var (
		p                                 Principal
		authAt, created, lastSeen, absExp int64
		mfaAt, revoked                    sql.NullInt64
		limited, needsEnroll              int64
	)
	err = svc.s.R().QueryRowContext(ctx, `SELECT public_id, user_id, csrf_secret, auth_method, provider_slug, auth_at, mfa_at, limited,
		needs_enroll, created_at, last_seen_at, absolute_expires_at, revoked_at FROM sessions WHERE id_mac = ?`, mac).Scan(
		&p.SessionID, &p.UserID, &p.csrfSecret, &p.Method, &p.Provider, &authAt, &mfaAt, &limited, &needsEnroll, &created, &lastSeen, &absExp, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	now := svc.opts.Clock.Now()
	if revoked.Valid || !now.Before(clock.FromMicros(absExp)) || now.Sub(clock.FromMicros(lastSeen)) > svc.opts.SessionIdle {
		return nil, ErrUnauthorized
	}
	if err := svc.loadUser(ctx, &p); err != nil {
		return nil, err
	}
	p.AuthAt = clock.FromMicros(authAt)
	if mfaAt.Valid {
		t := clock.FromMicros(mfaAt.Int64)
		p.MFAAt = &t
	}
	p.Limited, p.NeedsEnroll = limited == 1, needsEnroll == 1
	p.ExpiresAt = clock.FromMicros(absExp)
	if idle := clock.FromMicros(lastSeen).Add(svc.opts.SessionIdle); idle.Before(p.ExpiresAt) && !touch {
		p.ExpiresAt = idle
	}
	p.sessionMAC = mac
	// Touch at most once a minute to keep writes low.
	if touch && now.Sub(clock.FromMicros(lastSeen)) > time.Minute {
		_ = svc.s.W().TxNamed(context.WithoutCancel(ctx), "authn.session_touch", func(tx *store.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE sessions SET last_seen_at = ? WHERE id_mac = ? AND revoked_at IS NULL`, tx.NowMicros(), mac)
			return err
		})
	}
	return &p, nil
}

// loadUser fills profile, installation role and project roles; disabled users are unauthorized.
func (svc *Service) loadUser(ctx context.Context, p *Principal) error {
	var status, role string
	err := svc.s.R().QueryRowContext(ctx, `SELECT COALESCE(email, ''), display_name, status, COALESCE(installation_role, '') FROM users WHERE id = ?`,
		p.UserID).Scan(&p.Email, &p.DisplayName, &status, &role)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrUnauthorized
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return ErrUnauthorized
	}
	p.InstallationRole = role
	rows, err := svc.s.R().QueryContext(ctx, `SELECT project_id, role FROM project_members WHERE user_id = ?`, p.UserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	p.ProjectRoles = map[string]string{}
	for rows.Next() {
		var pid, r string
		if err := rows.Scan(&pid, &r); err != nil {
			return err
		}
		p.ProjectRoles[pid] = r
	}
	return rows.Err()
}

// RequireRecentAuth enforces the re-authentication window for sensitive operations. Tokens are exempt: they are
// created under re-authentication and refused for interactive-only operations by the API layer.
func (svc *Service) RequireRecentAuth(p *Principal) error {
	if p.TokenID != "" {
		return nil
	}
	if svc.opts.Clock.Now().Sub(p.AuthAt) > svc.opts.ReauthWindow {
		return apperr.New(apperr.CodeReauth, "this action needs a recent sign-in")
	}
	return nil
}

// Logout revokes the session behind p.
func (svc *Service) Logout(ctx context.Context, p *Principal) error {
	return svc.s.W().TxNamed(ctx, "authn.logout", func(tx *store.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = ?, revoke_reason = 'logout' WHERE id_mac = ? AND revoked_at IS NULL`, tx.NowMicros(), p.sessionMAC); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "session.logout", Target: audit.Target{Type: "session", ID: p.SessionID}})
		return err
	})
}

// SessionInfo describes a session for the account page.
type SessionInfo struct {
	ID         string    `json:"id"`
	Method     string    `json:"method"`
	Provider   string    `json:"provider,omitempty"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

// ListSessions returns the user's active sessions.
func (svc *Service) ListSessions(ctx context.Context, p *Principal) ([]SessionInfo, error) {
	now := svc.opts.Clock.Now()
	rows, err := svc.s.R().QueryContext(ctx, `SELECT public_id, auth_method, provider_slug, ip, user_agent, created_at, last_seen_at, absolute_expires_at
		FROM sessions WHERE user_id = ? AND revoked_at IS NULL AND absolute_expires_at > ? AND last_seen_at > ? ORDER BY last_seen_at DESC`,
		p.UserID, now.UnixMicro(), now.Add(-svc.opts.SessionIdle).UnixMicro())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionInfo{}
	for rows.Next() {
		var s SessionInfo
		var c, l, e int64
		if err := rows.Scan(&s.ID, &s.Method, &s.Provider, &s.IP, &s.UserAgent, &c, &l, &e); err != nil {
			return nil, err
		}
		s.CreatedAt, s.LastSeenAt, s.ExpiresAt = clock.FromMicros(c), clock.FromMicros(l), clock.FromMicros(e)
		s.Current = s.ID == p.SessionID
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeSession revokes one of the user's sessions by public ID.
func (svc *Service) RevokeSession(ctx context.Context, p *Principal, publicID string) error {
	return svc.s.W().TxNamed(ctx, "authn.session_revoke", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `UPDATE sessions SET revoked_at = ?, revoke_reason = 'user' WHERE public_id = ? AND user_id = ? AND revoked_at IS NULL`,
			tx.NowMicros(), publicID, p.UserID)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodeNotFound, "session not found")
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "session.revoked", Target: audit.Target{Type: "session", ID: publicID}})
		return err
	})
}

// RevokeOtherSessions signs the user out everywhere else.
func (svc *Service) RevokeOtherSessions(ctx context.Context, p *Principal) error {
	return svc.s.W().TxNamed(ctx, "authn.sessions_revoke_others", func(tx *store.Tx) error {
		if err := revokeUserSessionsTx(ctx, tx, p.UserID, p.sessionMAC, "user_signout_everywhere"); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "session.revoked_others", Target: audit.Target{Type: "user", ID: p.UserID}})
		return err
	})
}

func revokeUserSessionsTx(ctx context.Context, tx *store.Tx, userID string, except []byte, reason string) error {
	if except == nil {
		except = []byte{}
	}
	_, err := tx.Exec(ctx, `UPDATE sessions SET revoked_at = ?, revoke_reason = ? WHERE user_id = ? AND revoked_at IS NULL AND id_mac != ?`,
		tx.NowMicros(), reason, userID, except)
	return err
}

// PruneSessions deletes expired and revoked sessions older than a day (maintenance).
func (svc *Service) PruneSessions(ctx context.Context) error {
	return svc.s.W().TxNamed(ctx, "authn.session_prune", func(tx *store.Tx) error {
		cutoff := tx.Now.Add(-24 * time.Hour).UnixMicro()
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE absolute_expires_at < ? OR (revoked_at IS NOT NULL AND revoked_at < ?) OR last_seen_at < ?`,
			tx.NowMicros(), cutoff, tx.Now.Add(-svc.opts.SessionIdle-24*time.Hour).UnixMicro()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM oidc_states WHERE expires_at < ?`, tx.NowMicros()); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM setup_tokens WHERE expires_at < ?`, cutoff); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM mfa_totp_pending WHERE expires_at < ?`, tx.NowMicros()); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM throttles WHERE updated_at < ?`, tx.Now.Add(-2*throttleDecay).UnixMicro())
		return err
	})
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// CreateSessionTx creates a full session for an externally authenticated user (OIDC) inside tx.
func (svc *Service) CreateSessionTx(ctx context.Context, tx *store.Tx, userID, method string, meta SessionMeta) (string, error) {
	return svc.createSession(ctx, tx, userID, method, tx.Now, nil, false, false, meta)
}

// RotateTx replaces p's session with a freshly authenticated one (external re-authentication).
func (svc *Service) RotateTx(ctx context.Context, tx *store.Tx, p *Principal, meta SessionMeta) (string, error) {
	return svc.rotateTx(ctx, tx, p, tx.Now, p.MFAAt, meta)
}
