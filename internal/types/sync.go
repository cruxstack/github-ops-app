// Package types provides shared type definitions used across packages.
package types

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
