package okta

import (
	"context"
	"log/slog"
	"regexp"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/domain"
	oktasdk "github.com/okta/okta-sdk-golang/v6/okta"
)

// extractGroupName returns the group name from either profile type.
func extractGroupName(group *oktasdk.Group) string {
	if group == nil || group.Profile == nil {
		return ""
	}
	if group.Profile.OktaUserGroupProfile != nil {
		return group.Profile.OktaUserGroupProfile.GetName()
	}
	if group.Profile.OktaActiveDirectoryGroupProfile != nil {
		return group.Profile.OktaActiveDirectoryGroupProfile.GetName()
	}
	return ""
}

// GetGroupsByPattern fetches all Okta groups matching a regex pattern.
func (c *Client) GetGroupsByPattern(ctx context.Context, pattern string) ([]*domain.GroupInfo, error) {
	if pattern == "" {
		return nil, domain.ErrEmptyPattern
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, errors.Wrapf(domain.ErrInvalidPattern, "'%s'", pattern)
	}

	allGroups, err := c.ListGroups(ctx)
	if err != nil {
		return nil, err
	}

	var matched []*domain.GroupInfo
	for _, group := range allGroups {
		groupName := extractGroupName(&group)
		if groupName == "" {
			continue
		}

		if re.MatchString(groupName) {
			result, err := c.GetGroupMembers(ctx, group.GetId())
			if err != nil {
				c.logger.Warn("failed to get group members, skipping",
					slog.String("group", groupName),
					slog.String("error", err.Error()))
				continue
			}

			matched = append(matched, &domain.GroupInfo{
				ID:                      group.GetId(),
				Name:                    groupName,
				Members:                 result.Members,
				SkippedNoGitHubUsername: result.SkippedNoGitHubUsername,
			})
		}
	}

	return matched, nil
}

// GetGroupInfo fetches details for a single Okta group by name.
func (c *Client) GetGroupInfo(ctx context.Context, groupName string) (*domain.GroupInfo, error) {
	group, err := c.GetGroupByName(ctx, groupName)
	if err != nil {
		return nil, err
	}

	result, err := c.GetGroupMembers(ctx, group.GetId())
	if err != nil {
		return nil, err
	}

	name := extractGroupName(group)
	if name == "" {
		name = groupName
	}

	return &domain.GroupInfo{
		ID:                      group.GetId(),
		Name:                    name,
		Members:                 result.Members,
		SkippedNoGitHubUsername: result.SkippedNoGitHubUsername,
	}, nil
}

// FilterEnabledGroups filters Okta groups to only those in the enabled list.
// returns all groups if enabled list is empty.
func FilterEnabledGroups(groups []oktasdk.Group, enabledNames []string) []oktasdk.Group {
	if len(enabledNames) == 0 {
		return groups
	}

	enabledMap := make(map[string]bool)
	for _, name := range enabledNames {
		enabledMap[name] = true
	}

	var filtered []oktasdk.Group
	for _, group := range groups {
		groupName := extractGroupName(&group)
		if groupName != "" && enabledMap[groupName] {
			filtered = append(filtered, group)
		}
	}

	return filtered
}
