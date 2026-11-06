package okta

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/okta/okta-sdk-golang/v8/okta"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// pageSize is the number of groups or users requested per page.
const pageSize = 200

// syncedStatuses are the user statuses whose members belong in GitHub teams.
// They are the statuses of users who can sign in, or will once they reset
// their password or are unlocked, so a password expiring does not remove
// anyone. STAGED and PROVISIONED users have not activated yet, and
// SUSPENDED and DEPROVISIONED users have been offboarded.
var syncedStatuses = map[string]bool{
	"ACTIVE":           true,
	"PASSWORD_EXPIRED": true,
	"LOCKED_OUT":       true,
	"RECOVERY":         true,
}

// groupName returns the group name from either profile type.
func groupName(group *okta.Group) string {
	if group == nil || group.Profile == nil {
		return ""
	}
	if p := group.Profile.OktaUserGroupProfile; p != nil {
		return p.GetName()
	}
	if p := group.Profile.OktaActiveDirectoryGroupProfile; p != nil {
		return p.GetName()
	}
	return ""
}

// listGroups fetches every Okta group.
func (c *Client) listGroups(ctx context.Context) ([]okta.Group, error) {
	groups, resp, err := c.api.GroupAPI.ListGroups(ctx).Limit(pageSize).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to list okta groups: %w", err)
	}
	for resp != nil && resp.HasNextPage() {
		var next []okta.Group
		if resp, err = resp.Next(&next); err != nil {
			return nil, fmt.Errorf("failed to list okta groups: %w", err)
		}
		groups = append(groups, next...)
	}
	return groups, nil
}

// getGroupByName finds the group with exactly the given name.
func (c *Client) getGroupByName(ctx context.Context, name string) (*okta.Group, error) {
	groups, resp, err := c.api.GroupAPI.ListGroups(ctx).Q(name).Limit(pageSize).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to search for okta group '%s': %w", name, err)
	}
	for {
		for i := range groups {
			if groupName(&groups[i]) == name {
				return &groups[i], nil
			}
		}
		if resp == nil || !resp.HasNextPage() {
			break
		}
		groups = nil
		if resp, err = resp.Next(&groups); err != nil {
			return nil, fmt.Errorf("failed to search for okta group '%s': %w", name, err)
		}
	}
	return nil, fmt.Errorf("okta group '%s': %w", name, domain.ErrGroupNotFound)
}

// getGroupMembers fetches the GitHub usernames of a group's members whose
// status is synced. Members without a valid GitHub username are listed by
// email in SkippedNoGitHubUsername.
func (c *Client) getGroupMembers(ctx context.Context, groupID string) (*domain.GroupMembersResult, error) {
	users, resp, err := c.api.GroupAPI.ListGroupUsers(ctx, groupID).Limit(pageSize).Execute()
	if err != nil {
		return nil, fmt.Errorf("failed to list members of okta group '%s': %w", groupID, err)
	}

	result := &domain.GroupMembersResult{
		Members:                 []string{},
		SkippedNoGitHubUsername: []string{},
	}
	for {
		for i := range users {
			c.addMember(&users[i], result)
		}
		if resp == nil || !resp.HasNextPage() {
			break
		}
		users = nil
		if resp, err = resp.Next(&users); err != nil {
			return nil, fmt.Errorf("failed to list members of okta group '%s': %w", groupID, err)
		}
	}
	return result, nil
}

// addMember adds a user's GitHub username to result, or records why it was
// skipped.
func (c *Client) addMember(user *okta.User, result *domain.GroupMembersResult) {
	if !syncedStatuses[user.GetStatus()] {
		return
	}

	profile := user.GetProfile()
	username := normalizeUsername(c.profileString(&profile, c.githubUserField))
	if domain.IsValidGitHubLogin(username) {
		result.Members = append(result.Members, username)
		return
	}

	who := profile.GetEmail()
	if who == "" {
		who = profile.GetLogin()
	}
	if username != "" {
		who = fmt.Sprintf("%s (invalid github username '%s')", who, username)
	}
	result.SkippedNoGitHubUsername = append(result.SkippedNoGitHubUsername, who)
}

// profileString returns a profile attribute as a string. It covers both
// standard attributes, which the SDK decodes into fields, and custom ones,
// which it keeps in AdditionalProperties.
func (c *Client) profileString(profile *okta.UserProfile, field string) string {
	if v, ok := profile.AdditionalProperties[field].(string); ok {
		return v
	}
	raw, err := json.Marshal(profile)
	if err != nil {
		return ""
	}
	var attrs map[string]any
	if err := json.Unmarshal(raw, &attrs); err != nil {
		return ""
	}
	v, _ := attrs[field].(string)
	return v
}

// normalizeUsername trims whitespace and a leading "@" that people often
// include when they type a GitHub username.
func normalizeUsername(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "@")
}

// GetGroupsByPattern fetches every Okta group whose name matches pattern,
// with its members. A group whose members can't be fetched is returned with
// Err set, so the caller can report it without syncing it.
func (c *Client) GetGroupsByPattern(ctx context.Context, pattern string) ([]*domain.GroupInfo, error) {
	if pattern == "" {
		return nil, domain.ErrEmptyPattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("pattern '%s': %w", pattern, domain.ErrInvalidPattern)
	}

	groups, err := c.listGroups(ctx)
	if err != nil {
		return nil, err
	}

	var matched []*domain.GroupInfo
	for i := range groups {
		name := groupName(&groups[i])
		if name == "" || !re.MatchString(name) {
			continue
		}

		info := &domain.GroupInfo{ID: groups[i].GetId(), Name: name}
		members, err := c.getGroupMembers(ctx, info.ID)
		if err != nil {
			info.Err = err
		} else {
			info.Members = members.Members
			info.SkippedNoGitHubUsername = members.SkippedNoGitHubUsername
		}
		matched = append(matched, info)
	}
	return matched, nil
}

// GetGroupInfo fetches a single Okta group by its exact name, with its
// members.
func (c *Client) GetGroupInfo(ctx context.Context, name string) (*domain.GroupInfo, error) {
	group, err := c.getGroupByName(ctx, name)
	if err != nil {
		return nil, err
	}
	members, err := c.getGroupMembers(ctx, group.GetId())
	if err != nil {
		return nil, err
	}
	return &domain.GroupInfo{
		ID:                      group.GetId(),
		Name:                    name,
		Members:                 members.Members,
		SkippedNoGitHubUsername: members.SkippedNoGitHubUsername,
	}, nil
}
