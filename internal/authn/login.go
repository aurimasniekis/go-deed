// Adapted from HallPass internal/opauth/login.go (Apache-2.0).

package authn

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"strings"
	"time"

	"github.com/aurimasniekis/go-deed/internal/apperr"
	"github.com/aurimasniekis/go-deed/internal/audit"
	"github.com/aurimasniekis/go-deed/internal/clock"
	"github.com/aurimasniekis/go-deed/internal/keyring"
	"github.com/aurimasniekis/go-deed/internal/store"
)

// Throttling: after throttleFree failures the delay doubles per failure up to throttleMax; counters decay after a day
// without failures. There is no permanent lockout.
const (
	throttleFree  = 3
	throttleMax   = 15 * time.Minute
	throttleDecay = 24 * time.Hour
)

// Errors returned by login. Messages never reveal whether an account exists.
var (
	ErrThrottled          = apperr.New(apperr.CodeRateLimited, "too many attempts; try again later")
	ErrInvalidCredentials = apperr.New(apperr.CodeUnauthorized, "invalid email or password")
	ErrLocalDisabled      = apperr.New(apperr.CodeForbidden, "local sign-in is disabled on this installation")
	ErrInvalidCode        = apperr.New(apperr.CodeUnauthorized, "invalid code")
)

func ipKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return "ip:" + ip
	}
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return "ip:" + p.String()
	}
	return "ip:" + a.String()
}

func accountKey(email string) string { return "acct:" + strings.ToLower(strings.TrimSpace(email)) }

func mfaKey(userID string) string { return "mfa:" + userID }

func (svc *Service) throttled(ctx context.Context, keys ...string) (bool, error) {
	now := svc.opts.Clock.Now().UnixMicro()
	for _, k := range keys {
		var next int64
		err := svc.s.R().QueryRowContext(ctx, `SELECT next_allowed_at FROM throttles WHERE key = ?`, k).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return false, err
		}
		if next > now {
			return true, nil
		}
	}
	return false, nil
}

func recordFailure(ctx context.Context, tx *store.Tx, keys ...string) error {
	for _, k := range keys {
		failures := int64(1)
		var prev, updated int64
		err := tx.QueryRow(ctx, `SELECT failures, updated_at FROM throttles WHERE key = ?`, k).Scan(&prev, &updated)
		switch {
		case err == nil:
			if tx.Now.Sub(clock.FromMicros(updated)) < throttleDecay {
				failures = prev + 1
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		next := tx.Now
		if failures > throttleFree {
			d := time.Second << min(failures-throttleFree-1, 20)
			next = tx.Now.Add(min(d, throttleMax))
		}
		if _, err := tx.Exec(ctx, `INSERT INTO throttles (key, failures, next_allowed_at, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (key) DO UPDATE SET failures = excluded.failures, next_allowed_at = excluded.next_allowed_at, updated_at = excluded.updated_at`,
			k, failures, next.UnixMicro(), tx.NowMicros()); err != nil {
			return err
		}
	}
	return nil
}

func clearThrottle(ctx context.Context, tx *store.Tx, keys ...string) error {
	for _, k := range keys {
		if _, err := tx.Exec(ctx, `DELETE FROM throttles WHERE key = ?`, k); err != nil {
			return err
		}
	}
	return nil
}

// LoginResult is a successful first factor. When MFA is pending the session is Limited until VerifyMFA succeeds.
type LoginResult struct {
	Token      string
	Principal  *Principal
	MFAPending bool
	MustEnroll bool
}

// Login verifies an email and password. Unknown accounts, wrong passwords and disabled accounts all return
// ErrInvalidCredentials after the same work (a dummy hash for unknown accounts).
func (svc *Service) Login(ctx context.Context, email, password string, meta SessionMeta) (*LoginResult, error) {
	res, err := svc.login(ctx, email, password, meta)
	svc.observe(MethodPassword, err)
	return res, err
}

func (svc *Service) observe(method string, err error) {
	switch {
	case err == nil:
		svc.opts.Metrics.Login(method, "success")
	case errors.Is(err, ErrThrottled):
		svc.opts.Metrics.Login(method, "throttled")
	case apperr.CodeOf(err) == apperr.CodeUnauthorized:
		svc.opts.Metrics.Login(method, "failure")
	case errors.Is(err, ErrLocalDisabled):
	default:
		svc.opts.Metrics.Login(method, "error")
	}
}

func (svc *Service) login(ctx context.Context, email, password string, meta SessionMeta) (*LoginResult, error) {
	if !svc.opts.LocalEnabled {
		return nil, ErrLocalDisabled
	}
	acctK, ipK := accountKey(email), ipKey(meta.IP)
	if t, err := svc.throttled(ctx, acctK, ipK); err != nil {
		return nil, err
	} else if t {
		return nil, ErrThrottled
	}
	pw, perr := NormalizePassword(password, 1)
	norm, eerr := NormalizeEmail(email)
	var userID, status, phc string
	var paramsVersion int64
	found := false
	if eerr == nil {
		err := svc.s.R().QueryRowContext(ctx, `SELECT u.id, u.status, c.password_phc, c.params_version FROM users u
			JOIN local_credentials c ON c.user_id = u.id WHERE u.email = ?`, norm).Scan(&userID, &status, &phc, &paramsVersion)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		found = err == nil
	}
	if !found {
		phc = dummyHash()
	}
	if perr != nil {
		pw = []byte("invalid")
	}
	if err := svc.acquire(ctx); err != nil {
		return nil, err
	}
	ok, verr := VerifyPassword(pw, phc)
	svc.release()
	if !found || !ok || verr != nil || perr != nil || status != "active" {
		err := svc.s.W().TxNamed(ctx, "authn.login_failed", func(tx *store.Tx) error {
			if err := recordFailure(ctx, tx, acctK, ipK); err != nil {
				return err
			}
			_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: userID}, Action: "auth.login",
				Outcome: audit.OutcomeFailure, ReasonCode: "invalid_credentials", SourceIP: meta.IP, AuthMethod: MethodPassword})
			return err
		})
		if err != nil {
			return nil, err
		}
		return nil, ErrInvalidCredentials
	}
	u, err := svc.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	limited := u.MFAEnrolled || svc.MFARequired(u)
	needsEnroll := !u.MFAEnrolled && svc.MFARequired(u)
	var token string
	// Upgrading the hash parameters re-hashes outside the write transaction: Argon2id must never hold the writer lock.
	var rehashed string
	if uint32(paramsVersion) != svc.params().Version && svc.acquire(ctx) == nil { //nolint:gosec // small version
		if fresh, err := HashPassword(pw, svc.params()); err == nil {
			rehashed = fresh
		}
		svc.release()
	}
	err = svc.s.W().TxNamed(ctx, "authn.login", func(tx *store.Tx) error {
		if err := clearThrottle(ctx, tx, acctK); err != nil {
			return err
		}
		if rehashed != "" {
			if err := setPasswordTx(ctx, tx, userID, rehashed, svc.params().Version); err != nil {
				return err
			}
		}
		var err error
		if token, err = svc.createSession(ctx, tx, userID, MethodPassword, tx.Now, nil, limited, needsEnroll, meta); err != nil {
			return err
		}
		action := "auth.login"
		if limited {
			action = "auth.login_first_factor"
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: userID}, Action: action,
			SourceIP: meta.IP, AuthMethod: MethodPassword})
		return err
	})
	if err != nil {
		return nil, err
	}
	p, err := svc.Authenticate(ctx, token)
	if err != nil {
		return nil, err
	}
	return &LoginResult{Token: token, Principal: p, MFAPending: limited && !needsEnroll, MustEnroll: needsEnroll}, nil
}

// VerifyMFA completes a Limited session with a TOTP or recovery code and returns the rotated session token.
func (svc *Service) VerifyMFA(ctx context.Context, p *Principal, code string, meta SessionMeta) (string, error) {
	if !p.Limited || p.NeedsEnroll {
		return "", apperr.New(apperr.CodePrecondition, "no second factor is pending for this session")
	}
	ok, method, err := svc.verifySecondFactor(ctx, p.UserID, code, meta.IP, "login")
	if err != nil {
		return "", err
	}
	if !ok {
		svc.opts.Metrics.Login("totp", "failure")
		return "", ErrInvalidCode
	}
	var token string
	err = svc.s.W().TxNamed(ctx, "authn.mfa_verified", func(tx *store.Tx) error {
		mfaAt := tx.Now
		var err error
		if token, err = svc.rotateTx(ctx, tx, p, tx.Now, &mfaAt, meta); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "auth.login", SourceIP: meta.IP, AuthMethod: p.Method,
			Details: map[string]any{"second_factor": method}})
		return err
	})
	if err == nil {
		svc.opts.Metrics.Login("totp", "success")
	}
	return token, err
}

// Reauth refreshes the authentication time of a full session after re-entering the password (and a code when MFA
// is enrolled). OIDC sessions re-authenticate through the provider instead (prompt=login).
func (svc *Service) Reauth(ctx context.Context, p *Principal, password, code string, meta SessionMeta) (string, error) {
	if p.Limited || p.TokenID != "" {
		return "", ErrUnauthorized
	}
	if t, err := svc.throttled(ctx, mfaKey(p.UserID)); err != nil {
		return "", err
	} else if t {
		return "", ErrThrottled
	}
	u, err := svc.GetUser(ctx, p.UserID)
	if err != nil {
		return "", err
	}
	if !u.HasPassword {
		return "", apperr.New(apperr.CodePrecondition, "this account has no password; re-authenticate with your identity provider")
	}
	var phc string
	if err := svc.s.R().QueryRowContext(ctx, `SELECT password_phc FROM local_credentials WHERE user_id = ?`, p.UserID).Scan(&phc); err != nil {
		return "", err
	}
	pw, _ := NormalizePassword(password, 1)
	if err := svc.acquire(ctx); err != nil {
		return "", err
	}
	ok, verr := VerifyPassword(pw, phc)
	svc.release()
	if !ok || verr != nil {
		_ = svc.s.W().TxNamed(ctx, "authn.reauth_failed", func(tx *store.Tx) error {
			if err := recordFailure(ctx, tx, mfaKey(p.UserID)); err != nil {
				return err
			}
			_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "auth.reauth", Outcome: audit.OutcomeFailure,
				ReasonCode: "invalid_password", SourceIP: meta.IP})
			return err
		})
		return "", ErrInvalidCredentials
	}
	var mfaAt *time.Time
	if u.MFAEnrolled {
		ok, _, err := svc.verifySecondFactor(ctx, p.UserID, code, meta.IP, "reauth")
		if err != nil {
			return "", err
		}
		if !ok {
			return "", ErrInvalidCode
		}
		now := svc.opts.Clock.Now()
		mfaAt = &now
	}
	var token string
	err = svc.s.W().TxNamed(ctx, "authn.reauth", func(tx *store.Tx) error {
		var err error
		if token, err = svc.rotateTx(ctx, tx, p, tx.Now, mfaAt, meta); err != nil {
			return err
		}
		_, err = svc.audit.Append(ctx, tx, audit.Event{Actor: p.Actor(), Action: "auth.reauth", SourceIP: meta.IP, AuthMethod: p.Method})
		return err
	})
	return token, err
}

// verifySecondFactor checks a TOTP code (no step reuse) or consumes a recovery code, honoring the MFA throttle.
func (svc *Service) verifySecondFactor(ctx context.Context, userID, code, ip, purpose string) (bool, string, error) {
	if t, err := svc.throttled(ctx, mfaKey(userID)); err != nil {
		return false, "", err
	} else if t {
		return false, "", ErrThrottled
	}
	code = strings.TrimSpace(code)
	ok, method := false, ""
	err := svc.s.W().TxNamed(ctx, "authn.second_factor", func(tx *store.Tx) error {
		ok, method = false, ""
		if len(code) == totpDigits {
			var enc []byte
			var last int64
			err := tx.QueryRow(ctx, `SELECT secret_enc, last_step FROM mfa_totp WHERE user_id = ?`, userID).Scan(&enc, &last)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil {
				secret, err := svc.kr.Open(keyring.PurposeField, totpAAD(userID), enc)
				if err != nil {
					return err
				}
				if step, valid := VerifyTOTP(secret, code, tx.Now, last); valid {
					if _, err := tx.Exec(ctx, `UPDATE mfa_totp SET last_step = ? WHERE user_id = ?`, step, userID); err != nil {
						return err
					}
					ok, method = true, "totp"
				}
			}
		} else if code != "" {
			macs, err := svc.recoveryMACs(userID, code)
			if err != nil {
				return err
			}
			for _, m := range macs {
				n, err := tx.ExecN(ctx, `UPDATE recovery_codes SET used_at = ? WHERE code_mac = ? AND user_id = ? AND used_at IS NULL`, tx.NowMicros(), m, userID)
				if err != nil {
					return err
				}
				if n == 1 {
					ok, method = true, "recovery_code"
					break
				}
			}
		}
		if ok {
			if err := clearThrottle(ctx, tx, mfaKey(userID)); err != nil {
				return err
			}
			if method == "recovery_code" {
				_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: userID}, Action: "auth.recovery_code_used", SourceIP: ip})
				return err
			}
			return nil
		}
		if err := recordFailure(ctx, tx, mfaKey(userID)); err != nil {
			return err
		}
		_, err := svc.audit.Append(ctx, tx, audit.Event{Actor: audit.Actor{Type: audit.ActorUser, ID: userID}, Action: "auth.mfa",
			Outcome: audit.OutcomeFailure, ReasonCode: "invalid_code", SourceIP: ip, Details: map[string]any{"purpose": purpose}})
		return err
	})
	return ok, method, err
}
