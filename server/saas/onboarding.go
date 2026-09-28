package saas

import (
	"context"
	"regexp"
	"strconv"
	"strings"
)

var dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// needsOnboarding reports whether the first-run setup should be shown
func needsOnboarding(c *Company, m *Member) bool {
	return c != nil && m != nil && m.Role == RoleOwner && c.OnboardingDoneAt == nil && c.ProvisioningStatus == ProvProvisioned
}

// UpdateCompanyProfile updates the company profile (Owner and Administrator)
func (svc *Service) UpdateCompanyProfile(ctx context.Context, actorID uint64, p CompanyProfile, ip string) error {
	c, m, d, err := svc.AccessForUser(ctx, actorID)
	if err != nil {
		return err
	}

	if c == nil || m == nil || !m.Role.CanManageMembers() {
		return ErrNotAllowed
	}

	if d.Level != AccessFull {
		return userErr("An active subscription is required to update the company profile.")
	}

	p.Name = strings.TrimSpace(p.Name)
	p.Website = strings.TrimSpace(p.Website)
	p.Phone = strings.TrimSpace(p.Phone)
	p.Address = strings.TrimSpace(p.Address)
	p.Industry = strings.TrimSpace(p.Industry)

	switch {
	case len(p.Name) < 2 || len(p.Name) > 100:
		return userErr("Company name must be between 2 and 100 characters.")
	case len(p.Website) > 200 || len(p.Phone) > 50 || len(p.Address) > 300 || len(p.Industry) > 100:
		return userErr("One of the profile fields is too long.")
	case p.Website != "" && !strings.HasPrefix(p.Website, "http://") && !strings.HasPrefix(p.Website, "https://"):
		p.Website = "https://" + p.Website
	}

	if err = svc.repo.UpdateCompanyProfile(ctx, c.ID, p); err != nil {
		return err
	}

	if p.Name != c.Name {
		if err = svc.platform.RenameWorkspace(ctx, c, p.Name); err != nil {
			svc.log.Warn("could not rename workspace")
		}
	}

	svc.cache.invalidateCompany(c.ID)
	svc.audit(ctx, userActor(actorID, m.Role, c.ID, ip).with("company.profile.update", p.Name, ResultSuccess, nil))
	return nil
}

// AddFirstRecord creates a customer or task from the onboarding flow
func (svc *Service) AddFirstRecord(ctx context.Context, actorID uint64, kind string, values map[string]string, ip string) error {
	c, m, d, err := svc.AccessForUser(ctx, actorID)
	if err != nil {
		return err
	}

	if c == nil || m == nil || d.Level != AccessFull {
		return ErrNotAllowed
	}

	var module string
	switch kind {
	case "customer":
		module = "Customer"
		values = map[string]string{
			"Name":   strings.TrimSpace(values["Name"]),
			"Email":  strings.TrimSpace(values["Email"]),
			"Phone":  strings.TrimSpace(values["Phone"]),
			"Status": "Active",
		}
		if values["Name"] == "" || len(values["Name"]) > 200 {
			return userErr("Enter the customer’s name.")
		}
		if values["Email"] != "" && !emailRE.MatchString(values["Email"]) {
			return userErr("Enter a valid email address for the customer.")
		}

	case "task":
		module = "Task"
		values = map[string]string{
			"Title":      strings.TrimSpace(values["Title"]),
			"DueDate":    strings.TrimSpace(values["DueDate"]),
			"Status":     "Open",
			"Priority":   "Normal",
			"AssignedTo": strconv.FormatUint(actorID, 10),
		}
		if values["Title"] == "" || len(values["Title"]) > 200 {
			return userErr("Enter a title for the task.")
		}
		if values["DueDate"] != "" && !dateRE.MatchString(values["DueDate"]) {
			return userErr("Enter the due date as YYYY-MM-DD.")
		}

	default:
		return ErrNotAllowed
	}

	if _, err = svc.platform.CreateRecord(ctx, c, actorID, m.Role, module, values); err != nil {
		svc.log.Warn("onboarding record creation failed")
		return userErr("We could not save that right now. Please try again.")
	}

	svc.audit(ctx, userActor(actorID, m.Role, c.ID, ip).with("onboarding."+kind+".create", "", ResultSuccess, nil))
	return nil
}

// FinishOnboarding marks first-run setup as complete (or skipped)
func (svc *Service) FinishOnboarding(ctx context.Context, actorID uint64, ip string) error {
	c, m, _, err := svc.AccessForUser(ctx, actorID)
	if err != nil {
		return err
	}

	if c == nil || m == nil || !m.Role.CanManageMembers() {
		return ErrNotAllowed
	}

	if err = svc.repo.CompleteOnboarding(ctx, c.ID, svc.now()); err != nil {
		return err
	}

	svc.cache.invalidateCompany(c.ID)
	svc.audit(ctx, userActor(actorID, m.Role, c.ID, ip).with("onboarding.complete", c.Name, ResultSuccess, nil))
	return nil
}
