// Package config reads and validates the configuration from environment
// variables. Invalid, incomplete, or contradictory settings are errors, all
// reported at once, so a misconfigured service fails at startup instead of
// silently running with a feature disabled or a default it was not given.
//
// Any value may be an AWS SSM Parameter Store ARN, which is replaced by the
// parameter's decrypted value.
package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/url"
	"os"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cruxstack/github-app-setup-go/configstore"
	"github.com/cruxstack/github-app-setup-go/ssmresolver"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// Defaults for unset variables.
const (
	DefaultListenAddr                = ":8080"
	DefaultGitHubWebURL              = "https://github.com"
	DefaultOktaGitHubUserField       = "githubUsername"
	DefaultOktaSyncSafetyThreshold   = 0.5
	DefaultSecurityAlertsMinAgeDays  = 30
	DefaultSecurityAlertsMinSeverity = domain.SeverityHigh
	// MinAdminTokenLength is the shortest admin token accepted.
	MinAdminTokenLength = 16
)

// Default lists for unset variables.
var (
	DefaultPRMonitoredBranches = []string{"main", "master"}
	DefaultOktaScopes          = []string{"okta.groups.read", "okta.users.read"}
)

// removed lists variables that are no longer read, with what to use
// instead. Setting one is an error so an old configuration is not silently
// ignored.
var removed = map[string]string{
	"APP_DEBUG_ENABLED": "APP_LOG_LEVEL=debug",
	"APP_PORT":          "APP_LISTEN_ADDR",
	"APP_OKTA_KEY_ID":   "APP_OKTA_PRIVATE_KEY_ID",
}

// Config is the application's configuration.
type Config struct {
	Bootstrap

	// AdminToken guards the admin and scheduled endpoints. When empty,
	// those endpoints are disabled.
	AdminToken string

	GitHub         GitHub
	PRCompliance   PRCompliance
	Okta           Okta
	SecurityAlerts SecurityAlerts
	Slack          Slack
}

// Bootstrap is the configuration needed before the rest can be loaded: how
// to log, serve, and run the GitHub App installer. It doesn't need the
// GitHub App's credentials, so it loads before the app is set up. Like the
// rest, its APP_ values may be SSM ARNs; the installer's own variables are
// read as github-app-setup-go reads them, without SSM resolution.
type Bootstrap struct {
	LogLevel  slog.Level
	Server    Server
	Installer Installer
}

// Server configures the HTTP server.
type Server struct {
	// ListenAddr is the TCP address to listen on, such as ":8080".
	ListenAddr string
	// BasePath is the path every route is mounted under: empty, or a path
	// with a leading slash and no trailing slash.
	BasePath string
	// PublicURL is the external URL of the service, including BasePath. It
	// is used to build the installer's redirect and webhook URLs; when
	// empty, they are derived from each request.
	PublicURL string
}

// Installer configures the GitHub App manifest installer.
type Installer struct {
	Enabled bool
	// StorageMode is where the installer saves credentials: envfile or
	// aws-ssm.
	StorageMode string
	// StorageDir is the env file path for the envfile mode.
	StorageDir string
	// GitHubOrg is the organization the app is created in.
	GitHubOrg string
	// GitHubURL is the GitHub web URL the app is created on.
	GitHubURL string
}

// GitHub configures the GitHub App.
type GitHub struct {
	Org   string
	AppID int64
	// InstallationID is the app's installation on Org. When zero it is
	// looked up.
	InstallationID int64
	PrivateKey     []byte
	WebhookSecret  string
	// BaseURL is the API URL for GitHub Enterprise Server, or empty.
	BaseURL string
	// WebURL is the web URL that links point to.
	WebURL string
}

// PRCompliance configures PR compliance monitoring.
type PRCompliance struct {
	Enabled bool
	// MonitoredBranches are branch names or glob patterns, such as
	// "release/*".
	MonitoredBranches []string
}

// Okta configures Okta group sync.
type Okta struct {
	Enabled                   bool
	Domain                    string
	ClientID                  string
	PrivateKey                []byte
	PrivateKeyID              string
	Scopes                    []string
	BaseURL                   string
	GitHubUserField           string
	SyncRules                 []domain.SyncRule
	SyncSafetyThreshold       float64
	OrphanedUserNotifications bool
}

// SecurityAlerts configures stale security alert reports.
type SecurityAlerts struct {
	Enabled     bool
	MinAgeDays  int
	MinSeverity string
}

// Slack configures notifications.
type Slack struct {
	Enabled               bool
	Token                 string
	Channel               string
	ChannelPRBypass       string
	ChannelOktaSync       string
	ChannelOrphanedUsers  string
	ChannelSecurityAlerts string
	PRBypassFooterNote    string
	APIURL                string
}

// Env is where configuration is read from.
type Env struct {
	// Lookup has the signature of os.LookupEnv.
	Lookup func(key string) (string, bool)
	// ResolveSSM returns the value of the SSM parameter an ARN names. When
	// nil, parameters are read from AWS with the default credentials.
	ResolveSSM func(ctx context.Context, arn string) (string, error)
}

// ProcessEnv reads the process environment and resolves SSM ARNs in AWS.
func ProcessEnv() Env {
	return Env{Lookup: os.LookupEnv}
}

// LoadBootstrap reads the bootstrap configuration.
func LoadBootstrap(env Env) (*Bootstrap, error) {
	r := newReader(context.Background(), env)
	b := r.bootstrap()
	if err := r.err(); err != nil {
		return nil, err
	}
	return b, nil
}

// Load reads the full configuration. All problems are reported together.
func Load(ctx context.Context, env Env) (*Config, error) {
	r := newReader(ctx, env)
	for _, key := range slices.Sorted(maps.Keys(removed)) {
		if r.has(key) {
			r.fail(fmt.Errorf("%s is no longer supported: use %s", key, removed[key]))
		}
	}

	cfg := &Config{Bootstrap: *r.bootstrap()}
	cfg.AdminToken = r.str("APP_ADMIN_TOKEN")
	if cfg.AdminToken != "" && len(cfg.AdminToken) < MinAdminTokenLength {
		r.fail(fmt.Errorf("APP_ADMIN_TOKEN must be at least %d characters", MinAdminTokenLength))
	}

	cfg.GitHub = r.github()
	cfg.PRCompliance = PRCompliance{
		Enabled:           r.bool("APP_PR_COMPLIANCE_ENABLED", false),
		MonitoredBranches: r.list("APP_PR_MONITORED_BRANCHES", DefaultPRMonitoredBranches),
	}
	for _, pattern := range cfg.PRCompliance.MonitoredBranches {
		if _, err := path.Match(pattern, ""); err != nil {
			r.fail(fmt.Errorf("APP_PR_MONITORED_BRANCHES: invalid pattern %q", pattern))
		}
	}
	cfg.Okta = r.okta()
	cfg.SecurityAlerts = r.securityAlerts()
	cfg.Slack = r.slack()

	if err := r.err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// reader reads variables and collects every problem it finds.
type reader struct {
	ctx  context.Context
	env  Env
	errs []error
	ssm  *ssmresolver.Resolver
}

func newReader(ctx context.Context, env Env) *reader {
	if env.Lookup == nil {
		env.Lookup = os.LookupEnv
	}
	return &reader{ctx: ctx, env: env}
}

func (r *reader) fail(err error) {
	r.errs = append(r.errs, err)
}

func (r *reader) err() error {
	if len(r.errs) == 0 {
		return nil
	}
	return fmt.Errorf("invalid configuration: %w", errors.Join(r.errs...))
}

func (r *reader) has(key string) bool {
	_, ok := r.env.Lookup(key)
	return ok
}

// raw returns a variable's value, resolving an SSM ARN, without trimming.
func (r *reader) raw(key string) string {
	v, _ := r.env.Lookup(key)
	if !ssmresolver.IsSSMARN(strings.TrimSpace(v)) {
		return v
	}
	resolved, err := r.resolveSSM(strings.TrimSpace(v))
	if err != nil {
		r.fail(fmt.Errorf("%s: %w", key, err))
		return ""
	}
	return resolved
}

func (r *reader) resolveSSM(arn string) (string, error) {
	if r.env.ResolveSSM != nil {
		return r.env.ResolveSSM(r.ctx, arn)
	}
	if r.ssm == nil {
		resolver, err := ssmresolver.New(r.ctx)
		if err != nil {
			return "", err
		}
		r.ssm = resolver
	}
	return r.ssm.ResolveValue(r.ctx, arn)
}

// plain returns a variable's value without SSM resolution, trimmed.
func (r *reader) plain(key string) string {
	v, _ := r.env.Lookup(key)
	return strings.TrimSpace(v)
}

func (r *reader) str(key string) string {
	return strings.TrimSpace(r.raw(key))
}

func (r *reader) bool(key string, def bool) bool {
	v := r.str(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		r.fail(fmt.Errorf("%s: %q is not a boolean", key, v))
		return def
	}
	return b
}

func (r *reader) int64(key string) int64 {
	v := r.str(key)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		r.fail(fmt.Errorf("%s: %q is not a positive integer", key, v))
		return 0
	}
	return n
}

func (r *reader) list(key string, def []string) []string {
	v := r.str(key)
	if v == "" {
		return slices.Clone(def)
	}
	var items []string
	for item := range strings.SplitSeq(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		r.fail(fmt.Errorf("%s: list is empty", key))
	}
	return items
}

func (r *reader) url(key string) string {
	v := r.str(key)
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		r.fail(fmt.Errorf("%s: %q is not an http(s) url", key, v))
		return ""
	}
	return strings.TrimRight(v, "/")
}

// pem returns a PEM value from key, or from the file named by key_PATH.
// Setting both is an error.
func (r *reader) pem(key string) []byte {
	pathKey := key + "_PATH"
	value, file := r.raw(key), r.plain(pathKey)
	switch {
	case strings.TrimSpace(value) != "" && file != "":
		r.fail(fmt.Errorf("%s and %s are mutually exclusive", key, pathKey))
		return nil
	case file != "":
		b, err := os.ReadFile(file)
		if err != nil {
			r.fail(fmt.Errorf("%s: %w", pathKey, err))
			return nil
		}
		return b
	case strings.TrimSpace(value) != "":
		// the installer's env file and many secret stores keep a pem on one
		// line with escaped newlines
		return []byte(strings.ReplaceAll(strings.TrimSpace(value), `\n`, "\n"))
	default:
		return nil
	}
}

var (
	basePathPattern = regexp.MustCompile(`^(/[A-Za-z0-9._~-]+)+$`)
	// githubOrgPattern matches organization logins.
	githubOrgPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	hostnamePattern  = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)
)

func (r *reader) bootstrap() *Bootstrap {
	b := &Bootstrap{LogLevel: slog.LevelInfo}

	if level := r.str("APP_LOG_LEVEL"); level != "" {
		if err := b.LogLevel.UnmarshalText([]byte(level)); err != nil {
			r.fail(fmt.Errorf("APP_LOG_LEVEL: %q is not one of debug, info, warn, error", level))
		}
	}

	b.Server.ListenAddr = r.str("APP_LISTEN_ADDR")
	if b.Server.ListenAddr == "" {
		b.Server.ListenAddr = DefaultListenAddr
	}
	if _, port, err := net.SplitHostPort(b.Server.ListenAddr); err != nil || port == "" {
		r.fail(fmt.Errorf("APP_LISTEN_ADDR: %q is not a host:port address", b.Server.ListenAddr))
	}

	basePath := strings.TrimRight(r.str("APP_BASE_PATH"), "/")
	if basePath != "" {
		if !strings.HasPrefix(basePath, "/") {
			basePath = "/" + basePath
		}
		if !basePathPattern.MatchString(basePath) {
			r.fail(fmt.Errorf("APP_BASE_PATH: %q is not a valid path", basePath))
		}
	}
	b.Server.BasePath = basePath

	if v := r.str("APP_PUBLIC_URL"); v != "" {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			r.fail(fmt.Errorf("APP_PUBLIC_URL: %q is not an http(s) url", v))
		}
		b.Server.PublicURL = strings.TrimRight(v, "/")
	}

	// the installer's settings use the github-app-setup-go names
	switch strings.ToLower(r.plain(configstore.EnvGitHubAppInstallerEnabled)) {
	case "true", "1", "yes":
		b.Installer.Enabled = true
	}
	b.Installer.StorageMode = r.plain(configstore.EnvStorageMode)
	if b.Installer.StorageMode == "" {
		b.Installer.StorageMode = configstore.StorageModeEnvFile
	}
	b.Installer.StorageDir = r.plain(configstore.EnvStorageDir)
	if b.Installer.StorageDir == "" {
		b.Installer.StorageDir = "./.env"
	}
	b.Installer.GitHubOrg = r.str("APP_GITHUB_ORG")
	b.Installer.GitHubURL = DefaultGitHubWebURL
	if api := r.str("APP_GITHUB_BASE_URL"); api != "" {
		b.Installer.GitHubURL = webURLFromAPI(api)
	}
	if b.Installer.Enabled {
		if b.Installer.GitHubOrg == "" {
			r.fail(errors.New("APP_GITHUB_ORG is required when the installer is enabled"))
		}
		switch b.Installer.StorageMode {
		case configstore.StorageModeEnvFile:
			if r.has("AWS_LAMBDA_FUNCTION_NAME") {
				r.fail(fmt.Errorf("%s=%s does not persist in lambda: use %s", configstore.EnvStorageMode, configstore.StorageModeEnvFile, configstore.StorageModeAWSSSM))
			}
		case configstore.StorageModeAWSSSM:
			if r.plain(configstore.EnvAWSSSMParameterPfx) == "" {
				r.fail(fmt.Errorf("%s is required when %s is %s", configstore.EnvAWSSSMParameterPfx, configstore.EnvStorageMode, configstore.StorageModeAWSSSM))
			}
		default:
			r.fail(fmt.Errorf("%s: %q is not supported: use %s or %s", configstore.EnvStorageMode,
				b.Installer.StorageMode, configstore.StorageModeEnvFile, configstore.StorageModeAWSSSM))
		}
	}

	return b
}

func (r *reader) github() GitHub {
	g := GitHub{
		Org:            r.str("APP_GITHUB_ORG"),
		AppID:          r.int64("APP_GITHUB_APP_ID"),
		InstallationID: r.int64("APP_GITHUB_INSTALLATION_ID"),
		PrivateKey:     r.pem("APP_GITHUB_APP_PRIVATE_KEY"),
		WebhookSecret:  r.raw("APP_GITHUB_WEBHOOK_SECRET"),
		BaseURL:        r.url("APP_GITHUB_BASE_URL"),
		WebURL:         DefaultGitHubWebURL,
	}

	switch {
	case g.Org == "":
		r.fail(errors.New("APP_GITHUB_ORG is required"))
	case !githubOrgPattern.MatchString(g.Org):
		r.fail(fmt.Errorf("APP_GITHUB_ORG: %q is not a valid organization name", g.Org))
	}
	if r.plain("APP_GITHUB_APP_ID") == "" {
		r.fail(errors.New("APP_GITHUB_APP_ID is required"))
	}
	if len(g.PrivateKey) == 0 && r.plain("APP_GITHUB_APP_PRIVATE_KEY_PATH") == "" {
		r.fail(errors.New("APP_GITHUB_APP_PRIVATE_KEY or APP_GITHUB_APP_PRIVATE_KEY_PATH is required"))
	}
	if g.WebhookSecret == "" {
		r.fail(errors.New("APP_GITHUB_WEBHOOK_SECRET is required"))
	}

	if g.BaseURL != "" {
		g.WebURL = webURLFromAPI(g.BaseURL)
	}
	return g
}

// webURLFromAPI derives a GitHub web URL from its API URL:
// https://ghe.example.com/api/v3 becomes https://ghe.example.com, and
// https://api.example.com becomes https://example.com.
func webURLFromAPI(apiURL string) string {
	u, err := url.Parse(apiURL)
	if err != nil {
		return DefaultGitHubWebURL
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/api/v3")
	u.Host = strings.TrimPrefix(u.Host, "api.")
	return strings.TrimRight(u.String(), "/")
}

func (r *reader) okta() Okta {
	o := Okta{
		Domain:          r.str("APP_OKTA_DOMAIN"),
		ClientID:        r.str("APP_OKTA_CLIENT_ID"),
		PrivateKey:      r.pem("APP_OKTA_PRIVATE_KEY"),
		PrivateKeyID:    r.str("APP_OKTA_PRIVATE_KEY_ID"),
		Scopes:          r.list("APP_OKTA_SCOPES", DefaultOktaScopes),
		BaseURL:         r.url("APP_OKTA_BASE_URL"),
		GitHubUserField: r.str("APP_OKTA_GITHUB_USER_FIELD"),
	}
	rulesJSON := r.str("APP_OKTA_SYNC_RULES")

	o.Enabled = o.Domain != "" || o.ClientID != "" || len(o.PrivateKey) > 0 ||
		r.plain("APP_OKTA_PRIVATE_KEY_PATH") != "" || rulesJSON != ""
	if !o.Enabled {
		return o
	}

	if o.Domain == "" {
		r.fail(errors.New("APP_OKTA_DOMAIN is required when okta sync is configured"))
	} else if !hostnamePattern.MatchString(o.Domain) {
		r.fail(fmt.Errorf("APP_OKTA_DOMAIN: %q must be a host name, such as example.okta.com", o.Domain))
	}
	if o.ClientID == "" {
		r.fail(errors.New("APP_OKTA_CLIENT_ID is required when okta sync is configured"))
	}
	if len(o.PrivateKey) == 0 && r.plain("APP_OKTA_PRIVATE_KEY_PATH") == "" {
		r.fail(errors.New("APP_OKTA_PRIVATE_KEY or APP_OKTA_PRIVATE_KEY_PATH is required when okta sync is configured"))
	}
	if o.GitHubUserField == "" {
		o.GitHubUserField = DefaultOktaGitHubUserField
	}

	if rulesJSON == "" {
		r.fail(errors.New("APP_OKTA_SYNC_RULES is required when okta sync is configured"))
	} else {
		dec := json.NewDecoder(bytes.NewReader([]byte(rulesJSON)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&o.SyncRules); err != nil {
			r.fail(fmt.Errorf("APP_OKTA_SYNC_RULES: %w", err))
		} else if len(o.SyncRules) == 0 {
			r.fail(errors.New("APP_OKTA_SYNC_RULES: no rules"))
		}
		for i, rule := range o.SyncRules {
			if err := rule.Validate(); err != nil {
				r.fail(fmt.Errorf("APP_OKTA_SYNC_RULES: rule %d (%s): %w", i, rule.GetName(), err))
			}
		}
	}

	o.SyncSafetyThreshold = DefaultOktaSyncSafetyThreshold
	if v := r.str("APP_OKTA_SYNC_SAFETY_THRESHOLD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			r.fail(fmt.Errorf("APP_OKTA_SYNC_SAFETY_THRESHOLD: %q is not a number from 0 to 1", v))
		} else {
			o.SyncSafetyThreshold = f
		}
	}
	o.OrphanedUserNotifications = r.bool("APP_OKTA_ORPHANED_USER_NOTIFICATIONS", true)
	return o
}

func (r *reader) securityAlerts() SecurityAlerts {
	s := SecurityAlerts{
		Enabled:     r.bool("APP_SECURITY_ALERTS_ENABLED", false),
		MinAgeDays:  DefaultSecurityAlertsMinAgeDays,
		MinSeverity: DefaultSecurityAlertsMinSeverity,
	}
	if v := r.str("APP_SECURITY_ALERTS_MIN_AGE_DAYS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			r.fail(fmt.Errorf("APP_SECURITY_ALERTS_MIN_AGE_DAYS: %q is not a non-negative integer", v))
		} else {
			s.MinAgeDays = n
		}
	}
	if v := strings.ToLower(r.str("APP_SECURITY_ALERTS_MIN_SEVERITY")); v != "" {
		if !domain.IsSeverity(v) {
			r.fail(fmt.Errorf("APP_SECURITY_ALERTS_MIN_SEVERITY: %q is not one of critical, high, medium, low", v))
		} else {
			s.MinSeverity = v
		}
	}
	return s
}

func (r *reader) slack() Slack {
	s := Slack{
		Token:                 r.raw("APP_SLACK_TOKEN"),
		Channel:               r.str("APP_SLACK_CHANNEL"),
		ChannelPRBypass:       r.str("APP_SLACK_CHANNEL_PR_BYPASS"),
		ChannelOktaSync:       r.str("APP_SLACK_CHANNEL_OKTA_SYNC"),
		ChannelOrphanedUsers:  r.str("APP_SLACK_CHANNEL_ORPHANED_USERS"),
		ChannelSecurityAlerts: r.str("APP_SLACK_CHANNEL_SECURITY_ALERTS"),
		PRBypassFooterNote:    r.str("APP_SLACK_FOOTER_NOTE_PR_BYPASS"),
		APIURL:                r.url("APP_SLACK_API_URL"),
	}
	s.Token = strings.TrimSpace(s.Token)
	s.Enabled = s.Token != "" || s.Channel != ""
	if s.Enabled {
		if s.Token == "" {
			r.fail(errors.New("APP_SLACK_TOKEN is required when APP_SLACK_CHANNEL is set"))
		}
		if s.Channel == "" {
			r.fail(errors.New("APP_SLACK_CHANNEL is required when APP_SLACK_TOKEN is set"))
		}
	}
	if s.APIURL != "" {
		// the slack client appends method names to this url
		s.APIURL += "/"
	}
	return s
}

// ShouldMonitorBranch returns true if merges into branch are checked for
// compliance.
func (c *Config) ShouldMonitorBranch(branch string) bool {
	if !c.PRCompliance.Enabled {
		return false
	}
	branch = strings.TrimPrefix(branch, "refs/heads/")
	for _, pattern := range c.PRCompliance.MonitoredBranches {
		if ok, _ := path.Match(pattern, branch); ok {
			return true
		}
	}
	return false
}

// RedactedConfig is the configuration with secrets redacted, safe to log or
// return from the admin API.
type RedactedConfig struct {
	LogLevel         string `json:"log_level"`
	BasePath         string `json:"base_path"`
	PublicURL        string `json:"public_url"`
	InstallerEnabled bool   `json:"installer_enabled"`
	AdminToken       string `json:"admin_token"`

	GitHubOrg            string `json:"github_org"`
	GitHubAppID          int64  `json:"github_app_id"`
	GitHubInstallationID int64  `json:"github_installation_id"`
	GitHubAppPrivateKey  string `json:"github_app_private_key"`
	GitHubWebhookSecret  string `json:"github_webhook_secret"`
	GitHubBaseURL        string `json:"github_base_url"`

	PRComplianceEnabled bool     `json:"pr_compliance_enabled"`
	PRMonitoredBranches []string `json:"pr_monitored_branches"`

	OktaEnabled                   bool              `json:"okta_enabled"`
	OktaDomain                    string            `json:"okta_domain"`
	OktaClientID                  string            `json:"okta_client_id"`
	OktaPrivateKey                string            `json:"okta_private_key"`
	OktaPrivateKeyID              string            `json:"okta_private_key_id"`
	OktaScopes                    []string          `json:"okta_scopes"`
	OktaGitHubUserField           string            `json:"okta_github_user_field"`
	OktaSyncRules                 []domain.SyncRule `json:"okta_sync_rules"`
	OktaSyncSafetyThreshold       float64           `json:"okta_sync_safety_threshold"`
	OktaOrphanedUserNotifications bool              `json:"okta_orphaned_user_notifications"`

	SecurityAlertsEnabled     bool   `json:"security_alerts_enabled"`
	SecurityAlertsMinAgeDays  int    `json:"security_alerts_min_age_days"`
	SecurityAlertsMinSeverity string `json:"security_alerts_min_severity"`

	SlackEnabled               bool   `json:"slack_enabled"`
	SlackToken                 string `json:"slack_token"`
	SlackChannel               string `json:"slack_channel"`
	SlackChannelPRBypass       string `json:"slack_channel_pr_bypass"`
	SlackChannelOktaSync       string `json:"slack_channel_okta_sync"`
	SlackChannelOrphanedUsers  string `json:"slack_channel_orphaned_users"`
	SlackChannelSecurityAlerts string `json:"slack_channel_security_alerts"`
}

const redactedValue = "***REDACTED***"

func redact[T string | []byte](v T) string {
	if len(v) == 0 {
		return ""
	}
	return redactedValue
}

// Redacted returns the configuration with secrets redacted.
func (c *Config) Redacted() RedactedConfig {
	return RedactedConfig{
		LogLevel:         c.LogLevel.String(),
		BasePath:         c.Server.BasePath,
		PublicURL:        c.Server.PublicURL,
		InstallerEnabled: c.Installer.Enabled,
		AdminToken:       redact(c.AdminToken),

		GitHubOrg:            c.GitHub.Org,
		GitHubAppID:          c.GitHub.AppID,
		GitHubInstallationID: c.GitHub.InstallationID,
		GitHubAppPrivateKey:  redact(c.GitHub.PrivateKey),
		GitHubWebhookSecret:  redact(c.GitHub.WebhookSecret),
		GitHubBaseURL:        c.GitHub.BaseURL,

		PRComplianceEnabled: c.PRCompliance.Enabled,
		PRMonitoredBranches: c.PRCompliance.MonitoredBranches,

		OktaEnabled:                   c.Okta.Enabled,
		OktaDomain:                    c.Okta.Domain,
		OktaClientID:                  c.Okta.ClientID,
		OktaPrivateKey:                redact(c.Okta.PrivateKey),
		OktaPrivateKeyID:              c.Okta.PrivateKeyID,
		OktaScopes:                    c.Okta.Scopes,
		OktaGitHubUserField:           c.Okta.GitHubUserField,
		OktaSyncRules:                 c.Okta.SyncRules,
		OktaSyncSafetyThreshold:       c.Okta.SyncSafetyThreshold,
		OktaOrphanedUserNotifications: c.Okta.OrphanedUserNotifications,

		SecurityAlertsEnabled:     c.SecurityAlerts.Enabled,
		SecurityAlertsMinAgeDays:  c.SecurityAlerts.MinAgeDays,
		SecurityAlertsMinSeverity: c.SecurityAlerts.MinSeverity,

		SlackEnabled:               c.Slack.Enabled,
		SlackToken:                 redact(c.Slack.Token),
		SlackChannel:               c.Slack.Channel,
		SlackChannelPRBypass:       c.Slack.ChannelPRBypass,
		SlackChannelOktaSync:       c.Slack.ChannelOktaSync,
		SlackChannelOrphanedUsers:  c.Slack.ChannelOrphanedUsers,
		SlackChannelSecurityAlerts: c.Slack.ChannelSecurityAlerts,
	}
}
