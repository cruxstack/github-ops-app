package domain

import "context"

// GitHubClient defines the GitHub API operations the application uses.
// Implemented by internal/github/client.Client.
type GitHubClient interface {
	// CheckPRCompliance verifies whether a merged PR met the branch
	// protection and ruleset requirements of its base branch.
	CheckPRCompliance(ctx context.Context, owner, repo string, prNumber int) (*PRComplianceResult, error)

	// ResolveTeam returns the slug of the team with the given name. If the
	// team does not exist, it is created when create is true, and
	// ErrTeamNotFound is returned otherwise.
	ResolveTeam(ctx context.Context, name, privacy string, create bool) (string, error)

	// SyncTeamMembers adds and removes members to match desired state.
	SyncTeamMembers(ctx context.Context, teamSlug string, desiredMembers []string, safetyThreshold float64) (*TeamSyncResult, error)

	// GetTeamMembers returns the logins of all team members.
	GetTeamMembers(ctx context.Context, teamSlug string) ([]string, error)

	// ListOrgMembers returns the logins of all organization members.
	ListOrgMembers(ctx context.Context) ([]string, error)

	// GetAppSlug returns the GitHub App's slug.
	GetAppSlug(ctx context.Context) (string, error)

	// ListSecurityAlerts fetches open security alerts across the org that
	// are older than minAgeDays and at or above minSeverity. It covers
	// dependabot, code scanning, and secret scanning alerts.
	ListSecurityAlerts(ctx context.Context, minAgeDays int, minSeverity string) (*SecurityAlertsReport, error)
}

// OktaClient defines the Okta API operations the application uses.
// Implemented by internal/okta.Client.
type OktaClient interface {
	// GetGroupsByPattern fetches all Okta groups matching a regex pattern.
	GetGroupsByPattern(ctx context.Context, pattern string) ([]*GroupInfo, error)

	// GetGroupInfo fetches details for a single Okta group by name.
	GetGroupInfo(ctx context.Context, groupName string) (*GroupInfo, error)
}

// Notifier defines the interface for sending notifications.
// Implemented by internal/notifiers.SlackNotifier.
type Notifier interface {
	// NotifyPRBypass sends a notification when branch protection is
	// bypassed.
	NotifyPRBypass(ctx context.Context, result *PRComplianceResult, repoFullName string) error

	// NotifyOktaSync sends a notification with Okta sync results.
	NotifyOktaSync(ctx context.Context, reports []*SyncReport, githubOrg string) error

	// NotifyOrphanedUsers sends a notification about organization members
	// not in any synced teams.
	NotifyOrphanedUsers(ctx context.Context, report *OrphanedUsersReport) error

	// NotifySecurityAlerts sends a notification about stale security
	// alerts across the organization.
	NotifySecurityAlerts(ctx context.Context, report *SecurityAlertsReport, githubOrg string) error
}
