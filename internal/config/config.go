// Package config provides application configuration loaded from environment
// variables.
package config

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/okta"
)

// Config holds all application configuration loaded from environment
// variables.
type Config struct {
	DebugEnabled bool

	GitHubOrg           string
	GitHubWebhookSecret string
	GitHubBaseURL       string

	GitHubAppID         int64
	GitHubAppPrivateKey []byte
	GitHubInstallID     int64

	OktaDomain              string
	OktaClientID            string
	OktaPrivateKey          []byte
	OktaScopes              []string
	OktaBaseURL             string
	OktaSyncRules           []okta.SyncRule
	OktaGitHubUserField     string
	OktaSyncSafetyThreshold float64

	PRComplianceEnabled bool
	PRMonitoredBranches []string

	OktaOrphanedUserNotifications bool

	SlackEnabled bool
	SlackToken   string
	SlackChannel string
	SlackAPIURL  string

	BasePath string
}

var (
	ssmClient     *ssm.Client
	ssmClientOnce sync.Once
	ssmClientErr  error
)

// getSSMClient initializes and returns a cached SSM client.
// lazy initialization ensures we only create the client when SSM parameters
// are actually needed.
func getSSMClient(ctx context.Context) (*ssm.Client, error) {
	ssmClientOnce.Do(func() {
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			ssmClientErr = errors.Wrap(err, "failed to load aws config for ssm")
			return
		}
		ssmClient = ssm.NewFromConfig(cfg)
	})
	return ssmClient, ssmClientErr
}

// resolveEnvValue resolves an environment variable value.
// if the value starts with "arn:aws:ssm:", fetches the parameter from SSM.
// automatically decrypts SecureString parameters.
func resolveEnvValue(ctx context.Context, key, value string) (string, error) {
	if value == "" {
		return "", nil
	}

	if !strings.HasPrefix(value, "arn:aws:ssm:") {
		return value, nil
	}

	client, err := getSSMClient(ctx)
	if err != nil {
		return "", errors.Wrapf(err, "failed to init ssm client for %s", key)
	}

	paramName := strings.TrimPrefix(value, "arn:aws:ssm:")
	idx := strings.Index(paramName, ":parameter/")
	if idx == -1 {
		return "", errors.Newf("invalid ssm parameter arn format for %s: %s", key, value)
	}
	paramName = paramName[idx+len(":parameter/"):]

	input := &ssm.GetParameterInput{
		Name:           &paramName,
		WithDecryption: aws.Bool(true),
	}

	result, err := client.GetParameter(ctx, input)
	if err != nil {
		return "", errors.Wrapf(err, "failed to get ssm parameter '%s' for %s", paramName, key)
	}

	if result.Parameter == nil || result.Parameter.Value == nil {
		return "", errors.Newf("ssm parameter '%s' for %s returned nil value", paramName, key)
	}

	return *result.Parameter.Value, nil
}

// getEnv retrieves an environment variable and resolves SSM parameters if
// needed.
func getEnv(ctx context.Context, key string) (string, error) {
	value := os.Getenv(key)
	return resolveEnvValue(ctx, key, value)
}

// NewConfig loads configuration from environment variables.
// returns error if required values are missing or invalid.
// supports SSM parameter references in format:
// arn:aws:ssm:REGION:ACCOUNT:parameter/path/to/param
func NewConfig() (*Config, error) {
	return NewConfigWithContext(context.Background())
}

// NewConfigWithContext loads configuration from environment variables with
// the given context. supports SSM parameter resolution with automatic
// decryption.
func NewConfigWithContext(ctx context.Context) (*Config, error) {
	debugEnabled, _ := strconv.ParseBool(os.Getenv("APP_DEBUG_ENABLED"))

	oktaGitHubUserField := os.Getenv("APP_OKTA_GITHUB_USER_FIELD")
	if oktaGitHubUserField == "" {
		oktaGitHubUserField = "githubUsername"
	}

	oktaSyncSafetyThreshold := 0.5
	if thresholdStr := os.Getenv("APP_OKTA_SYNC_SAFETY_THRESHOLD"); thresholdStr != "" {
		if threshold, err := strconv.ParseFloat(thresholdStr, 64); err == nil && threshold >= 0 && threshold <= 1 {
			oktaSyncSafetyThreshold = threshold
		}
	}

	githubWebhookSecret, err := getEnv(ctx, "APP_GITHUB_WEBHOOK_SECRET")
	if err != nil {
		return nil, err
	}

	slackToken, err := getEnv(ctx, "APP_SLACK_TOKEN")
	if err != nil {
		return nil, err
	}

	cfg := Config{
		DebugEnabled:            debugEnabled,
		GitHubOrg:               os.Getenv("APP_GITHUB_ORG"),
		GitHubWebhookSecret:     githubWebhookSecret,
		GitHubBaseURL:           os.Getenv("APP_GITHUB_BASE_URL"),
		OktaDomain:              os.Getenv("APP_OKTA_DOMAIN"),
		OktaClientID:            os.Getenv("APP_OKTA_CLIENT_ID"),
		OktaBaseURL:             os.Getenv("APP_OKTA_BASE_URL"),
		OktaGitHubUserField:     oktaGitHubUserField,
		OktaSyncSafetyThreshold: oktaSyncSafetyThreshold,
		SlackToken:              slackToken,
		SlackChannel:            os.Getenv("APP_SLACK_CHANNEL"),
		SlackAPIURL:             os.Getenv("APP_SLACK_API_URL"),
	}

	if appIDStr := os.Getenv("APP_GITHUB_APP_ID"); appIDStr != "" {
		appID, err := strconv.ParseInt(appIDStr, 10, 64)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse APP_GITHUB_APP_ID '%s'", appIDStr)
		}
		cfg.GitHubAppID = appID
	}

	if privateKeyPath := os.Getenv("APP_GITHUB_APP_PRIVATE_KEY_PATH"); privateKeyPath != "" {
		privateKey, err := os.ReadFile(privateKeyPath)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read private key from %s", privateKeyPath)
		}
		cfg.GitHubAppPrivateKey = privateKey
	} else if privateKeyEnv, err := getEnv(ctx, "APP_GITHUB_APP_PRIVATE_KEY"); err != nil {
		return nil, err
	} else if privateKeyEnv != "" {
		cfg.GitHubAppPrivateKey = []byte(privateKeyEnv)
	}

	if installIDStr := os.Getenv("APP_GITHUB_INSTALLATION_ID"); installIDStr != "" {
		installID, err := strconv.ParseInt(installIDStr, 10, 64)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse APP_GITHUB_INSTALLATION_ID '%s'", installIDStr)
		}
		cfg.GitHubInstallID = installID
	}

	if privateKeyPath := os.Getenv("APP_OKTA_PRIVATE_KEY_PATH"); privateKeyPath != "" {
		privateKey, err := os.ReadFile(privateKeyPath)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read okta private key from %s", privateKeyPath)
		}
		cfg.OktaPrivateKey = privateKey
	} else if privateKeyEnv, err := getEnv(ctx, "APP_OKTA_PRIVATE_KEY"); err != nil {
		return nil, err
	} else if privateKeyEnv != "" {
		cfg.OktaPrivateKey = []byte(privateKeyEnv)
	}

	if scopesStr := os.Getenv("APP_OKTA_SCOPES"); scopesStr != "" {
		scopes := strings.Split(scopesStr, ",")
		for i := range scopes {
			scopes[i] = strings.TrimSpace(scopes[i])
		}
		cfg.OktaScopes = scopes
	} else {
		cfg.OktaScopes = okta.DefaultScopes
	}

	prComplianceEnabled, _ := strconv.ParseBool(os.Getenv("APP_PR_COMPLIANCE_ENABLED"))
	cfg.PRComplianceEnabled = prComplianceEnabled

	monitoredBranchesStr := os.Getenv("APP_PR_MONITORED_BRANCHES")
	if monitoredBranchesStr != "" {
		branches := strings.Split(monitoredBranchesStr, ",")
		for i := range branches {
			branches[i] = strings.TrimSpace(branches[i])
		}
		cfg.PRMonitoredBranches = branches
	} else {
		cfg.PRMonitoredBranches = []string{"main", "master"}
	}

	syncRulesJSON := os.Getenv("APP_OKTA_SYNC_RULES")
	if syncRulesJSON != "" {
		var rules []okta.SyncRule
		if err := json.Unmarshal([]byte(syncRulesJSON), &rules); err != nil {
			return nil, errors.Wrap(err, "failed to parse APP_OKTA_SYNC_RULES")
		}
		cfg.OktaSyncRules = rules
	}

	cfg.SlackEnabled = cfg.SlackToken != "" && cfg.SlackChannel != ""

	basePath := os.Getenv("APP_BASE_PATH")
	if basePath != "" {
		basePath = "/" + strings.Trim(basePath, "/")
	}
	cfg.BasePath = basePath

	orphanedUserNotifications, _ := strconv.ParseBool(os.Getenv("APP_OKTA_ORPHANED_USER_NOTIFICATIONS"))
	if os.Getenv("APP_OKTA_ORPHANED_USER_NOTIFICATIONS") == "" {
		orphanedUserNotifications = cfg.IsOktaSyncEnabled()
	}
	cfg.OktaOrphanedUserNotifications = orphanedUserNotifications

	return &cfg, nil
}

// NewLogger creates a new structured logger.
// uses JSON format in Lambda, text format elsewhere.
// sets log level to debug when APP_DEBUG_ENABLED is true.
func NewLogger() *slog.Logger {
	var handler slog.Handler

	debugEnabled, _ := strconv.ParseBool(os.Getenv("APP_DEBUG_ENABLED"))

	level := slog.LevelInfo
	if debugEnabled {
		level = slog.LevelDebug
	}

	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		})
	} else {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: level,
		})
	}

	return slog.New(handler)
}

// IsOktaSyncEnabled returns true if Okta sync is fully configured.
func (c *Config) IsOktaSyncEnabled() bool {
	return c.OktaDomain != "" && c.OktaClientID != "" && len(c.OktaPrivateKey) > 0 && len(c.OktaSyncRules) > 0
}

// IsPRComplianceEnabled returns true if PR compliance checking is enabled.
func (c *Config) IsPRComplianceEnabled() bool {
	return c.PRComplianceEnabled && c.IsGitHubConfigured()
}

// IsGitHubConfigured returns true if GitHub App credentials are configured.
func (c *Config) IsGitHubConfigured() bool {
	return c.GitHubOrg != "" &&
		c.GitHubAppID != 0 &&
		len(c.GitHubAppPrivateKey) > 0 &&
		c.GitHubInstallID != 0
}

// ShouldMonitorBranch returns true if the given branch should be monitored
// for PR compliance.
func (c *Config) ShouldMonitorBranch(branch string) bool {
	if !c.IsPRComplianceEnabled() {
		return false
	}
	branch = strings.TrimPrefix(branch, "refs/heads/")
	for _, monitored := range c.PRMonitoredBranches {
		if branch == monitored {
			return true
		}
	}
	return false
}

// RedactedConfig contains configuration with sensitive values redacted.
// safe for logging and API responses.
type RedactedConfig struct {
	DebugEnabled bool `json:"debug_enabled"`

	GitHubOrg           string `json:"github_org"`
	GitHubWebhookSecret string `json:"github_webhook_secret"`
	GitHubBaseURL       string `json:"github_base_url"`

	GitHubAppID         int64  `json:"github_app_id"`
	GitHubAppPrivateKey string `json:"github_app_private_key"`
	GitHubInstallID     int64  `json:"github_install_id"`

	OktaDomain              string          `json:"okta_domain"`
	OktaClientID            string          `json:"okta_client_id"`
	OktaPrivateKey          string          `json:"okta_private_key"`
	OktaScopes              []string        `json:"okta_scopes"`
	OktaBaseURL             string          `json:"okta_base_url"`
	OktaSyncRules           []okta.SyncRule `json:"okta_sync_rules"`
	OktaGitHubUserField     string          `json:"okta_github_user_field"`
	OktaSyncSafetyThreshold float64         `json:"okta_sync_safety_threshold"`

	PRComplianceEnabled bool     `json:"pr_compliance_enabled"`
	PRMonitoredBranches []string `json:"pr_monitored_branches"`

	OktaOrphanedUserNotifications bool `json:"okta_orphaned_user_notifications"`

	SlackEnabled bool   `json:"slack_enabled"`
	SlackToken   string `json:"slack_token"`
	SlackChannel string `json:"slack_channel"`
	SlackAPIURL  string `json:"slack_api_url"`

	BasePath string `json:"base_path"`
}

// Redacted returns a copy of the config with secrets redacted.
func (c *Config) Redacted() RedactedConfig {
	redact := func(s string) string {
		if s == "" {
			return ""
		}
		return "***REDACTED***"
	}

	redactBytes := func(b []byte) string {
		if len(b) == 0 {
			return ""
		}
		return "***REDACTED***"
	}

	return RedactedConfig{
		DebugEnabled:                  c.DebugEnabled,
		GitHubOrg:                     c.GitHubOrg,
		GitHubWebhookSecret:           redact(c.GitHubWebhookSecret),
		GitHubBaseURL:                 c.GitHubBaseURL,
		GitHubAppID:                   c.GitHubAppID,
		GitHubAppPrivateKey:           redactBytes(c.GitHubAppPrivateKey),
		GitHubInstallID:               c.GitHubInstallID,
		OktaDomain:                    c.OktaDomain,
		OktaClientID:                  redact(c.OktaClientID),
		OktaPrivateKey:                redactBytes(c.OktaPrivateKey),
		OktaScopes:                    c.OktaScopes,
		OktaBaseURL:                   c.OktaBaseURL,
		OktaSyncRules:                 c.OktaSyncRules,
		OktaGitHubUserField:           c.OktaGitHubUserField,
		OktaSyncSafetyThreshold:       c.OktaSyncSafetyThreshold,
		PRComplianceEnabled:           c.PRComplianceEnabled,
		PRMonitoredBranches:           c.PRMonitoredBranches,
		OktaOrphanedUserNotifications: c.OktaOrphanedUserNotifications,
		SlackEnabled:                  c.SlackEnabled,
		SlackToken:                    redact(c.SlackToken),
		SlackChannel:                  c.SlackChannel,
		SlackAPIURL:                   c.SlackAPIURL,
		BasePath:                      c.BasePath,
	}
}
