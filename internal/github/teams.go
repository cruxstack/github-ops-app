// Package github provides GitHub team management and membership sync.
package github

import (
	"context"
	"fmt"

	"github.com/cockroachdb/errors"
	internalerrors "github.com/cruxstack/github-ops-app/internal/errors"
	"github.com/google/go-github/v79/github"
)

// TeamSyncResult contains the results of syncing team membership.
type TeamSyncResult struct {
	TeamName               string
	MembersAdded           []string
	MembersRemoved         []string
	MembersSkippedExternal []string
	Errors                 []string
}

// GetOrCreateTeam fetches an existing team by slug or creates it if missing.
func (c *Client) GetOrCreateTeam(ctx context.Context, teamName, privacy string) (*github.Team, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}

	team, resp, err := c.client.Teams.GetTeamBySlug(ctx, c.org, teamName)
	if err == nil {
		return team, nil
	}

	if resp != nil && resp.StatusCode == 404 {
		newTeam := &github.NewTeam{
			Name:    teamName,
			Privacy: &privacy,
		}
		team, _, err = c.client.Teams.CreateTeam(ctx, c.org, *newTeam)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create team '%s' in org '%s'", teamName, c.org)
		}
		return team, nil
	}

	return nil, errors.Wrapf(internalerrors.ErrTeamNotFound, "failed to fetch team '%s' from org '%s'", teamName, c.org)
}

// GetTeamMembers returns GitHub usernames of all team members.
func (c *Client) GetTeamMembers(ctx context.Context, teamSlug string) ([]string, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}

	members, _, err := c.client.Teams.ListTeamMembersBySlug(ctx, c.org, teamSlug, nil)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list members for team '%s'", teamSlug)
	}

	logins := make([]string, 0, len(members))
	for _, member := range members {
		if member.Login != nil {
			logins = append(logins, *member.Login)
		}
	}

	return logins, nil
}

// SyncTeamMembers adds and removes members to match desired state.
// collects errors for individual operations but continues processing. skips
// removal of external collaborators (outside org members). applies safety
// threshold to prevent mass removal during outages.
func (c *Client) SyncTeamMembers(ctx context.Context, teamSlug string, desiredMembers []string, safetyThreshold float64) (*TeamSyncResult, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}

	result := &TeamSyncResult{
		TeamName:               teamSlug,
		MembersAdded:           []string{},
		MembersRemoved:         []string{},
		MembersSkippedExternal: []string{},
		Errors:                 []string{},
	}

	currentMembers, err := c.GetTeamMembers(ctx, teamSlug)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to fetch current members for team '%s'", teamSlug)
	}

	currentSet := make(map[string]bool)
	for _, member := range currentMembers {
		currentSet[member] = true
	}

	desiredSet := make(map[string]bool)
	for _, member := range desiredMembers {
		desiredSet[member] = true
	}

	for _, desired := range desiredMembers {
		if !currentSet[desired] {
			_, _, err := c.client.Teams.AddTeamMembershipBySlug(ctx, c.org, teamSlug, desired, nil)
			if err != nil {
				errMsg := fmt.Sprintf("failed to add '%s' to team '%s': %v", desired, teamSlug, err)
				result.Errors = append(result.Errors, errMsg)
			} else {
				result.MembersAdded = append(result.MembersAdded, desired)
			}
		}
	}

	var toRemove []string
	for _, current := range currentMembers {
		if !desiredSet[current] {
			toRemove = append(toRemove, current)
		}
	}

	if len(currentMembers) > 0 {
		removalRatio := float64(len(toRemove)) / float64(len(currentMembers))
		if removalRatio > safetyThreshold {
			errMsg := fmt.Sprintf("refusing to remove %d of %d members (%.0f%%) as it exceeds safety threshold of %.0f%%",
				len(toRemove), len(currentMembers), removalRatio*100, safetyThreshold*100)
			result.Errors = append(result.Errors, errMsg)
			return result, nil
		}
	}

	for _, username := range toRemove {
		isExternal, err := c.IsExternalCollaborator(ctx, username)
		if err != nil {
			errMsg := fmt.Sprintf("failed to check if '%s' is external: %v", username, err)
			result.Errors = append(result.Errors, errMsg)
			continue
		}

		if isExternal {
			result.MembersSkippedExternal = append(result.MembersSkippedExternal, username)
			continue
		}

		_, err = c.client.Teams.RemoveTeamMembershipBySlug(ctx, c.org, teamSlug, username)
		if err != nil {
			errMsg := fmt.Sprintf("failed to remove '%s' from team '%s': %v", username, teamSlug, err)
			result.Errors = append(result.Errors, errMsg)
		} else {
			result.MembersRemoved = append(result.MembersRemoved, username)
		}
	}

	return result, nil
}
