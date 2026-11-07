package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/cruxstack/github-ops-app/internal/github/webhooks"
	"github.com/cruxstack/github-ops-app/internal/teamsync"
)

// RunScheduled runs a scheduled action. Callers authenticate the request
// first: the HTTP endpoint requires the admin token, and EventBridge
// invocations are authorized by AWS.
func (a *App) RunScheduled(ctx context.Context, action string) error {
	a.logger.InfoContext(ctx, "running scheduled action", "action", action)
	switch action {
	case ActionOktaSync:
		return a.runOktaSync(ctx)
	case ActionSecurityAlerts:
		return a.runSecurityAlerts(ctx)
	case ActionSlackTest:
		return a.runSlackTest(ctx)
	default:
		return fmt.Errorf("%w '%s'", domain.ErrUnknownAction, action)
	}
}

// WebhookResult says what was done with a webhook.
type WebhookResult string

// Webhook results.
const (
	WebhookProcessed WebhookResult = "processed"
	WebhookQueued    WebhookResult = "queued"
	WebhookIgnored   WebhookResult = "ignored"
	WebhookPong      WebhookResult = "pong"
)

// processWebhook handles a verified GitHub webhook event. Events the
// application does not use, and events from other organizations, are
// ignored.
func (a *App) processWebhook(ctx context.Context, eventType string, payload []byte) (WebhookResult, error) {
	switch eventType {
	case webhooks.EventPing:
		return WebhookPong, nil
	case webhooks.EventPullRequest:
		return a.handlePullRequestWebhook(ctx, payload)
	case webhooks.EventTeam:
		return a.handleTeamWebhook(ctx, payload)
	case webhooks.EventMembership:
		return a.handleMembershipWebhook(ctx, payload)
	default:
		a.logger.DebugContext(ctx, "ignoring unhandled webhook event", "event_type", eventType)
		return WebhookIgnored, nil
	}
}

// isOwnOrg returns true if login is the configured organization. The app
// acts only on its own organization, even if it is installed elsewhere.
func (a *App) isOwnOrg(ctx context.Context, login, eventType string) bool {
	if strings.EqualFold(login, a.cfg.GitHub.Org) {
		return true
	}
	a.logger.WarnContext(ctx, "ignoring webhook from another organization",
		"event_type", eventType,
		"org", login)
	return false
}

// handlePullRequestWebhook checks merged PRs into monitored branches for
// compliance and reports the ones merged without meeting the requirements.
func (a *App) handlePullRequestWebhook(ctx context.Context, payload []byte) (WebhookResult, error) {
	event, err := webhooks.ParsePullRequestEvent(payload)
	if err != nil {
		return "", err
	}
	if !a.isOwnOrg(ctx, event.GetRepoOwner(), webhooks.EventPullRequest) {
		return WebhookIgnored, nil
	}

	log := a.logger.With(
		"repo", event.GetRepoFullName(),
		"pr_number", event.GetNumber(),
		"branch", event.GetBaseBranch())

	if !event.IsMerged() {
		log.DebugContext(ctx, "pr not merged, skipping", "action", event.Action)
		return WebhookIgnored, nil
	}
	if !a.cfg.ShouldMonitorBranch(event.GetBaseBranch()) {
		log.DebugContext(ctx, "branch not monitored, skipping")
		return WebhookIgnored, nil
	}

	result, err := a.github.CheckPRCompliance(ctx, event.GetRepoOwner(), event.GetRepoName(), event.GetNumber())
	if err != nil {
		return "", fmt.Errorf("failed to check compliance of pr #%d in %s: %w",
			event.GetNumber(), event.GetRepoFullName(), errors.Join(domain.ErrAPI, err))
	}

	if !result.WasBypassed() {
		log.DebugContext(ctx, "pr met branch requirements")
		return WebhookProcessed, nil
	}

	violations := make([]string, 0, len(result.Violations))
	for _, v := range result.Violations {
		violations = append(violations, v.Description)
	}
	log.WarnContext(ctx, "pr merged without meeting branch requirements",
		"merged_by", result.PR.GetMergedBy().GetLogin(),
		"bypass_reason", result.UserBypassReason,
		"violations", violations)

	if a.notifier != nil {
		if err := a.notifier.NotifyPRBypass(ctx, result, event.GetRepoFullName()); err != nil {
			log.ErrorContext(ctx, "failed to send pr bypass notification", "error", err.Error())
		}
	}
	return WebhookProcessed, nil
}

// handleTeamWebhook syncs Okta groups when someone other than this app
// creates, deletes, or renames a team.
func (a *App) handleTeamWebhook(ctx context.Context, payload []byte) (WebhookResult, error) {
	event, err := webhooks.ParseTeamEvent(payload)
	if err != nil {
		return "", err
	}
	if !a.isOwnOrg(ctx, event.GetOrgLogin(), webhooks.EventTeam) || a.okta == nil || !event.AffectsMembership() {
		return WebhookIgnored, nil
	}
	return a.syncAfterExternalChange(ctx, webhooks.EventTeam, event.Action, event, event.GetTeamSlug(), event.GetPreviousTeamSlug())
}

// handleMembershipWebhook syncs Okta groups when someone other than this
// app changes a team's members.
func (a *App) handleMembershipWebhook(ctx context.Context, payload []byte) (WebhookResult, error) {
	event, err := webhooks.ParseMembershipEvent(payload)
	if err != nil {
		return "", err
	}
	if !a.isOwnOrg(ctx, event.GetOrgLogin(), webhooks.EventMembership) || a.okta == nil || !event.IsTeamScope() {
		return WebhookIgnored, nil
	}
	return a.syncAfterExternalChange(ctx, webhooks.EventMembership, event.Action, event, event.GetTeamSlug())
}

// sender identifies who triggered a webhook.
type sender interface {
	GetSenderType() string
	GetSenderLogin() string
}

// syncAfterExternalChange syncs Okta groups after a change to one of teams.
// Changes by bots, including this app, and to teams no sync rule manages are
// ignored. With a Background, the sync runs after the response, since GitHub
// waits only 10 seconds for one.
func (a *App) syncAfterExternalChange(ctx context.Context, eventType, action string, s sender, teams ...string) (WebhookResult, error) {
	log := a.logger.With(
		"event_type", eventType,
		"action", action,
		"team", teams[0],
		"sender", s.GetSenderLogin())

	if a.isOwnChange(ctx, s) {
		log.DebugContext(ctx, "ignoring change made by a bot or this app")
		return WebhookIgnored, nil
	}
	if !a.isManagedTeam(teams...) {
		log.DebugContext(ctx, "ignoring change to a team no sync rule manages")
		return WebhookIgnored, nil
	}

	log.InfoContext(ctx, "external team change detected, syncing okta groups")
	if a.background != nil {
		a.background.Go(func(ctx context.Context) {
			if err := a.runOktaSync(ctx); err != nil {
				a.logger.ErrorContext(ctx, "okta sync failed", "trigger", eventType, "error", err.Error())
			}
		})
		return WebhookQueued, nil
	}
	if err := a.runOktaSync(ctx); err != nil {
		return "", err
	}
	return WebhookProcessed, nil
}

// isOwnChange returns true if a change was made by a bot, such as this app,
// so the sync's own changes don't trigger another sync.
func (a *App) isOwnChange(ctx context.Context, s sender) bool {
	if s.GetSenderType() == "Bot" {
		return true
	}
	slug, err := a.github.GetAppSlug(ctx)
	if err != nil {
		a.logger.WarnContext(ctx, "failed to get app slug", "error", err.Error())
		return false
	}
	return strings.EqualFold(s.GetSenderLogin(), slug+"[bot]")
}

// isManagedTeam returns true if any of the slugs is a team the last sync
// managed. Before a complete sync, which teams are managed is unknown, so
// every team counts.
func (a *App) isManagedTeam(slugs ...string) bool {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	if a.managedTeams == nil {
		return true
	}
	for _, slug := range slugs {
		if slug != "" && a.managedTeams[domain.TeamSlug(slug)] {
			return true
		}
	}
	return false
}

// setManagedTeams records the teams a sync managed, or forgets them when the
// sync could not find every team.
func (a *App) setManagedTeams(result *domain.SyncResult) {
	var managed map[string]bool
	if result.Complete() {
		managed = make(map[string]bool)
		for _, slug := range result.ManagedTeams() {
			managed[slug] = true
		}
	}
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	a.managedTeams = managed
}

// runOktaSync syncs Okta groups to GitHub teams. A sync requested while
// another is running doesn't start a second one: the running sync runs once
// more when it finishes, whether or not it failed, so it picks up the latest
// changes. The follow-up gets its own JobTimeout.
func (a *App) runOktaSync(ctx context.Context) error {
	if a.okta == nil {
		return fmt.Errorf("okta sync: %w", domain.ErrFeatureDisabled)
	}

	a.syncMu.Lock()
	if a.syncRunning {
		a.syncPending = true
		a.syncMu.Unlock()
		a.logger.InfoContext(ctx, "okta sync already running, queued a follow-up sync")
		return nil
	}
	a.syncRunning = true
	a.syncMu.Unlock()

	err := a.oktaSyncOnce(ctx)
	for a.takePendingSync() {
		a.logger.InfoContext(ctx, "running queued okta sync")
		followUp, cancel := context.WithTimeout(context.WithoutCancel(ctx), JobTimeout)
		err = errors.Join(err, a.oktaSyncOnce(followUp))
		cancel()
	}
	return err
}

// takePendingSync returns true and clears the pending flag if a sync was
// queued; otherwise it marks the sync as finished.
func (a *App) takePendingSync() bool {
	a.syncMu.Lock()
	defer a.syncMu.Unlock()
	if a.syncPending {
		a.syncPending = false
		return true
	}
	a.syncRunning = false
	return false
}

func (a *App) oktaSyncOnce(ctx context.Context) error {
	syncer := teamsync.NewSyncer(a.okta, a.github, a.cfg.Okta.SyncRules, a.cfg.Okta.SyncSafetyThreshold, a.logger)
	result, syncErr := syncer.Sync(ctx)
	if result == nil {
		return fmt.Errorf("okta sync failed: %w", syncErr)
	}
	a.setManagedTeams(result)

	var added, removed, failed int
	for _, r := range result.Reports {
		added += len(r.MembersAdded)
		removed += len(r.MembersRemoved)
		attrs := []any{
			"rule", r.Rule,
			"okta_group", r.OktaGroup,
			"team", r.GitHubTeam,
			"added", r.MembersAdded,
			"removed", r.MembersRemoved,
			"skipped_external", r.MembersSkippedExternal,
			"skipped_no_github_username", len(r.MembersSkippedNoGHUsername),
		}
		if r.HasErrors() {
			failed++
			a.logger.WarnContext(ctx, "okta group synced with errors", append(attrs, "errors", r.Errors)...)
		} else {
			a.logger.InfoContext(ctx, "okta group synced", attrs...)
		}
	}
	a.logger.InfoContext(ctx, "okta sync completed",
		"teams", len(result.Reports),
		"teams_with_errors", failed,
		"members_added", added,
		"members_removed", removed)

	if a.notifier != nil {
		if err := a.notifier.NotifyOktaSync(ctx, result.Reports, a.cfg.GitHub.Org); err != nil {
			a.logger.ErrorContext(ctx, "failed to send okta sync notification", "error", err.Error())
		}
	}
	if syncErr != nil {
		return fmt.Errorf("okta sync failed: %w", syncErr)
	}

	if a.cfg.Okta.OrphanedUserNotifications {
		a.reportOrphanedUsers(ctx, syncer, result)
	}
	return nil
}

// reportOrphanedUsers reports organization members in none of the synced
// teams. It is skipped when a team could not be found, since its members
// would be reported as orphaned. Failures are logged, since the sync itself
// succeeded.
func (a *App) reportOrphanedUsers(ctx context.Context, syncer *teamsync.Syncer, result *domain.SyncResult) {
	if len(result.Reports) == 0 {
		return
	}
	if !result.Complete() {
		a.logger.WarnContext(ctx, "skipping orphaned user detection because not every synced team was found")
		return
	}
	report, err := syncer.DetectOrphanedUsers(ctx, result)
	if err != nil {
		a.logger.ErrorContext(ctx, "failed to detect orphaned users", "error", err.Error())
		return
	}
	if len(report.OrphanedUsers) == 0 {
		return
	}

	a.logger.InfoContext(ctx, "orphaned users detected",
		"count", len(report.OrphanedUsers),
		"users", report.OrphanedUsers)
	if a.notifier != nil {
		if err := a.notifier.NotifyOrphanedUsers(ctx, report); err != nil {
			a.logger.ErrorContext(ctx, "failed to send orphaned users notification", "error", err.Error())
		}
	}
}

// runSecurityAlerts reports open security alerts older than the configured
// age across the organization.
func (a *App) runSecurityAlerts(ctx context.Context) error {
	if !a.cfg.SecurityAlerts.Enabled {
		return fmt.Errorf("security alerts: %w", domain.ErrFeatureDisabled)
	}

	report, err := a.github.ListSecurityAlerts(ctx, a.cfg.SecurityAlerts.MinAgeDays, a.cfg.SecurityAlerts.MinSeverity)
	if err != nil {
		return fmt.Errorf("failed to list security alerts for org '%s': %w", a.cfg.GitHub.Org, err)
	}

	for _, e := range report.Errors {
		a.logger.ErrorContext(ctx, "failed to fetch security alerts", "error", e)
	}
	a.logger.InfoContext(ctx, "security alerts check completed",
		"alerts", report.TotalAlerts,
		"repos", report.RepoCount(),
		"errors", len(report.Errors))

	if a.notifier != nil {
		if err := a.notifier.NotifySecurityAlerts(ctx, report, a.cfg.GitHub.Org); err != nil {
			a.logger.ErrorContext(ctx, "failed to send security alerts notification", "error", err.Error())
		}
	}
	return nil
}

// runSlackTest sends every notification type with sample data, to check
// the Slack setup and preview the messages.
func (a *App) runSlackTest(ctx context.Context) error {
	if a.notifier == nil {
		return fmt.Errorf("slack: %w", domain.ErrFeatureDisabled)
	}

	steps := []struct {
		name string
		send func() error
	}{
		{"pr bypass", func() error {
			return a.notifier.NotifyPRBypass(ctx, samplePRComplianceResult(), "acme-corp/demo-repo")
		}},
		{"okta sync", func() error { return a.notifier.NotifyOktaSync(ctx, sampleOktaSyncReports(), "acme-corp") }},
		{"orphaned users", func() error { return a.notifier.NotifyOrphanedUsers(ctx, sampleOrphanedUsersReport()) }},
		{"security alerts", func() error {
			return a.notifier.NotifySecurityAlerts(ctx, sampleSecurityAlertsReport(), "acme-corp")
		}},
	}
	for _, step := range steps {
		if err := step.send(); err != nil {
			return fmt.Errorf("failed to send test %s notification: %w", step.name, err)
		}
		a.logger.InfoContext(ctx, "sent test notification", "type", step.name)
	}
	return nil
}
