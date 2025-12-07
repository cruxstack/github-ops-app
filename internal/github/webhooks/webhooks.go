// Package webhooks provides GitHub webhook event parsing and signature
// validation. Supports pull_request, team, and membership event types.
package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/cockroachdb/errors"
	internalerrors "github.com/cruxstack/github-ops-app/internal/errors"
	"github.com/google/go-github/v79/github"
)

// PullRequestEvent represents a GitHub pull_request webhook payload.
type PullRequestEvent struct {
	Action       string               `json:"action"`
	Number       int                  `json:"number"`
	PullRequest  *github.PullRequest  `json:"pull_request"`
	Repository   *github.Repository   `json:"repository"`
	Sender       *github.User         `json:"sender"`
	Installation *github.Installation `json:"installation"`
}

// TeamEvent represents a GitHub team webhook payload.
type TeamEvent struct {
	Action       string               `json:"action"`
	Team         *github.Team         `json:"team"`
	Changes      *TeamChanges         `json:"changes,omitempty"`
	Repository   *github.Repository   `json:"repository,omitempty"`
	Organization *github.Organization `json:"organization"`
	Sender       *github.User         `json:"sender"`
	Installation *github.Installation `json:"installation"`
}

// TeamChanges contains details about what changed in a team event.
type TeamChanges struct {
	Name        *TeamChangeDetail `json:"name,omitempty"`
	Description *TeamChangeDetail `json:"description,omitempty"`
	Privacy     *TeamChangeDetail `json:"privacy,omitempty"`
	Repository  *TeamChangeDetail `json:"repository,omitempty"`
}

// TeamChangeDetail contains the previous value before a change.
type TeamChangeDetail struct {
	From string `json:"from"`
}

// MembershipEvent represents a GitHub membership webhook payload.
type MembershipEvent struct {
	Action       string               `json:"action"`
	Scope        string               `json:"scope"`
	Member       *github.User         `json:"member"`
	Team         *github.Team         `json:"team"`
	Organization *github.Organization `json:"organization"`
	Sender       *github.User         `json:"sender"`
	Installation *github.Installation `json:"installation"`
}

// ValidateWebhookSignature verifies HMAC-SHA256 webhook signature.
// returns error if signature is invalid or missing when required.
func ValidateWebhookSignature(payload []byte, signature string, secret string) error {
	if secret == "" {
		if signature != "" {
			return internalerrors.ErrUnexpectedSignature
		}
		return nil
	}

	if signature == "" {
		return internalerrors.ErrMissingSignature
	}

	if !strings.HasPrefix(signature, "sha256=") {
		return errors.Wrap(internalerrors.ErrInvalidSignature, "must start with 'sha256='")
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))
	expectedSignature := "sha256=" + expectedMAC

	if !hmac.Equal([]byte(signature), []byte(expectedSignature)) {
		return errors.Wrap(internalerrors.ErrInvalidSignature, "computed signature does not match")
	}

	return nil
}

// ParsePullRequestEvent unmarshals and validates a pull_request webhook.
// returns error if required fields are missing.
func ParsePullRequestEvent(payload []byte) (*PullRequestEvent, error) {
	var event PullRequestEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal pull request event")
	}
	if event.PullRequest == nil {
		return nil, errors.Wrap(internalerrors.ErrMissingPRData, "missing pull_request field")
	}
	if event.PullRequest.Number == nil {
		return nil, errors.Wrap(internalerrors.ErrMissingPRData, "missing pr number")
	}
	if event.PullRequest.Base == nil || event.PullRequest.Base.Ref == nil {
		return nil, errors.Wrap(internalerrors.ErrMissingPRData, "missing base branch")
	}
	if event.Repository == nil {
		return nil, errors.Wrap(internalerrors.ErrMissingPRData, "missing repository")
	}
	return &event, nil
}

// IsMerged returns true if the PR was closed via merge.
func (e *PullRequestEvent) IsMerged() bool {
	return e.Action == "closed" && e.PullRequest != nil && e.PullRequest.Merged != nil && *e.PullRequest.Merged
}

// GetBaseBranch returns the target branch name.
func (e *PullRequestEvent) GetBaseBranch() string {
	if e.PullRequest != nil && e.PullRequest.Base != nil && e.PullRequest.Base.Ref != nil {
		return *e.PullRequest.Base.Ref
	}
	return ""
}

// GetRepoFullName returns the repository in owner/name format.
func (e *PullRequestEvent) GetRepoFullName() string {
	if e.Repository != nil && e.Repository.FullName != nil {
		return *e.Repository.FullName
	}
	return ""
}

// GetRepoOwner returns the repository owner login.
func (e *PullRequestEvent) GetRepoOwner() string {
	if e.Repository != nil && e.Repository.Owner != nil && e.Repository.Owner.Login != nil {
		return *e.Repository.Owner.Login
	}
	return ""
}

// GetRepoName returns the repository name without owner.
func (e *PullRequestEvent) GetRepoName() string {
	if e.Repository != nil && e.Repository.Name != nil {
		return *e.Repository.Name
	}
	return ""
}

// GetInstallationID returns the GitHub App installation ID.
func (e *PullRequestEvent) GetInstallationID() int64 {
	if e.Installation != nil && e.Installation.ID != nil {
		return *e.Installation.ID
	}
	return 0
}

// ParseTeamEvent unmarshals and validates a team webhook.
func ParseTeamEvent(payload []byte) (*TeamEvent, error) {
	var event TeamEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal team event")
	}
	if event.Team == nil {
		return nil, errors.New("missing team field in event")
	}
	if event.Sender == nil {
		return nil, errors.New("missing sender field in event")
	}
	return &event, nil
}

// ParseMembershipEvent unmarshals and validates a membership webhook.
func ParseMembershipEvent(payload []byte) (*MembershipEvent, error) {
	var event MembershipEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, errors.Wrap(err, "failed to unmarshal membership event")
	}
	if event.Team == nil {
		return nil, errors.New("missing team field in event")
	}
	if event.Member == nil {
		return nil, errors.New("missing member field in event")
	}
	if event.Sender == nil {
		return nil, errors.New("missing sender field in event")
	}
	return &event, nil
}

// GetInstallationID returns the GitHub App installation ID.
func (e *TeamEvent) GetInstallationID() int64 {
	if e.Installation != nil && e.Installation.ID != nil {
		return *e.Installation.ID
	}
	return 0
}

// GetTeamSlug returns the team's URL-friendly identifier.
func (e *TeamEvent) GetTeamSlug() string {
	if e.Team != nil && e.Team.Slug != nil {
		return *e.Team.Slug
	}
	return ""
}

// GetSenderLogin returns the username of the user who triggered the event.
func (e *TeamEvent) GetSenderLogin() string {
	if e.Sender != nil && e.Sender.Login != nil {
		return *e.Sender.Login
	}
	return ""
}

// GetSenderType returns the sender's type (User or Bot).
func (e *TeamEvent) GetSenderType() string {
	if e.Sender != nil && e.Sender.Type != nil {
		return *e.Sender.Type
	}
	return ""
}

// GetInstallationID returns the GitHub App installation ID.
func (e *MembershipEvent) GetInstallationID() int64 {
	if e.Installation != nil && e.Installation.ID != nil {
		return *e.Installation.ID
	}
	return 0
}

// GetTeamSlug returns the team's URL-friendly identifier.
func (e *MembershipEvent) GetTeamSlug() string {
	if e.Team != nil && e.Team.Slug != nil {
		return *e.Team.Slug
	}
	return ""
}

// GetSenderLogin returns the username of the user who triggered the event.
func (e *MembershipEvent) GetSenderLogin() string {
	if e.Sender != nil && e.Sender.Login != nil {
		return *e.Sender.Login
	}
	return ""
}

// GetSenderType returns the sender's type (User or Bot).
func (e *MembershipEvent) GetSenderType() string {
	if e.Sender != nil && e.Sender.Type != nil {
		return *e.Sender.Type
	}
	return ""
}

// IsTeamScope returns true if the membership event is for a team.
func (e *MembershipEvent) IsTeamScope() bool {
	return e.Scope == "team"
}
