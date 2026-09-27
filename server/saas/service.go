package saas

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

type (
	Service struct {
		cfg      Config
		repo     *Repo
		stripe   StripeAPI
		platform Platform
		mailer   Mailer
		log      *zap.Logger
		tpl      *template.Template

		// signs resume/status links handed to the browser
		secret []byte

		// identity of currently authenticated application user (auth session)
		sessionUser func(r httpRequest) uint64

		now   func() time.Time
		newID func() uint64

		cache *accessCache

		activityMu sync.Mutex
		activity   map[uint64]time.Time
	}

	SignupInput struct {
		CompanyName string
		FirstName   string
		LastName    string
		Email       string
		Password    string
		IP          string
	}

	// UserError is safe to show to the user
	UserError struct{ msg string }
)

func (e UserError) Error() string { return e.msg }

func userErr(f string, a ...any) error { return UserError{msg: fmt.Sprintf(f, a...)} }

var (
	ErrNotAllowed = UserError{msg: "You are not allowed to perform this action."}

	slugCleaner = regexp.MustCompile(`[^a-z0-9]+`)
	emailRE     = regexp.MustCompile(`^[a-zA-Z0-9.!#$%&'*+/=?^_{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)+$`)
)

// NewService constructs the commercial layer service
func NewService(cfg Config, repo *Repo, stripe StripeAPI, platform Platform, mailer Mailer, log *zap.Logger, secret []byte, newID func() uint64) (*Service, error) {
	if log == nil {
		log = zap.NewNop()
	}

	if len(secret) < 32 {
		secret = make([]byte, 32)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
	}

	svc := &Service{
		cfg:      cfg,
		repo:     repo,
		stripe:   stripe,
		platform: platform,
		mailer:   mailer,
		log:      log.Named("culpos"),
		secret:   secret,
		now:      func() time.Time { return time.Now().UTC() },
		newID:    newID,
		cache:    newAccessCache(15 * time.Second),
		activity: make(map[uint64]time.Time),
	}

	tpl, err := parseTemplates(cfg.Brand)
	if err != nil {
		return nil, err
	}

	svc.tpl = tpl
	return svc, nil
}

// Config returns the service configuration
func (svc *Service) Config() Config { return svc.cfg }

// Repo returns the repository
func (svc *Service) Repo() *Repo { return svc.repo }

// SetSessionUserResolver sets the function used to resolve the currently
// signed-in application user from the auth session
func (svc *Service) SetSessionUserResolver(fn func(r httpRequest) uint64) {
	svc.sessionUser = fn
}

// Signup ----------------------------------------------------------------

// Signup creates a pending company and its (suspended) owner and returns the
// Stripe Checkout URL. No access is granted until Stripe confirms payment
// through a verified webhook.
func (svc *Service) Signup(ctx context.Context, in SignupInput) (string, *Company, error) {
	in.CompanyName = strings.TrimSpace(in.CompanyName)
	in.FirstName = strings.TrimSpace(in.FirstName)
	in.LastName = strings.TrimSpace(in.LastName)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))

	switch {
	case len(in.CompanyName) < 2 || len(in.CompanyName) > 100:
		return "", nil, userErr("Company name must be between 2 and 100 characters.")
	case in.FirstName == "" || len(in.FirstName) > 60:
		return "", nil, userErr("Enter the owner's first name.")
	case in.LastName == "" || len(in.LastName) > 60:
		return "", nil, userErr("Enter the owner's last name.")
	case !emailRE.MatchString(in.Email) || len(in.Email) > 254:
		return "", nil, userErr("Enter a valid email address.")
	case len(in.Password) < 8 || len(in.Password) > 256 || !svc.platform.CheckPasswordStrength(in.Password):
		return "", nil, userErr("Choose a stronger password (at least 8 characters).")
	}

	if !svc.cfg.StripeConfigured() {
		svc.log.Error("signup attempted but Stripe is not configured")
		return "", nil, userErr("Online signup is temporarily unavailable. Please contact support.")
	}

	exists, err := svc.platform.UserExists(ctx, in.Email)
	if err != nil {
		return "", nil, err
	}

	if exists {
		return "", nil, userErr("An account with this email address already exists. Sign in instead.")
	}

	ownerName := in.FirstName + " " + in.LastName
	ownerID, err := svc.platform.CreatePendingOwner(ctx, in.Email, ownerName, in.Password)
	if err != nil {
		return "", nil, err
	}

	c := &Company{
		ID:                 svc.newID(),
		Name:               in.CompanyName,
		Status:             CompanyPending,
		OwnerUserID:        ownerID,
		OwnerEmail:         in.Email,
		OwnerName:          ownerName,
		StripePriceID:      svc.cfg.StripePriceID,
		ProvisioningStatus: ProvAwaitingPayment,
		CreatedAt:          svc.now(),
	}

	for attempt := 0; ; attempt++ {
		c.Slug = makeSlug(in.CompanyName, attempt)
		err = svc.repo.CreateCompany(ctx, c)
		if errors.Is(err, ErrConflict) && attempt < 5 {
			continue
		}
		break
	}

	if err != nil {
		return "", nil, err
	}

	if err = svc.repo.AddMember(ctx, &Member{CompanyID: c.ID, UserID: ownerID, Role: RoleOwner, CreatedAt: svc.now()}); err != nil {
		return "", nil, err
	}

	svc.audit(ctx, AuditEntry{ActorType: ActorPublic, ActorLabel: in.Email, IP: in.IP, CompanyID: c.ID}.
		with("company.signup", c.Name, ResultSuccess, map[string]string{"slug": c.Slug}))

	url, err := svc.StartCheckout(ctx, c)
	return url, c, err
}

// StartCheckout (re)creates a Stripe Checkout session for a company that has not paid yet
func (svc *Service) StartCheckout(ctx context.Context, c *Company) (string, error) {
	if c.ProvisioningStatus == ProvProvisioned && Evaluate(c, svc.now()).Level == AccessFull {
		return "", userErr("This company already has an active subscription.")
	}

	if c.StripeCustomerID == "" {
		cid, err := svc.stripe.CreateCustomer(ctx, CustomerParams{
			Email:          c.OwnerEmail,
			Name:           c.Name,
			CompanyID:      c.ID,
			IdempotencyKey: "culpos-company-" + strconv.FormatUint(c.ID, 10) + "-customer",
		})
		if err != nil {
			svc.log.Error("failed to create Stripe customer", zap.Uint64("companyID", c.ID), zap.Error(err))
			return "", userErr("We could not start checkout. Please try again shortly.")
		}

		if err = svc.repo.SetStripeCustomer(ctx, c.ID, cid); err != nil {
			return "", err
		}

		c.StripeCustomerID = cid
	}

	ref := svc.signedRef(c.ID)
	sess, err := svc.stripe.CreateCheckoutSession(ctx, CheckoutParams{
		CustomerID:        c.StripeCustomerID,
		PriceID:           svc.cfg.StripePriceID,
		SuccessURL:        svc.cfg.Brand.URL("/signup/complete?ref=" + ref),
		CancelURL:         svc.cfg.Brand.URL("/signup/checkout?ref=" + ref),
		ClientReferenceID: strconv.FormatUint(c.ID, 10),
		CompanyID:         c.ID,
	})

	if err != nil {
		svc.log.Error("failed to create Stripe checkout session", zap.Uint64("companyID", c.ID), zap.Error(err))
		return "", userErr("We could not start checkout. Please try again shortly.")
	}

	return sess.URL, nil
}

// Provisioning ----------------------------------------------------------

// Provision activates a paid company exactly once
func (svc *Service) Provision(ctx context.Context, companyID uint64) error {
	claimed, err := svc.repo.ClaimProvisioning(ctx, companyID)
	if err != nil {
		return err
	}

	if !claimed {
		// already provisioned or being provisioned by a concurrent delivery
		return nil
	}

	c, err := svc.repo.CompanyByID(ctx, companyID)
	if err != nil {
		return err
	}

	res, err := svc.platform.ProvisionCompany(ctx, c)
	if err != nil {
		_ = svc.repo.FailProvisioning(ctx, companyID, err.Error())
		svc.audit(ctx, AuditEntry{ActorType: ActorSystem, CompanyID: c.ID}.
			with("company.provision", c.Name, ResultFailure, nil))
		return fmt.Errorf("provisioning failed: %w", err)
	}

	if err = svc.repo.CompleteProvisioning(ctx, companyID, res); err != nil {
		return err
	}

	svc.cache.invalidateCompany(companyID)
	svc.audit(ctx, AuditEntry{ActorType: ActorSystem, CompanyID: c.ID}.
		with("company.provision", c.Name, ResultSuccess, map[string]string{"namespaceID": strconv.FormatUint(res.NamespaceID, 10)}))

	svc.sendMail(ctx, c.OwnerEmail, "Welcome to "+svc.cfg.Brand.ProductName, "welcome", map[string]any{
		"Company": c.Name,
		"Name":    c.OwnerName,
		"URL":     svc.cfg.Brand.URL("/"),
	})

	return nil
}

// Access ----------------------------------------------------------------

// AccessForUser evaluates the commercial access for an application user.
//
// Returns (nil, nil, decision{AccessFull}) for platform staff who do not
// belong to any company.
func (svc *Service) AccessForUser(ctx context.Context, userID uint64) (*Company, *Member, AccessDecision, error) {
	m, c, err := svc.cache.lookup(ctx, svc.repo, userID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil, AccessDecision{Level: AccessFull}, nil
	}

	if err != nil {
		return nil, nil, AccessDecision{Level: AccessNone, Reason: "error"}, err
	}

	return c, m, Evaluate(c, svc.now()), nil
}

// AuthGuard returns a redirect path when the user may not enter the
// application (used by the sign-in flow)
func (svc *Service) AuthGuard(ctx context.Context, userID uint64) string {
	_, _, d, err := svc.AccessForUser(ctx, userID)
	if err != nil {
		svc.log.Error("access check failed", zap.Error(err))
		return "/account/unavailable"
	}

	switch d.Level {
	case AccessNone:
		return "/account/disabled"
	case AccessBillingOnly:
		return "/billing"
	}

	return ""
}

func (svc *Service) touchActivity(ctx context.Context, companyID uint64) {
	now := svc.now()
	svc.activityMu.Lock()
	last, ok := svc.activity[companyID]
	if ok && now.Sub(last) < 5*time.Minute {
		svc.activityMu.Unlock()
		return
	}
	svc.activity[companyID] = now
	svc.activityMu.Unlock()

	if err := svc.repo.TouchActivity(ctx, companyID, now); err != nil {
		svc.log.Warn("failed to record company activity", zap.Error(err))
	}
}

// Members ---------------------------------------------------------------

// Invite adds a new user to the actor's company. Billing is company-level:
// invited users never pay individually.
func (svc *Service) Invite(ctx context.Context, actorID uint64, email, name string, role CompanyRole, ip string) error {
	c, actor, d, err := svc.AccessForUser(ctx, actorID)
	if err != nil {
		return err
	}

	if c == nil || actor == nil || !actor.Role.CanAssign(role) {
		return ErrNotAllowed
	}

	if d.Level != AccessFull {
		return userErr("An active subscription is required to invite team members.")
	}

	email = strings.ToLower(strings.TrimSpace(email))
	name = strings.TrimSpace(name)
	if !emailRE.MatchString(email) || len(email) > 254 {
		return userErr("Enter a valid email address.")
	}

	if len(name) > 120 {
		return userErr("Name is too long.")
	}

	if exists, err := svc.platform.UserExists(ctx, email); err != nil {
		return err
	} else if exists {
		return userErr("A user with this email address already exists.")
	}

	uid, acceptURL, err := svc.platform.InviteUser(ctx, c, email, name, role)
	if err != nil {
		return err
	}

	if err = svc.repo.AddMember(ctx, &Member{CompanyID: c.ID, UserID: uid, Role: role, InvitedBy: actorID, CreatedAt: svc.now()}); err != nil {
		return err
	}

	svc.cache.invalidateUser(uid)

	inviter := ""
	if uu, _ := svc.platform.Users(ctx, actorID); uu != nil {
		inviter = firstNonEmpty(uu[actorID].Name, uu[actorID].Email)
	}

	svc.audit(ctx, userActor(actorID, actor.Role, c.ID, ip).
		with("company.member.invite", email, ResultSuccess, map[string]string{"role": string(role)}))

	svc.sendMail(ctx, email, "You're invited to join "+c.Name+" on "+svc.cfg.Brand.ProductName, "invite", map[string]any{
		"Company": c.Name,
		"Inviter": inviter,
		"Name":    name,
		"Role":    role.Label(),
		"URL":     acceptURL,
	})

	return nil
}

// ChangeRole changes a company member's role within the hierarchy rules
func (svc *Service) ChangeRole(ctx context.Context, actorID, targetID uint64, role CompanyRole, ip string) error {
	c, actor, target, err := svc.memberPair(ctx, actorID, targetID)
	if err != nil {
		return err
	}

	if actorID == targetID || !actor.Role.CanAssign(role) || !actor.Role.Outranks(target.Role) {
		svc.audit(ctx, userActor(actorID, actor.Role, c.ID, ip).
			with("company.member.role", strconv.FormatUint(targetID, 10), ResultDenied, map[string]string{"role": string(role)}))
		return ErrNotAllowed
	}

	if err = svc.platform.SetMemberRole(ctx, c, targetID, target.Role, role); err != nil {
		return err
	}

	if err = svc.repo.UpdateMemberRole(ctx, c.ID, targetID, role); err != nil {
		return err
	}

	svc.cache.invalidateUser(targetID)
	svc.audit(ctx, userActor(actorID, actor.Role, c.ID, ip).
		with("company.member.role", strconv.FormatUint(targetID, 10), ResultSuccess, map[string]string{"from": string(target.Role), "to": string(role)}))
	return nil
}

// RemoveMember removes a user from the company (account is suspended, data is kept)
func (svc *Service) RemoveMember(ctx context.Context, actorID, targetID uint64, ip string) error {
	c, actor, target, err := svc.memberPair(ctx, actorID, targetID)
	if err != nil {
		return err
	}

	if actorID == targetID || target.Role == RoleOwner || !actor.Role.CanManageMembers() || !actor.Role.Outranks(target.Role) {
		svc.audit(ctx, userActor(actorID, actor.Role, c.ID, ip).
			with("company.member.remove", strconv.FormatUint(targetID, 10), ResultDenied, nil))
		return ErrNotAllowed
	}

	if err = svc.platform.RemoveUser(ctx, c, targetID, target.Role); err != nil {
		return err
	}

	if err = svc.platform.RevokeSessions(ctx, targetID); err != nil {
		svc.log.Warn("failed to revoke sessions of removed member", zap.Error(err))
	}

	if err = svc.repo.RemoveMember(ctx, c.ID, targetID); err != nil {
		return err
	}

	svc.cache.invalidateUser(targetID)
	svc.audit(ctx, userActor(actorID, actor.Role, c.ID, ip).
		with("company.member.remove", strconv.FormatUint(targetID, 10), ResultSuccess, nil))
	return nil
}

func (svc *Service) memberPair(ctx context.Context, actorID, targetID uint64) (*Company, *Member, *Member, error) {
	c, actor, d, err := svc.AccessForUser(ctx, actorID)
	if err != nil {
		return nil, nil, nil, err
	}

	if c == nil || actor == nil {
		return nil, nil, nil, ErrNotAllowed
	}

	if d.Level != AccessFull {
		return nil, nil, nil, userErr("An active subscription is required to manage team members.")
	}

	target, err := svc.repo.MemberByUser(ctx, targetID)
	if errors.Is(err, ErrNotFound) || (err == nil && target.CompanyID != c.ID) {
		// never reveal whether the user exists in another company
		return nil, nil, nil, ErrNotAllowed
	}

	if err != nil {
		return nil, nil, nil, err
	}

	return c, actor, target, nil
}

// MembersWithInfo returns company members with display data
func (svc *Service) MembersWithInfo(ctx context.Context, companyID uint64) ([]*Member, error) {
	mm, err := svc.repo.Members(ctx, companyID)
	if err != nil {
		return nil, err
	}

	ids := make([]uint64, len(mm))
	for i, m := range mm {
		ids[i] = m.UserID
	}

	info, err := svc.platform.Users(ctx, ids...)
	if err != nil {
		return nil, err
	}

	for _, m := range mm {
		if u, ok := info[m.UserID]; ok {
			m.Email, m.Name, m.Suspended = u.Email, u.Name, u.Suspended
		}
	}

	return mm, nil
}

// Founder company operations ----------------------------------------------

func (svc *Service) SetCompanyEnabled(ctx context.Context, f *Founder, companyID uint64, enabled bool, ip string) error {
	c, err := svc.repo.CompanyByID(ctx, companyID)
	if err != nil {
		return err
	}

	st, action := CompanyDisabled, "company.disable"
	if enabled {
		action = "company.enable"
		st = CompanyActive
		if c.ProvisioningStatus != ProvProvisioned {
			st = CompanyPending
		}
	}

	if err = svc.repo.SetCompanyStatus(ctx, companyID, st); err != nil {
		return err
	}

	svc.cache.invalidateCompany(companyID)
	svc.audit(ctx, founderActor(f, ip).with(action, c.Name, ResultSuccess, map[string]string{"companyID": strconv.FormatUint(c.ID, 10)}))
	return nil
}

func (svc *Service) SetUserEnabled(ctx context.Context, f *Founder, userID uint64, enabled bool, ip string) (err error) {
	action := "user.disable"
	if enabled {
		action = "user.enable"
		err = svc.platform.UnsuspendUser(ctx, userID)
	} else {
		err = svc.platform.SuspendUser(ctx, userID)
		if err == nil {
			err = svc.platform.RevokeSessions(ctx, userID)
		}
	}

	res := ResultSuccess
	if err != nil {
		res = ResultFailure
	}

	var companyID uint64
	if m, _ := svc.repo.MemberByUser(ctx, userID); m != nil {
		companyID = m.CompanyID
	}

	e := founderActor(f, ip).with(action, strconv.FormatUint(userID, 10), res, nil)
	e.CompanyID = companyID
	svc.audit(ctx, e)
	return err
}

// ResetUserAccess revokes all sessions and emails a password reset link
func (svc *Service) ResetUserAccess(ctx context.Context, f *Founder, userID uint64, ip string) error {
	uu, err := svc.platform.Users(ctx, userID)
	if err != nil {
		return err
	}

	u, ok := uu[userID]
	if !ok {
		return ErrNotFound
	}

	if err = svc.platform.RevokeSessions(ctx, userID); err != nil {
		return err
	}

	err = svc.platform.SendPasswordReset(ctx, u.Email)
	res := ResultSuccess
	if err != nil {
		res = ResultFailure
	}

	svc.audit(ctx, founderActor(f, ip).with("user.reset-access", strconv.FormatUint(userID, 10), res, nil))
	return err
}

// Signed references --------------------------------------------------------

// signedRef produces an opaque reference used in checkout return URLs; it
// lets the browser see provisioning status but never grants access.
func (svc *Service) signedRef(companyID uint64) string {
	exp := svc.now().Add(7 * 24 * time.Hour).Unix()
	payload := strconv.FormatUint(companyID, 10) + "." + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, svc.secret)
	mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (svc *Service) parseRef(ref string) (uint64, bool) {
	parts := strings.Split(ref, ".")
	if len(parts) != 3 {
		return 0, false
	}

	mac := hmac.New(sha256.New, svc.secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return 0, false
	}

	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || svc.now().Unix() > exp {
		return 0, false
	}

	id, err := strconv.ParseUint(parts[0], 10, 64)
	return id, err == nil
}

// helpers -------------------------------------------------------------------

func makeSlug(name string, attempt int) string {
	s := strings.Trim(slugCleaner.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}

	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "co-" + s
	}

	s = strings.Trim(s, "-")

	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return s + "-" + hex.EncodeToString(b)
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}

	return ""
}
