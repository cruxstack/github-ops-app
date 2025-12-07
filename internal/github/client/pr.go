package client

import (
	"context"
	"fmt"

	"github.com/cockroachdb/errors"
	internalerrors "github.com/cruxstack/github-ops-app/internal/errors"
	"github.com/google/go-github/v79/github"
)

// ComplianceViolation represents a single branch protection rule violation.
type ComplianceViolation struct {
	Type        string
	Description string
}

// PRComplianceResult contains PR compliance check results including
// violations and user bypass permissions.
type PRComplianceResult struct {
	PR               *github.PullRequest
	BaseBranch       string
	Protection       *github.Protection
	BranchRules      *github.BranchRules
	Violations       []ComplianceViolation
	UserHasBypass    bool
	UserBypassReason string
}

// CheckPRCompliance verifies if a merged PR met branch protection
// requirements. checks review requirements, status checks, and user bypass
// permissions.
func (c *Client) CheckPRCompliance(ctx context.Context, owner, repo string, prNumber int) (*PRComplianceResult, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}

	pr, _, err := c.client.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to fetch pr #%d from %s/%s", prNumber, owner, repo)
	}

	if pr == nil {
		return nil, errors.Wrapf(internalerrors.ErrMissingPRData, "pr #%d returned nil", prNumber)
	}

	if pr.Base == nil || pr.Base.Ref == nil {
		return nil, errors.Wrapf(internalerrors.ErrMissingPRData, "pr #%d missing base branch", prNumber)
	}

	baseBranch := *pr.Base.Ref

	result := &PRComplianceResult{
		PR:         pr,
		BaseBranch: baseBranch,
		Violations: []ComplianceViolation{},
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

	c.checkReviewRequirements(ctx, owner, repo, pr, result)
	c.checkStatusRequirements(ctx, owner, repo, pr, result)
	c.checkUserBypassPermission(ctx, owner, repo, pr, result)

	return result, nil
}

// checkReviewRequirements validates that PR had required approving reviews.
// checks both legacy branch protection and repository rulesets.
func (c *Client) checkReviewRequirements(ctx context.Context, owner, repo string, pr *github.PullRequest, result *PRComplianceResult) {
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
		return
	}

	reviews, _, err := c.client.PullRequests.ListReviews(ctx, owner, repo, *pr.Number, nil)
	if err != nil {
		return
	}

	approvedCount := 0
	for _, review := range reviews {
		if review.State != nil && *review.State == "APPROVED" {
			approvedCount++
		}
	}

	if approvedCount < requiredApprovals {
		result.Violations = append(result.Violations, ComplianceViolation{
			Type:        "insufficient_reviews",
			Description: fmt.Sprintf("required %d approving reviews, had %d", requiredApprovals, approvedCount),
		})
	}
}

// checkStatusRequirements validates that required status checks passed.
// checks both legacy branch protection and repository rulesets.
func (c *Client) checkStatusRequirements(ctx context.Context, owner, repo string, pr *github.PullRequest, result *PRComplianceResult) {
	if pr.Head == nil || pr.Head.SHA == nil {
		return
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
		return
	}

	combinedStatus, _, err := c.client.Repositories.GetCombinedStatus(ctx, owner, repo, *pr.Head.SHA, nil)
	if err != nil {
		return
	}

	passedChecks := make(map[string]bool)
	for _, status := range combinedStatus.Statuses {
		if status.Context != nil && status.State != nil && *status.State == "success" {
			passedChecks[*status.Context] = true
		}
	}

	for required := range requiredChecks {
		if !passedChecks[required] {
			result.Violations = append(result.Violations, ComplianceViolation{
				Type:        "missing_status_check",
				Description: fmt.Sprintf("required check '%s' did not pass", required),
			})
		}
	}
}

// checkUserBypassPermission checks if the user who merged the PR has admin or
// maintainer permissions allowing bypass.
func (c *Client) checkUserBypassPermission(ctx context.Context, owner, repo string, pr *github.PullRequest, result *PRComplianceResult) {
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

// HasViolations returns true if any compliance violations were detected.
func (r *PRComplianceResult) HasViolations() bool {
	return len(r.Violations) > 0
}

// WasBypassed returns true if violations exist and user had bypass
// permission.
func (r *PRComplianceResult) WasBypassed() bool {
	return r.HasViolations() && r.UserHasBypass
}
