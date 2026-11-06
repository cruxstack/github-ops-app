// Package okta is the Okta API client. It authenticates with OAuth 2.0
// private key JWTs and reads groups and their members' GitHub usernames.
package okta

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/okta/okta-sdk-golang/v8/okta"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// DefaultScopes are the OAuth scopes group sync needs.
var DefaultScopes = []string{"okta.groups.read", "okta.users.read"}

// requestTimeout bounds every Okta API request, in seconds.
const requestTimeout = 30

// Rate-limited requests are retried after the reset time Okta returns, up to
// this many times and this many seconds apart.
const (
	rateLimitMaxRetries = 3
	rateLimitMaxBackoff = 30
)

// Options configures a Client.
type Options struct {
	// Domain is the Okta org's host, such as example.okta.com.
	Domain   string
	ClientID string
	// PrivateKey is the PEM-encoded RSA key registered with the OAuth app.
	PrivateKey   []byte
	PrivateKeyID string
	Scopes       []string
	// GitHubUserField is the user profile attribute holding the GitHub
	// username.
	GitHubUserField string
	// BaseURL overrides https://<Domain>, for tests.
	BaseURL string
	// HTTPClient is the client requests are sent with, for tests.
	HTTPClient *http.Client
}

// Client reads Okta groups and members. Implements domain.OktaClient.
type Client struct {
	api             *okta.APIClient
	githubUserField string
}

var _ domain.OktaClient = (*Client)(nil)

// New creates a Client. It makes no API calls.
func New(opts Options) (*Client, error) {
	if opts.ClientID == "" {
		return nil, errors.New("okta client id is required")
	}
	if opts.GitHubUserField == "" {
		return nil, errors.New("okta github user field is required")
	}
	privateKey, err := toPKCS1(opts.PrivateKey)
	if err != nil {
		return nil, err
	}

	orgURL := opts.BaseURL
	if orgURL == "" {
		orgURL = "https://" + opts.Domain
	}
	scopes := opts.Scopes
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}

	// set every option that matters explicitly, so OKTA_CLIENT_* variables
	// and okta.yaml files can't change how the client behaves
	setters := []okta.ConfigSetter{
		okta.WithOrgUrl(orgURL),
		okta.WithAuthorizationMode("PrivateKey"),
		okta.WithClientId(opts.ClientID),
		okta.WithPrivateKey(string(privateKey)),
		okta.WithPrivateKeyId(opts.PrivateKeyID),
		okta.WithScopes(scopes),
		okta.WithToken(""),
		okta.WithCache(false),
		okta.WithRequestTimeout(requestTimeout),
		okta.WithRateLimitMaxRetries(rateLimitMaxRetries),
		okta.WithRateLimitMaxBackOff(rateLimitMaxBackoff),
	}
	if opts.HTTPClient != nil {
		setters = append(setters, okta.WithHttpClientPtr(opts.HTTPClient))
	} else {
		setters = append(setters, okta.WithHttpClientPtr(&http.Client{Timeout: requestTimeout * time.Second}))
	}

	cfg, err := okta.NewConfiguration(setters...)
	if err != nil {
		return nil, fmt.Errorf("failed to create okta configuration: %w", err)
	}

	// the SDK builds request URLs from the host name alone, dropping any
	// port, so a base URL with one (a test server) replaces the server list
	if opts.BaseURL != "" {
		u, err := url.Parse(opts.BaseURL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid okta base url %q", opts.BaseURL)
		}
		cfg.Servers = okta.ServerConfigurations{{URL: strings.TrimRight(opts.BaseURL, "/")}}
		cfg.Host = u.Host
		cfg.Scheme = u.Scheme
	}

	return &Client{
		api:             okta.NewAPIClient(cfg),
		githubUserField: opts.GitHubUserField,
	}, nil
}

// toPKCS1 returns a PEM-encoded RSA private key in PKCS#1 form, which the
// Okta SDK requires. Okta's console generates PKCS#8 keys, so those are
// converted. Escaped newlines ("\n") are accepted.
func toPKCS1(keyPEM []byte) ([]byte, error) {
	if len(keyPEM) == 0 {
		return nil, errors.New("okta private key is required")
	}
	keyPEM = []byte(strings.ReplaceAll(string(keyPEM), `\n`, "\n"))
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("failed to decode okta private key: not pem encoded")
	}

	switch block.Type {
	case "RSA PRIVATE KEY":
		if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			return nil, fmt.Errorf("failed to parse okta private key: %w", err)
		}
		return keyPEM, nil
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("failed to parse okta private key: %w", err)
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("okta private key is not an rsa key")
		}
		return pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(rsaKey),
		}), nil
	default:
		return nil, fmt.Errorf("unsupported okta private key type '%s'", block.Type)
	}
}
