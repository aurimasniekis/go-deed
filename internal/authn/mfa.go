// Adapted from HallPass internal/opauth/mfa.go (Apache-2.0).

package authn

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// RecoveryCodeCount is how many single-use recovery codes are issued.
const RecoveryCodeCount = 10

// TOTPPendingTTL bounds an enrollment in progress.
const TOTPPendingTTL = 15 * time.Minute

func totpAAD(userID string) []byte { return keyring.FieldAAD("mfa_totp", "secret_enc", userID) }

func normRecovery(code string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}

func (svc *Service) recoveryMACs(userID, code string) ([][]byte, error) {
	return svc.kr.MACAll(keyring.PurposeToken, []byte("recovery|"+userID+"|"+normRecovery(code)))
}

// TOTPEnrollment is shown once for the authenticator app.
type TOTPEnrollment struct {
	Secret string `json:"secret"`
	URI    string `json:"otpauth_uri"`
}

// ErrCompleteMFAFirst refuses enrollment on a session that still has to verify its existing second factor.
var ErrCompleteMFAFirst = apperr.New(apperr.CodeForbidden, "verify your current second factor before changing it")

// enrollGate: a Limited session may enroll only when policy requires a first factor; replacing a factor needs a
// recent full authentication.
func (svc *Service) enrollGate(p *Principal) error {
	switch {
	case p.TokenID != "":
		return ErrUnauthorized
	case p.Limited && p.NeedsEnroll:
		return nil
	case p.Limited:
		return ErrCompleteMFAFirst
	default:
		return svc.RequireRecentAuth(p)
	}
}

// BeginTOTP starts (or restarts) enrollment; a confirmed factor stays in force until ConfirmTOTP replaces it.
func (svc *Service) BeginTOTP(ctx context.Context, p *Principal) (*TOTPEnrollment, error) {
	if err := svc.enrollGate(p); err != nil {
		return nil, err
	}
	secret, err := NewTOTPSecret()
	if err != nil {
		return nil, err
	}
	sealed, err := svc.kr.Seal(keyring.PurposeField, totpAAD(p.UserID), secret)
	if err != nil {
		return nil, err
	}
	err = svc.s.W().TxNamed(ctx, "authn.totp_begin", func(tx *store.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO mfa_totp_pending (user_id, secret_enc, created_at, expires_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id) DO UPDATE SET secret_enc = excluded.secret_enc, created_at = excluded.created_at, expires_at = excluded.expires_at`,
			p.UserID, sealed, tx.NowMicros(), tx.Now.Add(TOTPPendingTTL).UnixMicro())
		if err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "user.mfa_enrollment_started", Target: audit.Target{Type: "user", ID: p.UserID}})
		return err
	})
	if err != nil {
		return nil, err
	}
	account := p.Email
	if account == "" {
		account = p.DisplayName
	}
	return &TOTPEnrollment{Secret: EncodeTOTPSecret(secret), URI: TOTPURI(secret, svc.opts.Issuer, account)}, nil
}

var errStartEnrollment = apperr.New(apperr.CodePrecondition, "start TOTP enrollment first")

// ConfirmTOTP completes enrollment with a valid code, issues fresh recovery codes (returned once) and revokes the
// user's other sessions. A Limited enrollment session is upgraded to a full session (the returned token replaces it).
func (svc *Service) ConfirmTOTP(ctx context.Context, p *Principal, code string, meta SessionMeta) ([]string, string, error) {
	if err := svc.enrollGate(p); err != nil {
		return nil, "", err
	}
	if t, err := svc.throttled(ctx, mfaKey(p.UserID)); err != nil {
		return nil, "", err
	} else if t {
		return nil, "", ErrThrottled
	}
	var enc []byte
	var exp int64
	err := svc.s.R().QueryRowContext(ctx, `SELECT secret_enc, expires_at FROM mfa_totp_pending WHERE user_id = ?`, p.UserID).Scan(&enc, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", errStartEnrollment
	} else if err != nil {
		return nil, "", err
	}
	now := svc.opts.Clock.Now()
	if !now.Before(clock.FromMicros(exp)) {
		return nil, "", errStartEnrollment
	}
	secret, err := svc.kr.Open(keyring.PurposeField, totpAAD(p.UserID), enc)
	if err != nil {
		return nil, "", err
	}
	step, ok := VerifyTOTP(secret, code, now, 0)
	if !ok {
		_ = svc.s.W().TxNamed(ctx, "authn.totp_confirm_failed", func(tx *store.Tx) error { return recordFailure(ctx, tx, mfaKey(p.UserID)) })
		return nil, "", apperr.New(apperr.CodeInvalid, "invalid code; check your authenticator's clock")
	}
	codes := newRecoveryCodes()
	var token string
	err = svc.s.W().TxNamed(ctx, "authn.totp_confirm", func(tx *store.Tx) error {
		n, err := tx.ExecN(ctx, `DELETE FROM mfa_totp_pending WHERE user_id = ? AND secret_enc = ?`, p.UserID, enc)
		if err != nil {
			return err
		}
		if n != 1 {
			return errStartEnrollment
		}
		if _, err := tx.Exec(ctx, `INSERT INTO mfa_totp (user_id, secret_enc, confirmed_at, last_step) VALUES (?, ?, ?, ?)
			ON CONFLICT (user_id) DO UPDATE SET secret_enc = excluded.secret_enc, confirmed_at = excluded.confirmed_at, last_step = excluded.last_step`,
			p.UserID, enc, tx.NowMicros(), step); err != nil {
			return err
		}
		if err := svc.storeRecoveryCodes(ctx, tx, p.UserID, codes); err != nil {
			return err
		}
		if err := clearThrottle(ctx, tx, mfaKey(p.UserID)); err != nil {
			return err
		}
		if err := revokeUserSessionsTx(ctx, tx, p.UserID, p.sessionMAC, "mfa_changed"); err != nil {
			return err
		}
		mfaAt := tx.Now
		authAt := p.AuthAt
		if p.Limited {
			authAt = tx.Now
		}
		if token, err = svc.rotateTx(ctx, tx, p, authAt, &mfaAt, meta); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "user.mfa_enrolled", Target: audit.Target{Type: "user", ID: p.UserID}, SourceIP: meta.IP})
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return codes, token, nil
}

// DisableTOTP removes the second factor (refused when policy requires MFA for the user).
func (svc *Service) DisableTOTP(ctx context.Context, p *Principal) error {
	if err := svc.RequireRecentAuth(p); err != nil {
		return err
	}
	u, err := svc.GetUser(ctx, p.UserID)
	if err != nil {
		return err
	}
	if svc.MFARequired(u) {
		return apperr.New(apperr.CodeForbidden, "MFA is mandatory for this account")
	}
	return svc.s.W().TxNamed(ctx, "authn.totp_disable", func(tx *store.Tx) error {
		for _, q := range []string{`DELETE FROM mfa_totp WHERE user_id = ?`, `DELETE FROM mfa_totp_pending WHERE user_id = ?`, `DELETE FROM recovery_codes WHERE user_id = ?`} {
			if _, err := tx.Exec(ctx, q, p.UserID); err != nil {
				return err
			}
		}
		if err := revokeUserSessionsTx(ctx, tx, p.UserID, p.sessionMAC, "mfa_changed"); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "user.mfa_disabled", Target: audit.Target{Type: "user", ID: p.UserID}})
		return err
	})
}

// RegenerateRecoveryCodes replaces every recovery code (recent authentication required).
func (svc *Service) RegenerateRecoveryCodes(ctx context.Context, p *Principal) ([]string, error) {
	if err := svc.RequireRecentAuth(p); err != nil {
		return nil, err
	}
	u, err := svc.GetUser(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	if !u.MFAEnrolled {
		return nil, apperr.New(apperr.CodePrecondition, "enroll TOTP first")
	}
	codes := newRecoveryCodes()
	err = svc.s.W().TxNamed(ctx, "authn.recovery_regen", func(tx *store.Tx) error {
		if err := svc.storeRecoveryCodes(ctx, tx, p.UserID, codes); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "user.recovery_codes_regenerated", Target: audit.Target{Type: "user", ID: p.UserID}})
		return err
	})
	return codes, err
}

// RemainingRecoveryCodes counts unused recovery codes.
func (svc *Service) RemainingRecoveryCodes(ctx context.Context, userID string) (int, error) {
	var n int
	err := svc.s.R().QueryRowContext(ctx, `SELECT COUNT(*) FROM recovery_codes WHERE user_id = ? AND used_at IS NULL`, userID).Scan(&n)
	return n, err
}

const recoveryAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

func newRecoveryCodes() []string {
	codes := make([]string, RecoveryCodeCount)
	for i := range codes {
		b := randomBytes(10)
		var sb strings.Builder
		for j, c := range b {
			if j == 5 {
				sb.WriteByte('-')
			}
			sb.WriteByte(recoveryAlphabet[int(c)%len(recoveryAlphabet)])
		}
		codes[i] = sb.String()
	}
	return codes
}

func (svc *Service) storeRecoveryCodes(ctx context.Context, tx *store.Tx, userID string, codes []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, c := range codes {
		mac, err := svc.kr.MAC(keyring.PurposeToken, []byte("recovery|"+userID+"|"+normRecovery(c)))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO recovery_codes (code_mac, user_id, created_at) VALUES (?, ?, ?)`, mac, userID, tx.NowMicros()); err != nil {
			return err
		}
	}
	return nil
}
