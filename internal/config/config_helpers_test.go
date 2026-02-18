package config

import (
	"testing"
)

func TestIsGitHubConfigured(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "fully configured",
			cfg: Config{
				GitHubOrg: "org", GitHubAppID: 1,
				GitHubAppPrivateKey: []byte("key"), GitHubInstallationID: 1,
			},
			want: true,
		},
		{name: "empty", cfg: Config{}, want: false},
		{
			name: "missing org",
			cfg:  Config{GitHubAppID: 1, GitHubAppPrivateKey: []byte("k"), GitHubInstallationID: 1},
			want: false,
		},
		{
			name: "missing private key",
			cfg:  Config{GitHubOrg: "org", GitHubAppID: 1, GitHubInstallationID: 1},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsGitHubConfigured(); got != tt.want {
				t.Errorf("IsGitHubConfigured() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsOktaSyncEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "empty", cfg: Config{}, want: false},
		{
			name: "missing rules",
			cfg:  Config{OktaDomain: "d", OktaClientID: "c", OktaPrivateKey: []byte("k")},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsOktaSyncEnabled(); got != tt.want {
				t.Errorf("IsOktaSyncEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsPRComplianceEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "disabled flag", cfg: Config{PRComplianceEnabled: false}, want: false},
		{
			name: "enabled but no github",
			cfg:  Config{PRComplianceEnabled: true},
			want: false,
		},
		{
			name: "enabled with github",
			cfg: Config{
				PRComplianceEnabled: true,
				GitHubOrg:           "org", GitHubAppID: 1,
				GitHubAppPrivateKey: []byte("k"), GitHubInstallationID: 1,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsPRComplianceEnabled(); got != tt.want {
				t.Errorf("IsPRComplianceEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldMonitorBranch(t *testing.T) {
	cfg := Config{
		PRComplianceEnabled:  true,
		PRMonitoredBranches:  []string{"main", "master"},
		GitHubOrg:            "org",
		GitHubAppID:          1,
		GitHubAppPrivateKey:  []byte("k"),
		GitHubInstallationID: 1,
	}

	tests := []struct {
		branch string
		want   bool
	}{
		{"main", true},
		{"master", true},
		{"develop", false},
		{"refs/heads/main", true},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.branch, func(t *testing.T) {
			if got := cfg.ShouldMonitorBranch(tt.branch); got != tt.want {
				t.Errorf("ShouldMonitorBranch(%q) = %v, want %v", tt.branch, got, tt.want)
			}
		})
	}
}

func TestIsSecurityAlertsEnabled(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
		want bool
	}{
		{name: "disabled flag", cfg: Config{SecurityAlertsEnabled: false}, want: false},
		{
			name: "enabled but no github",
			cfg:  Config{SecurityAlertsEnabled: true},
			want: false,
		},
		{
			name: "enabled with github",
			cfg: Config{
				SecurityAlertsEnabled: true,
				GitHubOrg:             "org", GitHubAppID: 1,
				GitHubAppPrivateKey: []byte("k"), GitHubInstallationID: 1,
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cfg.IsSecurityAlertsEnabled(); got != tt.want {
				t.Errorf("IsSecurityAlertsEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRedacted(t *testing.T) {
	cfg := Config{
		GitHubOrg:           "my-org",
		GitHubAppPrivateKey: []byte("secret-key"),
		GitHubWebhookSecret: "webhook-secret",
		AdminToken:          "admin-secret",
		SlackToken:          "xoxb-token",
		OktaClientID:        "client-id",
		OktaPrivateKey:      []byte("okta-key"),
		DebugEnabled:        true,
	}

	redacted := cfg.Redacted()

	// non-secrets should be preserved
	if redacted.GitHubOrg != "my-org" {
		t.Errorf("expected org to be preserved, got %q", redacted.GitHubOrg)
	}
	if !redacted.DebugEnabled {
		t.Error("expected DebugEnabled to be preserved")
	}

	// secrets should be redacted
	if redacted.GitHubAppPrivateKey != "***REDACTED***" {
		t.Errorf("expected private key to be redacted, got %q", redacted.GitHubAppPrivateKey)
	}
	if redacted.GitHubWebhookSecret != "***REDACTED***" {
		t.Errorf("expected webhook secret to be redacted, got %q", redacted.GitHubWebhookSecret)
	}
	if redacted.AdminToken != "***REDACTED***" {
		t.Errorf("expected admin token to be redacted, got %q", redacted.AdminToken)
	}
	if redacted.SlackToken != "***REDACTED***" {
		t.Errorf("expected slack token to be redacted, got %q", redacted.SlackToken)
	}
	if redacted.OktaClientID != "***REDACTED***" {
		t.Errorf("expected okta client id to be redacted, got %q", redacted.OktaClientID)
	}
}

func TestRedacted_EmptyValues(t *testing.T) {
	cfg := Config{}
	redacted := cfg.Redacted()

	if redacted.GitHubAppPrivateKey != "" {
		t.Error("expected empty private key to remain empty")
	}
	if redacted.SlackToken != "" {
		t.Error("expected empty slack token to remain empty")
	}
}

func TestNewConfigWithContext_Defaults(t *testing.T) {
	// clear all env vars that NewConfig reads
	envKeys := []string{
		"APP_DEBUG_ENABLED", "APP_GITHUB_ORG", "APP_GITHUB_APP_ID",
		"APP_GITHUB_APP_PRIVATE_KEY", "APP_GITHUB_APP_PRIVATE_KEY_PATH",
		"APP_GITHUB_INSTALLATION_ID", "APP_GITHUB_WEBHOOK_SECRET",
		"APP_GITHUB_BASE_URL", "APP_PR_COMPLIANCE_ENABLED",
		"APP_PR_MONITORED_BRANCHES", "APP_OKTA_DOMAIN", "APP_OKTA_CLIENT_ID",
		"APP_OKTA_PRIVATE_KEY", "APP_OKTA_PRIVATE_KEY_PATH",
		"APP_OKTA_PRIVATE_KEY_ID", "APP_OKTA_SCOPES",
		"APP_OKTA_BASE_URL", "APP_OKTA_GITHUB_USER_FIELD",
		"APP_OKTA_SYNC_RULES", "APP_OKTA_SYNC_SAFETY_THRESHOLD",
		"APP_OKTA_ORPHANED_USER_NOTIFICATIONS", "APP_SLACK_TOKEN",
		"APP_SLACK_CHANNEL", "APP_SLACK_API_URL", "APP_BASE_PATH",
		"APP_ADMIN_TOKEN",
	}
	for _, key := range envKeys {
		t.Setenv(key, "")
	}

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.OktaGitHubUserField != "githubUsername" {
		t.Errorf("expected default github user field, got %q", cfg.OktaGitHubUserField)
	}
	if cfg.OktaSyncSafetyThreshold != 0.5 {
		t.Errorf("expected default safety threshold 0.5, got %f", cfg.OktaSyncSafetyThreshold)
	}
	if len(cfg.PRMonitoredBranches) != 2 || cfg.PRMonitoredBranches[0] != "main" {
		t.Errorf("expected default monitored branches [main, master], got %v", cfg.PRMonitoredBranches)
	}
	if len(cfg.OktaScopes) != 2 {
		t.Errorf("expected default okta scopes, got %v", cfg.OktaScopes)
	}
}

func TestNewConfigWithContext_ParsesEnvVars(t *testing.T) {
	t.Setenv("APP_DEBUG_ENABLED", "true")
	t.Setenv("APP_GITHUB_ORG", "test-org")
	t.Setenv("APP_GITHUB_APP_ID", "12345")
	t.Setenv("APP_GITHUB_APP_PRIVATE_KEY", "test-key-data")
	t.Setenv("APP_GITHUB_INSTALLATION_ID", "67890")
	t.Setenv("APP_GITHUB_WEBHOOK_SECRET", "whsec")
	t.Setenv("APP_PR_COMPLIANCE_ENABLED", "true")
	t.Setenv("APP_PR_MONITORED_BRANCHES", "main,release")
	t.Setenv("APP_OKTA_SYNC_SAFETY_THRESHOLD", "0.3")
	t.Setenv("APP_BASE_PATH", "/api/v1")
	t.Setenv("APP_OKTA_SYNC_RULES", `[{"okta_group_name":"Eng","github_team_name":"eng"}]`)
	// clear keys that could interfere
	t.Setenv("APP_GITHUB_APP_PRIVATE_KEY_PATH", "")
	t.Setenv("APP_OKTA_PRIVATE_KEY_PATH", "")
	t.Setenv("APP_OKTA_PRIVATE_KEY", "")
	t.Setenv("APP_SLACK_TOKEN", "")
	t.Setenv("APP_SLACK_CHANNEL", "")
	t.Setenv("APP_ADMIN_TOKEN", "")
	t.Setenv("APP_OKTA_DOMAIN", "")
	t.Setenv("APP_OKTA_CLIENT_ID", "")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.DebugEnabled {
		t.Error("expected debug enabled")
	}
	if cfg.GitHubOrg != "test-org" {
		t.Errorf("expected org test-org, got %q", cfg.GitHubOrg)
	}
	if cfg.GitHubAppID != 12345 {
		t.Errorf("expected app id 12345, got %d", cfg.GitHubAppID)
	}
	if string(cfg.GitHubAppPrivateKey) != "test-key-data" {
		t.Errorf("expected key data, got %q", string(cfg.GitHubAppPrivateKey))
	}
	if cfg.GitHubInstallationID != 67890 {
		t.Errorf("expected installation 67890, got %d", cfg.GitHubInstallationID)
	}
	if cfg.OktaSyncSafetyThreshold != 0.3 {
		t.Errorf("expected threshold 0.3, got %f", cfg.OktaSyncSafetyThreshold)
	}
	if cfg.BasePath != "/api/v1" {
		t.Errorf("expected base path /api/v1, got %q", cfg.BasePath)
	}
	if len(cfg.PRMonitoredBranches) != 2 || cfg.PRMonitoredBranches[1] != "release" {
		t.Errorf("expected [main, release], got %v", cfg.PRMonitoredBranches)
	}
	if len(cfg.OktaSyncRules) != 1 {
		t.Errorf("expected 1 sync rule, got %d", len(cfg.OktaSyncRules))
	}
}

func TestNewConfigWithContext_InvalidAppID(t *testing.T) {
	t.Setenv("APP_GITHUB_APP_ID", "not-a-number")
	t.Setenv("APP_GITHUB_WEBHOOK_SECRET", "")
	t.Setenv("APP_SLACK_TOKEN", "")
	t.Setenv("APP_ADMIN_TOKEN", "")

	_, err := NewConfig()
	if err == nil {
		t.Fatal("expected error for invalid app id")
	}
}

func TestNewConfigWithContext_InvalidSyncRulesJSON(t *testing.T) {
	t.Setenv("APP_OKTA_SYNC_RULES", "not-json")
	t.Setenv("APP_GITHUB_APP_ID", "")
	t.Setenv("APP_GITHUB_WEBHOOK_SECRET", "")
	t.Setenv("APP_SLACK_TOKEN", "")
	t.Setenv("APP_ADMIN_TOKEN", "")

	_, err := NewConfig()
	if err == nil {
		t.Fatal("expected error for invalid sync rules JSON")
	}
}
