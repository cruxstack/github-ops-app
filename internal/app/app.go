// Package app provides the core application logic for the GitHub bot.
// Coordinates webhook processing, Okta sync, and PR compliance checks.
package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/domain"
)

// App is the main application instance containing all clients and
// configuration. depends on domain interfaces, not concrete implementations.
type App struct {
	Config       *config.Config
	Logger       *slog.Logger
	GitHubClient domain.GitHubClient
	OktaClient   domain.OktaClient
	Notifier     domain.Notifier
}

// ScheduledEvent represents a generic scheduled event.
type ScheduledEvent struct {
	Action string          `json:"action"`
	Data   json.RawMessage `json:"data,omitempty"`
}

// ProcessScheduledEvent handles scheduled events (e.g., cron jobs).
// Routes to appropriate handlers based on event action.
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
// Supports pull_request, team, and membership events.
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
		return errors.Wrapf(domain.ErrInvalidEventType, "%s", eventType)
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
