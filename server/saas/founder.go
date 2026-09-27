package saas

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

var (
	// ErrInvalidCredentials is the only error the Founder login ever reports;
	// it never reveals whether the username exists or the account is locked.
	ErrInvalidCredentials = errors.New("invalid credentials")

	// dummyHash is compared against when the username does not exist so that
	// response timing does not reveal account existence
	dummyHash, _ = bcrypt.GenerateFromPassword([]byte("culpos-timing-equalizer"), bcrypt.DefaultCost)
)

const founderBcryptCost = 12

// BootstrapFounder creates the platform Founder account from the deployment
// secret FOUNDER_BOOTSTRAP_PASSWORD. The plaintext is hashed with bcrypt and
// never stored, logged or returned.
func (svc *Service) BootstrapFounder(ctx context.Context) error {
	username := strings.ToLower(strings.TrimSpace(svc.cfg.FounderBootstrapUsername))
	if username == "" {
		return nil
	}

	existing, err := svc.repo.FounderByUsername(ctx, username)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}

	if svc.cfg.FounderBootstrapPassword == "" {
		if existing == nil {
			svc.log.Warn("Founder account not bootstrapped: FOUNDER_BOOTSTRAP_PASSWORD is not set")
		}
		return nil
	}

	if existing != nil && !svc.cfg.FounderBootstrapForceReset {
		return nil
	}

	if len(svc.cfg.FounderBootstrapPassword) < 12 {
		svc.log.Warn("FOUNDER_BOOTSTRAP_PASSWORD is shorter than 12 characters; rotate it after first sign-in")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(svc.cfg.FounderBootstrapPassword), founderBcryptCost)
	if err != nil {
		return err
	}

	if existing != nil {
		if err = svc.repo.SetFounderPassword(ctx, existing.ID, string(hash)); err != nil {
			return err
		}

		_ = svc.repo.DeleteFounderSessions(ctx, existing.ID)
		svc.audit(ctx, AuditEntry{ActorType: ActorSystem}.with("founder.password.reset", username, ResultSuccess, map[string]string{"source": "bootstrap"}))
		svc.log.Info("Founder password reset from bootstrap secret")
		return nil
	}

	f := &Founder{ID: svc.newID(), Username: username, PasswordHash: string(hash)}
	if err = svc.repo.CreateFounder(ctx, f); err != nil {
		return err
	}

	svc.audit(ctx, AuditEntry{ActorType: ActorSystem}.with("founder.bootstrap", username, ResultSuccess, nil))
	svc.log.Info("Founder account bootstrapped", zap.String("username", username))
	return nil
}

// FounderLogin authenticates the Founder and returns a new session token
func (svc *Service) FounderLogin(ctx context.Context, username, password, ip, ua string) (string, *FounderSession, error) {
	var (
		now = svc.now()
		f   *Founder
		err error
	)

	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) > 100 || len(password) > 256 {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		return "", nil, ErrInvalidCredentials
	}

	f, err = svc.repo.FounderByUsername(ctx, username)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", nil, err
	}

	if f == nil {
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(password))
		svc.audit(ctx, AuditEntry{ActorType: ActorFounder, ActorLabel: "(unknown)", Role: "Founder", IP: ip}.
			with("founder.login", "", ResultFailure, nil))
		return "", nil, ErrInvalidCredentials
	}

	pwErr := bcrypt.CompareHashAndPassword([]byte(f.PasswordHash), []byte(password))

	if f.LockedUntil != nil && now.Before(*f.LockedUntil) {
		svc.audit(ctx, founderActor(f, ip).with("founder.login", f.Username, ResultDenied, map[string]string{"reason": "locked"}))
		return "", nil, ErrInvalidCredentials
	}

	if pwErr != nil {
		_ = svc.repo.FounderLoginFailed(ctx, f.ID, svc.cfg.FounderMaxFailedLogins, svc.cfg.FounderLockoutDuration, now)
		svc.audit(ctx, founderActor(f, ip).with("founder.login", f.Username, ResultFailure, nil))
		return "", nil, ErrInvalidCredentials
	}

	token, err := randomToken(32)
	if err != nil {
		return "", nil, err
	}

	csrf, err := randomToken(32)
	if err != nil {
		return "", nil, err
	}

	if len(ua) > 250 {
		ua = ua[:250]
	}

	ses := &FounderSession{
		TokenHash:  hashToken(token),
		FounderID:  f.ID,
		CSRFToken:  csrf,
		CreatedAt:  now,
		ExpiresAt:  now.Add(svc.cfg.FounderSessionAbsoluteTTL),
		LastSeenAt: now,
		IP:         ip,
		UserAgent:  ua,
	}

	if err = svc.repo.CreateFounderSession(ctx, ses); err != nil {
		return "", nil, err
	}

	_ = svc.repo.FounderLoginSucceeded(ctx, f.ID, now)
	_ = svc.repo.DeleteExpiredFounderSessions(ctx, now)

	svc.audit(ctx, founderActor(f, ip).with("founder.login", f.Username, ResultSuccess, nil))
	return token, ses, nil
}

// FounderSession resolves and refreshes a Founder session from its token.
// Sessions expire after an absolute lifetime and after an idle period.
func (svc *Service) FounderSession(ctx context.Context, token string) (*Founder, *FounderSession, error) {
	if token == "" || len(token) > 200 {
		return nil, nil, ErrInvalidCredentials
	}

	var (
		now = svc.now()
		th  = hashToken(token)
	)

	ses, err := svc.repo.FounderSession(ctx, th)
	if err != nil {
		return nil, nil, ErrInvalidCredentials
	}

	if now.After(ses.ExpiresAt) || now.Sub(ses.LastSeenAt) > svc.cfg.FounderSessionIdleTTL {
		_ = svc.repo.DeleteFounderSession(ctx, th)
		return nil, nil, ErrInvalidCredentials
	}

	f, err := svc.repo.FounderByID(ctx, ses.FounderID)
	if err != nil {
		return nil, nil, ErrInvalidCredentials
	}

	if now.Sub(ses.LastSeenAt) > time.Minute {
		_ = svc.repo.TouchFounderSession(ctx, th, now)
	}

	return f, ses, nil
}

// FounderLogout invalidates the session server-side
func (svc *Service) FounderLogout(ctx context.Context, token, ip string) {
	f, _, err := svc.FounderSession(ctx, token)
	_ = svc.repo.DeleteFounderSession(ctx, hashToken(token))
	if err == nil {
		svc.audit(ctx, founderActor(f, ip).with("founder.logout", f.Username, ResultSuccess, nil))
	}
}

// FounderChangePassword rotates the Founder password and revokes all other sessions
func (svc *Service) FounderChangePassword(ctx context.Context, f *Founder, current, next, ip string) error {
	if bcrypt.CompareHashAndPassword([]byte(f.PasswordHash), []byte(current)) != nil {
		svc.audit(ctx, founderActor(f, ip).with("founder.password.change", f.Username, ResultFailure, nil))
		return userErr("Current password is incorrect.")
	}

	if len(next) < 12 || len(next) > 256 {
		return userErr("New password must be at least 12 characters.")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(next), founderBcryptCost)
	if err != nil {
		return err
	}

	if err = svc.repo.SetFounderPassword(ctx, f.ID, string(hash)); err != nil {
		return err
	}

	_ = svc.repo.DeleteFounderSessions(ctx, f.ID)
	svc.audit(ctx, founderActor(f, ip).with("founder.password.change", f.Username, ResultSuccess, nil))
	return nil
}

// ValidCSRF compares the submitted token with the session token in constant time
func (s *FounderSession) ValidCSRF(token string) bool {
	return s != nil && token != "" && subtle.ConstantTimeCompare([]byte(s.CSRFToken), []byte(token)) == 1
}
