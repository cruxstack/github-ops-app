package config

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPEM = "-----BEGIN RSA PRIVATE KEY-----\nMIIB\n-----END RSA PRIVATE KEY-----"

// baseEnv is the smallest valid configuration.
func baseEnv() map[string]string {
	return map[string]string{
		"APP_GITHUB_ORG":             "acme",
		"APP_GITHUB_APP_ID":          "123",
		"APP_GITHUB_APP_PRIVATE_KEY": testPEM,
		"APP_GITHUB_WEBHOOK_SECRET":  "whsec",
	}
}

func envOf(m map[string]string) Env {
	return Env{Lookup: func(k string) (string, bool) {
		v, ok := m[k]
		return v, ok
	}}
}

func load(t *testing.T, m map[string]string) (*Config, error) {
	t.Helper()
	return Load(context.Background(), envOf(m))
}

func with(overrides map[string]string) map[string]string {
	m := baseEnv()
	for k, v := range overrides {
		if v == "<unset>" {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	return m
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := load(t, baseEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != slog.LevelInfo || cfg.Server.ListenAddr != ":8080" || cfg.Server.BasePath != "" {
		t.Errorf("bootstrap = %+v", cfg.Bootstrap)
	}
	if cfg.GitHub.WebURL != "https://github.com" || cfg.GitHub.InstallationID != 0 || string(cfg.GitHub.PrivateKey) != testPEM {
		t.Errorf("github = %+v", cfg.GitHub)
	}
	if cfg.PRCompliance.Enabled || strings.Join(cfg.PRCompliance.MonitoredBranches, ",") != "main,master" {
		t.Errorf("pr compliance = %+v", cfg.PRCompliance)
	}
	if cfg.Okta.Enabled || cfg.Slack.Enabled || cfg.SecurityAlerts.Enabled || cfg.Installer.Enabled {
		t.Error("optional feature enabled by default")
	}
	if cfg.SecurityAlerts.MinAgeDays != 30 || cfg.SecurityAlerts.MinSeverity != "high" {
		t.Errorf("security alerts = %+v", cfg.SecurityAlerts)
	}
}

func TestLoad_AllFeatures(t *testing.T) {
	rules := `[{"name":"eng","okta_group_pattern":"^eng-","github_team_prefix":"eng-","create_team_if_missing":true,"team_privacy":"secret"}]`
	cfg, err := load(t, with(map[string]string{
		"APP_LOG_LEVEL":                        "debug",
		"APP_LISTEN_ADDR":                      "127.0.0.1:9000",
		"APP_BASE_PATH":                        "prod/",
		"APP_PUBLIC_URL":                       "https://ops.example.com/prod/",
		"APP_ADMIN_TOKEN":                      "0123456789abcdef",
		"APP_GITHUB_INSTALLATION_ID":           "456",
		"APP_GITHUB_BASE_URL":                  "https://ghe.example.com/api/v3/",
		"APP_PR_COMPLIANCE_ENABLED":            "true",
		"APP_PR_MONITORED_BRANCHES":            "main, release/* ,",
		"APP_OKTA_DOMAIN":                      "acme.okta.com",
		"APP_OKTA_CLIENT_ID":                   "client",
		"APP_OKTA_PRIVATE_KEY":                 strings.ReplaceAll(testPEM, "\n", `\n`),
		"APP_OKTA_SYNC_RULES":                  rules,
		"APP_OKTA_SYNC_SAFETY_THRESHOLD":       "0.25",
		"APP_OKTA_ORPHANED_USER_NOTIFICATIONS": "false",
		"APP_SECURITY_ALERTS_ENABLED":          "1",
		"APP_SECURITY_ALERTS_MIN_AGE_DAYS":     "7",
		"APP_SECURITY_ALERTS_MIN_SEVERITY":     "CRITICAL",
		"APP_SLACK_TOKEN":                      "xoxb-1",
		"APP_SLACK_CHANNEL":                    "C1",
		"APP_SLACK_API_URL":                    "https://slack.test/api",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.Server.BasePath != "/prod" || cfg.Server.PublicURL != "https://ops.example.com/prod" {
		t.Errorf("bootstrap = %+v", cfg.Bootstrap)
	}
	if cfg.GitHub.InstallationID != 456 || cfg.GitHub.WebURL != "https://ghe.example.com" {
		t.Errorf("github = %+v", cfg.GitHub)
	}
	if !cfg.Okta.Enabled || cfg.Okta.SyncSafetyThreshold != 0.25 || cfg.Okta.OrphanedUserNotifications ||
		cfg.Okta.GitHubUserField != "githubUsername" || string(cfg.Okta.PrivateKey) != testPEM {
		t.Errorf("okta = %+v", cfg.Okta)
	}
	if len(cfg.Okta.SyncRules) != 1 || !cfg.Okta.SyncRules[0].CreateTeamIfMissing {
		t.Errorf("rules = %+v", cfg.Okta.SyncRules)
	}
	if cfg.SecurityAlerts.MinAgeDays != 7 || cfg.SecurityAlerts.MinSeverity != "critical" {
		t.Errorf("security alerts = %+v", cfg.SecurityAlerts)
	}
	if !cfg.Slack.Enabled || cfg.Slack.APIURL != "https://slack.test/api/" {
		t.Errorf("slack = %+v", cfg.Slack)
	}
	for branch, want := range map[string]bool{"main": true, "refs/heads/release/1.2": true, "release/1/2": false, "dev": false} {
		if got := cfg.ShouldMonitorBranch(branch); got != want {
			t.Errorf("ShouldMonitorBranch(%q) = %v, want %v", branch, got, want)
		}
	}
}

func TestLoad_Errors(t *testing.T) {
	okta := map[string]string{
		"APP_OKTA_DOMAIN":      "acme.okta.com",
		"APP_OKTA_CLIENT_ID":   "client",
		"APP_OKTA_PRIVATE_KEY": testPEM,
		"APP_OKTA_SYNC_RULES":  `[{"okta_group_name":"eng"}]`,
	}
	withOkta := func(overrides map[string]string) map[string]string {
		m := with(okta)
		for k, v := range overrides {
			if v == "<unset>" {
				delete(m, k)
			} else {
				m[k] = v
			}
		}
		return m
	}

	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"missing org", with(map[string]string{"APP_GITHUB_ORG": "<unset>"}), "APP_GITHUB_ORG is required"},
		{"bad org", with(map[string]string{"APP_GITHUB_ORG": "acme/x"}), "not a valid organization"},
		{"empty app id", with(map[string]string{"APP_GITHUB_APP_ID": ""}), "APP_GITHUB_APP_ID is required"},
		{"bad app id", with(map[string]string{"APP_GITHUB_APP_ID": "abc"}), "not a positive integer"},
		{"missing key", with(map[string]string{"APP_GITHUB_APP_PRIVATE_KEY": "<unset>"}), "APP_GITHUB_APP_PRIVATE_KEY or"},
		{"key and path", with(map[string]string{"APP_GITHUB_APP_PRIVATE_KEY_PATH": "/k.pem"}), "mutually exclusive"},
		{"missing webhook secret", with(map[string]string{"APP_GITHUB_WEBHOOK_SECRET": "<unset>"}), "APP_GITHUB_WEBHOOK_SECRET is required"},
		{"bad base url", with(map[string]string{"APP_GITHUB_BASE_URL": "ghe.example.com"}), "not an http(s) url"},
		{"short admin token", with(map[string]string{"APP_ADMIN_TOKEN": "short"}), "at least 16"},
		{"bad bool", with(map[string]string{"APP_PR_COMPLIANCE_ENABLED": "yes please"}), "not a boolean"},
		{"bad branch glob", with(map[string]string{"APP_PR_MONITORED_BRANCHES": "release/["}), "invalid pattern"},
		{"bad log level", with(map[string]string{"APP_LOG_LEVEL": "loud"}), "APP_LOG_LEVEL"},
		{"bad listen addr", with(map[string]string{"APP_LISTEN_ADDR": "8080"}), "APP_LISTEN_ADDR"},
		{"bad base path", with(map[string]string{"APP_BASE_PATH": "/a b"}), "APP_BASE_PATH"},
		{"removed debug var", with(map[string]string{"APP_DEBUG_ENABLED": "true"}), "APP_LOG_LEVEL=debug"},
		{"removed port var", with(map[string]string{"APP_PORT": "8080"}), "APP_LISTEN_ADDR"},
		{"removed okta key var", with(map[string]string{"APP_OKTA_KEY_ID": "kid"}), "APP_OKTA_PRIVATE_KEY_ID"},
		{"partial okta", with(map[string]string{"APP_OKTA_DOMAIN": "acme.okta.com"}), "APP_OKTA_CLIENT_ID is required"},
		{"okta domain with scheme", withOkta(map[string]string{"APP_OKTA_DOMAIN": "https://acme.okta.com"}), "must be a host name"},
		{"okta rules missing", withOkta(map[string]string{"APP_OKTA_SYNC_RULES": "<unset>"}), "APP_OKTA_SYNC_RULES is required"},
		{"okta rules not json", withOkta(map[string]string{"APP_OKTA_SYNC_RULES": "{"}), "APP_OKTA_SYNC_RULES"},
		{"okta rules empty", withOkta(map[string]string{"APP_OKTA_SYNC_RULES": "[]"}), "no rules"},
		{"okta rule typo", withOkta(map[string]string{"APP_OKTA_SYNC_RULES": `[{"okta_group":"eng"}]`}), "unknown field"},
		{"okta rule invalid", withOkta(map[string]string{"APP_OKTA_SYNC_RULES": `[{"okta_group_pattern":"("}]`}), "rule 0"},
		{"okta threshold", withOkta(map[string]string{"APP_OKTA_SYNC_SAFETY_THRESHOLD": "1.5"}), "from 0 to 1"},
		{"alert age", with(map[string]string{"APP_SECURITY_ALERTS_MIN_AGE_DAYS": "-1"}), "non-negative"},
		{"alert severity", with(map[string]string{"APP_SECURITY_ALERTS_MIN_SEVERITY": "urgent"}), "not one of"},
		{"slack token only", with(map[string]string{"APP_SLACK_TOKEN": "xoxb"}), "APP_SLACK_CHANNEL is required"},
		{"slack channel only", with(map[string]string{"APP_SLACK_CHANNEL": "C1"}), "APP_SLACK_TOKEN is required"},
		{"installer files mode", with(map[string]string{"GITHUB_APP_INSTALLER_ENABLED": "true", "STORAGE_MODE": "files"}), "not supported"},
		{"installer ssm without prefix", with(map[string]string{"GITHUB_APP_INSTALLER_ENABLED": "true", "STORAGE_MODE": "aws-ssm"}), "AWS_SSM_PARAMETER_PREFIX"},
		{"installer envfile in lambda", with(map[string]string{"GITHUB_APP_INSTALLER_ENABLED": "true", "AWS_LAMBDA_FUNCTION_NAME": "fn"}), "does not persist in lambda"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.env)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoad_ReportsEveryProblem(t *testing.T) {
	_, err := load(t, map[string]string{"APP_LOG_LEVEL": "loud"})
	if err == nil {
		t.Fatal("empty configuration loaded")
	}
	for _, want := range []string{"APP_LOG_LEVEL", "APP_GITHUB_ORG", "APP_GITHUB_APP_ID", "APP_GITHUB_WEBHOOK_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %s: %v", want, err)
		}
	}
}

func TestLoad_ResolvesSSMParameters(t *testing.T) {
	arn := "arn:aws-us-gov:ssm:us-gov-west-1:123456789012:parameter/ops/webhook"
	m := with(map[string]string{"APP_GITHUB_WEBHOOK_SECRET": arn, "APP_GITHUB_APP_ID": "arn:aws:ssm:us-east-1:1:parameter/ops/app-id"})
	env := envOf(m)
	env.ResolveSSM = func(_ context.Context, got string) (string, error) {
		switch got {
		case arn:
			return "from-ssm", nil
		case m["APP_GITHUB_APP_ID"]:
			return "999", nil
		}
		return "", errors.New("unexpected arn " + got)
	}

	cfg, err := Load(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHub.WebhookSecret != "from-ssm" || cfg.GitHub.AppID != 999 {
		t.Errorf("github = %+v", cfg.GitHub)
	}

	env.ResolveSSM = func(context.Context, string) (string, error) { return "", errors.New("access denied") }
	if _, err := Load(context.Background(), env); err == nil || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("error = %v, want the ssm failure", err)
	}
}

func TestLoad_PrivateKeyPath(t *testing.T) {
	file := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(file, []byte(testPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := load(t, with(map[string]string{"APP_GITHUB_APP_PRIVATE_KEY": "<unset>", "APP_GITHUB_APP_PRIVATE_KEY_PATH": file}))
	if err != nil {
		t.Fatal(err)
	}
	if string(cfg.GitHub.PrivateKey) != testPEM {
		t.Errorf("private key = %q", cfg.GitHub.PrivateKey)
	}

	_, err = load(t, with(map[string]string{"APP_GITHUB_APP_PRIVATE_KEY": "<unset>", "APP_GITHUB_APP_PRIVATE_KEY_PATH": file + ".missing"}))
	if err == nil {
		t.Error("missing key file loaded")
	}
}

func TestLoadBootstrap_IgnoresAppSettings(t *testing.T) {
	env := map[string]string{
		"GITHUB_APP_INSTALLER_ENABLED": "yes",
		"STORAGE_DIR":                  "/data/.env",
		"APP_GITHUB_ORG":               "acme",
		"APP_GITHUB_BASE_URL":          "https://ghe.example.com/api/v3",
		"APP_GITHUB_APP_ID":            "not checked here",
	}
	b, err := LoadBootstrap(envOf(env))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Installer.Enabled || b.Installer.StorageMode != "envfile" || b.Installer.StorageDir != "/data/.env" ||
		b.Installer.GitHubOrg != "acme" || b.Installer.GitHubURL != "https://ghe.example.com" {
		t.Errorf("installer = %+v", b.Installer)
	}

	delete(env, "APP_GITHUB_ORG")
	if _, err := LoadBootstrap(envOf(env)); err == nil || !strings.Contains(err.Error(), "APP_GITHUB_ORG") {
		t.Errorf("error = %v, want the installer to require the org", err)
	}
}

func TestLoadBootstrap_ResolvesSSMParameters(t *testing.T) {
	env := envOf(map[string]string{
		"GITHUB_APP_INSTALLER_ENABLED": "true",
		"APP_GITHUB_ORG":               "arn:aws:ssm:us-east-1:123456789012:parameter/ops/org",
	})
	env.ResolveSSM = func(context.Context, string) (string, error) { return "acme", nil }
	b, err := LoadBootstrap(env)
	if err != nil {
		t.Fatal(err)
	}
	if b.Installer.GitHubOrg != "acme" {
		t.Errorf("installer org = %q, want the parameter's value", b.Installer.GitHubOrg)
	}
}

func TestWebURLFromAPI(t *testing.T) {
	for api, want := range map[string]string{
		"https://ghe.example.com/api/v3":  "https://ghe.example.com",
		"https://ghe.example.com/api/v3/": "https://ghe.example.com",
		"https://api.github.com":          "https://github.com",
		"https://127.0.0.1:8443":          "https://127.0.0.1:8443",
	} {
		if got := webURLFromAPI(api); got != want {
			t.Errorf("webURLFromAPI(%q) = %q, want %q", api, got, want)
		}
	}
}

func TestRedacted(t *testing.T) {
	cfg, err := load(t, with(map[string]string{"APP_ADMIN_TOKEN": "0123456789abcdef", "APP_SLACK_TOKEN": "xoxb-secret", "APP_SLACK_CHANNEL": "C1"}))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cfg.Redacted())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"0123456789abcdef", "xoxb-secret", "whsec", "BEGIN RSA"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("redacted config leaks %q", secret)
		}
	}
	if !strings.Contains(string(raw), `"github_org":"acme"`) {
		t.Errorf("redacted config lacks non-secret values: %s", raw)
	}
}
