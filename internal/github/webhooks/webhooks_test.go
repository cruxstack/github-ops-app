package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/google/go-github/v79/github"
)

func TestValidateWebhookSignature(t *testing.T) {
	tests := []struct {
		name      string
		payload   []byte
		signature string
		secret    string
		wantErr   error
	}{
		{
			name:      "no secret, no signature",
			payload:   []byte(`{"test": true}`),
			signature: "",
			secret:    "",
			wantErr:   nil,
		},
		{
			name:      "no secret, unexpected signature",
			payload:   []byte(`{"test": true}`),
			signature: "sha256=abc",
			secret:    "",
			wantErr:   domain.ErrUnexpectedSignature,
		},
		{
			name:      "secret configured, missing signature",
			payload:   []byte(`{"test": true}`),
			signature: "",
			secret:    "mysecret",
			wantErr:   domain.ErrMissingSignature,
		},
		{
			name:      "wrong prefix",
			payload:   []byte(`{"test": true}`),
			signature: "sha1=abc123",
			secret:    "mysecret",
			wantErr:   domain.ErrInvalidSignature,
		},
		{
			name:      "wrong signature value",
			payload:   []byte(`{"test": true}`),
			signature: "sha256=0000000000000000000000000000000000000000000000000000000000000000",
			secret:    "mysecret",
			wantErr:   domain.ErrInvalidSignature,
		},
		{
			name:      "valid signature",
			payload:   []byte(`{}`),
			signature: computeSignature([]byte(`{}`), "test-secret"),
			secret:    "test-secret",
			wantErr:   nil,
		},
		{
			name:      "empty payload with valid signature",
			payload:   []byte{},
			signature: computeSignature([]byte{}, "key"),
			secret:    "key",
			wantErr:   nil,
		},
		{
			name:      "truncated signature",
			payload:   []byte(`test`),
			signature: "sha256=abc",
			secret:    "mysecret",
			wantErr:   domain.ErrInvalidSignature,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateWebhookSignature(tt.payload, tt.signature, tt.secret)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("expected no error, got: %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error %v, got nil", tt.wantErr)
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("expected error %v, got: %v", tt.wantErr, err)
			}
		})
	}
}

// computeSignature generates a valid HMAC-SHA256 signature for testing.
func computeSignature(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestParsePullRequestEvent_Valid(t *testing.T) {
	prNumber := 42
	baseBranch := "main"
	payload := map[string]any{
		"action": "closed",
		"number": prNumber,
		"pull_request": map[string]any{
			"number": prNumber,
			"merged": true,
			"base":   map[string]any{"ref": baseBranch},
		},
		"repository": map[string]any{
			"name":      "test-repo",
			"full_name": "owner/test-repo",
			"owner":     map[string]any{"login": "owner"},
		},
	}

	data, _ := json.Marshal(payload)
	event, err := ParsePullRequestEvent(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if event.Number != prNumber {
		t.Errorf("expected number %d, got %d", prNumber, event.Number)
	}
	if !event.IsMerged() {
		t.Error("expected IsMerged() to return true")
	}
	if event.GetBaseBranch() != baseBranch {
		t.Errorf("expected base branch %q, got %q", baseBranch, event.GetBaseBranch())
	}
}

func TestParsePullRequestEvent_MissingFields(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{
			name:    "missing pull_request",
			payload: map[string]any{"action": "closed"},
		},
		{
			name: "missing pr number",
			payload: map[string]any{
				"action": "closed",
				"pull_request": map[string]any{
					"base": map[string]any{"ref": "main"},
				},
				"repository": map[string]any{},
			},
		},
		{
			name: "missing base branch",
			payload: map[string]any{
				"action": "closed",
				"pull_request": map[string]any{
					"number": 1,
				},
				"repository": map[string]any{},
			},
		},
		{
			name: "missing repository",
			payload: map[string]any{
				"action": "closed",
				"pull_request": map[string]any{
					"number": 1,
					"base":   map[string]any{"ref": "main"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.payload)
			_, err := ParsePullRequestEvent(data)
			if err == nil {
				t.Error("expected error for missing fields")
			}
			if !errors.Is(err, domain.ErrMissingPRData) {
				t.Errorf("expected ErrMissingPRData, got: %v", err)
			}
		})
	}
}

func TestParsePullRequestEvent_InvalidJSON(t *testing.T) {
	_, err := ParsePullRequestEvent([]byte(`{invalid`))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseTeamEvent_Valid(t *testing.T) {
	payload := map[string]any{
		"action": "edited",
		"team":   map[string]any{"slug": "engineering"},
		"sender": map[string]any{"login": "user1", "type": "User"},
	}

	data, _ := json.Marshal(payload)
	event, err := ParseTeamEvent(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if event.Action != "edited" {
		t.Errorf("expected action 'edited', got %q", event.Action)
	}
	if event.GetTeamSlug() != "engineering" {
		t.Errorf("expected team slug 'engineering', got %q", event.GetTeamSlug())
	}
	if event.GetSenderType() != "User" {
		t.Errorf("expected sender type 'User', got %q", event.GetSenderType())
	}
}

func TestParseTeamEvent_MissingFields(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{
			name:    "missing team",
			payload: map[string]any{"action": "edited", "sender": map[string]any{"login": "u1"}},
		},
		{
			name:    "missing sender",
			payload: map[string]any{"action": "edited", "team": map[string]any{"slug": "t1"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.payload)
			_, err := ParseTeamEvent(data)
			if err == nil {
				t.Error("expected error for missing fields")
			}
		})
	}
}

func TestParseMembershipEvent_Valid(t *testing.T) {
	payload := map[string]any{
		"action": "added",
		"scope":  "team",
		"member": map[string]any{"login": "newuser"},
		"team":   map[string]any{"slug": "engineering"},
		"sender": map[string]any{"login": "admin", "type": "User"},
	}

	data, _ := json.Marshal(payload)
	event, err := ParseMembershipEvent(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !event.IsTeamScope() {
		t.Error("expected IsTeamScope() to return true")
	}
	if event.GetSenderLogin() != "admin" {
		t.Errorf("expected sender login 'admin', got %q", event.GetSenderLogin())
	}
}

func TestParseMembershipEvent_MissingFields(t *testing.T) {
	tests := []struct {
		name    string
		payload map[string]any
	}{
		{
			name:    "missing team",
			payload: map[string]any{"action": "added", "member": map[string]any{"login": "u"}, "sender": map[string]any{"login": "s"}},
		},
		{
			name:    "missing member",
			payload: map[string]any{"action": "added", "team": map[string]any{"slug": "t"}, "sender": map[string]any{"login": "s"}},
		},
		{
			name:    "missing sender",
			payload: map[string]any{"action": "added", "team": map[string]any{"slug": "t"}, "member": map[string]any{"login": "u"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.payload)
			_, err := ParseMembershipEvent(data)
			if err == nil {
				t.Error("expected error for missing fields")
			}
		})
	}
}

func TestPullRequestEvent_IsMerged(t *testing.T) {
	merged := true
	notMerged := false

	tests := []struct {
		name   string
		action string
		merged *bool
		want   bool
	}{
		{"closed and merged", "closed", &merged, true},
		{"closed not merged", "closed", &notMerged, false},
		{"opened", "opened", nil, false},
		{"closed with nil merged", "closed", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := &PullRequestEvent{
				Action: tt.action,
				PullRequest: &github.PullRequest{
					Merged: tt.merged,
				},
			}
			if got := event.IsMerged(); got != tt.want {
				t.Errorf("IsMerged() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMembershipEvent_IsTeamScope(t *testing.T) {
	tests := []struct {
		scope string
		want  bool
	}{
		{"team", true},
		{"organization", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.scope, func(t *testing.T) {
			event := &MembershipEvent{Scope: tt.scope}
			if got := event.IsTeamScope(); got != tt.want {
				t.Errorf("IsTeamScope() = %v, want %v", got, tt.want)
			}
		})
	}
}
