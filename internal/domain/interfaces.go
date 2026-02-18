package domain

import (
	"context"

	"github.com/google/go-github/v79/github"
)

// GitHubClient defines the interface for GitHub API operations.
// implemented by internal/github/client.Client.
type GitHubClient interface {
	// CheckPRCompliance verifies if a merged PR met branch protection
	// requirements.
	CheckPRCompliance(ctx context.Context, owner, repo string, prNumber int) (*PRComplianceResult, error)

	// GetOrCreateTeam fetches an existing team by slug or creates it if
	// missing.
	GetOrCreateTeam(ctx context.Context, teamName, privacy string) (*github.Team, error)

	// SyncTeamMembers adds and removes members to match desired state.
	SyncTeamMembers(ctx context.Context, teamSlug string, desiredMembers []string, safetyThreshold float64) (*TeamSyncResult, error)

	// GetTeamMembers returns GitHub usernames of all team members.
	GetTeamMembers(ctx context.Context, teamSlug string) ([]string, error)

	// ListOrgMembers returns all organization members excluding external
	// collaborators.
	ListOrgMembers(ctx context.Context) ([]string, error)

	// IsExternalCollaborator checks if a user is an outside collaborator
	// rather than an organization member.
	IsExternalCollaborator(ctx context.Context, username string) (bool, error)

	// GetAppSlug fetches the GitHub App slug identifier.
	GetAppSlug(ctx context.Context) (string, error)

	// GetOrg returns the GitHub organization name.
	GetOrg() string

	// ListSecurityAlerts fetches open security alerts across the org
	// that are older than minAgeDays and at or above minSeverity.
	// covers dependabot, code scanning, and secret scanning alerts.
	ListSecurityAlerts(ctx context.Context, minAgeDays int, minSeverity string) (*SecurityAlertsReport, error)
}

// OktaClient defines the interface for Okta API operations.
// implemented by internal/okta.Client.
type OktaClient interface {
	// GetGroupsByPattern fetches all Okta groups matching a regex pattern.
	GetGroupsByPattern(ctx context.Context, pattern string) ([]*GroupInfo, error)

	// GetGroupInfo fetches details for a single Okta group by name.
	GetGroupInfo(ctx context.Context, groupName string) (*GroupInfo, error)
}

// Notifier defines the interface for sending notifications.
// implemented by internal/notifiers.SlackNotifier.
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
