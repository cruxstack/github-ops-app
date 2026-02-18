package sync

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/google/go-github/v79/github"
)

// mockGitHubClient implements domain.GitHubClient with overridable function
// fields for testing.
type mockGitHubClient struct {
	getOrCreateTeamFn        func(ctx context.Context, teamName, privacy string) (*github.Team, error)
	syncTeamMembersFn        func(ctx context.Context, teamSlug string, desiredMembers []string, threshold float64) (*domain.TeamSyncResult, error)
	getTeamMembersFn         func(ctx context.Context, teamSlug string) ([]string, error)
	listOrgMembersFn         func(ctx context.Context) ([]string, error)
	isExternalCollaboratorFn func(ctx context.Context, username string) (bool, error)
	getAppSlugFn             func(ctx context.Context) (string, error)
	checkPRComplianceFn      func(ctx context.Context, owner, repo string, prNumber int) (*domain.PRComplianceResult, error)
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
	return &domain.TeamSyncResult{
		TeamName:     teamSlug,
		MembersAdded: desiredMembers,
	}, nil
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

func (m *mockGitHubClient) ListSecurityAlerts(ctx context.Context, minAgeDays int, minSeverity string) (*domain.SecurityAlertsReport, error) {
	return &domain.SecurityAlertsReport{AlertsByRepo: map[string][]domain.SecurityAlert{}}, nil
}

func (m *mockGitHubClient) GetOrg() string {
	return "test-org"
}

// mockOktaClient implements domain.OktaClient with overridable function
// fields for testing.
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
	return &domain.GroupInfo{
		ID:      "group-1",
		Name:    groupName,
		Members: []string{},
	}, nil
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestSync_SingleRule(t *testing.T) {
	oktaClient := &mockOktaClient{
		getGroupInfoFn: func(_ context.Context, name string) (*domain.GroupInfo, error) {
			return &domain.GroupInfo{
				ID:      "g1",
				Name:    "Engineering",
				Members: []string{"alice", "bob"},
			}, nil
		},
	}

	ghClient := &mockGitHubClient{
		syncTeamMembersFn: func(_ context.Context, teamSlug string, members []string, _ float64) (*domain.TeamSyncResult, error) {
			return &domain.TeamSyncResult{
				TeamName:     teamSlug,
				MembersAdded: members,
			}, nil
		},
	}

	rules := []domain.SyncRule{
		{
			OktaGroupName:  "Engineering",
			GitHubTeamName: "engineering",
		},
	}

	syncer := NewSyncer(oktaClient, ghClient, rules, 0.5, testLogger())
	result, err := syncer.Sync(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Reports) != 1 {
		t.Fatalf("expected 1 report, got %d", len(result.Reports))
	}

	report := result.Reports[0]
	if report.GitHubTeam != "engineering" {
		t.Errorf("expected team 'engineering', got %q", report.GitHubTeam)
	}
	if len(report.MembersAdded) != 2 {
		t.Errorf("expected 2 members added, got %d", len(report.MembersAdded))
	}
}

func TestSync_DisabledRule(t *testing.T) {
	disabled := false
	rules := []domain.SyncRule{
		{
			Enabled:        &disabled,
			OktaGroupName:  "Engineering",
			GitHubTeamName: "engineering",
		},
	}

	syncer := NewSyncer(&mockOktaClient{}, &mockGitHubClient{}, rules, 0.5, testLogger())
	result, err := syncer.Sync(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Reports) != 0 {
		t.Errorf("expected 0 reports for disabled rule, got %d", len(result.Reports))
	}
}

func TestSync_OktaError_PartialSuccess(t *testing.T) {
	callCount := 0
	oktaClient := &mockOktaClient{
		getGroupInfoFn: func(_ context.Context, name string) (*domain.GroupInfo, error) {
			callCount++
			if callCount == 1 {
				return nil, errors.New("okta api error")
			}
			return &domain.GroupInfo{ID: "g2", Name: name, Members: []string{"charlie"}}, nil
		},
	}

	rules := []domain.SyncRule{
		{OktaGroupName: "BadGroup", GitHubTeamName: "bad-team"},
		{OktaGroupName: "GoodGroup", GitHubTeamName: "good-team"},
	}

	syncer := NewSyncer(oktaClient, &mockGitHubClient{}, rules, 0.5, testLogger())
	result, err := syncer.Sync(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Reports) != 2 {
		t.Fatalf("expected 2 reports, got %d", len(result.Reports))
	}

	// first report should have errors
	if !result.Reports[0].HasErrors() {
		t.Error("expected first report to have errors")
	}
	// second report should succeed
	if result.Reports[1].HasErrors() {
		t.Errorf("expected second report to have no errors, got: %v", result.Reports[1].Errors)
	}
}

func TestSync_AllRulesFail(t *testing.T) {
	oktaClient := &mockOktaClient{
		getGroupInfoFn: func(_ context.Context, name string) (*domain.GroupInfo, error) {
			return nil, errors.New("api failure")
		},
	}

	rules := []domain.SyncRule{
		{OktaGroupName: "Group1", GitHubTeamName: "team1"},
	}

	syncer := NewSyncer(oktaClient, &mockGitHubClient{}, rules, 0.5, testLogger())
	_, err := syncer.Sync(context.Background())
	if err == nil {
		t.Fatal("expected error when all rules fail")
	}
}

func TestSync_PatternMatching(t *testing.T) {
	oktaClient := &mockOktaClient{
		getGroupsByPatternFn: func(_ context.Context, pattern string) ([]*domain.GroupInfo, error) {
			return []*domain.GroupInfo{
				{ID: "g1", Name: "eng-frontend", Members: []string{"alice"}},
				{ID: "g2", Name: "eng-backend", Members: []string{"bob"}},
			}, nil
		},
	}

	rules := []domain.SyncRule{
		{OktaGroupPattern: "^eng-.*$"},
	}

	syncer := NewSyncer(oktaClient, &mockGitHubClient{}, rules, 0.5, testLogger())
	result, err := syncer.Sync(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Reports) != 2 {
		t.Fatalf("expected 2 reports for pattern match, got %d", len(result.Reports))
	}
}

func TestSync_SkippedUsersTracked(t *testing.T) {
	oktaClient := &mockOktaClient{
		getGroupInfoFn: func(_ context.Context, name string) (*domain.GroupInfo, error) {
			return &domain.GroupInfo{
				ID:                      "g1",
				Name:                    name,
				Members:                 []string{"alice"},
				SkippedNoGitHubUsername: []string{"new-hire@example.com"},
			}, nil
		},
	}

	rules := []domain.SyncRule{
		{OktaGroupName: "Engineering", GitHubTeamName: "engineering"},
	}

	syncer := NewSyncer(oktaClient, &mockGitHubClient{}, rules, 0.5, testLogger())
	result, err := syncer.Sync(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(result.Reports[0].MembersSkippedNoGHUsername) != 1 {
		t.Errorf("expected 1 skipped user, got %d", len(result.Reports[0].MembersSkippedNoGHUsername))
	}
}

func TestDetectOrphanedUsers(t *testing.T) {
	ghClient := &mockGitHubClient{
		listOrgMembersFn: func(_ context.Context) ([]string, error) {
			return []string{"alice", "bob", "charlie"}, nil
		},
		getTeamMembersFn: func(_ context.Context, teamSlug string) ([]string, error) {
			return []string{"alice", "bob"}, nil
		},
		isExternalCollaboratorFn: func(_ context.Context, username string) (bool, error) {
			return false, nil
		},
	}

	syncer := NewSyncer(&mockOktaClient{}, ghClient, nil, 0.5, testLogger())
	report, err := syncer.DetectOrphanedUsers(context.Background(), []string{"engineering"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.OrphanedUsers) != 1 {
		t.Fatalf("expected 1 orphaned user, got %d", len(report.OrphanedUsers))
	}
	if report.OrphanedUsers[0] != "charlie" {
		t.Errorf("expected orphaned user 'charlie', got %q", report.OrphanedUsers[0])
	}
}

func TestDetectOrphanedUsers_ExternalCollaboratorExcluded(t *testing.T) {
	ghClient := &mockGitHubClient{
		listOrgMembersFn: func(_ context.Context) ([]string, error) {
			return []string{"alice", "external-user"}, nil
		},
		getTeamMembersFn: func(_ context.Context, _ string) ([]string, error) {
			return []string{"alice"}, nil
		},
		isExternalCollaboratorFn: func(_ context.Context, username string) (bool, error) {
			return username == "external-user", nil
		},
	}

	syncer := NewSyncer(&mockOktaClient{}, ghClient, nil, 0.5, testLogger())
	report, err := syncer.DetectOrphanedUsers(context.Background(), []string{"engineering"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(report.OrphanedUsers) != 0 {
		t.Errorf("expected 0 orphaned users (external excluded), got %d", len(report.OrphanedUsers))
	}
}

func TestComputeTeamName(t *testing.T) {
	tests := []struct {
		name      string
		groupName string
		rule      domain.SyncRule
		want      string
	}{
		{
			name:      "explicit team name",
			groupName: "Engineering",
			rule:      domain.SyncRule{GitHubTeamName: "custom-team"},
			want:      "custom-team",
		},
		{
			name:      "lowercase and normalize",
			groupName: "My Team Name",
			rule:      domain.SyncRule{},
			want:      "my-team-name",
		},
		{
			name:      "strip prefix",
			groupName: "DEPT-Engineering",
			rule:      domain.SyncRule{StripPrefix: "DEPT-"},
			want:      "engineering",
		},
		{
			name:      "add prefix",
			groupName: "frontend",
			rule:      domain.SyncRule{GitHubTeamPrefix: "eng-"},
			want:      "eng-frontend",
		},
		{
			name:      "strip and add prefix",
			groupName: "OKTA-backend",
			rule:      domain.SyncRule{StripPrefix: "OKTA-", GitHubTeamPrefix: "gh-"},
			want:      "gh-backend",
		},
		{
			name:      "special characters replaced and dashes collapsed",
			groupName: "Team (US) & EU",
			rule:      domain.SyncRule{},
			want:      "team-us-eu",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeTeamName(tt.groupName, tt.rule)
			if got != tt.want {
				t.Errorf("computeTeamName(%q) = %q, want %q", tt.groupName, got, tt.want)
			}
		})
	}
}
