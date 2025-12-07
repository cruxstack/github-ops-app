package app

import (
	"github.com/cruxstack/github-ops-app/internal/github/client"
	"github.com/cruxstack/github-ops-app/internal/okta"
	gh "github.com/google/go-github/v79/github"
)

// fakePRComplianceResult returns sample PR compliance data for testing.
func fakePRComplianceResult() *client.PRComplianceResult {
	prNumber := 42
	prTitle := "Add new authentication feature"
	prURL := "https://github.com/acme-corp/demo-repo/pull/42"
	mergedByLogin := "test-user"

	return &client.PRComplianceResult{
		PR: &gh.PullRequest{
			Number:  &prNumber,
			Title:   &prTitle,
			HTMLURL: &prURL,
			MergedBy: &gh.User{
				Login: &mergedByLogin,
			},
		},
		BaseBranch:       "main",
		UserHasBypass:    true,
		UserBypassReason: "repository admin",
		Violations: []client.ComplianceViolation{
			{Type: "insufficient_reviews", Description: "required 2 approving reviews, had 0"},
			{Type: "missing_status_check", Description: "required check 'ci/build' did not pass"},
		},
	}
}

// fakeOktaSyncReports returns sample Okta sync reports for testing.
func fakeOktaSyncReports() []*okta.SyncReport {
	return []*okta.SyncReport{
		{
			Rule:           "engineering-team",
			OktaGroup:      "Engineering",
			GitHubTeam:     "engineering",
			MembersAdded:   []string{"alice", "bob"},
			MembersRemoved: []string{"charlie"},
		},
		{
			Rule:       "platform-team",
			OktaGroup:  "Platform",
			GitHubTeam: "platform",
			// no changes
		},
		{
			Rule:                       "security-team",
			OktaGroup:                  "Security",
			GitHubTeam:                 "security",
			MembersAdded:               []string{"dave"},
			MembersSkippedExternal:     []string{"external-contractor"},
			MembersSkippedNoGHUsername: []string{"new-hire@example.com"},
			Errors:                     []string{"failed to fetch group members: rate limited"},
		},
	}
}

// fakeOrphanedUsersReport returns sample orphaned users data for testing.
func fakeOrphanedUsersReport() *okta.OrphanedUsersReport {
	return &okta.OrphanedUsersReport{
		OrphanedUsers: []string{"orphan-user-1", "orphan-user-2", "legacy-bot"},
	}
}
