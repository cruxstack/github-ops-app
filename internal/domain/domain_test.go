package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSyncRuleValidate(t *testing.T) {
	tests := []struct {
		name    string
		rule    SyncRule
		wantErr string
	}{
		{"group name", SyncRule{OktaGroupName: "eng"}, ""},
		{"pattern", SyncRule{OktaGroupPattern: "^eng-", TeamPrivacy: "secret"}, ""},
		{"neither", SyncRule{}, "one of okta_group_pattern or okta_group_name"},
		{"both", SyncRule{OktaGroupName: "a", OktaGroupPattern: "b"}, "mutually exclusive"},
		{"bad pattern", SyncRule{OktaGroupPattern: "("}, "invalid okta_group_pattern"},
		{"pattern with team name", SyncRule{OktaGroupPattern: ".*", GitHubTeamName: "all"}, "cannot be combined"},
		{"bad privacy", SyncRule{OktaGroupName: "a", TeamPrivacy: "public"}, "invalid team_privacy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rule.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestSyncRuleDefaults(t *testing.T) {
	r := SyncRule{OktaGroupPattern: "^x"}
	if !r.IsEnabled() || !r.ShouldSyncMembers() || r.Privacy() != TeamPrivacyClosed || r.GetName() != "^x" {
		t.Errorf("defaults = %v %v %q %q", r.IsEnabled(), r.ShouldSyncMembers(), r.Privacy(), r.GetName())
	}
}

func TestIsValidGitHubLogin(t *testing.T) {
	valid := []string{"octocat", "a", "octo-cat", "user_acme", "Old-Account-", strings.Repeat("a", 63)}
	invalid := []string{"", "-lead", "has space", "a/b", "../x", "a?b", "a#b", "a%2f", "a.b", strings.Repeat("a", 64)}
	for _, s := range valid {
		if !IsValidGitHubLogin(s) {
			t.Errorf("%q rejected", s)
		}
	}
	for _, s := range invalid {
		if IsValidGitHubLogin(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestSeverity(t *testing.T) {
	if !MeetsSeverity("Critical", "high") || MeetsSeverity("medium", "high") || MeetsSeverity("", "low") {
		t.Error("MeetsSeverity ordering is wrong")
	}
	if !IsSeverity("LOW") || IsSeverity("warning") {
		t.Error("IsSeverity is wrong")
	}
	if got := HighestSeverity([]SecurityAlert{{Severity: "low"}, {Severity: "HIGH"}}); got != "high" {
		t.Errorf("HighestSeverity = %q", got)
	}
	if got := HighestSeverity(nil); got != "unknown" {
		t.Errorf("HighestSeverity(nil) = %q", got)
	}
}

func TestErrorKinds(t *testing.T) {
	err := fmt.Errorf("context: %w", ErrInvalidSignature)
	if !errors.Is(err, ErrInvalidSignature) || !errors.Is(err, ErrAuth) || errors.Is(err, ErrValidation) {
		t.Error("error kinds do not match through wrapping")
	}
}

func TestSyncResult(t *testing.T) {
	r := &SyncResult{Reports: []*SyncReport{
		{GitHubTeam: "a", TeamResolved: true},
		{GitHubTeam: "Team B", TeamResolved: true, Errors: []string{"x"}},
		{GitHubTeam: "a", TeamResolved: true},
		{GitHubTeam: ""},
	}}
	if got := strings.Join(r.ManagedTeams(), ","); got != "a,team-b" {
		t.Errorf("ManagedTeams = %q, want a,team-b", got)
	}
	if r.Complete() {
		t.Error("result with an unresolved team is complete")
	}
	r.Reports = r.Reports[:3]
	if !r.Complete() {
		t.Error("result with every team resolved is incomplete")
	}
}

func TestTeamSlug(t *testing.T) {
	tests := map[string]string{
		"Platform Team":  "platform-team",
		"ops & infra":    "ops-infra",
		"--eng--":        "eng",
		"snake_case":     "snake_case",
		"  spaced  out ": "spaced-out",
		"already-a-slug": "already-a-slug",
	}
	for name, want := range tests {
		if got := TeamSlug(name); got != want {
			t.Errorf("TeamSlug(%q) = %q, want %q", name, got, want)
		}
	}
}
