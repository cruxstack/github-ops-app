// Package teamsync syncs the members of Okta groups to GitHub teams. It
// depends only on the domain interfaces.
package teamsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// teamNameInvalidChars matches the characters replaced with a hyphen when a
// team name is derived from an Okta group name.
var teamNameInvalidChars = regexp.MustCompile(`[^a-z0-9-]+`)

// Syncer syncs Okta groups to GitHub teams according to a set of rules.
type Syncer struct {
	okta            domain.OktaClient
	github          domain.GitHubClient
	rules           []domain.SyncRule
	safetyThreshold float64
	logger          *slog.Logger
}

// NewSyncer creates a Syncer.
func NewSyncer(okta domain.OktaClient, github domain.GitHubClient, rules []domain.SyncRule, safetyThreshold float64, logger *slog.Logger) *Syncer {
	return &Syncer{
		okta:            okta,
		github:          github,
		rules:           rules,
		safetyThreshold: safetyThreshold,
		logger:          logger,
	}
}

// Sync runs every enabled rule and returns a report per synced group. A
// rule that fails is reported and the others still run; Sync returns an
// error only when every enabled rule failed.
func (s *Syncer) Sync(ctx context.Context) (*domain.SyncResult, error) {
	result := &domain.SyncResult{}
	// claimed maps each team synced in this run to the group synced into
	// it, since a second group would remove the first group's members
	claimed := make(map[string]string)
	var enabled, failed int

	for _, rule := range s.rules {
		if !rule.IsEnabled() {
			continue
		}
		enabled++

		reports, err := s.syncRule(ctx, rule, claimed)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			failed++
			s.logger.ErrorContext(ctx, "sync rule failed", "rule", rule.GetName(), "error", err.Error())
			result.Reports = append(result.Reports, &domain.SyncReport{
				Rule:      rule.GetName(),
				OktaGroup: rule.OktaGroupName,
				Errors:    []string{err.Error()},
			})
			continue
		}
		result.Reports = append(result.Reports, reports...)
	}

	if enabled > 0 && failed == enabled {
		return result, fmt.Errorf("all %d sync rules failed", failed)
	}
	return result, nil
}

// syncRule syncs every group a rule matches.
func (s *Syncer) syncRule(ctx context.Context, rule domain.SyncRule, claimed map[string]string) ([]*domain.SyncReport, error) {
	var groups []*domain.GroupInfo
	switch {
	case rule.OktaGroupPattern != "":
		matched, err := s.okta.GetGroupsByPattern(ctx, rule.OktaGroupPattern)
		if err != nil {
			return nil, fmt.Errorf("failed to match okta groups with pattern '%s': %w", rule.OktaGroupPattern, err)
		}
		groups = matched
	case rule.OktaGroupName != "":
		group, err := s.okta.GetGroupInfo(ctx, rule.OktaGroupName)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch okta group '%s': %w", rule.OktaGroupName, err)
		}
		groups = []*domain.GroupInfo{group}
	default:
		return nil, fmt.Errorf("rule '%s' has neither okta_group_pattern nor okta_group_name", rule.GetName())
	}

	reports := make([]*domain.SyncReport, 0, len(groups))
	for _, group := range groups {
		reports = append(reports, s.syncGroup(ctx, rule, group, claimed))
	}
	return reports, nil
}

// TeamName derives the GitHub team name for an Okta group under a rule.
func TeamName(oktaGroupName string, rule domain.SyncRule) string {
	if rule.GitHubTeamName != "" {
		return rule.GitHubTeamName
	}
	name := strings.TrimPrefix(oktaGroupName, rule.StripPrefix)
	name = strings.ToLower(rule.GitHubTeamPrefix + name)
	name = teamNameInvalidChars.ReplaceAllString(name, "-")
	return strings.Trim(name, "-")
}

// syncGroup syncs one Okta group to its GitHub team and reports the result.
func (s *Syncer) syncGroup(ctx context.Context, rule domain.SyncRule, group *domain.GroupInfo, claimed map[string]string) *domain.SyncReport {
	teamName := TeamName(group.Name, rule)
	report := &domain.SyncReport{
		Rule:                       rule.GetName(),
		OktaGroup:                  group.Name,
		GitHubTeam:                 teamName,
		MembersSkippedNoGHUsername: group.SkippedNoGitHubUsername,
		Errors:                     []string{},
	}
	fail := func(format string, args ...any) *domain.SyncReport {
		report.Errors = append(report.Errors, fmt.Sprintf(format, args...))
		return report
	}

	if group.Err != nil {
		return fail("failed to fetch members of okta group '%s': %v", group.Name, group.Err)
	}
	if teamName == "" {
		return fail("okta group '%s' maps to an empty team name", group.Name)
	}
	if len(group.SkippedNoGitHubUsername) > 0 {
		s.logger.WarnContext(ctx, "okta users skipped without a valid github username",
			"group", group.Name,
			"count", len(group.SkippedNoGitHubUsername))
	}

	slug, err := s.github.ResolveTeam(ctx, teamName, rule.Privacy(), rule.CreateTeamIfMissing)
	if err != nil {
		return fail("failed to resolve team '%s': %v", teamName, err)
	}
	report.GitHubTeam = slug
	report.TeamResolved = true

	if other, ok := claimed[slug]; ok {
		return fail("team '%s' is already synced from okta group '%s' in this run; skipping okta group '%s'", slug, other, group.Name)
	}
	claimed[slug] = group.Name

	if !rule.ShouldSyncMembers() {
		return report
	}

	result, err := s.github.SyncTeamMembers(ctx, slug, group.Members, s.safetyThreshold)
	if err != nil {
		return fail("failed to sync members of team '%s': %v", slug, err)
	}
	report.MembersAdded = result.MembersAdded
	report.MembersRemoved = result.MembersRemoved
	report.MembersSkippedExternal = result.MembersSkippedExternal
	report.Members = result.Members
	report.Errors = append(report.Errors, result.Errors...)
	return report
}

// DetectOrphanedUsers finds organization members who are in none of the
// teams a sync covered. It uses the member lists the sync ended with, and
// reads the members of teams whose members were not synced. It returns an
// error rather than report a team's members as orphaned when the result is
// incomplete or a team's members can't be read.
func (s *Syncer) DetectOrphanedUsers(ctx context.Context, result *domain.SyncResult) (*domain.OrphanedUsersReport, error) {
	if !result.Complete() {
		return nil, errors.New("not every synced team was found, so its members are unknown")
	}

	known := make(map[string][]string)
	for _, report := range result.Reports {
		if report.Members != nil {
			known[report.GitHubTeam] = report.Members
		}
	}

	covered := make(map[string]bool)
	for _, team := range result.ManagedTeams() {
		members, ok := known[team]
		if !ok {
			var err error
			if members, err = s.github.GetTeamMembers(ctx, team); err != nil {
				return nil, err
			}
		}
		for _, member := range members {
			covered[strings.ToLower(member)] = true
		}
	}

	orgMembers, err := s.github.ListOrgMembers(ctx)
	if err != nil {
		return nil, err
	}
	report := &domain.OrphanedUsersReport{OrphanedUsers: []string{}}
	for _, member := range orgMembers {
		if !covered[strings.ToLower(member)] {
			report.OrphanedUsers = append(report.OrphanedUsers, member)
		}
	}
	return report, nil
}
