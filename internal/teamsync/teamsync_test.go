package teamsync

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/cruxstack/github-ops-app/internal/fakes"
)

var discard = slog.New(slog.DiscardHandler)

func ptr[T any](v T) *T { return &v }

func TestSync_NamedGroup(t *testing.T) {
	okta := &fakes.Okta{GetGroupInfoFn: func(_ context.Context, name string) (*domain.GroupInfo, error) {
		return &domain.GroupInfo{Name: name, Members: []string{"alice"}, SkippedNoGitHubUsername: []string{"new@x.com"}}, nil
	}}
	var gotCreate bool
	var gotPrivacy string
	gh := &fakes.GitHub{ResolveTeamFn: func(_ context.Context, name, privacy string, create bool) (string, error) {
		gotPrivacy, gotCreate = privacy, create
		return "platform-team", nil
	}}
	rules := []domain.SyncRule{{Name: "r", OktaGroupName: "Platform", GitHubTeamName: "Platform Team", CreateTeamIfMissing: true}}

	result, err := NewSyncer(okta, gh, rules, 0.5, discard).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	report := result.Reports[0]
	if report.GitHubTeam != "platform-team" || strings.Join(report.MembersAdded, ",") != "alice" {
		t.Errorf("report = %+v, want the resolved slug and alice added", report)
	}
	if !gotCreate || gotPrivacy != domain.TeamPrivacyClosed {
		t.Errorf("ResolveTeam create=%v privacy=%q, want true closed", gotCreate, gotPrivacy)
	}
	if len(report.MembersSkippedNoGHUsername) != 1 {
		t.Errorf("skipped = %v", report.MembersSkippedNoGHUsername)
	}
}

func TestSync_DisabledRuleAndNoMemberSync(t *testing.T) {
	gh := &fakes.GitHub{}
	rules := []domain.SyncRule{
		{OktaGroupName: "off", Enabled: ptr(false)},
		{OktaGroupName: "teams-only", SyncMembers: ptr(false)},
	}
	result, err := NewSyncer(&fakes.Okta{}, gh, rules, 0.5, discard).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Reports) != 1 || gh.Count("SyncTeamMembers") != 0 {
		t.Errorf("reports = %d, member syncs = %d; want 1 and 0", len(result.Reports), gh.Count("SyncTeamMembers"))
	}
}

func TestSync_MissingTeamWithoutCreateIsReported(t *testing.T) {
	gh := &fakes.GitHub{ResolveTeamFn: func(context.Context, string, string, bool) (string, error) {
		return "", domain.ErrTeamNotFound
	}}
	rules := []domain.SyncRule{{OktaGroupName: "eng"}}
	result, err := NewSyncer(&fakes.Okta{}, gh, rules, 0.5, discard).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reports[0].HasErrors() || gh.Count("SyncTeamMembers") != 0 {
		t.Errorf("report = %+v, want an error and no member sync", result.Reports[0])
	}
	if result.Complete() || result.Reports[0].TeamResolved {
		t.Error("result with a missing team is complete")
	}
}

func TestSync_PatternWithFailedGroupAndDuplicateTeam(t *testing.T) {
	okta := &fakes.Okta{GetGroupsByPatternFn: func(context.Context, string) ([]*domain.GroupInfo, error) {
		return []*domain.GroupInfo{
			{Name: "gh-eng", Members: []string{"a"}},
			{Name: "gh-ENG", Members: []string{"b"}},
			{Name: "gh-ops", Err: errors.New("okta timeout")},
			{Name: "gh-data", Members: []string{"c"}},
		}, nil
	}}
	gh := &fakes.GitHub{}
	rules := []domain.SyncRule{{OktaGroupPattern: "^gh-", StripPrefix: "gh-", GitHubTeamPrefix: "t-"}}

	result, err := NewSyncer(okta, gh, rules, 0.5, discard).Sync(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Reports) != 4 {
		t.Fatalf("reports = %d, want 4", len(result.Reports))
	}
	// the failed group's team was never resolved
	if result.Complete() {
		t.Error("result with a failed group is complete")
	}
	if got := strings.Join(result.ManagedTeams(), ","); got != "t-eng,t-ops,t-data" {
		t.Errorf("managed teams = %v, want t-eng,t-ops,t-data", got)
	}
	if got := strings.Join(result.Reports[0].Members, ","); got != "a" {
		t.Errorf("t-eng members = %q, want the synced member list", got)
	}
	if gh.Count("SyncTeamMembers") != 2 {
		t.Errorf("member syncs = %d, want 2", gh.Count("SyncTeamMembers"))
	}
	if !strings.Contains(result.Reports[1].Errors[0], "already synced") {
		t.Errorf("duplicate team error = %v", result.Reports[1].Errors)
	}
	if !strings.Contains(result.Reports[2].Errors[0], "okta timeout") {
		t.Errorf("failed group error = %v", result.Reports[2].Errors)
	}
}

func TestSync_PartialAndTotalFailure(t *testing.T) {
	okta := &fakes.Okta{GetGroupInfoFn: func(_ context.Context, name string) (*domain.GroupInfo, error) {
		if name == "bad" {
			return nil, errors.New("okta down")
		}
		return &domain.GroupInfo{Name: name}, nil
	}}

	result, err := NewSyncer(okta, &fakes.GitHub{}, []domain.SyncRule{{OktaGroupName: "bad"}, {OktaGroupName: "good"}}, 0.5, discard).Sync(context.Background())
	if err != nil {
		t.Fatalf("partial failure returned error: %v", err)
	}
	if len(result.Reports) != 2 || !result.Reports[0].HasErrors() {
		t.Errorf("reports = %+v", result.Reports)
	}

	result, err = NewSyncer(okta, &fakes.GitHub{}, []domain.SyncRule{{OktaGroupName: "bad"}}, 0.5, discard).Sync(context.Background())
	if err == nil {
		t.Fatal("total failure returned no error")
	}
	if result == nil || len(result.Reports) != 1 {
		t.Error("total failure did not return the failed reports")
	}
}

func TestDetectOrphanedUsers(t *testing.T) {
	gh := &fakes.GitHub{
		ListOrgMembersFn: func(context.Context) ([]string, error) {
			return []string{"Alice", "bob", "carol", "dave"}, nil
		},
		GetTeamMembersFn: func(_ context.Context, team string) ([]string, error) {
			if team == "broken" {
				return nil, errors.New("api down")
			}
			return []string{"carol"}, nil
		},
	}
	s := NewSyncer(&fakes.Okta{}, gh, nil, 0.5, discard)
	result := &domain.SyncResult{Reports: []*domain.SyncReport{
		// a team with sync errors still covers its members
		{GitHubTeam: "eng", TeamResolved: true, Members: []string{"alice"}, Errors: []string{"failed to add x"}},
		// a team whose members weren't synced is read
		{GitHubTeam: "ops", TeamResolved: true},
	}}

	report, err := s.DetectOrphanedUsers(context.Background(), result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(report.OrphanedUsers, ",") != "bob,dave" {
		t.Errorf("orphaned = %v, want bob,dave", report.OrphanedUsers)
	}
	if gh.Count("GetTeamMembers") != 1 {
		t.Errorf("team member reads = %d, want 1 (only the team without a member list)", gh.Count("GetTeamMembers"))
	}

	result.Reports = append(result.Reports, &domain.SyncReport{GitHubTeam: "broken", TeamResolved: true})
	if _, err := s.DetectOrphanedUsers(context.Background(), result); err == nil {
		t.Error("unreadable team did not fail orphan detection")
	}

	result.Reports = append(result.Reports[:2], &domain.SyncReport{GitHubTeam: "missing"})
	if _, err := s.DetectOrphanedUsers(context.Background(), result); err == nil {
		t.Error("incomplete result did not fail orphan detection")
	}
}

func TestTeamName(t *testing.T) {
	tests := []struct {
		group string
		rule  domain.SyncRule
		want  string
	}{
		{"Engineering", domain.SyncRule{}, "engineering"},
		{"github-eng-platform", domain.SyncRule{StripPrefix: "github-eng-", GitHubTeamPrefix: "eng-"}, "eng-platform"},
		{"Data & ML", domain.SyncRule{}, "data-ml"},
		{"anything", domain.SyncRule{GitHubTeamName: "Exact Name"}, "Exact Name"},
		{"___", domain.SyncRule{}, ""},
	}
	for _, tt := range tests {
		if got := TeamName(tt.group, tt.rule); got != tt.want {
			t.Errorf("TeamName(%q) = %q, want %q", tt.group, got, tt.want)
		}
	}
}
