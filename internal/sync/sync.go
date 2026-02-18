// Package sync coordinates synchronization of Okta groups to GitHub teams.
// depends only on domain interfaces, not concrete client implementations.
package sync

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/domain"
)

// teamNameNormalizer replaces non-alphanumeric characters (except hyphens)
// in team names. compiled once at package init.
var teamNameNormalizer = regexp.MustCompile(`[^a-z0-9-]+`)

// Syncer coordinates synchronization of Okta groups to GitHub teams.
type Syncer struct {
	oktaClient      domain.OktaClient
	githubClient    domain.GitHubClient
	rules           []domain.SyncRule
	safetyThreshold float64
	logger          *slog.Logger
}

// NewSyncer creates a new Okta to GitHub syncer.
func NewSyncer(oktaClient domain.OktaClient, githubClient domain.GitHubClient, rules []domain.SyncRule, safetyThreshold float64, logger *slog.Logger) *Syncer {
	return &Syncer{
		oktaClient:      oktaClient,
		githubClient:    githubClient,
		rules:           rules,
		safetyThreshold: safetyThreshold,
		logger:          logger,
	}
}

// Sync executes all enabled sync rules and returns reports.
// continues processing remaining rules even if some fail.
func (s *Syncer) Sync(ctx context.Context) (*domain.SyncResult, error) {
	var reports []*domain.SyncReport
	var enabledRuleCount int
	var failedRuleCount int

	for _, rule := range s.rules {
		if !rule.IsEnabled() {
			continue
		}

		enabledRuleCount++

		ruleReports, err := s.syncRule(ctx, rule)
		if err != nil {
			failedRuleCount++
			s.logger.Error("sync rule failed",
				slog.String("rule", rule.GetName()),
				slog.String("error", err.Error()))

			reports = append(reports, &domain.SyncReport{
				Rule:       rule.GetName(),
				OktaGroup:  rule.OktaGroupName,
				GitHubTeam: rule.GitHubTeamName,
				Errors:     []string{err.Error()},
			})
			continue
		}

		reports = append(reports, ruleReports...)
	}

	if enabledRuleCount > 0 && failedRuleCount == enabledRuleCount {
		return nil, errors.Newf("all sync rules failed: %d errors", failedRuleCount)
	}

	return &domain.SyncResult{
		Reports:       reports,
		OrphanedUsers: nil,
	}, nil
}

// DetectOrphanedUsers finds organization members not in any synced teams.
// excludes external collaborators.
func (s *Syncer) DetectOrphanedUsers(ctx context.Context, syncedTeams []string) (*domain.OrphanedUsersReport, error) {
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

	return &domain.OrphanedUsersReport{
		OrphanedUsers: orphanedUsers,
	}, nil
}

// syncRule executes a single sync rule.
// supports both pattern matching and exact group name matching.
func (s *Syncer) syncRule(ctx context.Context, rule domain.SyncRule) ([]*domain.SyncReport, error) {
	var reports []*domain.SyncReport

	if rule.OktaGroupPattern != "" {
		groups, err := s.oktaClient.GetGroupsByPattern(ctx, rule.OktaGroupPattern)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to match groups with pattern '%s'", rule.OktaGroupPattern)
		}

		for _, group := range groups {
			teamName := computeTeamName(group.Name, rule)
			report := s.syncGroupToTeam(ctx, rule, group, teamName)
			reports = append(reports, report)
		}
	} else if rule.OktaGroupName != "" {
		group, err := s.oktaClient.GetGroupInfo(ctx, rule.OktaGroupName)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to fetch group '%s'", rule.OktaGroupName)
		}

		teamName := computeTeamName(group.Name, rule)
		report := s.syncGroupToTeam(ctx, rule, group, teamName)
		reports = append(reports, report)
	}

	return reports, nil
}

// computeTeamName generates GitHub team name from Okta group name.
// applies prefix stripping, prefix addition, and normalization.
func computeTeamName(oktaGroupName string, rule domain.SyncRule) string {
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
	teamName = teamNameNormalizer.ReplaceAllString(teamName, "-")
	teamName = strings.Trim(teamName, "-")

	return teamName
}

// syncGroupToTeam synchronizes a single Okta group to a GitHub team.
// creates team if missing and syncs members if enabled.
func (s *Syncer) syncGroupToTeam(ctx context.Context, rule domain.SyncRule, group *domain.GroupInfo, teamName string) *domain.SyncReport {
	report := &domain.SyncReport{
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
