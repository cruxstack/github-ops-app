package app

import (
	"context"
	"log/slog"

	"github.com/cockroachdb/errors"
	internalerrors "github.com/cruxstack/github-ops-app/internal/errors"
	"github.com/cruxstack/github-ops-app/internal/github/client"
	"github.com/cruxstack/github-ops-app/internal/github/webhooks"
	"github.com/cruxstack/github-ops-app/internal/okta"
)

// handleOktaSync executes Okta group synchronization to GitHub teams.
// sends Slack notification with sync results if configured.
func (a *App) handleOktaSync(ctx context.Context) error {
	if !a.Config.IsOktaSyncEnabled() {
		a.Logger.Info("okta sync is not enabled, skipping")
		return nil
	}

	if a.OktaClient == nil || a.GitHubClient == nil {
		return errors.Wrap(internalerrors.ErrClientNotInit, "okta or github client")
	}

	syncer := okta.NewSyncer(a.OktaClient, a.GitHubClient, a.Config.OktaSyncRules, a.Config.OktaSyncSafetyThreshold, a.Logger)
	syncResult, err := syncer.Sync(ctx)
	if err != nil {
		return errors.Wrap(err, "okta sync failed")
	}

	a.Logger.Info("okta sync completed", slog.Int("report_count", len(syncResult.Reports)))

	if a.Notifier != nil {
		if err := a.Notifier.NotifyOktaSync(ctx, syncResult.Reports, a.Config.GitHubOrg); err != nil {
			a.Logger.Warn("failed to send slack notification", slog.String("error", err.Error()))
		}
	}

	if a.Config.OktaOrphanedUserNotifications {
		syncedTeams := make([]string, 0, len(syncResult.Reports))
		for _, report := range syncResult.Reports {
			syncedTeams = append(syncedTeams, report.GitHubTeam)
		}

		orphanedReport, err := syncer.DetectOrphanedUsers(ctx, syncedTeams)
		if err != nil {
			a.Logger.Warn("failed to detect orphaned users", slog.String("error", err.Error()))
		} else if orphanedReport != nil && len(orphanedReport.OrphanedUsers) > 0 {
			a.Logger.Info("orphaned users detected", slog.Int("count", len(orphanedReport.OrphanedUsers)))

			if a.Notifier != nil {
				if err := a.Notifier.NotifyOrphanedUsers(ctx, orphanedReport); err != nil {
					a.Logger.Warn("failed to send orphaned users notification", slog.String("error", err.Error()))
				}
			}
		}
	}

	return nil
}

// handlePullRequestWebhook processes GitHub pull request webhook events.
// checks merged PRs for branch protection compliance violations.
func (a *App) handlePullRequestWebhook(ctx context.Context, payload []byte) error {
	prEvent, err := webhooks.ParsePullRequestEvent(payload)
	if err != nil {
		return err
	}

	if !prEvent.IsMerged() {
		if a.Config.DebugEnabled {
			a.Logger.Debug("pr not merged, skipping", slog.Int("pr_number", prEvent.Number))
		}
		return nil
	}

	baseBranch := prEvent.GetBaseBranch()
	if !a.Config.ShouldMonitorBranch(baseBranch) {
		if a.Config.DebugEnabled {
			a.Logger.Debug("branch not monitored, skipping", slog.String("branch", baseBranch))
		}
		return nil
	}

	ghClient := a.GitHubClient

	if prEvent.GetInstallationID() != 0 && prEvent.GetInstallationID() != a.Config.GitHubInstallationID {
		installClient, err := client.NewAppClientWithBaseURL(
			a.Config.GitHubAppID,
			prEvent.GetInstallationID(),
			a.Config.GitHubAppPrivateKey,
			a.Config.GitHubOrg,
			a.Config.GitHubBaseURL,
		)
		if err != nil {
			return errors.Wrapf(err, "failed to create client for installation %d", prEvent.GetInstallationID())
		}
		ghClient = installClient
	}

	if ghClient == nil {
		return errors.Wrap(internalerrors.ErrClientNotInit, "github client")
	}

	owner := prEvent.GetRepoOwner()
	repo := prEvent.GetRepoName()

	result, err := ghClient.CheckPRCompliance(ctx, owner, repo, prEvent.Number)
	if err != nil {
		return errors.Wrapf(err, "failed to check pr #%d compliance", prEvent.Number)
	}

	if result.WasBypassed() {
		a.Logger.Info("pr bypassed branch protection",
			slog.Int("pr_number", prEvent.Number),
			slog.String("branch", baseBranch))

		if a.Notifier != nil {
			repoFullName := prEvent.GetRepoFullName()
			if err := a.Notifier.NotifyPRBypass(ctx, result, repoFullName); err != nil {
				a.Logger.Warn("failed to send slack notification", slog.String("error", err.Error()))
			}
		}
	} else if a.Config.DebugEnabled {
		a.Logger.Debug("pr complied with branch protection", slog.Int("pr_number", prEvent.Number))
	}

	return nil
}

// handleTeamWebhook processes GitHub team webhook events.
// triggers Okta sync when team changes are made externally.
func (a *App) handleTeamWebhook(ctx context.Context, payload []byte) error {
	teamEvent, err := webhooks.ParseTeamEvent(payload)
	if err != nil {
		return err
	}

	if !a.Config.IsOktaSyncEnabled() {
		if a.Config.DebugEnabled {
			a.Logger.Debug("okta sync not enabled, skipping team webhook")
		}
		return nil
	}

	if a.shouldIgnoreWebhookChange(ctx, teamEvent) {
		if a.Config.DebugEnabled {
			a.Logger.Debug("ignoring team change from bot/app",
				slog.String("action", teamEvent.Action),
				slog.String("sender", teamEvent.GetSenderLogin()))
		}
		return nil
	}

	a.Logger.Info("external team change detected, triggering sync",
		slog.String("action", teamEvent.Action),
		slog.String("team", teamEvent.GetTeamSlug()),
		slog.String("sender", teamEvent.GetSenderLogin()))

	return a.handleOktaSync(ctx)
}

// handleMembershipWebhook processes GitHub membership webhook events.
// triggers Okta sync when team memberships are changed externally.
func (a *App) handleMembershipWebhook(ctx context.Context, payload []byte) error {
	membershipEvent, err := webhooks.ParseMembershipEvent(payload)
	if err != nil {
		return err
	}

	if !membershipEvent.IsTeamScope() {
		if a.Config.DebugEnabled {
			a.Logger.Debug("membership event is not team scope, skipping")
		}
		return nil
	}

	if !a.Config.IsOktaSyncEnabled() {
		if a.Config.DebugEnabled {
			a.Logger.Debug("okta sync not enabled, skipping membership webhook")
		}
		return nil
	}

	if a.shouldIgnoreWebhookChange(ctx, membershipEvent) {
		if a.Config.DebugEnabled {
			a.Logger.Debug("ignoring membership change from bot/app",
				slog.String("action", membershipEvent.Action),
				slog.String("team", membershipEvent.GetTeamSlug()),
				slog.String("sender", membershipEvent.GetSenderLogin()))
		}
		return nil
	}

	a.Logger.Info("external membership change detected, triggering sync",
		slog.String("action", membershipEvent.Action),
		slog.String("team", membershipEvent.GetTeamSlug()),
		slog.String("sender", membershipEvent.GetSenderLogin()))

	return a.handleOktaSync(ctx)
}

// webhookSender provides sender information for webhook events.
type webhookSender interface {
	GetSenderType() string
	GetSenderLogin() string
}

// shouldIgnoreWebhookChange checks if a webhook should be ignored.
// ignores changes made by bots or the GitHub App itself to prevent loops.
func (a *App) shouldIgnoreWebhookChange(ctx context.Context, event webhookSender) bool {
	if event.GetSenderType() == "Bot" {
		return true
	}

	if a.GitHubClient != nil {
		appSlug, err := a.GitHubClient.GetAppSlug(ctx)
		if err != nil {
			a.Logger.Warn("failed to get app slug", slog.String("error", err.Error()))
			return false
		}
		if event.GetSenderLogin() == appSlug+"[bot]" {
			return true
		}
	}

	return false
}

// handleSlackTest sends test notifications to Slack with sample data.
// useful for verifying Slack connectivity and previewing message formats.
func (a *App) handleSlackTest(ctx context.Context) error {
	if a.Notifier == nil {
		return errors.New("slack is not configured")
	}

	// test 1: PR bypass notification
	if err := a.Notifier.NotifyPRBypass(ctx, fakePRComplianceResult(), "acme-corp/demo-repo"); err != nil {
		return errors.Wrap(err, "failed to send test pr bypass notification")
	}
	a.Logger.Info("sent test pr bypass notification")

	// test 2: Okta sync notification
	if err := a.Notifier.NotifyOktaSync(ctx, fakeOktaSyncReports(), "acme-corp"); err != nil {
		return errors.Wrap(err, "failed to send test okta sync notification")
	}
	a.Logger.Info("sent test okta sync notification")

	// test 3: Orphaned users notification
	if err := a.Notifier.NotifyOrphanedUsers(ctx, fakeOrphanedUsersReport()); err != nil {
		return errors.Wrap(err, "failed to send test orphaned users notification")
	}
	a.Logger.Info("sent test orphaned users notification")

	return nil
}
