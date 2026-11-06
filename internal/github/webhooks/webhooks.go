// Package webhooks validates GitHub webhook signatures and parses the
// pull_request, team, and membership events the application handles.
package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/go-github/v93/github"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// Event types the application handles.
const (
	EventPing        = "ping"
	EventPullRequest = "pull_request"
	EventTeam        = "team"
	EventMembership  = "membership"
)

// ValidateSignature verifies the X-Hub-Signature-256 HMAC of a payload. An
// empty secret is rejected, so a missing configuration never accepts
// unsigned requests.
func ValidateSignature(payload []byte, signature, secret string) error {
	if secret == "" {
		return fmt.Errorf("webhook secret is not configured: %w", domain.ErrConfig)
	}
	if signature == "" {
		return domain.ErrMissingSignature
	}

	got, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return fmt.Errorf("signature must start with 'sha256=': %w", domain.ErrInvalidSignature)
	}
	gotMAC, err := hex.DecodeString(got)
	if err != nil {
		return fmt.Errorf("signature is not hex: %w", domain.ErrInvalidSignature)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	if !hmac.Equal(gotMAC, mac.Sum(nil)) {
		return domain.ErrInvalidSignature
	}
	return nil
}

// Sign returns the X-Hub-Signature-256 header value for a payload.
func Sign(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// sender is the part of an event that identifies who triggered it.
type sender struct {
	Sender *github.User `json:"sender"`
}

// GetSenderLogin returns the login of the user who triggered the event.
func (e sender) GetSenderLogin() string {
	return e.Sender.GetLogin()
}

// GetSenderType returns the type of the sender, such as User or Bot.
func (e sender) GetSenderType() string {
	return e.Sender.GetType()
}

// PullRequestEvent represents a GitHub pull_request webhook payload.
type PullRequestEvent struct {
	sender
	Action      string              `json:"action"`
	Number      int                 `json:"number"`
	PullRequest *github.PullRequest `json:"pull_request"`
	Repository  *github.Repository  `json:"repository"`
}

// TeamEvent represents a GitHub team webhook payload.
type TeamEvent struct {
	sender
	Action       string               `json:"action"`
	Team         *github.Team         `json:"team"`
	Changes      *TeamChanges         `json:"changes,omitempty"`
	Organization *github.Organization `json:"organization"`
}

// TeamChanges holds the previous values of an edited team's fields.
type TeamChanges struct {
	Name *struct {
		From string `json:"from"`
	} `json:"name,omitempty"`
}

// MembershipEvent represents a GitHub membership webhook payload.
type MembershipEvent struct {
	sender
	Action       string               `json:"action"`
	Scope        string               `json:"scope"`
	Member       *github.User         `json:"member"`
	Team         *github.Team         `json:"team"`
	Organization *github.Organization `json:"organization"`
}

func parse(payload []byte, event any, kind string) error {
	if err := json.Unmarshal(payload, event); err != nil {
		return fmt.Errorf("failed to unmarshal %s event: %w", kind, errors.Join(domain.ErrInvalidPayload, err))
	}
	return nil
}

func missing(kind, field string) error {
	return fmt.Errorf("%s event is missing %s: %w", kind, field, domain.ErrInvalidPayload)
}

// ParsePullRequestEvent unmarshals and validates a pull_request webhook.
func ParsePullRequestEvent(payload []byte) (*PullRequestEvent, error) {
	var event PullRequestEvent
	if err := parse(payload, &event, EventPullRequest); err != nil {
		return nil, err
	}
	switch {
	case event.PullRequest == nil:
		return nil, missing(EventPullRequest, "pull_request")
	case event.PullRequest.Number == nil:
		return nil, missing(EventPullRequest, "pull_request.number")
	case event.PullRequest.GetBase().GetRef() == "":
		return nil, missing(EventPullRequest, "pull_request.base.ref")
	case event.Repository.GetName() == "" || event.Repository.GetOwner().GetLogin() == "":
		return nil, missing(EventPullRequest, "repository")
	}
	return &event, nil
}

// IsMerged returns true if the PR was closed via merge.
func (e *PullRequestEvent) IsMerged() bool {
	return e.Action == "closed" && e.PullRequest.GetMerged()
}

// GetNumber returns the PR number.
func (e *PullRequestEvent) GetNumber() int {
	return e.PullRequest.GetNumber()
}

// GetBaseBranch returns the target branch name.
func (e *PullRequestEvent) GetBaseBranch() string {
	return e.PullRequest.GetBase().GetRef()
}

// GetRepoFullName returns the repository in owner/name format.
func (e *PullRequestEvent) GetRepoFullName() string {
	return e.Repository.GetFullName()
}

// GetRepoOwner returns the repository owner login.
func (e *PullRequestEvent) GetRepoOwner() string {
	return e.Repository.GetOwner().GetLogin()
}

// GetRepoName returns the repository name without owner.
func (e *PullRequestEvent) GetRepoName() string {
	return e.Repository.GetName()
}

// ParseTeamEvent unmarshals and validates a team webhook.
func ParseTeamEvent(payload []byte) (*TeamEvent, error) {
	var event TeamEvent
	if err := parse(payload, &event, EventTeam); err != nil {
		return nil, err
	}
	switch {
	case event.Team == nil:
		return nil, missing(EventTeam, "team")
	case event.Sender == nil:
		return nil, missing(EventTeam, "sender")
	case event.Organization.GetLogin() == "":
		return nil, missing(EventTeam, "organization")
	}
	return &event, nil
}

// GetTeamSlug returns the team's slug.
func (e *TeamEvent) GetTeamSlug() string {
	return e.Team.GetSlug()
}

// GetOrgLogin returns the login of the organization the team belongs to.
func (e *TeamEvent) GetOrgLogin() string {
	return e.Organization.GetLogin()
}

// AffectsMembership returns true if the change can affect which team the
// sync maps a group to: a team was created, deleted, or renamed. Changes to
// a team's description, privacy, or repository access cannot.
func (e *TeamEvent) AffectsMembership() bool {
	switch e.Action {
	case "created", "deleted":
		return true
	case "edited":
		return e.Changes != nil && e.Changes.Name != nil
	default:
		return false
	}
}

// GetPreviousTeamSlug returns the slug the team had before it was renamed,
// or "".
func (e *TeamEvent) GetPreviousTeamSlug() string {
	if e.Changes == nil || e.Changes.Name == nil {
		return ""
	}
	return domain.TeamSlug(e.Changes.Name.From)
}

// ParseMembershipEvent unmarshals and validates a membership webhook.
func ParseMembershipEvent(payload []byte) (*MembershipEvent, error) {
	var event MembershipEvent
	if err := parse(payload, &event, EventMembership); err != nil {
		return nil, err
	}
	switch {
	case event.Team == nil:
		return nil, missing(EventMembership, "team")
	case event.Member == nil:
		return nil, missing(EventMembership, "member")
	case event.Sender == nil:
		return nil, missing(EventMembership, "sender")
	case event.Organization.GetLogin() == "":
		return nil, missing(EventMembership, "organization")
	}
	return &event, nil
}

// GetTeamSlug returns the team's slug.
func (e *MembershipEvent) GetTeamSlug() string {
	return e.Team.GetSlug()
}

// GetOrgLogin returns the login of the organization the team belongs to.
func (e *MembershipEvent) GetOrgLogin() string {
	return e.Organization.GetLogin()
}

// IsTeamScope returns true if the membership event is for a team.
func (e *MembershipEvent) IsTeamScope() bool {
	return e.Scope == "team"
}
