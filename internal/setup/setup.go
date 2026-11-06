// Package setup integrates the github-app-setup-go installer, which creates
// the GitHub App from a manifest and saves its credentials.
package setup

import (
	"context"
	"strconv"
	"strings"

	"github.com/cruxstack/github-app-setup-go/configstore"
	"github.com/cruxstack/github-app-setup-go/installer"
)

// AppName is the default name of the GitHub App.
const AppName = "github-ops-app"

// Permissions are the GitHub App permissions the application needs, and no
// more.
var Permissions = map[string]string{
	// repository permissions
	"administration":         "read", // branch protection rules
	"checks":                 "read", // check runs on merged pr heads
	"metadata":               "read", // rulesets and repository permissions
	"pull_requests":          "read", // pr details and reviews
	"statuses":               "read", // commit statuses on merged pr heads
	"vulnerability_alerts":   "read", // dependabot alerts
	"security_events":        "read", // code scanning alerts
	"secret_scanning_alerts": "read", // secret scanning alerts
	// organization permissions
	"members": "write", // teams and team membership
}

// Events are the webhook events the application handles.
var Events = []string{"pull_request", "team", "membership"}

// Manifest returns the GitHub App manifest. The app is private, so only the
// organization that creates it can install it.
func Manifest() installer.Manifest {
	return installer.Manifest{
		Name:          AppName,
		URL:           "https://github.com/cruxstack/github-ops-app",
		Public:        false,
		DefaultPerms:  Permissions,
		DefaultEvents: Events,
	}
}

// The names the application reads the GitHub App credentials from.
const (
	EnvAppID         = "APP_GITHUB_APP_ID"
	EnvPrivateKey    = "APP_GITHUB_APP_PRIVATE_KEY"
	EnvWebhookSecret = "APP_GITHUB_WEBHOOK_SECRET"
)

// Store wraps a credential store so the credentials are also saved under the
// names the application reads. The store's own names, such as GITHUB_APP_ID,
// are still written, since its status checks read them.
type Store struct {
	configstore.Store
}

// Save adds the application's names for the credentials and saves them.
func (s Store) Save(ctx context.Context, creds *configstore.AppCredentials) error {
	if creds != nil {
		if creds.CustomFields == nil {
			creds.CustomFields = make(map[string]string)
		}
		creds.CustomFields[EnvAppID] = strconv.FormatInt(creds.AppID, 10)
		// one line, since the env file store writes a value per line; the
		// application turns escaped newlines back into newlines
		creds.CustomFields[EnvPrivateKey] = strings.ReplaceAll(creds.PrivateKey, "\n", `\n`)
		creds.CustomFields[EnvWebhookSecret] = creds.WebhookSecret
	}
	return s.Store.Save(ctx, creds)
}

// NopStore is a credential store for when the installer is disabled.
type NopStore struct{}

func (NopStore) Save(context.Context, *configstore.AppCredentials) error { return nil }

func (NopStore) Status(context.Context) (*configstore.InstallerStatus, error) {
	return &configstore.InstallerStatus{}, nil
}

func (NopStore) DisableInstaller(context.Context) error { return nil }
