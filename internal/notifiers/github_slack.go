// Package notifiers provides Slack notification formatting for GitHub and
// Okta events.
package notifiers

import (
	"context"
	"fmt"

	"github.com/cruxstack/github-ops-app/internal/errors"
	"github.com/cruxstack/github-ops-app/internal/github"
	"github.com/cruxstack/github-ops-app/internal/okta"
	"github.com/slack-go/slack"
)

// NotifyPRBypass sends a Slack notification when branch protection is
// bypassed.
func (s *SlackNotifier) NotifyPRBypass(ctx context.Context, result *github.PRComplianceResult, repoFullName string) error {
	if result.PR == nil {
		return fmt.Errorf("%w: pr result missing", errors.ErrMissingPRData)
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

	channel := s.channelFor(s.channels.PRBypass)
	_, _, err := s.client.PostMessageContext(
		ctx,
		channel,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(fmt.Sprintf("branch protection bypassed on pr #%d", prNumber), false),
	)

	if err != nil {
		return fmt.Errorf("failed to post pr bypass notification to slack: %w", err)
	}

	return nil
}

// NotifyOktaSync sends a Slack notification with Okta sync results.
func (s *SlackNotifier) NotifyOktaSync(ctx context.Context, reports []*okta.SyncReport, githubOrg string) error {
	if len(reports) == 0 {
		return nil
	}

	// aggregate stats
	var totalAdded, totalRemoved int
	var rulesWithChanges, rulesWithoutChanges []*okta.SyncReport
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
		return fmt.Errorf("failed to post okta sync notification to slack: %w", err)
	}

	return nil
}

// NotifyOrphanedUsers sends a Slack notification about organization members
// not in any synced teams.
func (s *SlackNotifier) NotifyOrphanedUsers(ctx context.Context, report *okta.OrphanedUsersReport) error {
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
		return fmt.Errorf("failed to post orphaned users notification to slack: %w", err)
	}

	return nil
}
