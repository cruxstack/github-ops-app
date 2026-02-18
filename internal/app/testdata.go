package app

import (
	"time"

	"github.com/cruxstack/github-ops-app/internal/domain"
	gh "github.com/google/go-github/v79/github"
)

// fakePRComplianceResult returns sample PR compliance data for testing.
func fakePRComplianceResult() *domain.PRComplianceResult {
	prNumber := 42
	prTitle := "Add new authentication feature"
	prURL := "https://github.com/acme-corp/demo-repo/pull/42"
	mergedByLogin := "test-user"

	return &domain.PRComplianceResult{
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
		Violations: []domain.ComplianceViolation{
			{Type: "insufficient_reviews", Description: "required 2 approving reviews, had 0"},
			{Type: "missing_status_check", Description: "required check 'ci/build' did not pass"},
		},
	}
}

// fakeOktaSyncReports returns sample Okta sync reports for testing.
func fakeOktaSyncReports() []*domain.SyncReport {
	return []*domain.SyncReport{
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
func fakeOrphanedUsersReport() *domain.OrphanedUsersReport {
	return &domain.OrphanedUsersReport{
		OrphanedUsers: []string{"orphan-user-1", "orphan-user-2", "legacy-bot"},
	}
}

// fakeSecurityAlertsReport returns sample security alerts data for
// testing.
func fakeSecurityAlertsReport() *domain.SecurityAlertsReport {
	now := time.Now()
	return &domain.SecurityAlertsReport{
		MinAgeDays:  30,
		MinSeverity: "high",
		TotalAlerts: 5,
		AlertsByRepo: map[string][]domain.SecurityAlert{
			"acme-corp/api-service": {
				{
					Type:      "dependabot",
					Repo:      "acme-corp/api-service",
					Number:    12,
					Severity:  "critical",
					Summary:   "Remote code execution in example-lib",
					HTMLURL:   "https://github.com/acme-corp/api-service/security/dependabot/12",
					CreatedAt: now.AddDate(0, 0, -45),
					AgeDays:   45,
				},
				{
					Type:      "code_scanning",
					Repo:      "acme-corp/api-service",
					Number:    7,
					Severity:  "high",
					Summary:   "SQL injection vulnerability",
					HTMLURL:   "https://github.com/acme-corp/api-service/security/code-scanning/7",
					CreatedAt: now.AddDate(0, 0, -60),
					AgeDays:   60,
				},
			},
			"acme-corp/web-app": {
				{
					Type:      "secret_scanning",
					Repo:      "acme-corp/web-app",
					Number:    3,
					Severity:  "high",
					Summary:   "GitHub Personal Access Token",
					HTMLURL:   "https://github.com/acme-corp/web-app/security/secret-scanning/3",
					CreatedAt: now.AddDate(0, 0, -90),
					AgeDays:   90,
				},
				{
					Type:      "dependabot",
					Repo:      "acme-corp/web-app",
					Number:    25,
					Severity:  "high",
					Summary:   "Prototype pollution in lodash",
					HTMLURL:   "https://github.com/acme-corp/web-app/security/dependabot/25",
					CreatedAt: now.AddDate(0, 0, -35),
					AgeDays:   35,
				},
				{
					Type:      "code_scanning",
					Repo:      "acme-corp/web-app",
					Number:    14,
					Severity:  "critical",
					Summary:   "Cross-site scripting vulnerability",
					HTMLURL:   "https://github.com/acme-corp/web-app/security/code-scanning/14",
					CreatedAt: now.AddDate(0, 0, -42),
					AgeDays:   42,
				},
			},
		},
	}
}
