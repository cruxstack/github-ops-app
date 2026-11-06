package client

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/go-github/v93/github"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// ResolveTeam returns the slug of the team with the given name, creating the
// team when it is missing and create is true.
func (c *Client) ResolveTeam(ctx context.Context, name, privacy string, create bool) (string, error) {
	slug := domain.TeamSlug(name)
	if slug == "" {
		return "", fmt.Errorf("invalid team name '%s'", name)
	}

	team, resp, err := c.gh.Teams.GetTeamBySlug(ctx, c.org, slug)
	if err == nil {
		return team.GetSlug(), nil
	}
	if !isNotFound(resp) {
		return "", fmt.Errorf("failed to fetch team '%s' from org '%s': %w", slug, c.org, err)
	}

	// the slug guess can miss for names GitHub slugifies differently
	if found, err := c.findTeamByName(ctx, name); err != nil || found != "" {
		return found, err
	}

	if !create {
		return "", fmt.Errorf("team '%s' in org '%s' does not exist and create_team_if_missing is false: %w", name, c.org, domain.ErrTeamNotFound)
	}

	team, _, err = c.gh.Teams.CreateTeam(ctx, c.org, github.CreateTeamRequest{
		Name:    name,
		Privacy: new(privacy),
	})
	if err != nil {
		return "", fmt.Errorf("failed to create team '%s' in org '%s': %w", name, c.org, err)
	}
	return team.GetSlug(), nil
}

// findTeamByName returns the slug of the team named name, ignoring case, or
// "" if there is none.
func (c *Client) findTeamByName(ctx context.Context, name string) (string, error) {
	for team, err := range c.gh.Teams.ListTeamsIter(ctx, c.org, &github.ListOptions{PerPage: 100}) {
		if err != nil {
			return "", fmt.Errorf("failed to list teams in org '%s': %w", c.org, err)
		}
		if strings.EqualFold(team.GetName(), name) {
			return team.GetSlug(), nil
		}
	}
	return "", nil
}

// GetTeamMembers returns the logins of all team members.
func (c *Client) GetTeamMembers(ctx context.Context, teamSlug string) ([]string, error) {
	opts := &github.TeamListTeamMembersOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var members []string
	for member, err := range c.gh.Teams.ListTeamMembersBySlugIter(ctx, c.org, teamSlug, opts) {
		if err != nil {
			return nil, fmt.Errorf("failed to list members for team '%s': %w", teamSlug, err)
		}
		if login := member.GetLogin(); login != "" {
			members = append(members, login)
		}
	}
	return members, nil
}

// pendingTeamInvitations returns the logins, lowercased, of users invited to
// the org through the team who have not accepted yet.
func (c *Client) pendingTeamInvitations(ctx context.Context, teamSlug string) (map[string]bool, error) {
	pending := make(map[string]bool)
	opts := &github.ListOptions{PerPage: 100}
	for inv, err := range c.gh.Teams.ListPendingTeamInvitationsBySlugIter(ctx, c.org, teamSlug, opts) {
		if err != nil {
			return nil, fmt.Errorf("failed to list pending invitations for team '%s': %w", teamSlug, err)
		}
		if login := inv.GetLogin(); login != "" {
			pending[strings.ToLower(login)] = true
		}
	}
	return pending, nil
}

// ListOrgMembers returns the logins of all organization members. Outside
// collaborators are not members, so they are not included.
func (c *Client) ListOrgMembers(ctx context.Context) ([]string, error) {
	opts := &github.ListMembersOptions{ListOptions: github.ListOptions{PerPage: 100}}
	var members []string
	for member, err := range c.gh.Organizations.ListMembersIter(ctx, c.org, opts) {
		if err != nil {
			return nil, fmt.Errorf("failed to list members for org '%s': %w", c.org, err)
		}
		if login := member.GetLogin(); login != "" {
			members = append(members, login)
		}
	}
	return members, nil
}

// isOrgMember returns true if the user is a member of the org.
func (c *Client) isOrgMember(ctx context.Context, login string) (bool, error) {
	membership, resp, err := c.gh.Organizations.GetOrgMembership(ctx, login, c.org)
	if err != nil {
		if isNotFound(resp) {
			return false, nil
		}
		return false, fmt.Errorf("failed to check org membership for user '%s': %w", login, err)
	}
	return membership != nil, nil
}

// childTeamMembers returns the logins, lowercased, of the members of a
// team's child teams. GitHub lists them as members of the parent team, but
// they can only be removed from the child team.
func (c *Client) childTeamMembers(ctx context.Context, teamSlug string) (map[string]bool, error) {
	members := make(map[string]bool)
	for child, err := range c.gh.Teams.ListChildTeamsByParentSlugIter(ctx, c.org, teamSlug, &github.ListOptions{PerPage: 100}) {
		if err != nil {
			return nil, fmt.Errorf("failed to list child teams of team '%s': %w", teamSlug, err)
		}
		// a child team's member list includes its own child teams
		logins, err := c.GetTeamMembers(ctx, child.GetSlug())
		if err != nil {
			return nil, err
		}
		for _, login := range logins {
			members[strings.ToLower(login)] = true
		}
	}
	return members, nil
}

// SyncTeamMembers adds and removes members so the team matches
// desiredMembers. Errors for individual users are collected and the rest
// continue. Members of child teams and users who are not org members are not
// removed, and no one is removed if more than safetyThreshold of the team's
// direct members would be, which guards against an empty or partial member
// list from Okta.
func (c *Client) SyncTeamMembers(ctx context.Context, teamSlug string, desiredMembers []string, safetyThreshold float64) (*domain.TeamSyncResult, error) {
	result := &domain.TeamSyncResult{
		TeamName:               teamSlug,
		MembersAdded:           []string{},
		MembersRemoved:         []string{},
		MembersSkippedExternal: []string{},
		Errors:                 []string{},
	}

	listed, err := c.GetTeamMembers(ctx, teamSlug)
	if err != nil {
		return nil, err
	}
	pending, err := c.pendingTeamInvitations(ctx, teamSlug)
	if err != nil {
		return nil, err
	}
	inChildTeam, err := c.childTeamMembers(ctx, teamSlug)
	if err != nil {
		return nil, err
	}

	current := make(map[string]bool, len(listed))
	var direct []string
	for _, member := range listed {
		key := strings.ToLower(member)
		current[key] = true
		if !inChildTeam[key] {
			direct = append(direct, member)
		}
	}

	desired := make(map[string]bool, len(desiredMembers))
	var toAdd []string
	for _, member := range desiredMembers {
		key := strings.ToLower(member)
		if desired[key] {
			continue
		}
		desired[key] = true
		if !domain.IsValidGitHubLogin(member) {
			result.Errors = append(result.Errors, fmt.Sprintf("skipped invalid github username '%s'", member))
			continue
		}
		if !current[key] && !pending[key] {
			toAdd = append(toAdd, member)
		}
	}

	var toRemove []string
	for _, member := range direct {
		if !desired[strings.ToLower(member)] {
			toRemove = append(toRemove, member)
		}
	}

	for _, login := range toAdd {
		if _, _, err := c.gh.Teams.AddTeamMembershipBySlug(ctx, c.org, teamSlug, login, github.AddTeamMembershipRequest{}); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("failed to add '%s' to team '%s': %v", login, teamSlug, err))
			continue
		}
		result.MembersAdded = append(result.MembersAdded, login)
	}

	removed := make(map[string]bool)
	defer func() {
		result.Members = finalMembers(listed, result.MembersAdded, removed)
	}()

	if len(direct) > 0 && len(toRemove) > 0 {
		ratio := float64(len(toRemove)) / float64(len(direct))
		if ratio > safetyThreshold {
			result.Errors = append(result.Errors, fmt.Sprintf(
				"refusing to remove %d of %d members (%.0f%%) from team '%s' as it exceeds the safety threshold of %.0f%%",
				len(toRemove), len(direct), ratio*100, teamSlug, safetyThreshold*100))
			return result, nil
		}
	}

	for _, login := range toRemove {
		member, err := c.isOrgMember(ctx, login)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			continue
		}
		if !member {
			result.MembersSkippedExternal = append(result.MembersSkippedExternal, login)
			continue
		}

		resp, err := c.gh.Teams.RemoveTeamMembershipBySlug(ctx, c.org, teamSlug, login)
		if err != nil {
			// a 404 means they already left; nothing was removed
			if !isNotFound(resp) {
				result.Errors = append(result.Errors, fmt.Sprintf("failed to remove '%s' from team '%s': %v", login, teamSlug, err))
			}
			continue
		}
		removed[strings.ToLower(login)] = true
		result.MembersRemoved = append(result.MembersRemoved, login)
	}

	return result, nil
}

// finalMembers returns the members listed before the sync, plus those added
// and minus those removed.
func finalMembers(listed, added []string, removed map[string]bool) []string {
	members := make([]string, 0, len(listed)+len(added))
	for _, login := range listed {
		if !removed[strings.ToLower(login)] {
			members = append(members, login)
		}
	}
	return append(members, added...)
}
