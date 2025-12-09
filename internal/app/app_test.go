package app

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/github/client"
	"github.com/cruxstack/github-ops-app/internal/okta"
)

func TestHandleSlackTest_NotConfigured(t *testing.T) {
	app := &App{
		Config:   &config.Config{},
		Logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Notifier: nil,
	}

	err := app.handleSlackTest(context.Background())
	if err == nil {
		t.Error("expected error when slack is not configured")
	}

	if err.Error() != "slack is not configured" {
		t.Errorf("expected 'slack is not configured' error, got: %v", err)
	}
}

func TestFakePRComplianceResult(t *testing.T) {
	result := fakePRComplianceResult()

	if result == nil {
		t.Fatal("expected non-nil result")
	}

	if result.PR == nil {
		t.Fatal("expected non-nil PR")
	}

	if result.PR.Number == nil || *result.PR.Number != 42 {
		t.Error("expected PR number to be 42")
	}

	if result.PR.Title == nil || *result.PR.Title != "Add new authentication feature" {
		t.Error("expected PR title to be 'Add new authentication feature'")
	}

	if result.BaseBranch != "main" {
		t.Errorf("expected base branch 'main', got %q", result.BaseBranch)
	}

	if !result.UserHasBypass {
		t.Error("expected UserHasBypass to be true")
	}

	if result.UserBypassReason != "repository admin" {
		t.Errorf("expected bypass reason 'repository admin', got %q", result.UserBypassReason)
	}

	if len(result.Violations) != 2 {
		t.Errorf("expected 2 violations, got %d", len(result.Violations))
	}

	// verify it passes the WasBypassed check (used by notifier)
	if !result.WasBypassed() {
		t.Error("expected WasBypassed() to return true")
	}
}

func TestFakeOktaSyncReports(t *testing.T) {
	reports := fakeOktaSyncReports()

	if len(reports) != 3 {
		t.Fatalf("expected 3 reports, got %d", len(reports))
	}

	// first report: has changes
	if !reports[0].HasChanges() {
		t.Error("expected first report to have changes")
	}
	if len(reports[0].MembersAdded) != 2 {
		t.Errorf("expected 2 members added, got %d", len(reports[0].MembersAdded))
	}
	if len(reports[0].MembersRemoved) != 1 {
		t.Errorf("expected 1 member removed, got %d", len(reports[0].MembersRemoved))
	}

	// second report: no changes
	if reports[1].HasChanges() {
		t.Error("expected second report to have no changes")
	}

	// third report: has changes, errors, and skipped members
	if !reports[2].HasChanges() {
		t.Error("expected third report to have changes")
	}
	if !reports[2].HasErrors() {
		t.Error("expected third report to have errors")
	}
	if len(reports[2].MembersSkippedExternal) != 1 {
		t.Errorf("expected 1 skipped external member, got %d",
			len(reports[2].MembersSkippedExternal))
	}
	if len(reports[2].MembersSkippedNoGHUsername) != 1 {
		t.Errorf("expected 1 skipped member without GH username, got %d",
			len(reports[2].MembersSkippedNoGHUsername))
	}
}

func TestFakeOrphanedUsersReport(t *testing.T) {
	report := fakeOrphanedUsersReport()

	if report == nil {
		t.Fatal("expected non-nil report")
	}

	if len(report.OrphanedUsers) != 3 {
		t.Errorf("expected 3 orphaned users, got %d", len(report.OrphanedUsers))
	}

	expectedUsers := []string{"orphan-user-1", "orphan-user-2", "legacy-bot"}
	for i, user := range report.OrphanedUsers {
		if user != expectedUsers[i] {
			t.Errorf("expected user %q at index %d, got %q", expectedUsers[i], i, user)
		}
	}
}

func TestProcessScheduledEvent_SlackTest(t *testing.T) {
	app := &App{
		Config:   &config.Config{},
		Logger:   slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Notifier: nil,
	}

	evt := ScheduledEvent{Action: "slack-test"}
	err := app.ProcessScheduledEvent(context.Background(), evt)

	// should fail because slack is not configured
	if err == nil {
		t.Error("expected error when slack is not configured")
	}
}

func TestProcessScheduledEvent_UnknownAction(t *testing.T) {
	app := &App{
		Config: &config.Config{},
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
	}

	evt := ScheduledEvent{Action: "unknown-action"}
	err := app.ProcessScheduledEvent(context.Background(), evt)

	if err == nil {
		t.Error("expected error for unknown action")
	}
}

// verify fake data types match expected interfaces
func TestFakeDataTypes(t *testing.T) {
	// ensure fake PR result is compatible with notifier
	var _ *client.PRComplianceResult = fakePRComplianceResult()

	// ensure fake sync reports are compatible with notifier
	var _ []*okta.SyncReport = fakeOktaSyncReports()

	// ensure fake orphaned users report is compatible with notifier
	var _ *okta.OrphanedUsersReport = fakeOrphanedUsersReport()
}

func TestCheckAdminAuth(t *testing.T) {
	tests := []struct {
		name        string
		adminToken  string
		authHeader  string
		expectError bool
	}{
		{
			name:        "no token configured, no header",
			adminToken:  "",
			authHeader:  "",
			expectError: false,
		},
		{
			name:        "no token configured, with header",
			adminToken:  "",
			authHeader:  "Bearer some-token",
			expectError: false,
		},
		{
			name:        "token configured, no header",
			adminToken:  "secret-token",
			authHeader:  "",
			expectError: true,
		},
		{
			name:        "token configured, wrong token",
			adminToken:  "secret-token",
			authHeader:  "Bearer wrong-token",
			expectError: true,
		},
		{
			name:        "token configured, correct token",
			adminToken:  "secret-token",
			authHeader:  "Bearer secret-token",
			expectError: false,
		},
		{
			name:        "token configured, lowercase bearer",
			adminToken:  "secret-token",
			authHeader:  "bearer secret-token",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{
				Config: &config.Config{AdminToken: tt.adminToken},
				Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
			}

			headers := map[string]string{}
			if tt.authHeader != "" {
				headers["authorization"] = tt.authHeader
			}

			req := Request{Headers: headers}
			resp := app.checkAdminAuth(req)

			if tt.expectError && resp == nil {
				t.Error("expected error response, got nil")
			}
			if !tt.expectError && resp != nil {
				t.Errorf("expected no error, got status %d", resp.StatusCode)
			}
			if tt.expectError && resp != nil && resp.StatusCode != 401 {
				t.Errorf("expected status 401, got %d", resp.StatusCode)
			}
		})
	}
}

func TestHandleRequest_AdminAuthOnProtectedEndpoints(t *testing.T) {
	tests := []struct {
		name           string
		path           string
		method         string
		adminToken     string
		authHeader     string
		expectedStatus int
	}{
		{
			name:           "status endpoint, no token configured",
			path:           "/server/status",
			method:         "GET",
			adminToken:     "",
			authHeader:     "",
			expectedStatus: 200,
		},
		{
			name:           "status endpoint, token required, missing",
			path:           "/server/status",
			method:         "GET",
			adminToken:     "secret",
			authHeader:     "",
			expectedStatus: 401,
		},
		{
			name:           "status endpoint, token required, correct",
			path:           "/server/status",
			method:         "GET",
			adminToken:     "secret",
			authHeader:     "Bearer secret",
			expectedStatus: 200,
		},
		{
			name:           "config endpoint, token required, missing",
			path:           "/server/config",
			method:         "GET",
			adminToken:     "secret",
			authHeader:     "",
			expectedStatus: 401,
		},
		{
			name:           "config endpoint, token required, correct",
			path:           "/server/config",
			method:         "GET",
			adminToken:     "secret",
			authHeader:     "Bearer secret",
			expectedStatus: 200,
		},
		{
			name:           "scheduled endpoint, token required, missing",
			path:           "/scheduled/slack-test",
			method:         "POST",
			adminToken:     "secret",
			authHeader:     "",
			expectedStatus: 401,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &App{
				Config: &config.Config{AdminToken: tt.adminToken},
				Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
			}

			headers := map[string]string{}
			if tt.authHeader != "" {
				headers["authorization"] = tt.authHeader
			}

			req := Request{
				Type:    RequestTypeHTTP,
				Method:  tt.method,
				Path:    tt.path,
				Headers: headers,
			}

			resp := app.HandleRequest(context.Background(), req)

			if resp.StatusCode != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, resp.StatusCode)
			}
		})
	}
}
