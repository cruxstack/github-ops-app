package domain

import (
	"time"

	"github.com/google/go-github/v79/github"
)

// SyncRule defines how to sync Okta groups to GitHub teams.
type SyncRule struct {
	Name                string `json:"name"`
	Enabled             *bool  `json:"enabled,omitempty"`
	OktaGroupPattern    string `json:"okta_group_pattern,omitempty"`
	OktaGroupName       string `json:"okta_group_name,omitempty"`
	GitHubTeamPrefix    string `json:"github_team_prefix,omitempty"`
	GitHubTeamName      string `json:"github_team_name,omitempty"`
	StripPrefix         string `json:"strip_prefix,omitempty"`
	SyncMembers         *bool  `json:"sync_members,omitempty"`
	CreateTeamIfMissing bool   `json:"create_team_if_missing"`
	TeamPrivacy         string `json:"team_privacy,omitempty"`
}

// IsEnabled returns true if the rule is enabled (defaults to true).
func (r SyncRule) IsEnabled() bool {
	return r.Enabled == nil || *r.Enabled
}

// ShouldSyncMembers returns true if members should be synced (defaults to
// true).
func (r SyncRule) ShouldSyncMembers() bool {
	return r.SyncMembers == nil || *r.SyncMembers
}

// GetName returns the rule name, defaulting to GitHubTeamName if not set.
func (r SyncRule) GetName() string {
	if r.Name != "" {
		return r.Name
	}
	if r.GitHubTeamName != "" {
		return r.GitHubTeamName
	}
	return r.OktaGroupName
}

// ComplianceViolation represents a single branch protection rule violation.
type ComplianceViolation struct {
	Type        string
	Description string
}

// PRComplianceResult contains PR compliance check results including
// violations and user bypass permissions.
type PRComplianceResult struct {
	PR               *github.PullRequest
	BaseBranch       string
	Protection       *github.Protection
	BranchRules      *github.BranchRules
	Violations       []ComplianceViolation
	UserHasBypass    bool
	UserBypassReason string
}

// HasViolations returns true if any compliance violations were detected.
func (r *PRComplianceResult) HasViolations() bool {
	return len(r.Violations) > 0
}

// WasBypassed returns true if violations exist and user had bypass
// permission.
func (r *PRComplianceResult) WasBypassed() bool {
	return r.HasViolations() && r.UserHasBypass
}

// TeamSyncResult contains the results of syncing team membership.
type TeamSyncResult struct {
	TeamName               string
	MembersAdded           []string
	MembersRemoved         []string
	MembersSkippedExternal []string
	Errors                 []string
}

// GroupInfo contains Okta group details and member list.
type GroupInfo struct {
	ID                      string
	Name                    string
	Members                 []string
	SkippedNoGitHubUsername []string
}

// GroupMembersResult contains the results of fetching group members.
type GroupMembersResult struct {
	Members                 []string
	SkippedNoGitHubUsername []string
}

// SyncReport contains the results of syncing a single Okta group to GitHub
// team.
type SyncReport struct {
	Rule                       string
	OktaGroup                  string
	GitHubTeam                 string
	MembersAdded               []string
	MembersRemoved             []string
	MembersSkippedExternal     []string
	MembersSkippedNoGHUsername []string
	Errors                     []string
}

// HasErrors returns true if any errors occurred during sync.
func (r *SyncReport) HasErrors() bool {
	return len(r.Errors) > 0
}

// HasChanges returns true if members were added or removed.
func (r *SyncReport) HasChanges() bool {
	return len(r.MembersAdded) > 0 || len(r.MembersRemoved) > 0
}

// OrphanedUsersReport contains users who are org members but not in any
// synced teams.
type OrphanedUsersReport struct {
	OrphanedUsers []string
}

// SyncResult contains all sync reports and orphaned users report.
type SyncResult struct {
	Reports       []*SyncReport
	OrphanedUsers *OrphanedUsersReport
}

// SecurityAlert represents a normalized security alert from any source
// (dependabot, code scanning, or secret scanning).
type SecurityAlert struct {
	Type      string
	Repo      string
	Number    int
	Severity  string
	Summary   string
	HTMLURL   string
	CreatedAt time.Time
	AgeDays   int
}

// SecurityAlertsReport contains stale security alerts grouped by repo.
type SecurityAlertsReport struct {
	MinAgeDays   int
	MinSeverity  string
	TotalAlerts  int
	AlertsByRepo map[string][]SecurityAlert
	Errors       []string
}

// HasAlerts returns true if any stale alerts were found.
func (r *SecurityAlertsReport) HasAlerts() bool {
	return r.TotalAlerts > 0
}

// HasErrors returns true if any errors occurred during alert fetching.
func (r *SecurityAlertsReport) HasErrors() bool {
	return len(r.Errors) > 0
}

// RepoCount returns the number of repos with stale alerts.
func (r *SecurityAlertsReport) RepoCount() int {
	return len(r.AlertsByRepo)
}
