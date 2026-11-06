package notifiers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// posted is a message the fake Slack API received.
type posted struct {
	Channel string
	Text    string
	Blocks  []map[string]any
}

// allText returns the text of every block, joined.
func (p posted) allText() string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(p.Blocks)
	return b.String()
}

func fakeSlack(t *testing.T) (*SlackNotifier, func() []posted) {
	t.Helper()
	var msgs []posted
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		p := posted{Channel: r.PostForm.Get("channel"), Text: r.PostForm.Get("text")}
		if err := json.Unmarshal([]byte(r.PostForm.Get("blocks")), &p.Blocks); err != nil {
			t.Errorf("decode blocks: %v", err)
		}
		msgs = append(msgs, p)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C1","ts":"1.2"}`))
	}))
	t.Cleanup(srv.Close)

	n := New(Options{
		Token:        "xoxb-test",
		Channels:     SlackChannels{Default: "C_DEFAULT", OktaSync: "C_SYNC"},
		Messages:     SlackMessages{PRBypassFooterNote: "_see <https://example.com|policy>_"},
		APIURL:       srv.URL + "/",
		GitHubWebURL: "https://ghe.example.com/",
	})
	return n, func() []posted { return msgs }
}

// checkLimits fails the test if a message breaks Slack's limits.
func checkLimits(t *testing.T, p posted) {
	t.Helper()
	if len(p.Blocks) > maxBlocks {
		t.Errorf("message has %d blocks, limit is %d", len(p.Blocks), maxBlocks)
	}
	for _, b := range p.Blocks {
		if text, ok := b["text"].(map[string]any); ok {
			if s, _ := text["text"].(string); len(s) > maxSectionText {
				t.Errorf("section text is %d chars, limit is %d", len(s), maxSectionText)
			}
		}
	}
}

func TestEscape(t *testing.T) {
	got := escape("<!channel> & <https://evil.example|click>")
	want := "&lt;!channel&gt; &amp; &lt;https://evil.example|click&gt;"
	if got != want {
		t.Errorf("escape = %q, want %q", got, want)
	}
}

func TestNotifyPRBypass(t *testing.T) {
	n, msgs := fakeSlack(t)
	result := &domain.PRComplianceResult{
		PR: &github.PullRequest{
			Number:   new(42),
			Title:    new("<!channel> urgent <https://evil.example|fix>"),
			HTMLURL:  new("https://ghe.example.com/acme/web/pull/42"),
			MergedBy: &github.User{Login: new("mallory")},
		},
		BaseBranch:       "main",
		UserHasBypass:    true,
		UserBypassReason: "repository admin",
		Violations:       []domain.ComplianceViolation{{Description: "required check 'ci' did not pass"}},
	}

	if err := n.NotifyPRBypass(context.Background(), result, "acme/web"); err != nil {
		t.Fatal(err)
	}
	p := msgs()[0]
	text := p.allText()
	if p.Channel != "C_DEFAULT" {
		t.Errorf("channel = %q, want the default", p.Channel)
	}
	if strings.Contains(text, "<!channel>") || strings.Contains(text, "<https://evil.example") {
		t.Errorf("unescaped pr title in message: %s", text)
	}
	for _, want := range []string{"mallory (repository admin)", "required check 'ci' did not pass", "<https://example.com|policy>"} {
		if !strings.Contains(text, want) {
			t.Errorf("message lacks %q: %s", want, text)
		}
	}
}

func TestNotifyPRBypass_MissingPR(t *testing.T) {
	n, _ := fakeSlack(t)
	if err := n.NotifyPRBypass(context.Background(), &domain.PRComplianceResult{}, "acme/web"); err == nil {
		t.Fatal("NotifyPRBypass succeeded without a pr")
	}
}

func TestNotifyOktaSync(t *testing.T) {
	n, msgs := fakeSlack(t)
	reports := []*domain.SyncReport{
		{Rule: "eng", GitHubTeam: "eng", MembersAdded: []string{"alice"}, MembersRemoved: []string{"bob"}},
		{Rule: "ops", GitHubTeam: "ops"},
		{Rule: "<!here>", GitHubTeam: "sec", Errors: []string{"boom <@U123>"}, MembersSkippedNoGHUsername: []string{"new@x.com"}},
	}

	if err := n.NotifyOktaSync(context.Background(), reports, "acme"); err != nil {
		t.Fatal(err)
	}
	p := msgs()[0]
	text := p.allText()
	checkLimits(t, p)
	if p.Channel != "C_SYNC" {
		t.Errorf("channel = %q, want the okta sync channel", p.Channel)
	}
	for _, want := range []string{
		"<https://ghe.example.com/orgs/acme/teams/eng|eng> (+1, -1)",
		"<https://ghe.example.com/orgs/acme/teams/ops|ops>",
		"&lt;!here&gt;: boom &lt;@U123&gt;",
		"new@x.com",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("message lacks %q: %s", want, text)
		}
	}
	if n.NotifyOktaSync(context.Background(), nil, "acme") != nil || len(msgs()) != 1 {
		t.Error("empty reports posted a message")
	}
}

func TestNotifyOktaSync_LargeReportFitsSlackLimits(t *testing.T) {
	n, msgs := fakeSlack(t)
	var reports []*domain.SyncReport
	for i := range 400 {
		r := &domain.SyncReport{Rule: fmt.Sprint("rule-", i), GitHubTeam: fmt.Sprintf("team-with-a-long-name-%03d", i)}
		if i%2 == 0 {
			r.MembersAdded = []string{"someone"}
			r.Errors = []string{strings.Repeat("x", 200)}
		}
		reports = append(reports, r)
	}

	if err := n.NotifyOktaSync(context.Background(), reports, "acme"); err != nil {
		t.Fatal(err)
	}
	p := msgs()[0]
	checkLimits(t, p)
	if !strings.Contains(p.allText(), "...and 100 more") {
		t.Error("long list was not summarized")
	}
}

func TestNotifyOrphanedUsers(t *testing.T) {
	n, msgs := fakeSlack(t)
	if err := n.NotifyOrphanedUsers(context.Background(), &domain.OrphanedUsersReport{}); err != nil || len(msgs()) != 0 {
		t.Fatalf("empty report: err %v, %d messages", err, len(msgs()))
	}

	var users []string
	for i := range 1000 {
		users = append(users, fmt.Sprintf("user-%04d", i))
	}
	if err := n.NotifyOrphanedUsers(context.Background(), &domain.OrphanedUsersReport{OrphanedUsers: users}); err != nil {
		t.Fatal(err)
	}
	p := msgs()[0]
	checkLimits(t, p)
	if !strings.Contains(p.allText(), "*1000* organization member(s)") || !strings.Contains(p.allText(), "user-0000") {
		t.Errorf("message = %s", p.allText())
	}
}

func TestNotifySecurityAlerts(t *testing.T) {
	n, msgs := fakeSlack(t)
	if err := n.NotifySecurityAlerts(context.Background(), &domain.SecurityAlertsReport{}, "acme"); err != nil || len(msgs()) != 0 {
		t.Fatalf("empty report: err %v, %d messages", err, len(msgs()))
	}

	report := &domain.SecurityAlertsReport{
		MinAgeDays:  30,
		MinSeverity: "high",
		TotalAlerts: 3,
		AlertsByRepo: map[string][]domain.SecurityAlert{
			"acme/api": {{Severity: "high"}},
			"acme/web": {{Severity: "high"}, {Severity: "critical", CreatedAt: time.Now()}},
		},
		Errors: []string{"failed to fetch secret scanning alerts"},
	}
	if err := n.NotifySecurityAlerts(context.Background(), report, "acme"); err != nil {
		t.Fatal(err)
	}
	text := msgs()[0].allText()
	web := strings.Index(text, "|acme/web>")
	api := strings.Index(text, "|acme/api>")
	if web < 0 || api < 0 || web > api {
		t.Errorf("repos not listed by alert count: %s", text)
	}
	for _, want := range []string{"https://ghe.example.com/acme/web/security", "2 alert(s), critical severity", "secret scanning"} {
		if !strings.Contains(text, want) {
			t.Errorf("message lacks %q: %s", want, text)
		}
	}
}

func TestNotifySecurityAlerts_ErrorsOnly(t *testing.T) {
	n, msgs := fakeSlack(t)
	report := &domain.SecurityAlertsReport{AlertsByRepo: map[string][]domain.SecurityAlert{}, Errors: []string{"forbidden"}}
	if err := n.NotifySecurityAlerts(context.Background(), report, "acme"); err != nil {
		t.Fatal(err)
	}
	if len(msgs()) != 1 || !strings.Contains(msgs()[0].allText(), "forbidden") {
		t.Error("report with only errors was not posted")
	}
}

func TestChannelFor(t *testing.T) {
	n := New(Options{Channels: SlackChannels{Default: "C_DEFAULT", SecurityAlerts: "C_SEC"}})
	if got := n.channelFor(n.channels.SecurityAlerts); got != "C_SEC" {
		t.Errorf("channelFor(set) = %q", got)
	}
	if got := n.channelFor(n.channels.PRBypass); got != "C_DEFAULT" {
		t.Errorf("channelFor(unset) = %q, want the default", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("héllo", 2); got != "h" {
		t.Errorf("truncateRunes split a rune: %q", got)
	}
	if got := truncateRunes("abc", 10); got != "abc" {
		t.Errorf("truncateRunes = %q", got)
	}
}
