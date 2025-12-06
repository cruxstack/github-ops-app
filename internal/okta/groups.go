// Package okta provides Okta group querying and filtering.
package okta

import (
	"regexp"

	"github.com/cockroachdb/errors"
	internalerrors "github.com/cruxstack/github-ops-app/internal/errors"
	"github.com/okta/okta-sdk-golang/v2/okta"
)

// GroupInfo contains Okta group details and member list.
type GroupInfo struct {
	ID                      string
	Name                    string
	Members                 []string
	SkippedNoGitHubUsername []string
}

// GetGroupsByPattern fetches all Okta groups matching a regex pattern.
func (c *Client) GetGroupsByPattern(pattern string) ([]*GroupInfo, error) {
	if pattern == "" {
		return nil, internalerrors.ErrEmptyPattern
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, errors.Wrapf(internalerrors.ErrInvalidPattern, "'%s'", pattern)
	}

	allGroups, err := c.ListGroups()
	if err != nil {
		return nil, err
	}

	var matched []*GroupInfo
	for _, group := range allGroups {
		if group == nil || group.Profile == nil {
			continue
		}

		if re.MatchString(group.Profile.Name) {
			result, err := c.GetGroupMembers(group.Id)
			if err != nil {
				continue
			}

			matched = append(matched, &GroupInfo{
				ID:                      group.Id,
				Name:                    group.Profile.Name,
				Members:                 result.Members,
				SkippedNoGitHubUsername: result.SkippedNoGitHubUsername,
			})
		}
	}

	return matched, nil
}

// GetGroupInfo fetches details for a single Okta group by name.
func (c *Client) GetGroupInfo(groupName string) (*GroupInfo, error) {
	group, err := c.GetGroupByName(groupName)
	if err != nil {
		return nil, err
	}

	result, err := c.GetGroupMembers(group.Id)
	if err != nil {
		return nil, err
	}

	return &GroupInfo{
		ID:                      group.Id,
		Name:                    group.Profile.Name,
		Members:                 result.Members,
		SkippedNoGitHubUsername: result.SkippedNoGitHubUsername,
	}, nil
}

// FilterEnabledGroups filters Okta groups to only those in the enabled list.
// returns all groups if enabled list is empty.
func FilterEnabledGroups(groups []*okta.Group, enabledNames []string) []*okta.Group {
	if len(enabledNames) == 0 {
		return groups
	}

	enabledMap := make(map[string]bool)
	for _, name := range enabledNames {
		enabledMap[name] = true
	}

	var filtered []*okta.Group
	for _, group := range groups {
		if enabledMap[group.Profile.Name] {
			filtered = append(filtered, group)
		}
	}

	return filtered
}
