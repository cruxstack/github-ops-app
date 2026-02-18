package app

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/google/go-github/v79/github"
)

// --- mock implementations ---

type mockGitHubClient struct {
	checkPRComplianceFn      func(ctx context.Context, owner, repo string, prNumber int) (*domain.PRComplianceResult, error)
	getOrCreateTeamFn        func(ctx context.Context, teamName, privacy string) (*github.Team, error)
	syncTeamMembersFn        func(ctx context.Context, teamSlug string, desiredMembers []string, threshold float64) (*domain.TeamSyncResult, error)
	getTeamMembersFn         func(ctx context.Context, teamSlug string) ([]string, error)
	listOrgMembersFn         func(ctx context.Context) ([]string, error)
	isExternalCollaboratorFn func(ctx context.Context, username string) (bool, error)
	getAppSlugFn             func(ctx context.Context) (string, error)
	listSecurityAlertsFn     func(ctx context.Context, minAgeDays int, minSeverity string) (*domain.SecurityAlertsReport, error)
}

func (m *mockGitHubClient) CheckPRCompliance(ctx context.Context, owner, repo string, prNumber int) (*domain.PRComplianceResult, error) {
	if m.checkPRComplianceFn != nil {
		return m.checkPRComplianceFn(ctx, owner, repo, prNumber)
	}
	return &domain.PRComplianceResult{}, nil
}
func (m *mockGitHubClient) GetOrCreateTeam(ctx context.Context, teamName, privacy string) (*github.Team, error) {
	if m.getOrCreateTeamFn != nil {
		return m.getOrCreateTeamFn(ctx, teamName, privacy)
	}
	slug := teamName
	return &github.Team{Slug: &slug}, nil
}
func (m *mockGitHubClient) SyncTeamMembers(ctx context.Context, teamSlug string, desiredMembers []string, threshold float64) (*domain.TeamSyncResult, error) {
	if m.syncTeamMembersFn != nil {
		return m.syncTeamMembersFn(ctx, teamSlug, desiredMembers, threshold)
	}
	return &domain.TeamSyncResult{TeamName: teamSlug, MembersAdded: desiredMembers}, nil
}
func (m *mockGitHubClient) GetTeamMembers(ctx context.Context, teamSlug string) ([]string, error) {
	if m.getTeamMembersFn != nil {
		return m.getTeamMembersFn(ctx, teamSlug)
	}
	return []string{}, nil
}
func (m *mockGitHubClient) ListOrgMembers(ctx context.Context) ([]string, error) {
	if m.listOrgMembersFn != nil {
		return m.listOrgMembersFn(ctx)
	}
	return []string{}, nil
}
func (m *mockGitHubClient) IsExternalCollaborator(ctx context.Context, username string) (bool, error) {
	if m.isExternalCollaboratorFn != nil {
		return m.isExternalCollaboratorFn(ctx, username)
	}
	return false, nil
}
func (m *mockGitHubClient) GetAppSlug(ctx context.Context) (string, error) {
	if m.getAppSlugFn != nil {
		return m.getAppSlugFn(ctx)
	}
	return "test-app", nil
}
func (m *mockGitHubClient) GetOrg() string { return "test-org" }
func (m *mockGitHubClient) ListSecurityAlerts(ctx context.Context, minAgeDays int, minSeverity string) (*domain.SecurityAlertsReport, error) {
	if m.listSecurityAlertsFn != nil {
		return m.listSecurityAlertsFn(ctx, minAgeDays, minSeverity)
	}
	return &domain.SecurityAlertsReport{AlertsByRepo: map[string][]domain.SecurityAlert{}}, nil
}

type mockOktaClient struct {
	getGroupsByPatternFn func(ctx context.Context, pattern string) ([]*domain.GroupInfo, error)
	getGroupInfoFn       func(ctx context.Context, groupName string) (*domain.GroupInfo, error)
}

func (m *mockOktaClient) GetGroupsByPattern(ctx context.Context, pattern string) ([]*domain.GroupInfo, error) {
	if m.getGroupsByPatternFn != nil {
		return m.getGroupsByPatternFn(ctx, pattern)
	}
	return []*domain.GroupInfo{}, nil
}
func (m *mockOktaClient) GetGroupInfo(ctx context.Context, groupName string) (*domain.GroupInfo, error) {
	if m.getGroupInfoFn != nil {
		return m.getGroupInfoFn(ctx, groupName)
	}
	return &domain.GroupInfo{ID: "g1", Name: groupName, Members: []string{}}, nil
}

type mockNotifier struct {
	notifyPRBypassFn       func(ctx context.Context, result *domain.PRComplianceResult, repoFullName string) error
	notifyOktaSyncFn       func(ctx context.Context, reports []*domain.SyncReport, githubOrg string) error
	notifyOrphanedUsersFn  func(ctx context.Context, report *domain.OrphanedUsersReport) error
	notifySecurityAlertsFn func(ctx context.Context, report *domain.SecurityAlertsReport, githubOrg string) error
}

func (m *mockNotifier) NotifyPRBypass(ctx context.Context, result *domain.PRComplianceResult, repoFullName string) error {
	if m.notifyPRBypassFn != nil {
		return m.notifyPRBypassFn(ctx, result, repoFullName)
	}
	return nil
}
func (m *mockNotifier) NotifyOktaSync(ctx context.Context, reports []*domain.SyncReport, githubOrg string) error {
	if m.notifyOktaSyncFn != nil {
		return m.notifyOktaSyncFn(ctx, reports, githubOrg)
	}
	return nil
}
func (m *mockNotifier) NotifyOrphanedUsers(ctx context.Context, report *domain.OrphanedUsersReport) error {
	if m.notifyOrphanedUsersFn != nil {
		return m.notifyOrphanedUsersFn(ctx, report)
	}
	return nil
}
func (m *mockNotifier) NotifySecurityAlerts(ctx context.Context, report *domain.SecurityAlertsReport, githubOrg string) error {
	if m.notifySecurityAlertsFn != nil {
		return m.notifySecurityAlertsFn(ctx, report, githubOrg)
	}
	return nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- handler tests ---

func TestHandleOktaSync_Disabled(t *testing.T) {
	a := &App{
		Config: &config.Config{},
		Logger: discardLogger(),
	}

	err := a.handleOktaSync(context.Background())
	if err != nil {
		t.Fatalf("expected nil error when sync disabled, got: %v", err)
	}
}

func TestHandleOktaSync_ClientsNil(t *testing.T) {
	a := &App{
		Config: &config.Config{
			OktaDomain:     "test.okta.com",
			OktaClientID:   "cid",
			OktaPrivateKey: []byte("key"),
			OktaSyncRules:  []domain.SyncRule{{OktaGroupName: "g1", GitHubTeamName: "t1"}},
		},
		Logger: discardLogger(),
	}

	err := a.handleOktaSync(context.Background())
	if err == nil {
		t.Fatal("expected error when clients are nil")
	}
	if !errors.Is(err, domain.ErrClientNotInit) {
		t.Errorf("expected ErrClientNotInit, got: %v", err)
	}
}

func TestHandleOktaSync_Success(t *testing.T) {
	notified := false
	a := &App{
		Config: &config.Config{
			OktaDomain:     "test.okta.com",
			OktaClientID:   "cid",
			OktaPrivateKey: []byte("key"),
			OktaSyncRules:  []domain.SyncRule{{OktaGroupName: "Engineering", GitHubTeamName: "engineering"}},
			GitHubOrg:      "test-org",
		},
		Logger:       discardLogger(),
		GitHubClient: &mockGitHubClient{},
		OktaClient: &mockOktaClient{
			getGroupInfoFn: func(_ context.Context, name string) (*domain.GroupInfo, error) {
				return &domain.GroupInfo{ID: "g1", Name: name, Members: []string{"alice"}}, nil
			},
		},
		Notifier: &mockNotifier{
			notifyOktaSyncFn: func(_ context.Context, reports []*domain.SyncReport, org string) error {
				notified = true
				if len(reports) != 1 {
					t.Errorf("expected 1 report, got %d", len(reports))
				}
				return nil
			},
		},
	}

	err := a.handleOktaSync(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !notified {
		t.Error("expected slack notification to be sent")
	}
}

func TestHandleOktaSync_NotifierFailureDoesNotFail(t *testing.T) {
	a := &App{
		Config: &config.Config{
			OktaDomain:     "test.okta.com",
			OktaClientID:   "cid",
			OktaPrivateKey: []byte("key"),
			OktaSyncRules:  []domain.SyncRule{{OktaGroupName: "Eng", GitHubTeamName: "eng"}},
			GitHubOrg:      "org",
		},
		Logger:       discardLogger(),
		GitHubClient: &mockGitHubClient{},
		OktaClient:   &mockOktaClient{},
		Notifier: &mockNotifier{
			notifyOktaSyncFn: func(_ context.Context, _ []*domain.SyncReport, _ string) error {
				return errors.New("slack api error")
			},
		},
	}

	err := a.handleOktaSync(context.Background())
	if err != nil {
		t.Fatalf("notifier failure should not propagate, got: %v", err)
	}
}

func TestHandlePullRequestWebhook_NotMerged(t *testing.T) {
	prNumber := 10
	baseBranch := "main"
	payload := map[string]any{
		"action": "opened",
		"number": prNumber,
		"pull_request": map[string]any{
			"number": prNumber,
			"base":   map[string]any{"ref": baseBranch},
		},
		"repository": map[string]any{
			"name":      "repo",
			"full_name": "org/repo",
			"owner":     map[string]any{"login": "org"},
		},
	}
	data, _ := json.Marshal(payload)

	a := &App{
		Config: &config.Config{PRComplianceEnabled: true, PRMonitoredBranches: []string{"main"},
			GitHubOrg: "org", GitHubAppID: 1, GitHubAppPrivateKey: []byte("k"), GitHubInstallationID: 1},
		Logger:       discardLogger(),
		GitHubClient: &mockGitHubClient{},
	}

	err := a.handlePullRequestWebhook(context.Background(), data)
	if err != nil {
		t.Fatalf("non-merged PR should not error, got: %v", err)
	}
}

func TestHandlePullRequestWebhook_UnmonitoredBranch(t *testing.T) {
	merged := true
	prNumber := 10
	payload := map[string]any{
		"action": "closed",
		"number": prNumber,
		"pull_request": map[string]any{
			"number": prNumber,
			"merged": merged,
			"base":   map[string]any{"ref": "develop"},
		},
		"repository": map[string]any{
			"name":      "repo",
			"full_name": "org/repo",
			"owner":     map[string]any{"login": "org"},
		},
	}
	data, _ := json.Marshal(payload)

	a := &App{
		Config: &config.Config{PRComplianceEnabled: true, PRMonitoredBranches: []string{"main"},
			GitHubOrg: "org", GitHubAppID: 1, GitHubAppPrivateKey: []byte("k"), GitHubInstallationID: 1},
		Logger:       discardLogger(),
		GitHubClient: &mockGitHubClient{},
	}

	err := a.handlePullRequestWebhook(context.Background(), data)
	if err != nil {
		t.Fatalf("unmonitored branch should not error, got: %v", err)
	}
}

func TestHandlePullRequestWebhook_ComplianceCheck(t *testing.T) {
	merged := true
	prNumber := 42
	checked := false
	payload := map[string]any{
		"action": "closed",
		"number": prNumber,
		"pull_request": map[string]any{
			"number": prNumber,
			"merged": merged,
			"base":   map[string]any{"ref": "main"},
		},
		"repository": map[string]any{
			"name":      "repo",
			"full_name": "org/repo",
			"owner":     map[string]any{"login": "org"},
		},
	}
	data, _ := json.Marshal(payload)

	a := &App{
		Config: &config.Config{
			PRComplianceEnabled: true, PRMonitoredBranches: []string{"main"},
			GitHubOrg: "org", GitHubAppID: 1, GitHubAppPrivateKey: []byte("k"), GitHubInstallationID: 1,
		},
		Logger: discardLogger(),
		GitHubClient: &mockGitHubClient{
			checkPRComplianceFn: func(_ context.Context, owner, repo string, num int) (*domain.PRComplianceResult, error) {
				checked = true
				if num != prNumber {
					t.Errorf("expected pr %d, got %d", prNumber, num)
				}
				return &domain.PRComplianceResult{
					Violations: []domain.ComplianceViolation{},
				}, nil
			},
		},
	}

	err := a.handlePullRequestWebhook(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !checked {
		t.Error("expected compliance check to be called")
	}
}

func TestHandlePullRequestWebhook_BypassNotification(t *testing.T) {
	merged := true
	prNumber := 42
	notified := false
	prURL := "https://github.com/org/repo/pull/42"
	payload := map[string]any{
		"action": "closed",
		"number": prNumber,
		"pull_request": map[string]any{
			"number":    prNumber,
			"merged":    merged,
			"base":      map[string]any{"ref": "main"},
			"html_url":  prURL,
			"merged_by": map[string]any{"login": "admin-user"},
		},
		"repository": map[string]any{
			"name":      "repo",
			"full_name": "org/repo",
			"owner":     map[string]any{"login": "org"},
		},
	}
	data, _ := json.Marshal(payload)

	pr := &github.PullRequest{}
	a := &App{
		Config: &config.Config{
			PRComplianceEnabled: true, PRMonitoredBranches: []string{"main"},
			GitHubOrg: "org", GitHubAppID: 1, GitHubAppPrivateKey: []byte("k"), GitHubInstallationID: 1,
		},
		Logger: discardLogger(),
		GitHubClient: &mockGitHubClient{
			checkPRComplianceFn: func(_ context.Context, _, _ string, _ int) (*domain.PRComplianceResult, error) {
				return &domain.PRComplianceResult{
					PR:            pr,
					UserHasBypass: true,
					Violations:    []domain.ComplianceViolation{{Type: "test", Description: "test"}},
				}, nil
			},
		},
		Notifier: &mockNotifier{
			notifyPRBypassFn: func(_ context.Context, result *domain.PRComplianceResult, repoName string) error {
				notified = true
				if repoName != "org/repo" {
					t.Errorf("expected repo org/repo, got %s", repoName)
				}
				return nil
			},
		},
	}

	err := a.handlePullRequestWebhook(context.Background(), data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !notified {
		t.Error("expected bypass notification to be sent")
	}
}

func TestHandleTeamWebhook_SyncDisabled(t *testing.T) {
	payload := map[string]any{
		"action": "edited",
		"team":   map[string]any{"slug": "engineering"},
		"sender": map[string]any{"login": "user1", "type": "User"},
	}
	data, _ := json.Marshal(payload)

	a := &App{
		Config: &config.Config{}, // no okta sync configured
		Logger: discardLogger(),
	}

	err := a.handleTeamWebhook(context.Background(), data)
	if err != nil {
		t.Fatalf("expected nil when sync disabled, got: %v", err)
	}
}

func TestHandleTeamWebhook_IgnoresBotSender(t *testing.T) {
	payload := map[string]any{
		"action": "edited",
		"team":   map[string]any{"slug": "engineering"},
		"sender": map[string]any{"login": "dependabot", "type": "Bot"},
	}
	data, _ := json.Marshal(payload)

	a := &App{
		Config: &config.Config{
			OktaDomain: "test.okta.com", OktaClientID: "cid",
			OktaPrivateKey: []byte("key"),
			OktaSyncRules:  []domain.SyncRule{{OktaGroupName: "g", GitHubTeamName: "t"}},
		},
		Logger:       discardLogger(),
		GitHubClient: &mockGitHubClient{},
		OktaClient:   &mockOktaClient{},
	}

	err := a.handleTeamWebhook(context.Background(), data)
	if err != nil {
		t.Fatalf("bot sender should be ignored, got: %v", err)
	}
}

func TestHandleMembershipWebhook_NonTeamScope(t *testing.T) {
	payload := map[string]any{
		"action": "added",
		"scope":  "organization",
		"member": map[string]any{"login": "user1"},
		"team":   map[string]any{"slug": "eng"},
		"sender": map[string]any{"login": "admin", "type": "User"},
	}
	data, _ := json.Marshal(payload)

	a := &App{
		Config: &config.Config{},
		Logger: discardLogger(),
	}

	err := a.handleMembershipWebhook(context.Background(), data)
	if err != nil {
		t.Fatalf("non-team scope should be skipped, got: %v", err)
	}
}

func TestShouldIgnoreWebhookChange_BotSender(t *testing.T) {
	a := &App{
		Config: &config.Config{},
		Logger: discardLogger(),
	}

	event := &stubSender{senderType: "Bot", senderLogin: "some-bot"}
	if !a.shouldIgnoreWebhookChange(context.Background(), event) {
		t.Error("expected Bot sender to be ignored")
	}
}

func TestShouldIgnoreWebhookChange_AppSlugMatch(t *testing.T) {
	a := &App{
		Config: &config.Config{},
		Logger: discardLogger(),
		GitHubClient: &mockGitHubClient{
			getAppSlugFn: func(_ context.Context) (string, error) {
				return "my-app", nil
			},
		},
	}

	event := &stubSender{senderType: "User", senderLogin: "my-app[bot]"}
	if !a.shouldIgnoreWebhookChange(context.Background(), event) {
		t.Error("expected app slug match to be ignored")
	}
}

func TestShouldIgnoreWebhookChange_HumanUser(t *testing.T) {
	a := &App{
		Config: &config.Config{},
		Logger: discardLogger(),
		GitHubClient: &mockGitHubClient{
			getAppSlugFn: func(_ context.Context) (string, error) {
				return "my-app", nil
			},
		},
	}

	event := &stubSender{senderType: "User", senderLogin: "human-user"}
	if a.shouldIgnoreWebhookChange(context.Background(), event) {
		t.Error("expected human user NOT to be ignored")
	}
}

func TestProcessWebhook_UnknownEventType(t *testing.T) {
	a := &App{
		Config: &config.Config{},
		Logger: discardLogger(),
	}

	err := a.processWebhook(context.Background(), []byte(`{}`), "deployment")
	if err == nil {
		t.Fatal("expected error for unknown event type")
	}
	if !errors.Is(err, domain.ErrInvalidEventType) {
		t.Errorf("expected ErrInvalidEventType, got: %v", err)
	}
}

func TestWriteErrorFromDomain(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"auth error direct", domain.ErrInvalidSignature, 401},
		{"auth error wrapped", errors.Wrap(domain.ErrInvalidSignature, "wrapped"), 401},
		{"validation error", domain.ErrMissingPRData, 400},
		{"validation error wrapped", errors.Wrap(domain.ErrInvalidEventType, "wrapped"), 400},
		{"config error", domain.ErrClientNotInit, 503},
		{"api error", errors.Wrap(domain.ErrTeamNotFound, "wrapped"), 502},
		{"generic error", errors.New("something"), 500},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeErrorFromDomain(rec, tt.err, "fallback")
			if rec.Code != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, rec.Code)
			}
		})
	}
}

func TestRouter_NotFound(t *testing.T) {
	a := &App{
		Config: &config.Config{},
		Logger: discardLogger(),
	}

	router := a.Handler()
	req := httptest.NewRequest(http.MethodGet, "/nonexistent", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestRouter_MethodNotAllowed(t *testing.T) {
	a := &App{
		Config: &config.Config{},
		Logger: discardLogger(),
	}

	router := a.Handler()
	req := httptest.NewRequest(http.MethodDelete, "/webhooks", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}

func TestRouter_BasePathStripping(t *testing.T) {
	a := &App{
		Config: &config.Config{BasePath: "/api/v1"},
		Logger: discardLogger(),
	}

	router := a.Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/server/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for base-path-stripped status, got %d (body: %s)",
			rec.Code, rec.Body.String())
	}
}

func TestHandleSecurityAlerts_Disabled(t *testing.T) {
	a := &App{
		Config: &config.Config{},
		Logger: discardLogger(),
	}

	err := a.handleSecurityAlerts(context.Background())
	if err != nil {
		t.Fatalf("expected nil when disabled, got: %v", err)
	}
}

func TestHandleSecurityAlerts_ClientNil(t *testing.T) {
	a := &App{
		Config: &config.Config{
			SecurityAlertsEnabled: true,
			GitHubOrg:             "org",
			GitHubAppID:           1,
			GitHubAppPrivateKey:   []byte("k"),
			GitHubInstallationID:  1,
		},
		Logger: discardLogger(),
	}

	err := a.handleSecurityAlerts(context.Background())
	if err == nil {
		t.Fatal("expected error when client is nil")
	}
	if !errors.Is(err, domain.ErrClientNotInit) {
		t.Errorf("expected ErrClientNotInit, got: %v", err)
	}
}

func TestHandleSecurityAlerts_Success(t *testing.T) {
	notified := false
	a := &App{
		Config: &config.Config{
			SecurityAlertsEnabled:     true,
			SecurityAlertsMinAgeDays:  30,
			SecurityAlertsMinSeverity: "high",
			GitHubOrg:                 "org",
			GitHubAppID:               1,
			GitHubAppPrivateKey:       []byte("k"),
			GitHubInstallationID:      1,
		},
		Logger: discardLogger(),
		GitHubClient: &mockGitHubClient{
			listSecurityAlertsFn: func(_ context.Context, minAge int, minSev string) (*domain.SecurityAlertsReport, error) {
				if minAge != 30 {
					t.Errorf("expected minAge 30, got %d", minAge)
				}
				if minSev != "high" {
					t.Errorf("expected minSev high, got %s", minSev)
				}
				return &domain.SecurityAlertsReport{
					TotalAlerts: 2,
					AlertsByRepo: map[string][]domain.SecurityAlert{
						"org/repo": {{Type: "dependabot", Severity: "critical"}},
					},
				}, nil
			},
		},
		Notifier: &mockNotifier{
			notifySecurityAlertsFn: func(_ context.Context, report *domain.SecurityAlertsReport, org string) error {
				notified = true
				if report.TotalAlerts != 2 {
					t.Errorf("expected 2 alerts, got %d", report.TotalAlerts)
				}
				if org != "org" {
					t.Errorf("expected org, got %s", org)
				}
				return nil
			},
		},
	}

	err := a.handleSecurityAlerts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !notified {
		t.Error("expected notification to be sent")
	}
}

func TestHandleSecurityAlerts_NoAlertsNoNotification(t *testing.T) {
	notified := false
	a := &App{
		Config: &config.Config{
			SecurityAlertsEnabled:     true,
			SecurityAlertsMinAgeDays:  30,
			SecurityAlertsMinSeverity: "high",
			GitHubOrg:                 "org",
			GitHubAppID:               1,
			GitHubAppPrivateKey:       []byte("k"),
			GitHubInstallationID:      1,
		},
		Logger: discardLogger(),
		GitHubClient: &mockGitHubClient{
			listSecurityAlertsFn: func(_ context.Context, _ int, _ string) (*domain.SecurityAlertsReport, error) {
				return &domain.SecurityAlertsReport{
					TotalAlerts:  0,
					AlertsByRepo: map[string][]domain.SecurityAlert{},
				}, nil
			},
		},
		Notifier: &mockNotifier{
			notifySecurityAlertsFn: func(_ context.Context, _ *domain.SecurityAlertsReport, _ string) error {
				notified = true
				return nil
			},
		},
	}

	err := a.handleSecurityAlerts(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notified {
		t.Error("should not notify when no alerts")
	}
}

func TestHandleSecurityAlerts_NotifierFailureDoesNotFail(t *testing.T) {
	a := &App{
		Config: &config.Config{
			SecurityAlertsEnabled:     true,
			SecurityAlertsMinAgeDays:  30,
			SecurityAlertsMinSeverity: "high",
			GitHubOrg:                 "org",
			GitHubAppID:               1,
			GitHubAppPrivateKey:       []byte("k"),
			GitHubInstallationID:      1,
		},
		Logger: discardLogger(),
		GitHubClient: &mockGitHubClient{
			listSecurityAlertsFn: func(_ context.Context, _ int, _ string) (*domain.SecurityAlertsReport, error) {
				return &domain.SecurityAlertsReport{
					TotalAlerts: 1,
					AlertsByRepo: map[string][]domain.SecurityAlert{
						"org/repo": {{Type: "dependabot"}},
					},
				}, nil
			},
		},
		Notifier: &mockNotifier{
			notifySecurityAlertsFn: func(_ context.Context, _ *domain.SecurityAlertsReport, _ string) error {
				return errors.New("slack api error")
			},
		},
	}

	err := a.handleSecurityAlerts(context.Background())
	if err != nil {
		t.Fatalf("notifier failure should not propagate, got: %v", err)
	}
}

func TestProcessScheduledEvent_SecurityAlerts(t *testing.T) {
	a := &App{
		Config: &config.Config{},
		Logger: discardLogger(),
	}

	err := a.processScheduledEvent(context.Background(), ScheduledEvent{Action: "security-alerts"})
	if err != nil {
		t.Fatalf("expected nil when feature disabled, got: %v", err)
	}
}

// stubSender is a simple webhookSender for testing shouldIgnoreWebhookChange
type stubSender struct {
	senderType  string
	senderLogin string
}

func (s *stubSender) GetSenderType() string  { return s.senderType }
func (s *stubSender) GetSenderLogin() string { return s.senderLogin }
