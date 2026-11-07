package client

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/go-github/v93/github"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// CheckPRCompliance verifies whether a merged PR met the requirements of its
// base branch: the approving reviews and status checks required by legacy
// branch protection and by repository rulesets. When it did not, the
// merger's repository role is recorded as the likely reason it could merge.
func (c *Client) CheckPRCompliance(ctx context.Context, owner, repo string, prNumber int) (*domain.PRComplianceResult, error) {
	pr, _, err := c.gh.PullRequests.Get(ctx, owner, repo, prNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch pr #%d from %s/%s: %w", prNumber, owner, repo, err)
	}
	if pr.GetBase().GetRef() == "" {
		return nil, fmt.Errorf("pr #%d in %s/%s has no base branch: %w", prNumber, owner, repo, domain.ErrMissingPRData)
	}

	result := &domain.PRComplianceResult{
		PR:         pr,
		BaseBranch: pr.GetBase().GetRef(),
	}

	protection, _, err := c.gh.Repositories.GetBranchProtection(ctx, owner, repo, result.BaseBranch)
	switch {
	case err == nil:
		result.Protection = protection
	case errors.Is(err, github.ErrBranchNotProtected):
	default:
		return nil, fmt.Errorf("failed to fetch branch protection for '%s' in %s/%s: %w", result.BaseBranch, owner, repo, err)
	}

	result.BranchRules, err = c.branchRules(ctx, owner, repo, result.BaseBranch)
	if err != nil {
		return nil, err
	}

	if err := c.checkReviewRequirements(ctx, owner, repo, pr, result); err != nil {
		return nil, err
	}
	if err := c.checkStatusRequirements(ctx, owner, repo, pr, result); err != nil {
		return nil, err
	}
	if result.HasViolations() {
		c.checkUserBypassPermission(ctx, owner, repo, pr, result)
	}

	return result, nil
}

// branchRules returns the active ruleset rules for a branch, across all
// pages.
func (c *Client) branchRules(ctx context.Context, owner, repo, branch string) (*github.BranchRules, error) {
	all := &github.BranchRules{}
	opts := &github.ListOptions{PerPage: 100}
	for {
		rules, resp, err := c.gh.Repositories.ListRulesForBranch(ctx, owner, repo, branch, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch rules for branch '%s' in %s/%s: %w", branch, owner, repo, err)
		}
		if rules != nil {
			all.PullRequest = append(all.PullRequest, rules.PullRequest...)
			all.RequiredStatusChecks = append(all.RequiredStatusChecks, rules.RequiredStatusChecks...)
		}
		if resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = resp.NextPage
	}
}

// requiredApprovals returns the highest approving review count required by
// branch protection or any ruleset.
func requiredApprovals(result *domain.PRComplianceResult) int {
	required := 0
	if reviews := result.Protection.GetRequiredPullRequestReviews(); reviews != nil {
		required = reviews.RequiredApprovingReviewCount
	}
	if result.BranchRules != nil {
		for _, rule := range result.BranchRules.PullRequest {
			required = max(required, rule.Parameters.RequiredApprovingReviewCount)
		}
	}
	return required
}

// checkReviewRequirements records a violation if the PR had fewer approving
// reviews than required. A reviewer counts once, by their latest review that
// approved, requested changes, or was dismissed; comments don't change it.
func (c *Client) checkReviewRequirements(ctx context.Context, owner, repo string, pr *github.PullRequest, result *domain.PRComplianceResult) error {
	required := requiredApprovals(result)
	if required == 0 {
		return nil
	}

	latest := make(map[string]string)
	opts := &github.ListOptions{PerPage: 100}
	for review, err := range c.gh.PullRequests.ListReviewsIter(ctx, owner, repo, pr.GetNumber(), opts) {
		if err != nil {
			return fmt.Errorf("failed to list reviews for pr #%d in %s/%s: %w", pr.GetNumber(), owner, repo, err)
		}
		login := strings.ToLower(review.GetUser().GetLogin())
		switch state := review.GetState(); state {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			if login != "" {
				latest[login] = state
			}
		}
	}

	approved := 0
	for _, state := range latest {
		if state == "APPROVED" {
			approved++
		}
	}

	if approved < required {
		result.Violations = append(result.Violations, domain.ComplianceViolation{
			Type:        domain.ViolationInsufficientReviews,
			Description: fmt.Sprintf("required %d approving reviews, had %d", required, approved),
		})
	}
	return nil
}

// requiredChecks returns the names of the status checks required by branch
// protection or any ruleset, sorted.
func requiredChecks(result *domain.PRComplianceResult) []string {
	set := make(map[string]bool)
	if checks := result.Protection.GetRequiredStatusChecks(); checks != nil {
		if checks.Contexts != nil {
			for _, name := range *checks.Contexts {
				set[name] = true
			}
		}
		if checks.Checks != nil {
			for _, check := range *checks.Checks {
				if check != nil {
					set[check.Context] = true
				}
			}
		}
	}
	if result.BranchRules != nil {
		for _, rule := range result.BranchRules.RequiredStatusChecks {
			for _, check := range rule.Parameters.RequiredStatusChecks {
				if check != nil {
					set[check.Context] = true
				}
			}
		}
	}
	delete(set, "")

	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// passingCheckConclusions are the check run conclusions GitHub accepts for a
// required check.
var passingCheckConclusions = map[string]bool{
	"success": true,
	"neutral": true,
	"skipped": true,
}

// checkStatusRequirements records a violation for each required status check
// that did not pass on the PR's head commit, as a commit status or a check
// run.
func (c *Client) checkStatusRequirements(ctx context.Context, owner, repo string, pr *github.PullRequest, result *domain.PRComplianceResult) error {
	required := requiredChecks(result)
	if len(required) == 0 {
		return nil
	}

	sha := pr.GetHead().GetSHA()
	if sha == "" {
		return fmt.Errorf("pr #%d in %s/%s has no head sha: %w", pr.GetNumber(), owner, repo, domain.ErrMissingPRData)
	}

	passed := make(map[string]bool)

	opts := &github.ListOptions{PerPage: 100}
	for {
		combined, resp, err := c.gh.Repositories.GetCombinedStatus(ctx, owner, repo, sha, opts)
		if err != nil {
			return fmt.Errorf("failed to get combined status for sha '%s' in %s/%s: %w", sha, owner, repo, err)
		}
		for _, status := range combined.Statuses {
			if status.GetState() == "success" {
				passed[status.GetContext()] = true
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	runOpts := &github.ListCheckRunsOptions{
		Filter:      new("latest"),
		ListOptions: github.ListOptions{PerPage: 100},
	}
	for run, err := range c.gh.Checks.ListCheckRunsForRefIter(ctx, owner, repo, sha, runOpts) {
		if err != nil {
			return fmt.Errorf("failed to list check runs for sha '%s' in %s/%s: %w", sha, owner, repo, err)
		}
		if passingCheckConclusions[run.GetConclusion()] {
			passed[run.GetName()] = true
		}
	}

	for _, name := range required {
		if !passed[name] {
			result.Violations = append(result.Violations, domain.ComplianceViolation{
				Type:        domain.ViolationMissingStatusCheck,
				Description: fmt.Sprintf("required check '%s' did not pass", name),
			})
		}
	}
	return nil
}

// checkUserBypassPermission records whether the user who merged the PR has a
// repository role that can bypass branch protection. It is best effort: the
// merger may instead be a ruleset bypass actor, or a bot.
func (c *Client) checkUserBypassPermission(ctx context.Context, owner, repo string, pr *github.PullRequest, result *domain.PRComplianceResult) {
	mergedBy := pr.GetMergedBy().GetLogin()
	if mergedBy == "" || !domain.IsValidGitHubLogin(mergedBy) {
		return
	}

	level, _, err := c.gh.Repositories.GetPermissionLevel(ctx, owner, repo, mergedBy)
	if err != nil {
		return
	}

	role := level.GetRoleName()
	if role == "" {
		role = level.GetPermission()
	}
	switch role {
	case "admin":
		result.UserHasBypass = true
		result.UserBypassReason = "repository admin"
	case "maintain":
		result.UserHasBypass = true
		result.UserBypassReason = "repository maintainer"
	}
}
