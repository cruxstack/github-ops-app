// Package okta provides Okta API client with OAuth 2.0 private key
// authentication.
package okta

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"

	"github.com/cockroachdb/errors"
	internalerrors "github.com/cruxstack/github-ops-app/internal/errors"
	"github.com/okta/okta-sdk-golang/v2/okta"
	"github.com/okta/okta-sdk-golang/v2/okta/query"
)

// DefaultScopes defines the required OAuth scopes for the Okta API.
// these scopes are necessary for group sync functionality.
var DefaultScopes = []string{"okta.groups.read", "okta.users.read"}

// Client wraps the Okta SDK client with custom configuration.
type Client struct {
	client          *okta.Client
	ctx             context.Context
	githubUserField string
}

// ClientConfig contains Okta client configuration.
type ClientConfig struct {
	Domain          string
	ClientID        string
	PrivateKey      []byte
	Scopes          []string
	GitHubUserField string
	BaseURL         string
}

// NewClient creates an Okta client with background context.
func NewClient(cfg *ClientConfig) (*Client, error) {
	return NewClientWithContext(context.Background(), cfg)
}

// NewClientWithContext creates an Okta client with OAuth 2.0 private key
// authentication. supports custom TLS certificate pools via context for
// testing.
func NewClientWithContext(ctx context.Context, cfg *ClientConfig) (*Client, error) {
	if cfg.ClientID == "" || len(cfg.PrivateKey) == 0 {
		return nil, internalerrors.ErrMissingOAuthCreds
	}

	orgURL := cfg.BaseURL
	if orgURL == "" {
		orgURL = fmt.Sprintf("https://%s", cfg.Domain)
	}

	opts := []okta.ConfigSetter{
		okta.WithOrgUrl(orgURL),
		okta.WithAuthorizationMode("PrivateKey"),
		okta.WithClientId(cfg.ClientID),
		okta.WithPrivateKey(string(cfg.PrivateKey)),
	}

	if len(cfg.Scopes) > 0 {
		opts = append(opts, okta.WithScopes(cfg.Scopes))
	} else {
		opts = append(opts, okta.WithScopes(DefaultScopes))
	}

	if certPool, ok := ctx.Value("okta_tls_cert_pool").(*x509.CertPool); ok && certPool != nil {
		httpClient := http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs: certPool,
				},
			},
		}
		opts = append(opts, okta.WithHttpClient(httpClient))
	}

	_, client, err := okta.NewClient(ctx, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create okta client")
	}

	return &Client{
		client:          client,
		ctx:             ctx,
		githubUserField: cfg.GitHubUserField,
	}, nil
}

// GetClient returns the underlying Okta SDK client.
func (c *Client) GetClient() *okta.Client {
	return c.client
}

// GetContext returns the context used for API requests.
func (c *Client) GetContext() context.Context {
	return c.ctx
}

// ListGroups fetches all Okta groups.
func (c *Client) ListGroups() ([]*okta.Group, error) {
	groups, _, err := c.client.Group.ListGroups(c.ctx, &query.Params{})
	if err != nil {
		return nil, errors.Wrap(err, "failed to list groups")
	}
	return groups, nil
}

// GetGroupByName searches for an Okta group by exact name match.
func (c *Client) GetGroupByName(name string) (*okta.Group, error) {
	groups, _, err := c.client.Group.ListGroups(c.ctx, &query.Params{
		Q: name,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to search for group '%s'", name)
	}

	for _, group := range groups {
		if group.Profile.Name == name {
			return group, nil
		}
	}

	return nil, errors.Newf("group '%s' not found", name)
}

// GetGroupMembers fetches GitHub usernames for all active members of an Okta
// group. only includes users with status "ACTIVE" to exclude
// suspended/deprovisioned users. falls back to email if GitHub username field
// is not set.
func (c *Client) GetGroupMembers(groupID string) ([]string, error) {
	users, _, err := c.client.Group.ListGroupUsers(c.ctx, groupID, &query.Params{})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list members for group '%s'", groupID)
	}

	logins := make([]string, 0, len(users))
	for _, user := range users {
		if user.Status != "ACTIVE" {
			continue
		}

		if user.Profile != nil {
			githubUsername := (*user.Profile)[c.githubUserField]
			if username, ok := githubUsername.(string); ok && username != "" {
				logins = append(logins, username)
			} else {
				email := (*user.Profile)["email"]
				if emailStr, ok := email.(string); ok && emailStr != "" {
					logins = append(logins, emailStr)
				}
			}
		}
	}

	return logins, nil
}
