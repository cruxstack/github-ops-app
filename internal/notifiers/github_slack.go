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

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject("plain_text", "🚨 Branch Protection Bypassed", false, false),
		),
		slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*PR #%d*: %s", prNumber, prTitle), false, false),
			nil, nil,
		),
	}

	detailsFields := []*slack.TextBlockObject{
		slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Repository*\n%s", repoFullName), false, false),
		slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Branch*\n%s", result.BaseBranch), false, false),
		slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Merged by*\n%s", mergedBy), false, false),
	}

	if result.UserHasBypass {
		detailsFields = append(detailsFields,
			slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*User Permission*\n%s", result.UserBypassReason), false, false),
		)
	}

	blocks = append(blocks, slack.NewSectionBlock(nil, detailsFields, nil))

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

	buttons := slack.NewActionBlock(
		"actions",
		slack.NewButtonBlockElement("view_pr", "view", slack.NewTextBlockObject("plain_text", "View PR", false, false)).WithStyle(slack.StylePrimary).WithURL(prURL),
	)
	blocks = append(blocks, buttons)

	_, _, err := s.client.PostMessageContext(
		ctx,
		s.channel,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(fmt.Sprintf("branch protection bypassed on pr #%d", prNumber), false),
	)

	if err != nil {
		return fmt.Errorf("failed to post pr bypass notification to slack: %w", err)
	}

	return nil
}

// NotifyOktaSync sends a Slack notification with Okta sync results.
func (s *SlackNotifier) NotifyOktaSync(ctx context.Context, reports []*okta.SyncReport) error {
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
		} else {
			rulesWithoutChanges = append(rulesWithoutChanges, report)
		}

		for _, err := range report.Errors {
			allErrors = append(allErrors, fmt.Sprintf("%s: %s", report.GitHubTeam, err))
		}

		allSkippedExternal = append(allSkippedExternal, report.MembersSkippedExternal...)
		allSkippedNoGHUsername = append(allSkippedNoGHUsername, report.MembersSkippedNoGHUsername...)
	}

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject("plain_text", "Okta Group Sync Complete", false, false),
		),
	}

	// summary stats in fields
	summaryFields := []*slack.TextBlockObject{
		slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Rules Processed*\n%d", len(reports)), false, false),
		slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Members Added*\n%d", totalAdded), false, false),
		slack.NewTextBlockObject("mrkdwn", fmt.Sprintf("*Members Removed*\n%d", totalRemoved), false, false),
	}
	blocks = append(blocks, slack.NewSectionBlock(nil, summaryFields, nil))

	// table of rules with changes
	if len(rulesWithChanges) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())

		tableText := "*Rules with changes:*\n"
		tableText += "```\n"
		tableText += fmt.Sprintf("%-32s %8s %8s\n", "Team", "Added", "Removed")
		for _, report := range rulesWithChanges {
			teamName := report.GitHubTeam
			if len(teamName) > 32 {
				teamName = teamName[:29] + "..."
			}
			tableText += fmt.Sprintf("%-32s %8d %8d\n", teamName, len(report.MembersAdded), len(report.MembersRemoved))
		}
		tableText += "```"

		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", tableText, false, false),
			nil, nil,
		))
	}

	// list of rules without changes
	if len(rulesWithoutChanges) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())

		noChangesText := "*Rules with no changes:*\n"
		for _, report := range rulesWithoutChanges {
			noChangesText += fmt.Sprintf("- %s\n", report.GitHubTeam)
		}

		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", noChangesText, false, false),
			nil, nil,
		))
	}

	// errors section
	if len(allErrors) > 0 {
		blocks = append(blocks, slack.NewDividerBlock())

		errorsText := "*Errors:*\n"
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

		skippedText := "*Skipped members:*\n"

		if len(allSkippedExternal) > 0 {
			skippedText += "_External collaborators:_\n"
			for _, member := range allSkippedExternal {
				skippedText += fmt.Sprintf("- %s\n", member)
			}
		}

		if len(allSkippedNoGHUsername) > 0 {
			if len(allSkippedExternal) > 0 {
				skippedText += "\n"
			}
			skippedText += "_No GitHub username in Okta:_\n"
			for _, member := range allSkippedNoGHUsername {
				skippedText += fmt.Sprintf("- %s\n", member)
			}
		}

		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", skippedText, false, false),
			nil, nil,
		))
	}

	_, _, err := s.client.PostMessageContext(
		ctx,
		s.channel,
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

	_, _, err := s.client.PostMessageContext(
		ctx,
		s.channel,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText(fmt.Sprintf("orphaned github users detected: %d users", len(report.OrphanedUsers)), false),
	)

	if err != nil {
		return fmt.Errorf("failed to post orphaned users notification to slack: %w", err)
	}

	return nil
}
