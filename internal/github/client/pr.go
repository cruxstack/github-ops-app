package client

import (
	"context"
	"fmt"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/google/go-github/v79/github"
)

// CheckPRCompliance verifies if a merged PR met branch protection
// requirements. checks review requirements, status checks, and user bypass
// permissions.
func (c *Client) CheckPRCompliance(ctx context.Context, owner, repo string, prNumber int) (*domain.PRComplianceResult, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}

	pr, _, err := c.client.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to fetch pr #%d from %s/%s", prNumber, owner, repo)
	}

	if pr == nil {
		return nil, errors.Wrapf(domain.ErrMissingPRData, "pr #%d returned nil", prNumber)
	}

	if pr.Base == nil || pr.Base.Ref == nil {
		return nil, errors.Wrapf(domain.ErrMissingPRData, "pr #%d missing base branch", prNumber)
	}

	baseBranch := *pr.Base.Ref

	result := &domain.PRComplianceResult{
		PR:         pr,
		BaseBranch: baseBranch,
		Violations: []domain.ComplianceViolation{},
	}

	// fetch legacy branch protection rules
	protection, _, err := c.client.Repositories.GetBranchProtection(ctx, owner, repo, baseBranch)
	if err == nil {
		result.Protection = protection
	}

	// fetch repository rulesets for the branch
	branchRules, _, err := c.client.Repositories.GetRulesForBranch(ctx, owner, repo, baseBranch, nil)
	if err == nil {
		result.BranchRules = branchRules
	}

	if err := c.checkReviewRequirements(ctx, owner, repo, pr, result); err != nil {
		return nil, errors.Wrapf(err, "failed to check review requirements for pr #%d", prNumber)
	}

	if err := c.checkStatusRequirements(ctx, owner, repo, pr, result); err != nil {
		return nil, errors.Wrapf(err, "failed to check status requirements for pr #%d", prNumber)
	}

	c.checkUserBypassPermission(ctx, owner, repo, pr, result)

	return result, nil
}

// checkReviewRequirements validates that PR had required approving reviews.
// checks both legacy branch protection and repository rulesets.
func (c *Client) checkReviewRequirements(ctx context.Context, owner, repo string, pr *github.PullRequest, result *domain.PRComplianceResult) error {
	requiredApprovals := 0

	// check legacy branch protection
	if result.Protection != nil && result.Protection.RequiredPullRequestReviews != nil {
		requiredApprovals = result.Protection.RequiredPullRequestReviews.RequiredApprovingReviewCount
	}

	// check repository rulesets (use the highest requirement)
	if result.BranchRules != nil {
		for _, rule := range result.BranchRules.PullRequest {
			if rule.Parameters.RequiredApprovingReviewCount > requiredApprovals {
				requiredApprovals = rule.Parameters.RequiredApprovingReviewCount
			}
		}
	}

	if requiredApprovals == 0 {
		return nil
	}

	reviews, _, err := c.client.PullRequests.ListReviews(ctx, owner, repo, *pr.Number, &github.ListOptions{PerPage: 100})
	if err != nil {
		return errors.Wrapf(err, "failed to list reviews for pr #%d in %s/%s", *pr.Number, owner, repo)
	}

	// deduplicate reviews per user, keeping only the latest state
	latestReviewByUser := make(map[string]string)
	for _, review := range reviews {
		if review.User == nil || review.User.Login == nil || review.State == nil {
			continue
		}
		latestReviewByUser[*review.User.Login] = *review.State
	}

	approvedCount := 0
	for _, state := range latestReviewByUser {
		if state == "APPROVED" {
			approvedCount++
		}
	}

	if approvedCount < requiredApprovals {
		result.Violations = append(result.Violations, domain.ComplianceViolation{
			Type:        "insufficient_reviews",
			Description: fmt.Sprintf("required %d approving reviews, had %d", requiredApprovals, approvedCount),
		})
	}

	return nil
}

// checkStatusRequirements validates that required status checks passed.
// checks both legacy branch protection and repository rulesets.
func (c *Client) checkStatusRequirements(ctx context.Context, owner, repo string, pr *github.PullRequest, result *domain.PRComplianceResult) error {
	if pr.Head == nil || pr.Head.SHA == nil {
		return nil
	}

	// collect required checks from both sources
	requiredChecks := make(map[string]bool)

	// check legacy branch protection
	if result.Protection != nil &&
		result.Protection.RequiredStatusChecks != nil &&
		result.Protection.RequiredStatusChecks.Contexts != nil {
		for _, check := range *result.Protection.RequiredStatusChecks.Contexts {
			requiredChecks[check] = true
		}
	}

	// check repository rulesets
	if result.BranchRules != nil {
		for _, rule := range result.BranchRules.RequiredStatusChecks {
			for _, check := range rule.Parameters.RequiredStatusChecks {
				requiredChecks[check.Context] = true
			}
		}
	}

	if len(requiredChecks) == 0 {
		return nil
	}

	combinedStatus, _, err := c.client.Repositories.GetCombinedStatus(ctx, owner, repo, *pr.Head.SHA, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to get combined status for sha '%s' in %s/%s", *pr.Head.SHA, owner, repo)
	}

	passedChecks := make(map[string]bool)
	for _, status := range combinedStatus.Statuses {
		if status.Context != nil && status.State != nil && *status.State == "success" {
			passedChecks[*status.Context] = true
		}
	}

	// also check GitHub Actions check runs (modern repos use these instead
	// of commit statuses)
	checkRuns, _, err := c.client.Checks.ListCheckRunsForRef(ctx, owner, repo, *pr.Head.SHA, &github.ListCheckRunsOptions{
		ListOptions: github.ListOptions{PerPage: 100},
	})
	if err == nil && checkRuns != nil {
		for _, run := range checkRuns.CheckRuns {
			if run.Name != nil && run.Conclusion != nil && *run.Conclusion == "success" {
				passedChecks[*run.Name] = true
			}
		}
	}

	for required := range requiredChecks {
		if !passedChecks[required] {
			result.Violations = append(result.Violations, domain.ComplianceViolation{
				Type:        "missing_status_check",
				Description: fmt.Sprintf("required check '%s' did not pass", required),
			})
		}
	}

	return nil
}

// checkUserBypassPermission checks if the user who merged the PR has admin or
// maintainer permissions allowing bypass.
func (c *Client) checkUserBypassPermission(ctx context.Context, owner, repo string, pr *github.PullRequest, result *domain.PRComplianceResult) {
	if pr.MergedBy == nil || pr.MergedBy.Login == nil {
		return
	}

	mergedBy := *pr.MergedBy.Login

	permissionLevel, _, err := c.client.Repositories.GetPermissionLevel(ctx, owner, repo, mergedBy)
	if err != nil {
		return
	}

	if permissionLevel.Permission != nil {
		switch perm := *permissionLevel.Permission; perm {
		case "admin":
			result.UserHasBypass = true
			result.UserBypassReason = "repository admin"
		case "maintain":
			result.UserHasBypass = true
			result.UserBypassReason = "repository maintainer"
		}
	}
}
