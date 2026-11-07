package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
)

// Team privacy values accepted by the GitHub API.
const (
	TeamPrivacyClosed = "closed"
	TeamPrivacySecret = "secret"
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

// GetName returns the rule name, defaulting to the team or group name.
func (r SyncRule) GetName() string {
	switch {
	case r.Name != "":
		return r.Name
	case r.GitHubTeamName != "":
		return r.GitHubTeamName
	case r.OktaGroupName != "":
		return r.OktaGroupName
	default:
		return r.OktaGroupPattern
	}
}

// Privacy returns the privacy for teams the rule creates.
func (r SyncRule) Privacy() string {
	if r.TeamPrivacy == "" {
		return TeamPrivacyClosed
	}
	return r.TeamPrivacy
}

// Validate reports every problem with the rule.
func (r SyncRule) Validate() error {
	var errs []error
	switch {
	case r.OktaGroupPattern == "" && r.OktaGroupName == "":
		errs = append(errs, errors.New("one of okta_group_pattern or okta_group_name is required"))
	case r.OktaGroupPattern != "" && r.OktaGroupName != "":
		errs = append(errs, errors.New("okta_group_pattern and okta_group_name are mutually exclusive"))
	}
	if r.OktaGroupPattern != "" {
		if _, err := regexp.Compile(r.OktaGroupPattern); err != nil {
			errs = append(errs, fmt.Errorf("invalid okta_group_pattern %q: %w", r.OktaGroupPattern, err))
		}
		// every matched group would sync into the one team, each removing
		// the members of the others
		if r.GitHubTeamName != "" {
			errs = append(errs, errors.New("github_team_name cannot be combined with okta_group_pattern; use github_team_prefix and strip_prefix"))
		}
	}
	switch r.TeamPrivacy {
	case "", TeamPrivacyClosed, TeamPrivacySecret:
	default:
		errs = append(errs, fmt.Errorf("invalid team_privacy %q: must be %q or %q", r.TeamPrivacy, TeamPrivacyClosed, TeamPrivacySecret))
	}
	return errors.Join(errs...)
}

// githubLoginPattern matches GitHub logins, including the underscore suffix
// of enterprise managed users. It rejects anything that could change the
// path of an API request.
var githubLoginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,62}$`)

// IsValidGitHubLogin returns true if s can be a GitHub login.
func IsValidGitHubLogin(s string) bool {
	return githubLoginPattern.MatchString(s)
}

// ComplianceViolation represents a single branch protection rule violation.
type ComplianceViolation struct {
	Type        string
	Description string
}

// Compliance violation types.
const (
	ViolationInsufficientReviews = "insufficient_reviews"
	ViolationMissingStatusCheck  = "missing_status_check"
)

// PRComplianceResult contains PR compliance check results including
// violations and the merger's bypass permissions.
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

// WasBypassed returns true if the PR was merged even though it did not meet
// the branch's requirements. GitHub blocks such merges unless someone
// bypasses the rules: an admin, a ruleset bypass actor, or a role allowed to
// skip branch protection.
func (r *PRComplianceResult) WasBypassed() bool {
	return r.HasViolations()
}

// TeamSyncResult contains the results of syncing team membership.
type TeamSyncResult struct {
	TeamName               string
	MembersAdded           []string
	MembersRemoved         []string
	MembersSkippedExternal []string
	Errors                 []string
	// Members are the team's members after the sync, including members of
	// its child teams.
	Members []string
}

// GroupInfo contains Okta group details and member list.
type GroupInfo struct {
	ID                      string
	Name                    string
	Members                 []string
	SkippedNoGitHubUsername []string
	// Err is set when the group's members could not be fetched. Such a
	// group must not be synced, since its member list is incomplete.
	Err error
}

// GroupMembersResult contains the results of fetching group members.
type GroupMembersResult struct {
	Members                 []string
	SkippedNoGitHubUsername []string
}

// SyncReport contains the results of syncing a single Okta group to a GitHub
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
	// TeamResolved is true when GitHubTeam is the slug of an existing team.
	TeamResolved bool
	// Members are the team's members after the sync, when its members were
	// synced.
	Members []string
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

// SyncResult contains the report for every synced group.
type SyncResult struct {
	Reports []*SyncReport
}

// ManagedTeams returns the slug of every team the sync targeted, whether or
// not its sync succeeded.
func (r *SyncResult) ManagedTeams() []string {
	seen := make(map[string]bool)
	var teams []string
	for _, report := range r.Reports {
		slug := TeamSlug(report.GitHubTeam)
		if slug != "" && !seen[slug] {
			seen[slug] = true
			teams = append(teams, slug)
		}
	}
	return teams
}

// Complete returns true if every report's team was resolved, so the result
// names every team the rules manage. A rule or group that failed before its
// team was found leaves the result incomplete.
func (r *SyncResult) Complete() bool {
	for _, report := range r.Reports {
		if !report.TeamResolved {
			return false
		}
	}
	return true
}

// slugInvalidChars matches the runs of characters GitHub replaces with a
// hyphen when it derives a team's slug from its name.
var slugInvalidChars = regexp.MustCompile(`[^a-z0-9_-]+`)

// TeamSlug returns the slug GitHub gives a team with the given name. A slug
// is returned unchanged.
func TeamSlug(name string) string {
	return strings.Trim(slugInvalidChars.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

// Security alert severities, from lowest to highest.
const (
	SeverityLow      = "low"
	SeverityMedium   = "medium"
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

var severityRank = map[string]int{
	SeverityLow:      1,
	SeverityMedium:   2,
	SeverityHigh:     3,
	SeverityCritical: 4,
}

// SeverityRank returns a number that orders severities, or 0 for an unknown
// severity.
func SeverityRank(severity string) int {
	return severityRank[strings.ToLower(severity)]
}

// IsSeverity returns true if s is one of the known severities.
func IsSeverity(s string) bool {
	return SeverityRank(s) > 0
}

// MeetsSeverity returns true if severity is at or above minSeverity.
func MeetsSeverity(severity, minSeverity string) bool {
	return SeverityRank(severity) >= SeverityRank(minSeverity)
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

// HighestSeverity returns the highest severity among alerts, or "unknown".
func HighestSeverity(alerts []SecurityAlert) string {
	best := ""
	for _, a := range alerts {
		if SeverityRank(a.Severity) > SeverityRank(best) {
			best = strings.ToLower(a.Severity)
		}
	}
	if best == "" {
		return "unknown"
	}
	return best
}
