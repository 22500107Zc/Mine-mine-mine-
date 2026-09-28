package saas

import (
	"context"
)

type (
	// UserInfo is the display information for a platform user
	UserInfo struct {
		ID        uint64
		Email     string
		Name      string
		Suspended bool
		CreatedAt string
	}

	// Platform abstracts the underlying application (users, roles, RBAC,
	// workspaces) so the commercial layer stays testable and does not
	// depend on internal storage details.
	Platform interface {
		// UserExists checks if an account with the email already exists
		UserExists(ctx context.Context, email string) (bool, error)

		// CheckPasswordStrength applies the configured password policy
		CheckPasswordStrength(password string) bool

		// CreatePendingOwner creates the Company Owner account at signup.
		// The account is suspended until payment is verified server-side.
		CreatePendingOwner(ctx context.Context, email, name, password string) (uint64, error)

		// ProvisionCompany creates the company roles, isolated workspace and
		// access rules, and activates the owner account.
		ProvisionCompany(ctx context.Context, c *Company) (ProvisioningResult, error)

		// InviteUser creates (suspended-until-accepted) user account, assigns
		// the company role and returns the invitation acceptance URL
		InviteUser(ctx context.Context, c *Company, email, name string, role CompanyRole) (uint64, string, error)

		// SetMemberRole swaps company role membership
		SetMemberRole(ctx context.Context, c *Company, userID uint64, from, to CompanyRole) error

		// RemoveUser detaches a user from the company and suspends the account
		RemoveUser(ctx context.Context, c *Company, userID uint64, role CompanyRole) error

		SuspendUser(ctx context.Context, userID uint64) error
		UnsuspendUser(ctx context.Context, userID uint64) error

		// RevokeSessions terminates all sessions and tokens for the user
		RevokeSessions(ctx context.Context, userID uint64) error

		// SendPasswordReset sends the branded password reset email
		SendPasswordReset(ctx context.Context, email string) error

		// CreateRecord adds a record to a module of the company workspace, as the given user
		CreateRecord(ctx context.Context, c *Company, userID uint64, role CompanyRole, module string, values map[string]string) (uint64, error)

		// RenameWorkspace keeps the workspace name in sync with the company name
		RenameWorkspace(ctx context.Context, c *Company, name string) error

		// Users returns display info for the given users
		Users(ctx context.Context, ids ...uint64) (map[uint64]UserInfo, error)

		// WorkspaceSnapshot returns the current workspace records as activity
		// (used once to seed the Command Deck for existing workspaces)
		WorkspaceSnapshot(ctx context.Context, c *Company) ([]ActivityEvent, error)

		// WorkspaceLookups returns department names and the record page of
		// each module (for links from the Command Deck)
		WorkspaceLookups(ctx context.Context, c *Company) (WorkspaceLookup, error)
	}

	// WorkspaceLookup holds display lookups for a company workspace
	WorkspaceLookup struct {
		Departments map[uint64]string
		RecordPages map[string]uint64
	}

	// Mailer sends transactional email
	Mailer interface {
		Send(ctx context.Context, to, subject, htmlBody string) error
	}
)
