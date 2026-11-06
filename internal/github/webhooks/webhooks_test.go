package webhooks

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

func TestValidateSignature(t *testing.T) {
	payload := []byte(`{"test": true}`)
	tests := []struct {
		name      string
		payload   []byte
		signature string
		secret    string
		wantErr   error
	}{
		{"valid", payload, Sign(payload, "s3cret"), "s3cret", nil},
		{"valid empty payload", []byte{}, Sign([]byte{}, "key"), "key", nil},
		{"no secret configured", payload, "", "", domain.ErrConfig},
		{"no secret configured with signature", payload, Sign(payload, "x"), "", domain.ErrConfig},
		{"missing signature", payload, "", "s3cret", domain.ErrMissingSignature},
		{"wrong prefix", payload, "sha1=abc123", "s3cret", domain.ErrInvalidSignature},
		{"not hex", payload, "sha256=zz", "s3cret", domain.ErrInvalidSignature},
		{"truncated", payload, "sha256=abcd", "s3cret", domain.ErrInvalidSignature},
		{"wrong secret", payload, Sign(payload, "other"), "s3cret", domain.ErrInvalidSignature},
		{"tampered payload", []byte(`{"test": false}`), Sign(payload, "s3cret"), "s3cret", domain.ErrInvalidSignature},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSignature(tt.payload, tt.signature, tt.secret)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func prPayload() map[string]any {
	return map[string]any{
		"action": "closed",
		"number": 42,
		"pull_request": map[string]any{
			"number": 42,
			"merged": true,
			"base":   map[string]any{"ref": "main"},
		},
		"repository": map[string]any{
			"name":      "web",
			"full_name": "acme/web",
			"owner":     map[string]any{"login": "acme"},
		},
		"sender": map[string]any{"login": "alice", "type": "User"},
	}
}

func TestParsePullRequestEvent(t *testing.T) {
	event, err := ParsePullRequestEvent(mustJSON(t, prPayload()))
	if err != nil {
		t.Fatal(err)
	}
	if !event.IsMerged() || event.GetNumber() != 42 || event.GetBaseBranch() != "main" {
		t.Errorf("event = merged %v, #%d, %q", event.IsMerged(), event.GetNumber(), event.GetBaseBranch())
	}
	if event.GetRepoOwner() != "acme" || event.GetRepoName() != "web" || event.GetRepoFullName() != "acme/web" {
		t.Errorf("repo = %q %q %q", event.GetRepoOwner(), event.GetRepoName(), event.GetRepoFullName())
	}
	if event.GetSenderLogin() != "alice" || event.GetSenderType() != "User" {
		t.Errorf("sender = %q %q", event.GetSenderLogin(), event.GetSenderType())
	}
}

func TestParsePullRequestEvent_Invalid(t *testing.T) {
	tests := map[string]func(map[string]any){
		"missing pull_request": func(p map[string]any) { delete(p, "pull_request") },
		"missing number":       func(p map[string]any) { delete(p["pull_request"].(map[string]any), "number") },
		"missing base":         func(p map[string]any) { delete(p["pull_request"].(map[string]any), "base") },
		"missing repository":   func(p map[string]any) { delete(p, "repository") },
		"missing repo owner":   func(p map[string]any) { delete(p["repository"].(map[string]any), "owner") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			p := prPayload()
			mutate(p)
			_, err := ParsePullRequestEvent(mustJSON(t, p))
			if !errors.Is(err, domain.ErrInvalidPayload) || !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("error = %v, want an invalid payload validation error", err)
			}
		})
	}

	t.Run("invalid json", func(t *testing.T) {
		_, err := ParsePullRequestEvent([]byte(`{`))
		if !errors.Is(err, domain.ErrValidation) {
			t.Fatalf("error = %v, want a validation error", err)
		}
	})
}

func TestPullRequestEvent_IsMerged(t *testing.T) {
	tests := []struct {
		action string
		merged bool
		want   bool
	}{
		{"closed", true, true},
		{"closed", false, false},
		{"opened", false, false},
		{"synchronize", true, false},
	}
	for _, tt := range tests {
		p := prPayload()
		p["action"] = tt.action
		p["pull_request"].(map[string]any)["merged"] = tt.merged
		event, err := ParsePullRequestEvent(mustJSON(t, p))
		if err != nil {
			t.Fatal(err)
		}
		if event.IsMerged() != tt.want {
			t.Errorf("action %q merged %v: IsMerged = %v", tt.action, tt.merged, !tt.want)
		}
	}
}

func teamPayload(action string) map[string]any {
	return map[string]any{
		"action":       action,
		"team":         map[string]any{"slug": "eng"},
		"organization": map[string]any{"login": "acme"},
		"sender":       map[string]any{"login": "bob", "type": "User"},
	}
}

func TestParseTeamEvent(t *testing.T) {
	event, err := ParseTeamEvent(mustJSON(t, teamPayload("created")))
	if err != nil {
		t.Fatal(err)
	}
	if event.GetTeamSlug() != "eng" || event.GetOrgLogin() != "acme" || event.GetSenderLogin() != "bob" {
		t.Errorf("event = %q %q %q", event.GetTeamSlug(), event.GetOrgLogin(), event.GetSenderLogin())
	}

	for _, field := range []string{"team", "sender", "organization"} {
		p := teamPayload("created")
		delete(p, field)
		if _, err := ParseTeamEvent(mustJSON(t, p)); !errors.Is(err, domain.ErrInvalidPayload) {
			t.Errorf("missing %s: error = %v, want ErrInvalidPayload", field, err)
		}
	}
}

func TestTeamEvent_AffectsMembership(t *testing.T) {
	tests := []struct {
		action  string
		changes map[string]any
		want    bool
	}{
		{"created", nil, true},
		{"deleted", nil, true},
		{"edited", map[string]any{"name": map[string]any{"from": "Old Eng"}}, true},
		{"edited", map[string]any{"privacy": map[string]any{"from": "secret"}}, false},
		{"edited", map[string]any{"repository": map[string]any{"permissions": map[string]any{"from": map[string]any{"push": false}}}}, false},
		{"added_to_repository", nil, false},
		{"removed_from_repository", nil, false},
	}
	for _, tt := range tests {
		p := teamPayload(tt.action)
		if tt.changes != nil {
			p["changes"] = tt.changes
		}
		event, err := ParseTeamEvent(mustJSON(t, p))
		if err != nil {
			t.Fatal(err)
		}
		if got := event.AffectsMembership(); got != tt.want {
			t.Errorf("%s %v: AffectsMembership = %v, want %v", tt.action, tt.changes, got, tt.want)
		}
	}

	p := teamPayload("edited")
	p["changes"] = map[string]any{"name": map[string]any{"from": "Old Eng"}}
	event, err := ParseTeamEvent(mustJSON(t, p))
	if err != nil {
		t.Fatal(err)
	}
	if got := event.GetPreviousTeamSlug(); got != "old-eng" {
		t.Errorf("GetPreviousTeamSlug = %q, want old-eng", got)
	}
}

func membershipPayload(scope string) map[string]any {
	return map[string]any{
		"action":       "added",
		"scope":        scope,
		"member":       map[string]any{"login": "carol"},
		"team":         map[string]any{"slug": "eng"},
		"organization": map[string]any{"login": "acme"},
		"sender":       map[string]any{"login": "bob", "type": "User"},
	}
}

func TestParseMembershipEvent(t *testing.T) {
	event, err := ParseMembershipEvent(mustJSON(t, membershipPayload("team")))
	if err != nil {
		t.Fatal(err)
	}
	if !event.IsTeamScope() || event.GetTeamSlug() != "eng" || event.GetOrgLogin() != "acme" {
		t.Errorf("event = %v %q %q", event.IsTeamScope(), event.GetTeamSlug(), event.GetOrgLogin())
	}

	other, err := ParseMembershipEvent(mustJSON(t, membershipPayload("organization")))
	if err != nil {
		t.Fatal(err)
	}
	if other.IsTeamScope() {
		t.Error("organization scope reported as team scope")
	}

	for _, field := range []string{"team", "member", "sender", "organization"} {
		p := membershipPayload("team")
		delete(p, field)
		if _, err := ParseMembershipEvent(mustJSON(t, p)); !errors.Is(err, domain.ErrInvalidPayload) {
			t.Errorf("missing %s: error = %v, want ErrInvalidPayload", field, err)
		}
	}
}
