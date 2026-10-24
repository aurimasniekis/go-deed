package authn

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math/big"
	"slices"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/ids"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Token prefixes (ADR-005). The secret part is 43 base62 characters (≈256 bits).
const (
	APITokenPrefix    = "deed_at_"
	SourceTokenPrefix = "deed_st_"
	tokenSecretLen    = 43
)

const base62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

func newTokenSecret() string {
	var sb strings.Builder
	max := big.NewInt(int64(len(base62)))
	for range tokenSecretLen {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			panic("crypto/rand: " + err.Error())
		}
		sb.WriteByte(base62[n.Int64()])
	}
	return sb.String()
}

// ValidTokenShape reports whether raw looks like a Deed bearer token (without revealing it in errors).
func ValidTokenShape(raw string) bool {
	var rest string
	switch {
	case strings.HasPrefix(raw, APITokenPrefix):
		rest = raw[len(APITokenPrefix):]
	case strings.HasPrefix(raw, SourceTokenPrefix):
		rest = raw[len(SourceTokenPrefix):]
	default:
		return false
	}
	if len(rest) != tokenSecretLen {
		return false
	}
	for i := range len(rest) {
		if !strings.ContainsRune(base62, rune(rest[i])) {
			return false
		}
	}
	return true
}

func (svc *Service) tokenMAC(raw string) ([]byte, error) {
	return svc.kr.MAC(keyring.PurposeToken, []byte("bearer|"+raw))
}

// Token describes an API or source token (never its secret).
type Token struct {
	ID               string     `json:"id"`
	Kind             string     `json:"kind"`
	Name             string     `json:"name"`
	UserID           string     `json:"user_id,omitempty"`
	SourceID         string     `json:"source_id,omitempty"`
	InstallationRole string     `json:"installation_role,omitempty"`
	ProjectRole      string     `json:"project_role,omitempty"`
	Projects         []string   `json:"projects"`
	CreatedBy        string     `json:"created_by,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	LastUsedAt       *time.Time `json:"last_used_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
}

// CreateAPITokenInput creates a user API token.
type CreateAPITokenInput struct {
	Name string
	// InstallationRole is "", admin or auditor and never exceeds the creator's role.
	InstallationRole string
	// ProjectRole caps project roles ("" = the creator's roles).
	ProjectRole string
	// Projects restricts the token to these project IDs (empty = every project the creator can access).
	Projects  []string
	ExpiresIn time.Duration
}

var installationRank = map[string]int{"": 0, RoleAuditor: 1, RoleAdmin: 2, RoleOwner: 3}

// CreateAPIToken issues a token for p's user. The raw token is returned once.
func (svc *Service) CreateAPIToken(ctx context.Context, p *Principal, in CreateAPITokenInput) (*Token, string, error) {
	if p.TokenID != "" || p.Limited {
		return nil, "", apperr.New(apperr.CodeForbidden, "tokens can only be created from an interactive sign-in")
	}
	if err := svc.RequireRecentAuth(p); err != nil {
		return nil, "", err
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		return nil, "", apperr.New(apperr.CodeInvalid, "name must be 1–100 characters")
	}
	switch in.InstallationRole {
	case "":
	case RoleAdmin, RoleAuditor:
		if installationRank[p.InstallationRole] < installationRank[in.InstallationRole] ||
			(in.InstallationRole == RoleAuditor && p.InstallationRole == "") {
			return nil, "", apperr.New(apperr.CodeForbidden, "a token cannot exceed your own installation role")
		}
	default:
		return nil, "", apperr.New(apperr.CodeInvalid, "installation_role must be admin, auditor or empty")
	}
	if in.ProjectRole != "" && !ValidProjectRole(in.ProjectRole) {
		return nil, "", apperr.New(apperr.CodeInvalid, "project_role must be viewer, editor or admin")
	}
	if in.Projects == nil {
		in.Projects = []string{}
	}
	if p.InstallationRole != RoleOwner && p.InstallationRole != RoleAdmin {
		for _, pid := range in.Projects {
			if _, ok := p.ProjectRoles[pid]; !ok {
				return nil, "", apperr.New(apperr.CodeForbidden, "you are not a member of project %s", pid)
			}
		}
	}
	exp, err := svc.expiry(in.ExpiresIn)
	if err != nil {
		return nil, "", err
	}
	raw := APITokenPrefix + newTokenSecret()
	t := &Token{ID: ids.New(ids.APIToken), Kind: "user", Name: in.Name, UserID: p.UserID, InstallationRole: in.InstallationRole,
		ProjectRole: in.ProjectRole, Projects: in.Projects, CreatedBy: p.UserID}
	err = svc.s.W().TxNamed(ctx, "authn.token_create", func(tx *store.Tx) error {
		t.CreatedAt, t.ExpiresAt = tx.Now, tx.Now.Add(exp)
		if err := svc.insertToken(ctx, tx, t, raw); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "token.created", Target: audit.Target{Type: "api_token", ID: t.ID},
			Details: map[string]any{"name": t.Name, "installation_role": t.InstallationRole, "project_role": t.ProjectRole, "projects": t.Projects,
				"expires_at": t.ExpiresAt.Format(time.RFC3339)}})
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return t, raw, nil
}

func (svc *Service) expiry(d time.Duration) (time.Duration, error) {
	if d == 0 {
		d = 90 * 24 * time.Hour
	}
	if d < time.Hour || d > svc.opts.MaxTokenLifetime {
		return 0, apperr.New(apperr.CodeInvalid, "expiry must be between 1 hour and %s", svc.opts.MaxTokenLifetime)
	}
	return d, nil
}

// CreateSourceTokenTx issues a source token inside the caller's transaction (sources service). Raw token returned once.
func (svc *Service) CreateSourceTokenTx(ctx context.Context, tx *store.Tx, actor audit.Actor, sourceID, name string, expiresIn time.Duration) (*Token, string, error) {
	exp, err := svc.expiry(expiresIn)
	if err != nil {
		return nil, "", err
	}
	if name = strings.TrimSpace(name); name == "" || len(name) > 100 {
		return nil, "", apperr.New(apperr.CodeInvalid, "name must be 1–100 characters")
	}
	raw := SourceTokenPrefix + newTokenSecret()
	t := &Token{ID: ids.New(ids.SourceToken), Kind: "source", Name: name, SourceID: sourceID, Projects: []string{}, CreatedBy: actor.ID,
		CreatedAt: tx.Now, ExpiresAt: tx.Now.Add(exp)}
	if err := svc.insertToken(ctx, tx, t, raw); err != nil {
		return nil, "", err
	}
	_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "source.token_created", Target: audit.Target{Type: "source", ID: sourceID},
		Details: map[string]any{"token_id": t.ID, "name": name, "expires_at": t.ExpiresAt.Format(time.RFC3339)}})
	return t, raw, err
}

func (svc *Service) insertToken(ctx context.Context, tx *store.Tx, t *Token, raw string) error {
	mac, err := svc.tokenMAC(raw)
	if err != nil {
		return err
	}
	pj, _ := json.Marshal(t.Projects)
	_, err = tx.Exec(ctx, `INSERT INTO api_tokens (id, token_mac, kind, name, user_id, source_id, installation_role, project_role, projects_json,
		created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.ID, mac, t.Kind, t.Name, nullStr(t.UserID), nullStr(t.SourceID),
		nullStr(t.InstallationRole), nullStr(t.ProjectRole), string(pj), nullStr(t.CreatedBy), t.CreatedAt.UnixMicro(), t.ExpiresAt.UnixMicro())
	return err
}

const tokenCols = `id, kind, name, COALESCE(user_id, ''), COALESCE(source_id, ''), COALESCE(installation_role, ''), COALESCE(project_role, ''),
	projects_json, COALESCE(created_by, ''), created_at, expires_at, last_used_at, revoked_at`

func scanToken(r rowScanner) (*Token, error) {
	var t Token
	var pj string
	var c, e int64
	var lu, rv sql.NullInt64
	if err := r.Scan(&t.ID, &t.Kind, &t.Name, &t.UserID, &t.SourceID, &t.InstallationRole, &t.ProjectRole, &pj, &t.CreatedBy, &c, &e, &lu, &rv); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(pj), &t.Projects)
	if t.Projects == nil {
		t.Projects = []string{}
	}
	t.CreatedAt, t.ExpiresAt = clock.FromMicros(c), clock.FromMicros(e)
	if lu.Valid {
		v := clock.FromMicros(lu.Int64)
		t.LastUsedAt = &v
	}
	if rv.Valid {
		v := clock.FromMicros(rv.Int64)
		t.RevokedAt = &v
	}
	return &t, nil
}

// ListTokens lists a user's API tokens (userID) or a source's tokens (sourceID).
func (svc *Service) ListTokens(ctx context.Context, userID, sourceID string) ([]*Token, error) {
	q, arg := `SELECT `+tokenCols+` FROM api_tokens WHERE kind = 'user' AND user_id = ? ORDER BY created_at DESC`, userID
	if sourceID != "" {
		q, arg = `SELECT `+tokenCols+` FROM api_tokens WHERE kind = 'source' AND source_id = ? ORDER BY created_at DESC`, sourceID
	}
	rows, err := svc.s.R().QueryContext(ctx, q, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// GetToken returns a token by ID.
func (svc *Service) GetToken(ctx context.Context, id string) (*Token, error) {
	t, err := scanToken(svc.s.R().QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, apperr.New(apperr.CodeNotFound, "token not found")
	}
	return t, err
}

// RevokeToken revokes a token. Callers check that the actor owns it or administers its source.
func (svc *Service) RevokeToken(ctx context.Context, actor audit.Actor, id string) error {
	return svc.s.W().TxNamed(ctx, "authn.token_revoke", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, tx.NowMicros(), id)
		if err != nil {
			return err
		}
		if n == 0 {
			return apperr.New(apperr.CodeNotFound, "token not found or already revoked")
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: actor, Action: "token.revoked", Target: audit.Target{Type: "api_token", ID: id}})
		return err
	})
}

// RevokeSourceTokensTx revokes every token of a source (source deletion).
func RevokeSourceTokensTx(ctx context.Context, tx *store.Tx, sourceID string) error {
	_, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = ? WHERE source_id = ? AND revoked_at IS NULL`, tx.NowMicros(), sourceID)
	return err
}

// AuthenticateToken resolves a bearer token to a principal. User tokens act for their (active) user with the token's
// caps; source tokens carry only their source ID.
func (svc *Service) AuthenticateToken(ctx context.Context, raw, ip string) (*Principal, error) {
	if !ValidTokenShape(raw) {
		return nil, ErrUnauthorized
	}
	macs, err := svc.kr.MACAll(keyring.PurposeToken, []byte("bearer|"+raw))
	if err != nil {
		return nil, err
	}
	var t *Token
	for _, m := range macs {
		t, err = scanToken(svc.s.R().QueryRowContext(ctx, `SELECT `+tokenCols+` FROM api_tokens WHERE token_mac = ?`, m))
		if err == nil {
			break
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	if t == nil {
		return nil, ErrUnauthorized
	}
	now := svc.opts.Clock.Now()
	if t.RevokedAt != nil || !now.Before(t.ExpiresAt) {
		return nil, ErrUnauthorized
	}
	if (t.Kind == "source") != strings.HasPrefix(raw, SourceTokenPrefix) {
		return nil, ErrUnauthorized
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) > time.Minute {
		_ = svc.s.W().TxNamed(context.WithoutCancel(ctx), "authn.token_touch", func(tx *store.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE api_tokens SET last_used_at = ?, last_used_ip = ? WHERE id = ?`, tx.NowMicros(), ip, t.ID)
			return err
		})
	}
	if t.Kind == "source" {
		return &Principal{TokenID: t.ID, SourceID: t.SourceID, Method: MethodSource, AuthAt: t.CreatedAt, ExpiresAt: t.ExpiresAt,
			DisplayName: "source token " + t.Name, ProjectRoles: map[string]string{}}, nil
	}
	p := &Principal{UserID: t.UserID, TokenID: t.ID, Method: MethodToken, AuthAt: t.CreatedAt, ExpiresAt: t.ExpiresAt}
	if err := svc.loadUser(ctx, p); err != nil {
		return nil, err
	}
	// Cap the installation role: a token never exceeds its own grant or the user's current role.
	userRole := p.InstallationRole
	p.InstallationRole = ""
	switch t.InstallationRole {
	case RoleAdmin:
		if userRole == RoleOwner || userRole == RoleAdmin {
			p.InstallationRole = RoleAdmin
		}
	case RoleAuditor:
		if userRole != "" {
			p.InstallationRole = RoleAuditor
		}
	}
	// Project access: implicit access through the user's installation role is kept only when the token carries an
	// installation role; otherwise only explicit memberships count.
	p.TokenProjectRole = t.ProjectRole
	if len(t.Projects) > 0 {
		p.TokenProjects = slices.Clone(t.Projects)
	}
	if p.InstallationRole == "" && (userRole == RoleOwner || userRole == RoleAdmin) && len(t.Projects) > 0 {
		// Owners/admins may scope tokens to projects they are not explicit members of: grant the capped role there.
		for _, pid := range t.Projects {
			if _, ok := p.ProjectRoles[pid]; !ok {
				p.ProjectRoles[pid] = ProjectAdmin
			}
		}
	}
	return p, nil
}
