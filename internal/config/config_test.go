package config

import (
	"context"
	"testing"
)

func TestSecurityAlertsConfigDefaults(t *testing.T) {
	t.Setenv("APP_SECURITY_ALERTS_ENABLED", "")
	t.Setenv("APP_SECURITY_ALERTS_MIN_AGE_DAYS", "")
	t.Setenv("APP_SECURITY_ALERTS_MIN_SEVERITY", "")
	t.Setenv("APP_SLACK_CHANNEL_SECURITY_ALERTS", "")
	// clear keys that could interfere
	t.Setenv("APP_GITHUB_APP_ID", "")
	t.Setenv("APP_GITHUB_WEBHOOK_SECRET", "")
	t.Setenv("APP_SLACK_TOKEN", "")
	t.Setenv("APP_ADMIN_TOKEN", "")
	t.Setenv("APP_OKTA_SYNC_RULES", "")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.SecurityAlertsEnabled {
		t.Error("expected security alerts disabled by default")
	}
	if cfg.SecurityAlertsMinAgeDays != 30 {
		t.Errorf("expected default min age 30, got %d",
			cfg.SecurityAlertsMinAgeDays)
	}
	if cfg.SecurityAlertsMinSeverity != "high" {
		t.Errorf("expected default severity high, got %q",
			cfg.SecurityAlertsMinSeverity)
	}
}

func TestSecurityAlertsConfigParsing(t *testing.T) {
	t.Setenv("APP_SECURITY_ALERTS_ENABLED", "true")
	t.Setenv("APP_SECURITY_ALERTS_MIN_AGE_DAYS", "14")
	t.Setenv("APP_SECURITY_ALERTS_MIN_SEVERITY", "critical")
	t.Setenv("APP_SLACK_CHANNEL_SECURITY_ALERTS", "C_SEC_ALERTS")
	// clear keys that could interfere
	t.Setenv("APP_GITHUB_APP_ID", "")
	t.Setenv("APP_GITHUB_WEBHOOK_SECRET", "")
	t.Setenv("APP_SLACK_TOKEN", "")
	t.Setenv("APP_ADMIN_TOKEN", "")
	t.Setenv("APP_OKTA_SYNC_RULES", "")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !cfg.SecurityAlertsEnabled {
		t.Error("expected security alerts enabled")
	}
	if cfg.SecurityAlertsMinAgeDays != 14 {
		t.Errorf("expected min age 14, got %d",
			cfg.SecurityAlertsMinAgeDays)
	}
	if cfg.SecurityAlertsMinSeverity != "critical" {
		t.Errorf("expected severity critical, got %q",
			cfg.SecurityAlertsMinSeverity)
	}
	if cfg.SlackChannelSecurityAlerts != "C_SEC_ALERTS" {
		t.Errorf("expected channel C_SEC_ALERTS, got %q",
			cfg.SlackChannelSecurityAlerts)
	}
}

func TestSecurityAlertsConfigInvalidSeverity(t *testing.T) {
	t.Setenv("APP_SECURITY_ALERTS_MIN_SEVERITY", "INVALID")
	t.Setenv("APP_SECURITY_ALERTS_ENABLED", "")
	t.Setenv("APP_SECURITY_ALERTS_MIN_AGE_DAYS", "")
	t.Setenv("APP_GITHUB_APP_ID", "")
	t.Setenv("APP_GITHUB_WEBHOOK_SECRET", "")
	t.Setenv("APP_SLACK_TOKEN", "")
	t.Setenv("APP_ADMIN_TOKEN", "")
	t.Setenv("APP_OKTA_SYNC_RULES", "")

	cfg, err := NewConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.SecurityAlertsMinSeverity != "high" {
		t.Errorf("expected default high for invalid severity, got %q",
			cfg.SecurityAlertsMinSeverity)
	}
}

func TestResolveEnvValue(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		key       string
		value     string
		wantSSM   bool
		wantError bool
	}{
		{
			name:      "empty value",
			key:       "TEST_KEY",
			value:     "",
			wantSSM:   false,
			wantError: false,
		},
		{
			name:      "plain text value",
			key:       "TEST_KEY",
			value:     "plain-text-secret",
			wantSSM:   false,
			wantError: false,
		},
		{
			name:      "valid ssm arn",
			key:       "TEST_KEY",
			value:     "arn:aws:ssm:us-east-1:123456789012:parameter/test/param",
			wantSSM:   true,
			wantError: true, // will error in test env without AWS creds
		},
		{
			name:      "invalid ssm arn missing parameter prefix",
			key:       "TEST_KEY",
			value:     "arn:aws:ssm:us-east-1:123456789012:test/param",
			wantSSM:   true,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := resolveEnvValue(ctx, tt.key, tt.value)

			if tt.wantError && err == nil {
				t.Errorf("expected error but got none")
			}

			if !tt.wantError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if !tt.wantSSM && err == nil && result != tt.value {
				t.Errorf("expected result %q, got %q", tt.value, result)
			}
		})
	}
}
