package notifiers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/google/go-github/v79/github"
)

// slackTestServer creates a mock Slack API server that captures posted
// messages and returns them via the messages channel.
func slackTestServer(t *testing.T) (*httptest.Server, chan map[string]any) {
	t.Helper()
	messages := make(chan map[string]any, 10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()

		// parse form-encoded body from slack client
		var msg map[string]any
		if strings.Contains(r.Header.Get("Content-Type"), "json") {
			json.Unmarshal(body, &msg)
		} else {
			msg = map[string]any{"raw_body": string(body), "path": r.URL.Path}
		}

		messages <- msg

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true,"channel":"C123","ts":"1234.5678"}`))
	}))

	return srv, messages
}

func TestNotifyPRBypass_Success(t *testing.T) {
	srv, messages := slackTestServer(t)
	defer srv.Close()

	notifier := NewSlackNotifierWithAPIURL(
		"xoxb-test",
		SlackChannels{Default: "C_DEFAULT"},
		SlackMessages{PRBypassFooterNote: "review this"},
		srv.URL+"/",
	)

	prNumber := 42
	prTitle := "fix: patch auth"
	prURL := "https://github.com/org/repo/pull/42"
	mergedBy := "admin-user"
	result := &domain.PRComplianceResult{
		PR: &github.PullRequest{
			Number:   &prNumber,
			Title:    &prTitle,
			HTMLURL:  &prURL,
			MergedBy: &github.User{Login: &mergedBy},
		},
		UserHasBypass:    true,
		UserBypassReason: "repository admin",
		Violations: []domain.ComplianceViolation{
			{Type: "insufficient_reviews", Description: "required 2, had 0"},
		},
	}

	err := notifier.NotifyPRBypass(context.Background(), result, "org/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg := <-messages
	if msg == nil {
		t.Fatal("expected a message to be posted")
	}
}

func TestNotifyPRBypass_NilPR(t *testing.T) {
	notifier := NewSlackNotifier("xoxb-test", SlackChannels{Default: "C"}, SlackMessages{})

	result := &domain.PRComplianceResult{PR: nil}
	err := notifier.NotifyPRBypass(context.Background(), result, "org/repo")
	if err == nil {
		t.Fatal("expected error for nil PR")
	}
}

func TestNotifyOktaSync_EmptyReports(t *testing.T) {
	notifier := NewSlackNotifier("xoxb-test", SlackChannels{Default: "C"}, SlackMessages{})

	err := notifier.NotifyOktaSync(context.Background(), nil, "org")
	if err != nil {
		t.Fatalf("expected nil for empty reports, got: %v", err)
	}

	err = notifier.NotifyOktaSync(context.Background(), []*domain.SyncReport{}, "org")
	if err != nil {
		t.Fatalf("expected nil for zero reports, got: %v", err)
	}
}

func TestNotifyOktaSync_Success(t *testing.T) {
	srv, messages := slackTestServer(t)
	defer srv.Close()

	notifier := NewSlackNotifierWithAPIURL(
		"xoxb-test",
		SlackChannels{Default: "C_DEFAULT", OktaSync: "C_OKTA"},
		SlackMessages{},
		srv.URL+"/",
	)

	reports := []*domain.SyncReport{
		{
			Rule: "eng", OktaGroup: "Engineering", GitHubTeam: "engineering",
			MembersAdded: []string{"alice"}, MembersRemoved: []string{"bob"},
		},
		{
			Rule: "platform", OktaGroup: "Platform", GitHubTeam: "platform",
		},
	}

	err := notifier.NotifyOktaSync(context.Background(), reports, "my-org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg := <-messages
	if msg == nil {
		t.Fatal("expected a message to be posted")
	}
}

func TestNotifyOrphanedUsers_NilReport(t *testing.T) {
	notifier := NewSlackNotifier("xoxb-test", SlackChannels{Default: "C"}, SlackMessages{})

	err := notifier.NotifyOrphanedUsers(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected nil for nil report, got: %v", err)
	}
}

func TestNotifyOrphanedUsers_EmptyUsers(t *testing.T) {
	notifier := NewSlackNotifier("xoxb-test", SlackChannels{Default: "C"}, SlackMessages{})

	err := notifier.NotifyOrphanedUsers(context.Background(), &domain.OrphanedUsersReport{})
	if err != nil {
		t.Fatalf("expected nil for empty users, got: %v", err)
	}
}

func TestNotifyOrphanedUsers_Success(t *testing.T) {
	srv, messages := slackTestServer(t)
	defer srv.Close()

	notifier := NewSlackNotifierWithAPIURL(
		"xoxb-test",
		SlackChannels{Default: "C_DEFAULT"},
		SlackMessages{},
		srv.URL+"/",
	)

	report := &domain.OrphanedUsersReport{
		OrphanedUsers: []string{"orphan-1", "orphan-2"},
	}

	err := notifier.NotifyOrphanedUsers(context.Background(), report)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg := <-messages
	if msg == nil {
		t.Fatal("expected a message to be posted")
	}
}

func TestNotifyOktaSync_WithErrors(t *testing.T) {
	srv, messages := slackTestServer(t)
	defer srv.Close()

	notifier := NewSlackNotifierWithAPIURL(
		"xoxb-test",
		SlackChannels{Default: "C"},
		SlackMessages{},
		srv.URL+"/",
	)

	reports := []*domain.SyncReport{
		{
			Rule: "eng", OktaGroup: "Engineering", GitHubTeam: "engineering",
			MembersAdded:               []string{"alice"},
			Errors:                     []string{"rate limited"},
			MembersSkippedExternal:     []string{"external-1"},
			MembersSkippedNoGHUsername: []string{"newhire@co.com"},
		},
	}

	err := notifier.NotifyOktaSync(context.Background(), reports, "org")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	msg := <-messages
	if msg == nil {
		t.Fatal("expected a message")
	}
}

func TestChannelFor_CustomChannels(t *testing.T) {
	n := &SlackNotifier{
		channels: SlackChannels{
			Default:       "C_DEFAULT",
			PRBypass:      "C_PR",
			OktaSync:      "C_OKTA",
			OrphanedUsers: "C_ORPHAN",
		},
	}

	if got := n.channelFor(n.channels.PRBypass); got != "C_PR" {
		t.Errorf("expected C_PR, got %s", got)
	}
	if got := n.channelFor(n.channels.OktaSync); got != "C_OKTA" {
		t.Errorf("expected C_OKTA, got %s", got)
	}
	if got := n.channelFor(n.channels.OrphanedUsers); got != "C_ORPHAN" {
		t.Errorf("expected C_ORPHAN, got %s", got)
	}
}
