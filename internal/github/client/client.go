// Package client is the GitHub API client. It authenticates as a GitHub App,
// checks merged pull requests for compliance, syncs team membership, and
// lists security alerts.
package client

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/go-github/v93/github"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

const (
	// jwtLifetime is how long an app JWT is valid. GitHub rejects JWTs that
	// expire more than 10 minutes after they are issued.
	jwtLifetime = 9 * time.Minute
	// jwtClockSkew backdates a JWT's issue time to allow for clock drift.
	jwtClockSkew = 60 * time.Second
	// tokenRefreshMargin is how long before expiry a JWT or installation
	// token is replaced, so no request carries one that expires in flight.
	tokenRefreshMargin = 5 * time.Minute
	// defaultTimeout bounds every GitHub API request.
	defaultTimeout = 30 * time.Second
)

// Options configures a Client.
type Options struct {
	AppID int64
	// InstallationID is the app's installation on Org. When zero, the
	// installation is looked up on first use.
	InstallationID int64
	// PrivateKey is the app's PEM-encoded RSA private key.
	PrivateKey []byte
	Org        string
	// BaseURL is the API base URL, such as https://ghe.example.com/api/v3
	// for GitHub Enterprise Server. Empty means https://api.github.com.
	BaseURL string
	// HTTPClient is the client requests are sent with. Its transport is
	// wrapped to add authentication. Defaults to a client with a timeout.
	HTTPClient *http.Client
}

// Client is a GitHub API client authenticated as a GitHub App installation.
// It is safe for concurrent use. Implements domain.GitHubClient.
type Client struct {
	org string
	// gh authenticates as the installation; app authenticates as the app
	// itself, for the few endpoints that require a JWT.
	gh  *github.Client
	app *github.Client

	slugMu sync.Mutex
	slug   string
}

var _ domain.GitHubClient = (*Client)(nil)

// New creates a Client. It makes no API calls: credentials are exchanged for
// an installation token on first use and refreshed before they expire.
func New(opts Options) (*Client, error) {
	if opts.AppID <= 0 {
		return nil, errors.New("github app id is required")
	}
	if opts.Org == "" {
		return nil, errors.New("github org is required")
	}
	key, err := ParsePrivateKey(opts.PrivateKey)
	if err != nil {
		return nil, err
	}

	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultTimeout}
	}
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}

	var baseURL *string
	apiHost := "api.github.com"
	if opts.BaseURL != "" {
		u, err := url.Parse(opts.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return nil, fmt.Errorf("invalid github base url %q", opts.BaseURL)
		}
		apiHost = u.Host
		baseURL = &opts.BaseURL
	}

	newGitHubClient := func(rt http.RoundTripper) (*github.Client, error) {
		hc := *httpClient
		hc.Transport = rt
		return github.NewClient(github.WithHTTPClient(&hc), github.WithURLs(baseURL, nil))
	}

	jwts := &appJWTs{appID: opts.AppID, key: key, now: time.Now}
	app, err := newGitHubClient(&authTransport{base: base, host: apiHost, token: jwts.Token})
	if err != nil {
		return nil, fmt.Errorf("failed to create github app client: %w", err)
	}

	tokens := &installationTokens{app: app, org: opts.Org, id: opts.InstallationID, now: time.Now}
	gh, err := newGitHubClient(&authTransport{base: base, host: apiHost, token: tokens.Token})
	if err != nil {
		return nil, fmt.Errorf("failed to create github installation client: %w", err)
	}

	return &Client{org: opts.Org, gh: gh, app: app}, nil
}

// ParsePrivateKey parses a PEM-encoded RSA private key in PKCS#1 or PKCS#8
// form. Escaped newlines ("\n"), as found in single-line environment
// variables, are accepted.
func ParsePrivateKey(privateKeyPEM []byte) (*rsa.PrivateKey, error) {
	if len(privateKeyPEM) == 0 {
		return nil, errors.New("github app private key is required")
	}
	pemBytes := []byte(strings.ReplaceAll(string(privateKeyPEM), `\n`, "\n"))
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("failed to decode github app private key: not pem encoded")
	}

	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse github app private key as pkcs1 or pkcs8: %w", err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github app private key is not an rsa key")
	}
	return key, nil
}

// authTransport adds a bearer token to requests for the GitHub API host, and
// only that host, so a redirect elsewhere never carries the token.
type authTransport struct {
	base  http.RoundTripper
	host  string
	token func(ctx context.Context) (string, error)
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != t.host {
		return t.base.RoundTrip(req)
	}
	token, err := t.token(req.Context())
	if err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	authed := req.Clone(req.Context())
	authed.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(authed)
}

// appJWTs signs and caches the JWT that authenticates as the app.
type appJWTs struct {
	appID int64
	key   *rsa.PrivateKey
	now   func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

func (j *appJWTs) Token(context.Context) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()

	now := j.now()
	if j.token != "" && now.Add(time.Minute).Before(j.expires) {
		return j.token, nil
	}

	expires := now.Add(jwtLifetime)
	token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		IssuedAt:  jwt.NewNumericDate(now.Add(-jwtClockSkew)),
		ExpiresAt: jwt.NewNumericDate(expires),
		Issuer:    strconv.FormatInt(j.appID, 10),
	}).SignedString(j.key)
	if err != nil {
		return "", fmt.Errorf("failed to sign jwt for github app %d: %w", j.appID, err)
	}
	j.token, j.expires = token, expires
	return token, nil
}

// installationTokens exchanges the app JWT for installation tokens and caches
// them until shortly before they expire. Concurrent callers share a single
// refresh.
type installationTokens struct {
	app *github.Client
	org string
	now func() time.Time

	mu      sync.Mutex
	id      int64
	token   string
	expires time.Time
}

func (s *installationTokens) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.token != "" && s.now().Add(tokenRefreshMargin).Before(s.expires) {
		return s.token, nil
	}

	if s.id == 0 {
		inst, resp, err := s.app.Apps.GetOrganizationInstallation(ctx, s.org)
		if err != nil {
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				return "", fmt.Errorf("failed to find installation for org '%s': %w", s.org, domain.ErrInstallationMissing)
			}
			return "", fmt.Errorf("failed to find installation for org '%s': %w", s.org, err)
		}
		s.id = inst.GetID()
	}

	token, _, err := s.app.Apps.CreateInstallationToken(ctx, s.id, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create token for installation %d: %w", s.id, err)
	}
	if token.GetToken() == "" {
		return "", fmt.Errorf("github returned an empty token for installation %d", s.id)
	}
	s.token, s.expires = token.GetToken(), token.GetExpiresAt().Time
	return s.token, nil
}

// GetAppSlug returns the GitHub App's slug, used to recognize changes the app
// made itself. It is fetched once and cached.
func (c *Client) GetAppSlug(ctx context.Context) (string, error) {
	c.slugMu.Lock()
	defer c.slugMu.Unlock()
	if c.slug != "" {
		return c.slug, nil
	}

	app, _, err := c.app.Apps.Get(ctx, "")
	if err != nil {
		return "", fmt.Errorf("failed to fetch github app: %w", err)
	}
	if app.GetSlug() == "" {
		return "", errors.New("github app has no slug")
	}
	c.slug = app.GetSlug()
	return c.slug, nil
}

// isNotFound returns true if resp is a 404 response.
func isNotFound(resp *github.Response) bool {
	return resp != nil && resp.StatusCode == http.StatusNotFound
}
