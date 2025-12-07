// Package app provides the core application logic for the GitHub bot.
// Coordinates webhook processing, Okta sync, and PR compliance checks.
package app

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/config"
	internalerrors "github.com/cruxstack/github-ops-app/internal/errors"
	"github.com/cruxstack/github-ops-app/internal/github/client"
	"github.com/cruxstack/github-ops-app/internal/notifiers"
	"github.com/cruxstack/github-ops-app/internal/okta"
)

// App is the main application instance containing all clients and
// configuration.
type App struct {
	Config       *config.Config
	Logger       *slog.Logger
	GitHubClient *client.Client
	OktaClient   *okta.Client
	Notifier     *notifiers.SlackNotifier
}

// New creates a new App instance with configured clients.
// Initializes GitHub, Okta, and Slack clients based on config.
func New(ctx context.Context, cfg *config.Config) (*App, error) {
	logger := config.NewLogger()

	app := &App{
		Config: cfg,
		Logger: logger,
	}

	if cfg.IsGitHubConfigured() {
		ghClient, err := client.NewAppClientWithBaseURL(
			cfg.GitHubAppID,
			cfg.GitHubInstallationID,
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
		channels := notifiers.SlackChannels{
			Default:       cfg.SlackChannel,
			PRBypass:      cfg.SlackChannelPRBypass,
			OktaSync:      cfg.SlackChannelOktaSync,
			OrphanedUsers: cfg.SlackChannelOrphanedUsers,
		}
		app.Notifier = notifiers.NewSlackNotifierWithAPIURL(cfg.SlackToken, channels, cfg.SlackAPIURL)
	}

	return app, nil
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
		return errors.Wrapf(internalerrors.ErrInvalidEventType, "%s", eventType)
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
