package notifiers

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/slack-go/slack"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// post sends blocks to a channel with a plain-text fallback.
func (s *SlackNotifier) post(ctx context.Context, channel, fallback string, blocks []slack.Block) error {
	_, _, err := s.client.PostMessageContext(ctx, channel,
		slack.MsgOptionBlocks(limitBlocks(blocks)...),
		slack.MsgOptionText(fallback, true),
	)
	return err
}

// NotifyPRBypass sends a Slack notification when a PR was merged without
// meeting its branch's requirements.
func (s *SlackNotifier) NotifyPRBypass(ctx context.Context, result *domain.PRComplianceResult, repoFullName string) error {
	if result == nil || result.PR == nil {
		return fmt.Errorf("pr result missing: %w", domain.ErrMissingPRData)
	}
	pr := result.PR

	mergedBy := pr.GetMergedBy().GetLogin()
	if mergedBy == "" {
		mergedBy = "unknown"
	}
	mergedByText := "Merged by " + escape(mergedBy)
	if result.UserHasBypass && result.UserBypassReason != "" {
		mergedByText += " (" + escape(result.UserBypassReason) + ")"
	}

	title := fmt.Sprintf("%s#%d", repoFullName, pr.GetNumber())
	blocks := []slack.Block{
		slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, "🚨 Branch Protection Bypassed", false, false)),
		section(fmt.Sprintf("%s — %s", link(pr.GetHTMLURL(), title), escape(pr.GetTitle()))),
		section(mergedByText + " into `" + escape(result.BaseBranch) + "`"),
	}

	violations := make([]string, 0, len(result.Violations))
	for _, v := range result.Violations {
		violations = append(violations, "• "+escape(v.Description))
	}
	blocks = append(blocks, listSections("*Violations:*", violations)...)

	if s.messages.PRBypassFooterNote != "" {
		blocks = append(blocks, slack.NewContextBlock("footer", mrkdwn(s.messages.PRBypassFooterNote)))
	}

	err := s.post(ctx, s.channelFor(s.channels.PRBypass),
		fmt.Sprintf("Branch protection bypassed on %s", title), blocks)
	if err != nil {
		return fmt.Errorf("failed to post pr bypass notification for %s to slack: %w", title, err)
	}
	return nil
}

// NotifyOktaSync sends a Slack notification with Okta sync results.
func (s *SlackNotifier) NotifyOktaSync(ctx context.Context, reports []*domain.SyncReport, githubOrg string) error {
	if len(reports) == 0 {
		return nil
	}

	var totalAdded, totalRemoved int
	var changed, unchanged, errs, skippedExternal, skippedNoUsername []string
	for _, report := range reports {
		totalAdded += len(report.MembersAdded)
		totalRemoved += len(report.MembersRemoved)

		team := s.teamLink(githubOrg, report.GitHubTeam)
		switch {
		case report.HasChanges():
			changed = append(changed, fmt.Sprintf("• %s (+%d, -%d)", team, len(report.MembersAdded), len(report.MembersRemoved)))
		case !report.HasErrors():
			unchanged = append(unchanged, "• "+team)
		}
		for _, err := range report.Errors {
			errs = append(errs, fmt.Sprintf("• %s: %s", escape(report.Rule), escape(err)))
		}
		for _, m := range report.MembersSkippedExternal {
			skippedExternal = append(skippedExternal, "• "+escape(m))
		}
		for _, m := range report.MembersSkippedNoGHUsername {
			skippedNoUsername = append(skippedNoUsername, "• "+escape(m))
		}
	}

	blocks := []slack.Block{
		slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, "Okta GitHub Team Sync Complete", false, false)),
		slack.NewSectionBlock(nil, []*slack.TextBlockObject{
			mrkdwn(fmt.Sprintf("*Teams Processed*\n%d", len(reports))),
			mrkdwn(fmt.Sprintf("*Errors*\n%d", len(errs))),
			mrkdwn(fmt.Sprintf("*Members Added*\n%d", totalAdded)),
			mrkdwn(fmt.Sprintf("*Members Removed*\n%d", totalRemoved)),
		}, nil),
	}

	for _, list := range []struct {
		heading string
		lines   []string
	}{
		{"*Teams With Changes*", changed},
		{"*Teams With No Changes*", unchanged},
		{"*Errors*", errs},
		{"*Skipped: Not Organization Members*", skippedExternal},
		{"*Skipped: No GitHub Username In Okta*", skippedNoUsername},
	} {
		if len(list.lines) > 0 {
			blocks = append(blocks, slack.NewDividerBlock())
			blocks = append(blocks, listSections(list.heading, list.lines)...)
		}
	}

	err := s.post(ctx, s.channelFor(s.channels.OktaSync),
		fmt.Sprintf("Okta sync: %d teams, +%d/-%d members, %d errors", len(reports), totalAdded, totalRemoved, len(errs)), blocks)
	if err != nil {
		return fmt.Errorf("failed to post okta sync notification to slack: %w", err)
	}
	return nil
}

// teamLink returns a mrkdwn link to a team.
func (s *SlackNotifier) teamLink(org, slug string) string {
	if slug == "" {
		return "_unknown team_"
	}
	return link(fmt.Sprintf("%s/orgs/%s/teams/%s", s.githubWebURL, org, slug), slug)
}

// NotifyOrphanedUsers sends a Slack notification about organization members
// not in any synced teams.
func (s *SlackNotifier) NotifyOrphanedUsers(ctx context.Context, report *domain.OrphanedUsersReport) error {
	if report == nil || len(report.OrphanedUsers) == 0 {
		return nil
	}

	users := slices.Clone(report.OrphanedUsers)
	slices.SortFunc(users, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	lines := make([]string, 0, len(users))
	for _, user := range users {
		lines = append(lines, "• `"+escape(user)+"`")
	}

	blocks := []slack.Block{
		slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, "⚠️ Orphaned GitHub Users Detected", false, false)),
		section(fmt.Sprintf("Found *%d* organization member(s) not in any Okta-synced GitHub team:", len(users))),
	}
	blocks = append(blocks, listSections("", lines)...)
	blocks = append(blocks, slack.NewContextBlock("context",
		mrkdwn("_These users may need to be added to Okta groups or removed from the organization._")))

	err := s.post(ctx, s.channelFor(s.channels.OrphanedUsers),
		fmt.Sprintf("Orphaned GitHub users detected: %d users", len(users)), blocks)
	if err != nil {
		return fmt.Errorf("failed to post orphaned users notification to slack: %w", err)
	}
	return nil
}

// NotifySecurityAlerts sends a Slack notification summarizing stale open
// security alerts across the organization.
func (s *SlackNotifier) NotifySecurityAlerts(ctx context.Context, report *domain.SecurityAlertsReport, githubOrg string) error {
	if report == nil || (!report.HasAlerts() && !report.HasErrors()) {
		return nil
	}

	blocks := []slack.Block{
		slack.NewHeaderBlock(slack.NewTextBlockObject(slack.PlainTextType, "Security Alerts Report", false, false)),
		section(fmt.Sprintf("*%d* open alert(s) across *%d* repository(s) in `%s`",
			report.TotalAlerts, report.RepoCount(), escape(githubOrg))),
	}

	repos := sortedReposByAlertCount(report.AlertsByRepo)
	lines := make([]string, 0, len(repos))
	for _, repo := range repos {
		alerts := report.AlertsByRepo[repo]
		lines = append(lines, fmt.Sprintf("• %s — %d alert(s), %s severity",
			link(fmt.Sprintf("%s/%s/security", s.githubWebURL, repo), repo), len(alerts), domain.HighestSeverity(alerts)))
	}
	blocks = append(blocks, listSections("", lines)...)

	errs := make([]string, 0, len(report.Errors))
	for _, e := range report.Errors {
		errs = append(errs, "• "+escape(e))
	}
	blocks = append(blocks, listSections("*Errors:*", errs)...)

	blocks = append(blocks, slack.NewContextBlock("context", mrkdwn(fmt.Sprintf(
		"Open alerts older than %d days with severity %s or above", report.MinAgeDays, escape(report.MinSeverity)))))

	err := s.post(ctx, s.channelFor(s.channels.SecurityAlerts),
		fmt.Sprintf("Security alerts: %d open alerts across %d repos in %s", report.TotalAlerts, report.RepoCount(), githubOrg), blocks)
	if err != nil {
		return fmt.Errorf("failed to post security alerts notification to slack: %w", err)
	}
	return nil
}

// sortedReposByAlertCount returns repo names sorted by descending alert
// count, then by name.
func sortedReposByAlertCount(alertsByRepo map[string][]domain.SecurityAlert) []string {
	repos := make([]string, 0, len(alertsByRepo))
	for repo := range alertsByRepo {
		repos = append(repos, repo)
	}
	slices.SortFunc(repos, func(a, b string) int {
		if d := len(alertsByRepo[b]) - len(alertsByRepo[a]); d != 0 {
			return d
		}
		return strings.Compare(a, b)
	})
	return repos
}
