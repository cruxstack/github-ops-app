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

	blocks := []slack.Block{
		slack.NewHeaderBlock(
			slack.NewTextBlockObject("plain_text", "✅ Okta Group Sync Complete", false, false),
		),
	}

	for _, report := range reports {
		sectionText := fmt.Sprintf("*Rule:* %s\n*Okta Group:* %s\n*GitHub Team:* %s", report.Rule, report.OktaGroup, report.GitHubTeam)

		if len(report.MembersAdded) > 0 {
			sectionText += fmt.Sprintf("\n*Added:* %d members", len(report.MembersAdded))
		}
		if len(report.MembersRemoved) > 0 {
			sectionText += fmt.Sprintf("\n*Removed:* %d members", len(report.MembersRemoved))
		}
		if len(report.MembersSkippedExternal) > 0 {
			sectionText += fmt.Sprintf("\n*Skipped (External):* %d members", len(report.MembersSkippedExternal))
		}
		if len(report.Errors) > 0 {
			sectionText += fmt.Sprintf("\n*Errors:* %d", len(report.Errors))
		}

		blocks = append(blocks, slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn", sectionText, false, false),
			nil, nil,
		))
		blocks = append(blocks, slack.NewDividerBlock())
	}

	_, _, err := s.client.PostMessageContext(
		ctx,
		s.channel,
		slack.MsgOptionBlocks(blocks...),
		slack.MsgOptionText("okta group sync completed", false),
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
