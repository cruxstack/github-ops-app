package notifiers

import (
	"context"
	"fmt"
	"sort"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/slack-go/slack"
)

// NotifyPRBypass sends a Slack notification when branch protection is
// bypassed.
func (s *SlackNotifier) NotifyPRBypass(ctx context.Context, result *domain.PRComplianceResult, repoFullName string) error {
	if result.PR == nil {
		return errors.Wrap(domain.ErrMissingPRData, "pr result missing")
	}

	prURL := ""
	prTitle := "unknown pr"
	prNumber := 0
	mergedBy := "unknown"

	if result.PR.HTMLURL != nil {
		prURL = *result.PR.HTMLURL
	}
	if result.PR.Title != nil {
		prTitle = *result.PR.Title
	}
	if result.PR.Number != nil {
		prNumber = *result.PR.Number
	}
	if result.PR.MergedBy != nil && result.PR.MergedBy.Login != nil {
		mergedBy = *result.PR.MergedBy.Login
	}

	// build merged by line with optional bypass reason
	mergedByText := fmt.Sprintf("Merged by %s", mergedBy)
	if result.UserHasBypass {
		mergedByText = fmt.Sprintf("Merged by %s (%s)", mergedBy, result.UserBypassReason)
	}

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject("plain_text", "🚨 Branch Protection Bypassed", false, false),
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("<%s|%s#%d> — %s", prURL, repoFullName, prNumber, prTitle), false, false),
			nil, nil,
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", mergedByText, false, false),
			nil, nil,
		),
	}

	if len(result.Violations) > 0 {
		violationText := "*Violations:*\n"
		for _, v := range result.Violations {
			violationText += fmt.Sprintf("• %s\n", v.Description)
		}
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", violationText, false, false),
			nil, nil,
		))
	}

	if s.messages.PRBypassFooterNote != "" {
		blocks = append(blocks, slack.NewContextBlock(
			"footer",
			slack.NewTextBlockObject("mrkdwn", s.messages.PRBypassFooterNote, false, false),
		))
	}

	channel := s.channelFor(s.channels.PRBypass)
	_, _, err := s.client.PostMessageContext(
		ctx,
		channel,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(fmt.Sprintf("branch protection bypassed on pr #%d", prNumber), false),
	)

	if err != nil {
		return errors.Wrap(err, "failed to post pr bypass notification to slack")
	}

	return nil
}

// NotifyOktaSync sends a Slack notification with Okta sync results.
func (s *SlackNotifier) NotifyOktaSync(ctx context.Context, reports []*domain.SyncReport, githubOrg string) error {
	if len(reports) == 0 {
		return nil
	}

	// aggregate stats
	var totalAdded, totalRemoved int
	var rulesWithChanges, rulesWithoutChanges []*domain.SyncReport
	var allErrors []string
	var allSkippedExternal, allSkippedNoGHUsername []string

	for _, report := range reports {
		totalAdded += len(report.MembersAdded)
		totalRemoved += len(report.MembersRemoved)

		if report.HasChanges() {
			rulesWithChanges = append(rulesWithChanges, report)
		} else if !report.HasErrors() {
			// only list as "no changes" if it didn't fail entirely
			rulesWithoutChanges = append(rulesWithoutChanges, report)
		}

		for _, err := range report.Errors {
			// use rule name as identifier since team/group may be empty on failure
			allErrors = append(allErrors, fmt.Sprintf("%s: %s", report.Rule, err))
		}

		allSkippedExternal = append(allSkippedExternal, report.MembersSkippedExternal...)
		allSkippedNoGHUsername = append(allSkippedNoGHUsername, report.MembersSkippedNoGHUsername...)
	}

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject("plain_text", "Okta GitHub Team Sync Complete", false, false),
		),
	}

	// summary stats (slack allows max 2 columns per row)
	rulesProcessedFields := []*slack.TextBlockObject{
		slack.NewTextBlockObject("mrkdwn", "*Rules Processed*", false, false),
		slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("%d", len(reports)), false, false),
	}
	memberChangesFields := []*slack.TextBlockObject{
		slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Members Added*\n%d", totalAdded), false, false),
		slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Members Removed*\n%d", totalRemoved), false, false),
	}
	blocks = append(blocks, slack.NewSectionBlock(nil, rulesProcessedFields, nil))
	blocks = append(blocks, slack.NewSectionBlock(nil, memberChangesFields, nil))

	// helper to build team URL
	teamURL := func(teamSlug string) string {
		return fmt.Sprintf("https://github.com/orgs/%s/teams/%s", githubOrg, teamSlug)
	}

	// list of rules with changes
	if len(rulesWithChanges) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())

		changesText := "*Rules With Changes*\n"
		for _, report := range rulesWithChanges {
			changesText += fmt.Sprintf("- <%s|%s> (+%d, -%d)\n",
				teamURL(report.GitHubTeam),
				report.GitHubTeam,
				len(report.MembersAdded),
				len(report.MembersRemoved))
		}

		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", changesText, false, false),
			nil, nil,
		))
	}

	// list of rules without changes
	if len(rulesWithoutChanges) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())

		noChangesText := "*Rules With No Changes*\n"
		for _, report := range rulesWithoutChanges {
			noChangesText += fmt.Sprintf("- <%s|%s>\n", teamURL(report.GitHubTeam), report.GitHubTeam)
		}

		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", noChangesText, false, false),
			nil, nil,
		))
	}

	// errors section
	if len(allErrors) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())

		errorsText := "*Errors*\n"
		for _, err := range allErrors {
			errorsText += fmt.Sprintf("- %s\n", err)
		}

		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", errorsText, false, false),
			nil, nil,
		))
	}

	// skipped members section
	if len(allSkippedExternal) > 0 || len(allSkippedNoGHUsername) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())

		skippedText := "*Skipped Members*\n"

		if len(allSkippedExternal) > 0 {
			skippedText += "_External Collaborators_\n"
			for _, member := range allSkippedExternal {
				skippedText += fmt.Sprintf("- %s\n", member)
			}
		}

		if len(allSkippedNoGHUsername) > 0 {
			if len(allSkippedExternal) > 0 {
				skippedText += "\n"
			}
			skippedText += "_No GitHub Username In Okta:_\n"
			for _, member := range allSkippedNoGHUsername {
				skippedText += fmt.Sprintf("- %s\n", member)
			}
		}

		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", skippedText, false, false),
			nil, nil,
		))
	}

	channel := s.channelFor(s.channels.OktaSync)
	_, _, err := s.client.PostMessageContext(
		ctx,
		channel,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(fmt.Sprintf("okta sync: %d rules, +%d/-%d members", len(reports), totalAdded, totalRemoved), false),
	)

	if err != nil {
		return errors.Wrap(err, "failed to post okta sync notification to slack")
	}

	return nil
}

// NotifyOrphanedUsers sends a Slack notification about organization members
// not in any synced teams.
func (s *SlackNotifier) NotifyOrphanedUsers(ctx context.Context, report *domain.OrphanedUsersReport) error {
	if report == nil || len(report.OrphanedUsers) == 0 {
		return nil
	}

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject("plain_text", "⚠️ Orphaned GitHub Users Detected", false, false),
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("Found *%d* organization member(s) not in any Okta-synced GitHub teams:", len(report.OrphanedUsers)),
				false, false),
			nil, nil,
		),
	}

	userList := ""
	for _, user := range report.OrphanedUsers {
		userList += fmt.Sprintf("• `%s`\n", user)
	}

	blocks = append(blocks, slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", userList, false, false),
		nil, nil,
	))

	blocks = append(blocks, slack.NewContextBlock(
		"context",
		slack.NewTextBlockObject("mrkdwn", "_These users may need to be added to Okta groups or removed from the organization._", false, false),
	))

	channel := s.channelFor(s.channels.OrphanedUsers)
	_, _, err := s.client.PostMessageContext(
		ctx,
		channel,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(fmt.Sprintf("orphaned github users detected: %d users", len(report.OrphanedUsers)), false),
	)

	if err != nil {
		return errors.Wrap(err, "failed to post orphaned users notification to slack")
	}

	return nil
}

// maxReposInNotification limits the number of repositories shown in a
// single Slack message to avoid exceeding block limits.
const maxReposInNotification = 15

// NotifySecurityAlerts sends a Slack notification summarizing open
// security alerts across the organization.
func (s *SlackNotifier) NotifySecurityAlerts(ctx context.Context, report *domain.SecurityAlertsReport, githubOrg string) error {
	if report == nil || !report.HasAlerts() {
		return nil
	}

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject("plain_text",
				"Security Alerts Report", false, false),
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf(
					"*%d* open alert(s) across *%d* repository(s) in `%s`",
					report.TotalAlerts,
					report.RepoCount(),
					githubOrg,
				),
				false, false),
			nil, nil,
		),
	}

	// repo list sorted by most alerts first
	repos := sortedReposByAlertCount(report.AlertsByRepo)

	repoLines := ""
	for i, repo := range repos {
		if i >= maxReposInNotification {
			repoLines += fmt.Sprintf(
				"\n_…and %d more_", len(repos)-i)
			break
		}

		alerts := report.AlertsByRepo[repo]
		highest := highestSeverity(alerts)
		secURL := fmt.Sprintf(
			"https://github.com/%s/security", repo)

		repoLines += fmt.Sprintf("• <%s|%s> — %d alert(s), %s severity\n",
			secURL, repo, len(alerts), highest)
	}

	blocks = append(blocks, slack.NewSectionBlock(
		slack.NewTextBlockObject("mrkdwn", repoLines, false, false),
		nil, nil,
	))

	if report.HasErrors() {
		errText := "*Errors:*\n"
		for _, e := range report.Errors {
			errText += fmt.Sprintf("• %s\n", e)
		}
		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", errText,
				false, false),
			nil, nil,
		))
	}

	blocks = append(blocks, slack.NewContextBlock("",
		slack.NewTextBlockObject("mrkdwn",
			fmt.Sprintf(
				"Filtered to open alerts older than %d days · severity %s or above",
				report.MinAgeDays,
				report.MinSeverity,
			),
			false, false),
	))

	channel := s.channelFor(s.channels.SecurityAlerts)
	_, _, err := s.client.PostMessageContext(
		ctx,
		channel,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(
			fmt.Sprintf("security alerts: %d open alerts across %d repos in %s",
				report.TotalAlerts, report.RepoCount(), githubOrg),
			false),
	)

	if err != nil {
		return errors.Wrap(err,
			"failed to post security alerts notification to slack")
	}

	return nil
}

// sortedReposByAlertCount returns repo names sorted by descending
// alert count, with alphabetical tiebreak.
func sortedReposByAlertCount(alertsByRepo map[string][]domain.SecurityAlert) []string {
	repos := make([]string, 0, len(alertsByRepo))
	for repo := range alertsByRepo {
		repos = append(repos, repo)
	}
	sort.Slice(repos, func(i, j int) bool {
		ci := len(alertsByRepo[repos[i]])
		cj := len(alertsByRepo[repos[j]])
		if ci != cj {
			return ci > cj
		}
		return repos[i] < repos[j]
	})
	return repos
}

// highestSeverity returns the highest severity label among alerts.
func highestSeverity(alerts []domain.SecurityAlert) string {
	best := 0
	for _, a := range alerts {
		if r := severityRank[a.Severity]; r > best {
			best = r
		}
	}
	for _, sev := range []string{"critical", "high", "medium", "low"} {
		if severityRank[sev] == best {
			return sev
		}
	}
	return "unknown"
}

var severityRank = map[string]int{
	"critical": 4,
	"high":     3,
	"medium":   2,
	"low":      1,
}
