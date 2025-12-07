// Package app provides the core application logic for the GitHub bot.
// coordinates webhook processing, Okta sync, and PR compliance checks.
package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/config"
	internalerrors "github.com/cruxstack/github-ops-app/internal/errors"
	"github.com/cruxstack/github-ops-app/internal/github"
	"github.com/cruxstack/github-ops-app/internal/notifiers"
	"github.com/cruxstack/github-ops-app/internal/okta"
	gh "github.com/google/go-github/v79/github"
)

// App is the main application instance containing all clients and
// configuration.
type App struct {
	Config       *config.Config
	Logger       *slog.Logger
	GitHubClient *github.Client
	OktaClient   *okta.Client
	Notifier     *notifiers.SlackNotifier
}

// New creates a new App instance with configured clients.
// initializes GitHub, Okta, and Slack clients based on config.
func New(ctx context.Context, cfg *config.Config) (*App, error) {
	logger := config.NewLogger()

	app := &App{
		Config: cfg,
		Logger: logger,
	}

	if cfg.IsGitHubConfigured() {
		ghClient, err := github.NewAppClientWithBaseURL(
			cfg.GitHubAppID,
			cfg.GitHubInstallID,
			cfg.GitHubAppPrivateKey,
			cfg.GitHubOrg,
			cfg.GitHubBaseURL,
		)
		if err != nil {
			return nil, errors.Wrap(err, "failed to create github app client")
		}
		app.GitHubClient = ghClient
	}

	if cfg.IsOktaSyncEnabled() {
		oktaClient, err := okta.NewClientWithContext(ctx, &okta.ClientConfig{
			Domain:          cfg.OktaDomain,
			ClientID:        cfg.OktaClientID,
			PrivateKey:      cfg.OktaPrivateKey,
			PrivateKeyID:    cfg.OktaPrivateKeyID,
			Scopes:          cfg.OktaScopes,
			GitHubUserField: cfg.OktaGitHubUserField,
			BaseURL:         cfg.OktaBaseURL,
		})
		if err != nil {
			return nil, errors.Wrap(err, "failed to create okta client")
		}
		app.OktaClient = oktaClient
	}

	if cfg.SlackEnabled {
		app.Notifier = notifiers.NewSlackNotifierWithAPIURL(cfg.SlackToken, cfg.SlackChannel, cfg.SlackAPIURL)
	}

	return app, nil
}

// ScheduledEvent represents a generic scheduled event.
type ScheduledEvent struct {
	Action string          `json:"action"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// ProcessScheduledEvent handles scheduled events (e.g., cron jobs).
// routes to appropriate handlers based on event action.
func (a *App) ProcessScheduledEvent(ctx context.Context, evt ScheduledEvent) error {
	if a.Config.DebugEnabled {
		j, _ := json.Marshal(evt)
		a.Logger.Debug("received scheduled event", slog.String("event", string(j)))
	}

	switch evt.Action {
	case "okta-sync":
		return a.handleOktaSync(ctx)
	case "slack-test":
		return a.handleSlackTest(ctx)
	default:
		return errors.Newf("unknown scheduled action: %s", evt.Action)
	}
}

// ProcessWebhook handles incoming GitHub webhook events.
// supports pull_request, team, and membership events.
func (a *App) ProcessWebhook(ctx context.Context, payload []byte, eventType string) error {
	if a.Config.DebugEnabled {
		a.Logger.Debug("received webhook", slog.String("event_type", eventType))
	}

	switch eventType {
	case "pull_request":
		return a.handlePullRequestWebhook(ctx, payload)
	case "team":
		return a.handleTeamWebhook(ctx, payload)
	case "membership":
		return a.handleMembershipWebhook(ctx, payload)
	default:
		return errors.Wrapf(internalerrors.ErrInvalidEventType, "%s", eventType)
	}
}

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
	prEvent, err := github.ParsePullRequestEvent(payload)
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

	if prEvent.GetInstallationID() != 0 && prEvent.GetInstallationID() != a.Config.GitHubInstallID {
		installClient, err := github.NewAppClientWithBaseURL(
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
	teamEvent, err := github.ParseTeamEvent(payload)
	if err != nil {
		return err
	}

	if !a.Config.IsOktaSyncEnabled() {
		if a.Config.DebugEnabled {
			a.Logger.Debug("okta sync not enabled, skipping team webhook")
		}
		return nil
	}

	if a.shouldIgnoreTeamChange(ctx, teamEvent) {
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
	membershipEvent, err := github.ParseMembershipEvent(payload)
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

	if a.shouldIgnoreMembershipChange(ctx, membershipEvent) {
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

// shouldIgnoreTeamChange checks if a team webhook should be ignored.
// ignores changes made by bots or the GitHub App itself to prevent loops.
func (a *App) shouldIgnoreTeamChange(ctx context.Context, event *github.TeamEvent) bool {
	senderType := event.GetSenderType()
	if senderType == "Bot" {
		return true
	}

	if a.GitHubClient != nil {
		appSlug, err := a.GitHubClient.GetAppSlug(ctx)
		if err != nil {
			a.Logger.Warn("failed to get app slug", slog.String("error", err.Error()))
			return false
		}
		senderLogin := event.GetSenderLogin()
		if senderLogin == appSlug+"[bot]" {
			return true
		}
	}

	return false
}

// shouldIgnoreMembershipChange checks if a membership webhook should be
// ignored. ignores changes made by bots or the GitHub App itself to prevent
// loops.
func (a *App) shouldIgnoreMembershipChange(ctx context.Context, event *github.MembershipEvent) bool {
	senderType := event.GetSenderType()
	if senderType == "Bot" {
		return true
	}

	if a.GitHubClient != nil {
		appSlug, err := a.GitHubClient.GetAppSlug(ctx)
		if err != nil {
			a.Logger.Warn("failed to get app slug", slog.String("error", err.Error()))
			return false
		}
		senderLogin := event.GetSenderLogin()
		if senderLogin == appSlug+"[bot]" {
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

// fakePRComplianceResult returns sample PR compliance data for testing.
func fakePRComplianceResult() *github.PRComplianceResult {
	prNumber := 42
	prTitle := "Add new authentication feature"
	prURL := "https://github.com/acme-corp/demo-repo/pull/42"
	mergedByLogin := "test-user"

	return &github.PRComplianceResult{
		PR: &gh.PullRequest{
			Number:  &prNumber,
			Title:   &prTitle,
			HTMLURL: &prURL,
			MergedBy: &gh.User{
				Login: &mergedByLogin,
			},
		},
		BaseBranch:       "main",
		UserHasBypass:    true,
		UserBypassReason: "repository admin",
		Violations: []github.ComplianceViolation{
			{Type: "insufficient_reviews", Description: "required 2 approving reviews, had 0"},
			{Type: "missing_status_check", Description: "required check 'ci/build' did not pass"},
		},
	}
}

// fakeOktaSyncReports returns sample Okta sync reports for testing.
func fakeOktaSyncReports() []*okta.SyncReport {
	return []*okta.SyncReport{
		{
			Rule:           "engineering-team",
			OktaGroup:      "Engineering",
			GitHubTeam:     "engineering",
			MembersAdded:   []string{"alice", "bob"},
			MembersRemoved: []string{"charlie"},
		},
		{
			Rule:       "platform-team",
			OktaGroup:  "Platform",
			GitHubTeam: "platform",
			// no changes
		},
		{
			Rule:                       "security-team",
			OktaGroup:                  "Security",
			GitHubTeam:                 "security",
			MembersAdded:               []string{"dave"},
			MembersSkippedExternal:     []string{"external-contractor"},
			MembersSkippedNoGHUsername: []string{"new-hire@example.com"},
			Errors:                     []string{"failed to fetch group members: rate limited"},
		},
	}
}

// fakeOrphanedUsersReport returns sample orphaned users data for testing.
func fakeOrphanedUsersReport() *okta.OrphanedUsersReport {
	return &okta.OrphanedUsersReport{
		OrphanedUsers: []string{"orphan-user-1", "orphan-user-2", "legacy-bot"},
	}
}

// StatusResponse contains application status and feature flags.
type StatusResponse struct {
	Status            string `json:"status"`
	GitHubConfigured  bool   `json:"github_configured"`
	OktaSyncEnabled   bool   `json:"okta_sync_enabled"`
	PRComplianceCheck bool   `json:"pr_compliance_check"`
	SlackEnabled      bool   `json:"slack_enabled"`
}

// GetStatus returns current application status and enabled features.
func (a *App) GetStatus() StatusResponse {
	return StatusResponse{
		Status:            "ok",
		GitHubConfigured:  a.Config.IsGitHubConfigured(),
		OktaSyncEnabled:   a.Config.IsOktaSyncEnabled(),
		PRComplianceCheck: a.Config.IsPRComplianceEnabled(),
		SlackEnabled:      a.Config.SlackEnabled,
	}
}
