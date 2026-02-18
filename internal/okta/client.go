// Package okta provides Okta API client for group and user management.
// Uses OAuth 2.0 with private key authentication.
package okta

import (
	"context"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/okta/okta-sdk-golang/v6/okta"
)

// DefaultScopes defines the required OAuth scopes for the Okta API.
// these scopes are necessary for group sync functionality.
var DefaultScopes = []string{"okta.groups.read", "okta.users.read"}

// certPoolKey is a typed context key for TLS certificate pool injection.
type certPoolKey struct{}

// WithCertPool returns a new context with the given TLS certificate pool.
// used by integration tests to inject self-signed certs.
func WithCertPool(ctx context.Context, pool *x509.CertPool) context.Context {
	return context.WithValue(ctx, certPoolKey{}, pool)
}

// convertToPKCS1 converts a PEM-encoded private key to PKCS#1 format if needed.
// the Okta SDK requires PKCS#1 format (BEGIN RSA PRIVATE KEY), but Okta's
// console generates PKCS#8 keys (BEGIN PRIVATE KEY). this function detects the
// format and converts PKCS#8 to PKCS#1 automatically.
func convertToPKCS1(keyPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("failed to decode pem block from private key")
	}

	// already in PKCS#1 format
	if block.Type == "RSA PRIVATE KEY" {
		return keyPEM, nil
	}

	// convert PKCS#8 to PKCS#1
	if block.Type == "PRIVATE KEY" {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.Wrap(err, "failed to parse pkcs#8 private key")
		}

		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not an rsa key")
		}

		pkcs1Bytes := x509.MarshalPKCS1PrivateKey(rsaKey)
		pkcs1PEM := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: pkcs1Bytes,
		})

		return pkcs1PEM, nil
	}

	return nil, errors.Newf("unsupported private key type: %s", block.Type)
}

// Client wraps the Okta SDK client with custom configuration.
// implements domain.OktaClient.
type Client struct {
	apiClient       *okta.APIClient
	githubUserField string
	logger          *slog.Logger
}

// compile-time assertion
var _ domain.OktaClient = (*Client)(nil)

// ClientConfig contains Okta client configuration.
type ClientConfig struct {
	Domain          string
	ClientID        string
	PrivateKey      []byte
	PrivateKeyID    string
	Scopes          []string
	GitHubUserField string
	BaseURL         string
	Logger          *slog.Logger
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
		return nil, domain.ErrMissingOAuthCreds
	}

	orgURL := cfg.BaseURL
	if orgURL == "" {
		orgURL = fmt.Sprintf("https://%s", cfg.Domain)
	}

	privateKey, err := convertToPKCS1(cfg.PrivateKey)
	if err != nil {
		return nil, errors.Wrap(err, "failed to convert private key")
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}

	// v6 uses NewConfiguration which returns (config, error)
	opts := []okta.ConfigSetter{
		okta.WithOrgUrl(orgURL),
		okta.WithAuthorizationMode("PrivateKey"),
		okta.WithClientId(cfg.ClientID),
		okta.WithPrivateKey(string(privateKey)),
		okta.WithScopes(scopes),
	}

	if cfg.PrivateKeyID != "" {
		opts = append(opts, okta.WithPrivateKeyId(cfg.PrivateKeyID))
	}

	if certPool, ok := ctx.Value(certPoolKey{}).(*x509.CertPool); ok && certPool != nil {
		httpClient := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					RootCAs: certPool,
				},
			},
		}
		opts = append(opts, okta.WithHttpClientPtr(httpClient))
	}

	oktaCfg, err := okta.NewConfiguration(opts...)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create okta configuration")
	}

	// v6 SDK parses OrgUrl and uses url.Parse().Hostname() which strips the port
	// for testing with custom ports, we need to override the server configuration
	// the SDK uses Servers[0].URL for building API call URLs
	if cfg.BaseURL != "" {
		parsedURL, err := url.Parse(cfg.BaseURL)
		if err == nil {
			// Override the default server configuration with full URL including port
			oktaCfg.Servers = okta.ServerConfigurations{
				okta.ServerConfiguration{
					URL:         cfg.BaseURL,
					Description: "Custom Okta server (test mode)",
				},
			}
			// Also update Host and Scheme for consistency
			// Include port in Host if present
			if parsedURL.Port() != "" {
				oktaCfg.Host = parsedURL.Host // Host includes port
			} else {
				oktaCfg.Host = parsedURL.Hostname()
			}
			oktaCfg.Scheme = parsedURL.Scheme
		}
	}

	apiClient := okta.NewAPIClient(oktaCfg)

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	return &Client{
		apiClient:       apiClient,
		githubUserField: cfg.GitHubUserField,
		logger:          logger,
	}, nil
}

// GetAPIClient returns the underlying Okta SDK API client.
func (c *Client) GetAPIClient() *okta.APIClient {
	return c.apiClient
}

// ListGroups fetches all Okta groups with pagination.
func (c *Client) ListGroups(ctx context.Context) ([]okta.Group, error) {
	groups, resp, err := c.apiClient.GroupAPI.ListGroups(ctx).Limit(200).Execute()
	if err != nil {
		return nil, errors.Wrap(err, "failed to list groups")
	}

	allGroups := make([]okta.Group, 0, len(groups))
	allGroups = append(allGroups, groups...)

	for resp != nil && resp.HasNextPage() {
		var nextGroups []okta.Group
		resp, err = resp.Next(&nextGroups)
		if err != nil {
			return nil, errors.Wrap(err, "failed to fetch next page of groups")
		}
		allGroups = append(allGroups, nextGroups...)
	}

	return allGroups, nil
}

// GetGroupByName searches for an Okta group by exact name match.
// paginates through results in case the group is not on the first page.
func (c *Client) GetGroupByName(ctx context.Context, name string) (*okta.Group, error) {
	groups, resp, err := c.apiClient.GroupAPI.ListGroups(ctx).Q(name).Limit(200).Execute()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to search for group '%s'", name)
	}

	for {
		for i := range groups {
			group := &groups[i]
			groupName := extractGroupName(group)
			if groupName == name {
				return group, nil
			}
		}

		if resp == nil || !resp.HasNextPage() {
			break
		}

		var nextGroups []okta.Group
		resp, err = resp.Next(&nextGroups)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to fetch next page while searching for group '%s'", name)
		}
		groups = nextGroups
	}

	return nil, errors.Newf("group '%s' not found", name)
}

// GetGroupMembers fetches GitHub usernames for all active members of an Okta
// group. paginates through all members. only includes users with status
// "ACTIVE" to exclude suspended/deprovisioned users. skips users without a
// GitHub username in their profile and tracks them separately.
func (c *Client) GetGroupMembers(ctx context.Context, groupID string) (*domain.GroupMembersResult, error) {
	users, resp, err := c.apiClient.GroupAPI.ListGroupUsers(ctx, groupID).Limit(200).Execute()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list members for group '%s'", groupID)
	}

	result := &domain.GroupMembersResult{
		Members:                 make([]string, 0, len(users)),
		SkippedNoGitHubUsername: []string{},
	}

	for {
		c.processGroupUsers(users, result)

		if resp == nil || !resp.HasNextPage() {
			break
		}

		var nextUsers []okta.User
		resp, err = resp.Next(&nextUsers)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to fetch next page of members for group '%s'", groupID)
		}
		users = nextUsers
	}

	return result, nil
}

// processGroupUsers extracts GitHub usernames from a batch of Okta users
// and appends them to the result.
func (c *Client) processGroupUsers(users []okta.User, result *domain.GroupMembersResult) {
	for _, user := range users {
		if user.GetStatus() != "ACTIVE" {
			continue
		}

		profile := user.GetProfile()
		additionalProps := profile.AdditionalProperties
		if additionalProps == nil {
			continue
		}

		githubUsername, ok := additionalProps[c.githubUserField]
		if ok {
			if username, ok := githubUsername.(string); ok && username != "" {
				result.Members = append(result.Members, username)
				continue
			}
		}

		// user doesn't have github username, track by email
		if email, ok := additionalProps["email"].(string); ok && email != "" {
			result.SkippedNoGitHubUsername = append(
				result.SkippedNoGitHubUsername, email)
		} else if profile.GetEmail() != "" {
			result.SkippedNoGitHubUsername = append(
				result.SkippedNoGitHubUsername, profile.GetEmail())
		}
	}
}
