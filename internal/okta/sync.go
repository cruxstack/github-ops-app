// Package okta provides Okta group to GitHub team synchronization.
package okta

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/github"
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

// ShouldSyncMembers returns true if members should be synced (defaults to true).
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

// OrphanedUsersReport contains users who are org members but not in any synced
// teams.
type OrphanedUsersReport struct {
	OrphanedUsers []string
}

// HasErrors returns true if any errors occurred during sync.
func (r *SyncReport) HasErrors() bool {
	return len(r.Errors) > 0
}

// HasChanges returns true if members were added or removed.
func (r *SyncReport) HasChanges() bool {
	return len(r.MembersAdded) > 0 || len(r.MembersRemoved) > 0
}

// Syncer coordinates synchronization of Okta groups to GitHub teams.
type Syncer struct {
	oktaClient      *Client
	githubClient    *github.Client
	rules           []SyncRule
	safetyThreshold float64
	logger          *slog.Logger
}

// NewSyncer creates a new Okta to GitHub syncer.
func NewSyncer(oktaClient *Client, githubClient *github.Client, rules []SyncRule, safetyThreshold float64, logger *slog.Logger) *Syncer {
	return &Syncer{
		oktaClient:      oktaClient,
		githubClient:    githubClient,
		rules:           rules,
		safetyThreshold: safetyThreshold,
		logger:          logger,
	}
}

// SyncResult contains all sync reports and orphaned users report.
type SyncResult struct {
	Reports       []*SyncReport
	OrphanedUsers *OrphanedUsersReport
}

// Sync executes all enabled sync rules and returns reports.
// continues processing remaining rules even if some fail.
func (s *Syncer) Sync(ctx context.Context) (*SyncResult, error) {
	var reports []*SyncReport
	var failedRuleCount int

	for _, rule := range s.rules {
		if !rule.IsEnabled() {
			continue
		}

		ruleReports, err := s.syncRule(ctx, rule)
		if err != nil {
			failedRuleCount++
			s.logger.Error("sync rule failed",
				slog.String("rule", rule.GetName()),
				slog.String("error", err.Error()))

			// create a report for the failed rule so error is visible
			reports = append(reports, &SyncReport{
				Rule:       rule.GetName(),
				OktaGroup:  rule.OktaGroupName,
				GitHubTeam: rule.GitHubTeamName,
				Errors:     []string{err.Error()},
			})
			continue
		}

		reports = append(reports, ruleReports...)
	}

	if failedRuleCount > 0 && failedRuleCount == len(reports) {
		return nil, errors.Newf("all sync rules failed: %d errors", failedRuleCount)
	}

	return &SyncResult{
		Reports:       reports,
		OrphanedUsers: nil,
	}, nil
}

// DetectOrphanedUsers finds organization members not in any synced teams.
// excludes external collaborators.
func (s *Syncer) DetectOrphanedUsers(ctx context.Context, syncedTeams []string) (*OrphanedUsersReport, error) {
	orgMembers, err := s.githubClient.ListOrgMembers(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list organization members")
	}

	syncedUsers := make(map[string]bool)
	for _, teamSlug := range syncedTeams {
		members, err := s.githubClient.GetTeamMembers(ctx, teamSlug)
		if err != nil {
			s.logger.Warn("failed to get team members for orphaned user check",
				slog.String("team", teamSlug),
				slog.String("error", err.Error()))
			continue
		}
		for _, member := range members {
			syncedUsers[member] = true
		}
	}

	var orphanedUsers []string
	for _, member := range orgMembers {
		if !syncedUsers[member] {
			isExternal, err := s.githubClient.IsExternalCollaborator(ctx, member)
			if err != nil {
				s.logger.Warn("failed to check if user is external for orphaned user check",
					slog.String("user", member),
					slog.String("error", err.Error()))
				continue
			}

			if !isExternal {
				orphanedUsers = append(orphanedUsers, member)
			}
		}
	}

	return &OrphanedUsersReport{
		OrphanedUsers: orphanedUsers,
	}, nil
}

// syncRule executes a single sync rule.
// supports both pattern matching and exact group name matching.
func (s *Syncer) syncRule(ctx context.Context, rule SyncRule) ([]*SyncReport, error) {
	var reports []*SyncReport

	if rule.OktaGroupPattern != "" {
		groups, err := s.oktaClient.GetGroupsByPattern(rule.OktaGroupPattern)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to match groups with pattern '%s'", rule.OktaGroupPattern)
		}

		for _, group := range groups {
			teamName := s.computeTeamName(group.Name, rule)
			report := s.syncGroupToTeam(ctx, rule, group, teamName)
			reports = append(reports, report)
		}
	} else if rule.OktaGroupName != "" {
		group, err := s.oktaClient.GetGroupInfo(rule.OktaGroupName)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to fetch group '%s'", rule.OktaGroupName)
		}

		teamName := s.computeTeamName(group.Name, rule)
		report := s.syncGroupToTeam(ctx, rule, group, teamName)
		reports = append(reports, report)
	}

	return reports, nil
}

// computeTeamName generates GitHub team name from Okta group name.
// applies prefix stripping, prefix addition, and normalization.
func (s *Syncer) computeTeamName(oktaGroupName string, rule SyncRule) string {
	if rule.GitHubTeamName != "" {
		return rule.GitHubTeamName
	}

	teamName := oktaGroupName

	if rule.StripPrefix != "" {
		teamName = strings.TrimPrefix(teamName, rule.StripPrefix)
	}

	if rule.GitHubTeamPrefix != "" {
		teamName = rule.GitHubTeamPrefix + teamName
	}

	teamName = strings.ToLower(teamName)
	teamName = regexp.MustCompile(`[^a-z0-9-]`).ReplaceAllString(teamName, "-")

	return teamName
}

// syncGroupToTeam synchronizes a single Okta group to a GitHub team.
// creates team if missing and syncs members if enabled.
func (s *Syncer) syncGroupToTeam(ctx context.Context, rule SyncRule, group *GroupInfo, teamName string) *SyncReport {
	report := &SyncReport{
		Rule:                       rule.GetName(),
		OktaGroup:                  group.Name,
		GitHubTeam:                 teamName,
		MembersSkippedNoGHUsername: group.SkippedNoGitHubUsername,
		Errors:                     []string{},
	}

	if len(group.SkippedNoGitHubUsername) > 0 {
		s.logger.Warn("okta users skipped due to missing github username",
			slog.String("group", group.Name),
			slog.Int("count", len(group.SkippedNoGitHubUsername)))
	}

	privacy := "closed"
	if rule.TeamPrivacy != "" {
		privacy = rule.TeamPrivacy
	}

	team, err := s.githubClient.GetOrCreateTeam(ctx, teamName, privacy)
	if err != nil {
		errMsg := fmt.Sprintf("failed to get/create team '%s': %v", teamName, err)
		report.Errors = append(report.Errors, errMsg)
		return report
	}

	if team == nil {
		errMsg := fmt.Sprintf("team '%s' is nil after get/create", teamName)
		report.Errors = append(report.Errors, errMsg)
		return report
	}

	if !rule.ShouldSyncMembers() {
		return report
	}

	teamSlug := teamName
	if team.Slug != nil {
		teamSlug = *team.Slug
	}

	syncResult, err := s.githubClient.SyncTeamMembers(ctx, teamSlug, group.Members, s.safetyThreshold)
	if err != nil {
		report.Errors = append(report.Errors, fmt.Sprintf("failed to sync members for team '%s': %v", teamSlug, err))
		return report
	}

	report.MembersAdded = syncResult.MembersAdded
	report.MembersRemoved = syncResult.MembersRemoved
	report.MembersSkippedExternal = syncResult.MembersSkippedExternal
	report.Errors = append(report.Errors, syncResult.Errors...)

	return report
}
