package app

import (
	"context"
	"log/slog"

	"github.com/cruxstack/github-ops-app/internal/config"
	ghclient "github.com/cruxstack/github-ops-app/internal/github/client"
	"github.com/cruxstack/github-ops-app/internal/notifiers"
	"github.com/cruxstack/github-ops-app/internal/okta"
)

// NewApp creates a new App instance with configured clients (composition
// root). concrete implementations are instantiated here and wired as domain
// interfaces. this is the single shared factory used by all entry points.
func NewApp(ctx context.Context, cfg *config.Config, logger *slog.Logger) (*App, error) {
	a := &App{
		Config: cfg,
		Logger: logger,
	}

	if cfg.IsGitHubConfigured() {
		ghClient, err := ghclient.NewAppClientWithBaseURL(
			cfg.GitHubAppID,
			cfg.GitHubInstallationID,
			cfg.GitHubAppPrivateKey,
			cfg.GitHubOrg,
			cfg.GitHubBaseURL,
		)
		if err != nil {
			return nil, err
		}
		a.GitHubClient = ghClient
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
			Logger:          logger,
		})
		if err != nil {
			return nil, err
		}
		a.OktaClient = oktaClient
	}

	if cfg.SlackEnabled {
		channels := notifiers.SlackChannels{
			Default:       cfg.SlackChannel,
			PRBypass:      cfg.SlackChannelPRBypass,
			OktaSync:      cfg.SlackChannelOktaSync,
			OrphanedUsers: cfg.SlackChannelOrphanedUsers,
		}
		messages := notifiers.SlackMessages{
			PRBypassFooterNote: cfg.SlackPRBypassFooterNote,
		}
		a.Notifier = notifiers.NewSlackNotifierWithAPIURL(
			cfg.SlackToken, channels, messages, cfg.SlackAPIURL,
		)
	}

	return a, nil
}
