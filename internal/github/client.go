// Package github provides GitHub API client with App authentication.
// handles JWT generation, installation token management, and automatic token
// refresh.
package github

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v79/github"
	"golang.org/x/oauth2"
)

// Client wraps the GitHub API client with App authentication.
// automatically refreshes installation tokens before expiry.
type Client struct {
	client  *github.Client
	org     string
	baseURL string

	appID          int64
	privateKey     *rsa.PrivateKey
	installationID int64

	tokenMu    sync.RWMutex
	token      string
	tokenExpAt time.Time
}

// NewAppClient creates a GitHub App client with default base URL.
func NewAppClient(appID, installationID int64, privateKeyPEM []byte, org string) (*Client, error) {
	return NewAppClientWithBaseURL(appID, installationID, privateKeyPEM, org, "")
}

// NewAppClientWithBaseURL creates a GitHub App client with custom base URL.
// supports GitHub Enterprise Server instances.
func NewAppClientWithBaseURL(appID, installationID int64, privateKeyPEM []byte, org, baseURL string) (*Client, error) {
	privateKey, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse private key")
	}

	c := &Client{
		org:            org,
		appID:          appID,
		privateKey:     privateKey,
		installationID: installationID,
		baseURL:        baseURL,
	}

	if err := c.refreshToken(context.Background()); err != nil {
		return nil, errors.Wrap(err, "failed to get initial token")
	}

	return c, nil
}

// parsePrivateKey parses RSA private key from PEM format.
// supports both PKCS1 and PKCS8 formats.
func parsePrivateKey(privateKeyPEM []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return nil, errors.New("failed to decode pem block: invalid format")
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		pkcs8Key, err2 := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err2 != nil {
			return nil, errors.Wrap(err2, "failed to parse private key as pkcs1 or pkcs8")
		}
		rsaKey, ok := pkcs8Key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("private key is not rsa format")
		}
		return rsaKey, nil
	}

	return key, nil
}

// createJWT generates a JWT token for GitHub App authentication.
// token is valid for 10 minutes and backdated by 60 seconds for clock skew.
func (c *Client) createJWT() (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now.Add(-60 * time.Second)),
		ExpiresAt: jwt.NewNumericDate(now.Add(10 * time.Minute)),
		Issuer:    fmt.Sprintf("%d", c.appID),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	return token.SignedString(c.privateKey)
}

// refreshToken exchanges JWT for installation token and updates client.
// installation tokens are valid for 1 hour.
func (c *Client) refreshToken(ctx context.Context) error {
	jwtToken, err := c.createJWT()
	if err != nil {
		return errors.Wrap(err, "failed to create JWT")
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: jwtToken})
	tc := oauth2.NewClient(ctx, ts)
	appClient := github.NewClient(tc)
	if c.baseURL != "" {
		appClient.BaseURL, _ = appClient.BaseURL.Parse(c.baseURL)
	}

	installToken, resp, err := appClient.Apps.CreateInstallationToken(
		ctx,
		c.installationID,
		&github.InstallationTokenOptions{},
	)
	if err != nil {
		return errors.WithDetailf(
			errors.Wrap(err, "failed to create installation token"),
			"installation_id=%d app_id=%d", c.installationID, c.appID,
		)
	}
	defer resp.Body.Close()

	c.tokenMu.Lock()
	c.token = installToken.GetToken()
	c.tokenExpAt = installToken.GetExpiresAt().Time
	ts2 := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: c.token})
	tc2 := oauth2.NewClient(ctx, ts2)
	c.client = github.NewClient(tc2)
	if c.baseURL != "" {
		c.client.BaseURL, _ = c.client.BaseURL.Parse(c.baseURL)
	}
	c.tokenMu.Unlock()

	return nil
}

// ensureValidToken refreshes the installation token if it expires within 5
// minutes.
func (c *Client) ensureValidToken(ctx context.Context) error {
	c.tokenMu.RLock()
	needsRefresh := time.Now().Add(5 * time.Minute).After(c.tokenExpAt)
	c.tokenMu.RUnlock()

	if needsRefresh {
		return c.refreshToken(ctx)
	}

	return nil
}

// GetOrg returns the GitHub organization name.
func (c *Client) GetOrg() string {
	return c.org
}

// GetClient returns the underlying go-github client.
func (c *Client) GetClient() *github.Client {
	return c.client
}

// Do executes an HTTP request with authentication.
// ensures token is valid before executing request.
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	return c.client.Client().Do(req)
}

// GetAppSlug fetches the GitHub App slug identifier.
// used to detect changes made by the app itself.
func (c *Client) GetAppSlug(ctx context.Context) (string, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return "", err
	}

	app, _, err := c.client.Apps.Get(ctx, "")
	if err != nil {
		return "", errors.Wrapf(err, "failed to fetch app info for app id %d", c.appID)
	}

	if app.Slug == nil {
		return "", errors.Newf("app slug missing for app id %d", c.appID)
	}

	return *app.Slug, nil
}

// IsExternalCollaborator checks if a user is an outside collaborator rather
// than an organization member. returns true if user is not a full org member.
func (c *Client) IsExternalCollaborator(ctx context.Context, username string) (bool, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return false, err
	}

	membership, resp, err := c.client.Organizations.GetOrgMembership(ctx, username, c.org)
	if err != nil {
		if resp != nil && resp.StatusCode == 404 {
			return true, nil
		}
		return false, errors.Wrapf(err, "failed to check org membership for user '%s'", username)
	}

	return membership == nil, nil
}

// ListOrgMembers returns all organization members excluding external
// collaborators.
func (c *Client) ListOrgMembers(ctx context.Context) ([]string, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}

	opts := &github.ListMembersOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	}

	var allMembers []string
	for {
		members, resp, err := c.client.Organizations.ListMembers(ctx, c.org, opts)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list members for org '%s'", c.org)
		}

		for _, member := range members {
			if member.Login != nil {
				allMembers = append(allMembers, *member.Login)
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return allMembers, nil
}
