// Package app is the application: it builds the GitHub, Okta, and Slack
// clients from the configuration, serves the webhook, admin, and scheduled
// endpoints, and runs the Okta sync, PR compliance, and security alert
// features.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/domain"
	ghclient "github.com/cruxstack/github-ops-app/internal/github/client"
	"github.com/cruxstack/github-ops-app/internal/notifiers"
	"github.com/cruxstack/github-ops-app/internal/okta"
)

// Scheduled actions.
const (
	ActionOktaSync       = "okta-sync"
	ActionSecurityAlerts = "security-alerts"
	ActionSlackTest      = "slack-test"
)

// JobTimeout bounds a sync or report, and a queued follow-up sync.
const JobTimeout = 10 * time.Minute

// Background runs work after the response to a request is sent, with a
// context of its own.
type Background interface {
	Go(f func(ctx context.Context))
}

// Option replaces a dependency that New would otherwise build.
type Option func(*deps)

type deps struct {
	httpClient *http.Client
	github     domain.GitHubClient
	okta       domain.OktaClient
	notifier   domain.Notifier
	background Background
}

// WithBackground runs webhook-triggered syncs in the background, so the
// webhook is answered at once. Without it they run before responding, as
// Lambda requires.
func WithBackground(b Background) Option {
	return func(d *deps) { d.background = b }
}

// WithHTTPClient sets the HTTP client every API client sends requests with.
func WithHTTPClient(c *http.Client) Option {
	return func(d *deps) { d.httpClient = c }
}

// WithGitHubClient replaces the GitHub client.
func WithGitHubClient(c domain.GitHubClient) Option {
	return func(d *deps) { d.github = c }
}

// WithOktaClient replaces the Okta client.
func WithOktaClient(c domain.OktaClient) Option {
	return func(d *deps) { d.okta = c }
}

// WithNotifier replaces the Slack notifier.
func WithNotifier(n domain.Notifier) Option {
	return func(d *deps) { d.notifier = n }
}

// App is the configured application. It is safe for concurrent use.
type App struct {
	cfg      *config.Config
	logger   *slog.Logger
	github   domain.GitHubClient
	okta     domain.OktaClient // nil when okta sync is not configured
	notifier domain.Notifier   // nil when slack is not configured
	// background runs webhook-triggered syncs; nil runs them inline
	background Background
	handler    http.Handler

	// syncMu guards syncRunning and syncPending, which coalesce okta syncs
	// requested while one is running into a single follow-up sync, and
	// managedTeams, the slugs the last complete sync managed (nil before
	// one).
	syncMu       sync.Mutex
	syncRunning  bool
	syncPending  bool
	managedTeams map[string]bool
}

// New builds the application. It makes no network calls, so it fails only
// on invalid configuration.
func New(cfg *config.Config, logger *slog.Logger, opts ...Option) (*App, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	var d deps
	for _, opt := range opts {
		opt(&d)
	}

	a := &App{cfg: cfg, logger: logger, github: d.github, okta: d.okta, notifier: d.notifier, background: d.background}

	if a.github == nil {
		gh, err := ghclient.New(ghclient.Options{
			AppID:          cfg.GitHub.AppID,
			InstallationID: cfg.GitHub.InstallationID,
			PrivateKey:     cfg.GitHub.PrivateKey,
			Org:            cfg.GitHub.Org,
			BaseURL:        cfg.GitHub.BaseURL,
			HTTPClient:     d.httpClient,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create github client: %w", err)
		}
		a.github = gh
	}

	if a.okta == nil && cfg.Okta.Enabled {
		oc, err := okta.New(okta.Options{
			Domain:          cfg.Okta.Domain,
			ClientID:        cfg.Okta.ClientID,
			PrivateKey:      cfg.Okta.PrivateKey,
			PrivateKeyID:    cfg.Okta.PrivateKeyID,
			Scopes:          cfg.Okta.Scopes,
			GitHubUserField: cfg.Okta.GitHubUserField,
			BaseURL:         cfg.Okta.BaseURL,
			HTTPClient:      d.httpClient,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create okta client: %w", err)
		}
		a.okta = oc
	}

	if a.notifier == nil && cfg.Slack.Enabled {
		a.notifier = notifiers.New(notifiers.Options{
			Token: cfg.Slack.Token,
			Channels: notifiers.SlackChannels{
				Default:        cfg.Slack.Channel,
				PRBypass:       cfg.Slack.ChannelPRBypass,
				OktaSync:       cfg.Slack.ChannelOktaSync,
				OrphanedUsers:  cfg.Slack.ChannelOrphanedUsers,
				SecurityAlerts: cfg.Slack.ChannelSecurityAlerts,
			},
			Messages:     notifiers.SlackMessages{PRBypassFooterNote: cfg.Slack.PRBypassFooterNote},
			APIURL:       cfg.Slack.APIURL,
			GitHubWebURL: cfg.GitHub.WebURL,
			HTTPClient:   d.httpClient,
		})
	}

	a.handler = a.newRouter()
	return a, nil
}

// Config returns the application's configuration.
func (a *App) Config() *config.Config {
	return a.cfg
}

// Handler returns the application's HTTP handler.
func (a *App) Handler() http.Handler {
	return a.handler
}

// Status describes the application and its enabled features.
type Status struct {
	Status                string `json:"status"`
	GitHubOrg             string `json:"github_org"`
	OktaSyncEnabled       bool   `json:"okta_sync_enabled"`
	PRComplianceEnabled   bool   `json:"pr_compliance_enabled"`
	SecurityAlertsEnabled bool   `json:"security_alerts_enabled"`
	SlackEnabled          bool   `json:"slack_enabled"`
}

// Status returns the application's status.
func (a *App) Status() Status {
	return Status{
		Status:                "ok",
		GitHubOrg:             a.cfg.GitHub.Org,
		OktaSyncEnabled:       a.okta != nil,
		PRComplianceEnabled:   a.cfg.PRCompliance.Enabled,
		SecurityAlertsEnabled: a.cfg.SecurityAlerts.Enabled,
		SlackEnabled:          a.notifier != nil,
	}
}
