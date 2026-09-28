// Package integration connects the CulpOS commercial layer to the application
// services (users, roles, access control and workspaces).
package integration

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	composeRest "github.com/cortezaproject/corteza/server/compose/rest"
	composeRequest "github.com/cortezaproject/corteza/server/compose/rest/request"
	composeService "github.com/cortezaproject/corteza/server/compose/service"
	composeTypes "github.com/cortezaproject/corteza/server/compose/types"
	"github.com/cortezaproject/corteza/server/pkg/auth"
	"github.com/cortezaproject/corteza/server/pkg/id"
	"github.com/cortezaproject/corteza/server/pkg/rbac"
	"github.com/cortezaproject/corteza/server/saas"
	"github.com/cortezaproject/corteza/server/store"
	systemService "github.com/cortezaproject/corteza/server/system/service"
	systemTypes "github.com/cortezaproject/corteza/server/system/types"
	"go.uber.org/zap"
)

type (
	// Platform implements saas.Platform on top of the application services
	Platform struct {
		Store store.Storer
		Log   *zap.Logger
		Rbac  interface {
			Grant(context.Context, ...*rbac.Rule) error
		}
		Invite func(ctx context.Context, email string) (string, error)
		// AuthBaseURL is used to build invitation links, e.g. https://app.example/auth
		AuthBaseURL string
		// WorkspaceTemplateSlug is the namespace cloned for every new company
		WorkspaceTemplateSlug string
	}
)

var _ saas.Platform = &Platform{}

// sys returns context with the internal service identity used for provisioning
func sys(ctx context.Context) context.Context {
	return auth.SetIdentityToContext(ctx, auth.ServiceUser())
}

func (p *Platform) UserExists(ctx context.Context, email string) (bool, error) {
	_, err := store.LookupUserByEmail(ctx, p.Store, email)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}

	return err == nil, err
}

func (p *Platform) CheckPasswordStrength(password string) bool {
	if systemService.DefaultAuth == nil {
		return len(password) >= 8
	}

	return systemService.DefaultAuth.CheckPasswordStrength(password)
}

func (p *Platform) newUser(email, name string, suspended bool) *systemTypes.User {
	now := time.Now().Round(time.Second)
	u := &systemTypes.User{
		ID:             id.Next(),
		Email:          email,
		Name:           name,
		EmailConfirmed: true,
		Kind:           systemTypes.NormalUser,
		Meta:           &systemTypes.UserMeta{},
		CreatedAt:      now,
	}

	if suspended {
		u.SuspendedAt = &now
	}

	return u
}

// CreatePendingOwner creates the suspended Company Owner. The password is
// hashed by the application's credential service (bcrypt).
func (p *Platform) CreatePendingOwner(ctx context.Context, email, name, password string) (uint64, error) {
	ctx = sys(ctx)
	u := p.newUser(email, name, true)

	err := store.Tx(ctx, p.Store, func(ctx context.Context, s store.Storer) error {
		if err := store.CreateUser(ctx, s, u); err != nil {
			return err
		}

		return systemService.SetPasswordCredentials(ctx, s, u.ID, password)
	})

	if err != nil {
		return 0, err
	}

	return u.ID, nil
}

var roleDefs = []struct {
	role   saas.CompanyRole
	suffix string
}{
	{saas.RoleOwner, "owner"},
	{saas.RoleAdministrator, "administrator"},
	{saas.RoleManager, "manager"},
	{saas.RoleEmployee, "employee"},
}

// ProvisionCompany creates company roles, the isolated workspace, access
// rules, and activates the owner. Safe to retry: existing roles/workspace
// are reused.
func (p *Platform) ProvisionCompany(ctx context.Context, c *saas.Company) (res saas.ProvisioningResult, err error) {
	ctx = sys(ctx)

	roles := map[saas.CompanyRole]uint64{}
	for _, def := range roleDefs {
		handle := "co_" + strings.ReplaceAll(c.Slug, "-", "_") + "_" + def.suffix
		if r, lerr := store.LookupRoleByHandle(ctx, p.Store, handle); lerr == nil {
			roles[def.role] = r.ID
			continue
		}

		r, cerr := systemService.DefaultRole.Create(ctx, &systemTypes.Role{
			Name:   c.Name + " · " + def.role.Label(),
			Handle: handle,
			Meta:   &systemTypes.RoleMeta{Description: "CulpOS company role for company " + strconv.FormatUint(c.ID, 10)},
		})

		if cerr != nil {
			return res, fmt.Errorf("could not create role %s: %w", def.suffix, cerr)
		}

		roles[def.role] = r.ID
	}

	res.RoleOwnerID = roles[saas.RoleOwner]
	res.RoleAdminID = roles[saas.RoleAdministrator]
	res.RoleManagerID = roles[saas.RoleManager]
	res.RoleEmployeeID = roles[saas.RoleEmployee]

	ns, err := p.workspace(ctx, c)
	if err != nil {
		return res, err
	}

	res.NamespaceID = ns.ID

	if err = p.Rbac.Grant(ctx, workspaceRules(ns.ID, roles)...); err != nil {
		return res, fmt.Errorf("could not apply access rules: %w", err)
	}

	if err = systemService.DefaultRole.MemberAdd(ctx, res.RoleOwnerID, c.OwnerUserID); err != nil && !isDuplicate(err) {
		return res, fmt.Errorf("could not assign owner role: %w", err)
	}

	if err = p.UnsuspendUser(ctx, c.OwnerUserID); err != nil {
		return res, fmt.Errorf("could not activate owner: %w", err)
	}

	return res, nil
}

// workspace returns the company namespace, cloning the CulpOS workspace
// template when available
func (p *Platform) workspace(ctx context.Context, c *saas.Company) (*composeTypes.Namespace, error) {
	if ns, err := store.LookupComposeNamespaceBySlug(ctx, p.Store, c.Slug); err == nil {
		return ns, nil
	}

	tpl, err := store.LookupComposeNamespaceBySlug(ctx, p.Store, p.WorkspaceTemplateSlug)
	if err == nil {
		ctrl := (composeRest.Namespace{}).New()
		if _, err = ctrl.Clone(ctx, &composeRequest.NamespaceClone{
			NamespaceID: tpl.ID,
			Name:        c.Name,
			Slug:        c.Slug,
		}); err != nil {
			return nil, fmt.Errorf("could not create workspace from template: %w", err)
		}

		ns, err := store.LookupComposeNamespaceBySlug(ctx, p.Store, c.Slug)
		if err != nil {
			return nil, err
		}

		ns.Enabled = true
		ns.Name = c.Name
		ns.Meta.Subtitle = "CulpOS"
		ns.Meta.Description = "Operations workspace for " + c.Name
		if ns, err = composeService.DefaultNamespace.Update(ctx, ns); err != nil {
			return nil, err
		}

		return ns, nil
	}

	p.Log.Warn("workspace template not found; creating an empty workspace", zap.String("template", p.WorkspaceTemplateSlug))
	return composeService.DefaultNamespace.Create(ctx, &composeTypes.Namespace{
		Name:    c.Name,
		Slug:    c.Slug,
		Enabled: true,
		Meta:    composeTypes.NamespaceMeta{Subtitle: "CulpOS", Description: "Operations workspace for " + c.Name},
	})
}

// workspaceRules scopes every company role to its own namespace only
func workspaceRules(nsID uint64, roles map[saas.CompanyRole]uint64) (rr []*rbac.Rule) {
	var (
		ns       = strconv.FormatUint(nsID, 10)
		nsRes    = composeTypes.NamespaceResourceType + "/" + ns
		modRes   = composeTypes.ModuleResourceType + "/" + ns + "/*"
		fieldRes = composeTypes.ModuleFieldResourceType + "/" + ns + "/*/*"
		recRes   = composeTypes.RecordResourceType + "/" + ns + "/*/*"
		pageRes  = composeTypes.PageResourceType + "/" + ns + "/*"
		plRes    = composeTypes.PageLayoutResourceType + "/" + ns + "/*/*"
		chartRes = composeTypes.ChartResourceType + "/" + ns + "/*"
		comp     = composeTypes.ComponentResourceType + "/"

		allow = func(role uint64, res string, ops ...string) {
			for _, op := range ops {
				rr = append(rr, rbac.AllowRule(role, res, op))
			}
		}
	)

	for role, roleID := range roles {
		allow(roleID, comp, "namespaces.search")
		allow(roleID, nsRes, "read", "pages.search", "modules.search", "charts.search")
		allow(roleID, fieldRes, "record.value.read", "record.value.update")
		allow(roleID, pageRes, "read")
		allow(roleID, plRes, "read")
		allow(roleID, chartRes, "read")
		allow(roleID, modRes, "read", "records.search", "record.create")
		allow(roleID, recRes, "read")

		switch role {
		case saas.RoleOwner, saas.RoleAdministrator:
			// Workspace Configuration (forms, data, pages, reports) inside their own workspace only
			allow(roleID, nsRes, "update", "manage", "export", "modules.export", "charts.export", "pages.export",
				"page.create", "module.create", "chart.create")
			allow(roleID, modRes, "update", "delete", "export")
			allow(roleID, recRes, "update", "delete", "revisions.search")
			allow(roleID, pageRes, "update", "delete", "export")
			allow(roleID, plRes, "update", "delete")
			allow(roleID, chartRes, "update", "delete", "export")
			allow(roleID, modRes, "export")
			allow(roleID, comp, "grant")

		case saas.RoleManager:
			allow(roleID, recRes, "update", "delete", "revisions.search")
			allow(roleID, modRes, "export")

		case saas.RoleEmployee:
			allow(roleID, recRes, "update")
		}
	}

	return rr
}

func (p *Platform) InviteUser(ctx context.Context, c *saas.Company, email, name string, role saas.CompanyRole) (uint64, string, error) {
	ctx = sys(ctx)
	u := p.newUser(email, name, false)
	u.EmailConfirmed = false

	if err := store.CreateUser(ctx, p.Store, u); err != nil {
		return 0, "", err
	}

	if roleID := roleFor(c, role); roleID > 0 {
		if err := systemService.DefaultRole.MemberAdd(ctx, roleID, u.ID); err != nil {
			return 0, "", err
		}
	}

	token, err := p.Invite(ctx, email)
	if err != nil {
		return 0, "", err
	}

	return u.ID, strings.TrimRight(p.AuthBaseURL, "/") + "/accept-invite?token=" + url.QueryEscape(token), nil
}

func roleFor(c *saas.Company, r saas.CompanyRole) uint64 {
	switch r {
	case saas.RoleOwner:
		return c.RoleOwnerID
	case saas.RoleAdministrator:
		return c.RoleAdminID
	case saas.RoleManager:
		return c.RoleManagerID
	case saas.RoleEmployee:
		return c.RoleEmployeeID
	}

	return 0
}

func (p *Platform) SetMemberRole(ctx context.Context, c *saas.Company, userID uint64, from, to saas.CompanyRole) error {
	ctx = sys(ctx)
	if err := systemService.DefaultRole.MemberAdd(ctx, roleFor(c, to), userID); err != nil && !isDuplicate(err) {
		return err
	}

	if rid := roleFor(c, from); rid > 0 && from != to {
		return systemService.DefaultRole.MemberRemove(ctx, rid, userID)
	}

	return nil
}

func (p *Platform) RemoveUser(ctx context.Context, c *saas.Company, userID uint64, role saas.CompanyRole) error {
	ctx = sys(ctx)
	for _, rid := range []uint64{c.RoleOwnerID, c.RoleAdminID, c.RoleManagerID, c.RoleEmployeeID} {
		if rid == 0 {
			continue
		}

		if err := systemService.DefaultRole.MemberRemove(ctx, rid, userID); err != nil && !errors.Is(err, store.ErrNotFound) {
			p.Log.Debug("role membership removal", zap.Error(err))
		}
	}

	return p.SuspendUser(ctx, userID)
}

func (p *Platform) SuspendUser(ctx context.Context, userID uint64) error {
	ctx = sys(ctx)
	u, err := store.LookupUserByID(ctx, p.Store, userID)
	if err != nil {
		return err
	}

	if u.SuspendedAt != nil {
		return nil
	}

	now := time.Now().Round(time.Second)
	u.SuspendedAt = &now
	return store.UpdateUser(ctx, p.Store, u)
}

func (p *Platform) UnsuspendUser(ctx context.Context, userID uint64) error {
	ctx = sys(ctx)
	u, err := store.LookupUserByID(ctx, p.Store, userID)
	if err != nil {
		return err
	}

	if u.SuspendedAt == nil {
		return nil
	}

	u.SuspendedAt = nil
	return store.UpdateUser(ctx, p.Store, u)
}

func (p *Platform) RevokeSessions(ctx context.Context, userID uint64) error {
	ctx = sys(ctx)
	if err := store.DeleteAuthSessionsByUserID(ctx, p.Store, userID); err != nil {
		return err
	}

	return store.DeleteAuthOA2TokenByUserID(ctx, p.Store, userID)
}

func (p *Platform) SendPasswordReset(ctx context.Context, email string) error {
	return systemService.DefaultAuth.SendPasswordResetToken(sys(ctx), email)
}

func (p *Platform) Users(ctx context.Context, ids ...uint64) (map[uint64]saas.UserInfo, error) {
	out := make(map[uint64]saas.UserInfo, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	ctx = sys(ctx)
	for _, uid := range ids {
		u, err := store.LookupUserByID(ctx, p.Store, uid)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}

		if err != nil {
			return nil, err
		}

		out[uid] = saas.UserInfo{
			ID:        u.ID,
			Email:     u.Email,
			Name:      u.Name,
			Suspended: u.SuspendedAt != nil || u.DeletedAt != nil,
			CreatedAt: u.CreatedAt.Format(time.RFC3339),
		}
	}

	return out, nil
}

func isDuplicate(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "already"))
}

// CreateRecord creates a record in the company workspace acting as the user,
// so access control and ownership apply exactly as in the application
func (p *Platform) CreateRecord(ctx context.Context, c *saas.Company, userID uint64, role saas.CompanyRole, module string, values map[string]string) (uint64, error) {
	if c.NamespaceID == 0 {
		return 0, fmt.Errorf("workspace not provisioned")
	}

	mod, err := store.LookupComposeModuleByNamespaceIDHandle(sys(ctx), p.Store, c.NamespaceID, module)
	if err != nil {
		return 0, fmt.Errorf("module %s not found: %w", module, err)
	}

	rr := []uint64{roleFor(c, role)}
	for _, r := range auth.AuthenticatedRoles() {
		rr = append(rr, r.ID)
	}

	uctx := auth.SetIdentityToContext(ctx, auth.Authenticated(userID, rr...))

	rec := &composeTypes.Record{NamespaceID: c.NamespaceID, ModuleID: mod.ID}
	for name, v := range values {
		if strings.TrimSpace(v) == "" {
			continue
		}
		rec.Values = append(rec.Values, &composeTypes.RecordValue{Name: name, Value: v})
	}

	out, verr, err := composeService.DefaultRecord.Create(uctx, rec)
	if err != nil {
		return 0, err
	}

	if verr != nil && !verr.IsValid() {
		return 0, fmt.Errorf("invalid values: %v", verr)
	}

	return out.ID, nil
}

// RenameWorkspace updates the workspace display name
func (p *Platform) RenameWorkspace(ctx context.Context, c *saas.Company, name string) error {
	if c.NamespaceID == 0 {
		return nil
	}

	ctx = sys(ctx)
	ns, err := store.LookupComposeNamespaceByID(ctx, p.Store, c.NamespaceID)
	if err != nil {
		return err
	}

	ns.Name = name
	_, err = composeService.DefaultNamespace.Update(ctx, ns)
	return err
}
