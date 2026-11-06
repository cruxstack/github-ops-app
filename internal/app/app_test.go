package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-github/v93/github"

	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/cruxstack/github-ops-app/internal/fakes"
	"github.com/cruxstack/github-ops-app/internal/github/webhooks"
)

const (
	testSecret     = "webhook-secret"
	testAdminToken = "admin-token-0123456789"
)

type harness struct {
	app      *App
	github   *fakes.GitHub
	okta     *fakes.Okta
	notifier *fakes.Notifier
}

func testConfig() *config.Config {
	return &config.Config{
		AdminToken: testAdminToken,
		GitHub: config.GitHub{
			Org:           "acme",
			AppID:         1,
			WebhookSecret: testSecret,
			WebURL:        config.DefaultGitHubWebURL,
		},
		PRCompliance:   config.PRCompliance{Enabled: true, MonitoredBranches: []string{"main", "release/*"}},
		Okta:           config.Okta{Enabled: true, SyncRules: []domain.SyncRule{{OktaGroupName: "eng"}}, SyncSafetyThreshold: 0.5, OrphanedUserNotifications: true},
		SecurityAlerts: config.SecurityAlerts{Enabled: true, MinAgeDays: 30, MinSeverity: "high"},
	}
}

func newHarness(t *testing.T, cfg *config.Config, withOkta bool) *harness {
	t.Helper()
	h := &harness{github: &fakes.GitHub{}, okta: &fakes.Okta{}, notifier: &fakes.Notifier{}}
	opts := []Option{WithGitHubClient(h.github), WithNotifier(h.notifier)}
	if withOkta {
		opts = append(opts, WithOktaClient(h.okta))
	} else {
		cfg.Okta.Enabled = false
	}
	a, err := New(cfg, slog.New(slog.DiscardHandler), opts...)
	if err != nil {
		t.Fatal(err)
	}
	h.app = a
	return h
}

func (h *harness) do(t *testing.T, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(rec, req)
	return rec
}

func webhookRequest(t *testing.T, event string, payload any, secret string) *http.Request {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/webhooks", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	if secret != "" {
		req.Header.Set("X-Hub-Signature-256", webhooks.Sign(body, secret))
	}
	return req
}

func adminRequest(method, target, auth string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), method, target, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	return req
}

func mergedPR(org, branch string) map[string]any {
	return map[string]any{
		"action": "closed",
		"pull_request": map[string]any{
			"number": 7,
			"merged": true,
			"base":   map[string]any{"ref": branch},
		},
		"repository": map[string]any{"name": "web", "full_name": org + "/web", "owner": map[string]any{"login": org}},
		"sender":     map[string]any{"login": "alice", "type": "User"},
	}
}

func TestWebhook_Signature(t *testing.T) {
	h := newHarness(t, testConfig(), true)
	payload := map[string]any{"zen": "hi"}

	if rec := h.do(t, webhookRequest(t, "ping", payload, "")); rec.Code != http.StatusUnauthorized {
		t.Errorf("unsigned: status %d, want 401", rec.Code)
	}
	if rec := h.do(t, webhookRequest(t, "ping", payload, "wrong")); rec.Code != http.StatusUnauthorized {
		t.Errorf("bad signature: status %d, want 401", rec.Code)
	}
	if rec := h.do(t, webhookRequest(t, "ping", payload, testSecret)); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "pong") {
		t.Errorf("ping: status %d body %s, want 200 pong", rec.Code, rec.Body)
	}

	cfg := testConfig()
	cfg.GitHub.WebhookSecret = ""
	h = newHarness(t, cfg, true)
	if rec := h.do(t, webhookRequest(t, "ping", payload, "")); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no secret configured: status %d, want 503", rec.Code)
	}
}

func TestWebhook_UnhandledAndInvalid(t *testing.T) {
	h := newHarness(t, testConfig(), true)
	if rec := h.do(t, webhookRequest(t, "push", map[string]any{}, testSecret)); rec.Code != http.StatusAccepted {
		t.Errorf("unhandled event: status %d, want 202", rec.Code)
	}
	if rec := h.do(t, webhookRequest(t, "pull_request", map[string]any{"action": "closed"}, testSecret)); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid payload: status %d, want 400", rec.Code)
	}
}

func TestWebhook_PullRequest(t *testing.T) {
	bypassed := &domain.PRComplianceResult{
		PR:         &github.PullRequest{Number: new(7), MergedBy: &github.User{Login: new("mallory")}},
		Violations: []domain.ComplianceViolation{{Description: "required check 'ci' did not pass"}},
	}

	tests := []struct {
		name       string
		payload    map[string]any
		result     *domain.PRComplianceResult
		resultErr  error
		wantStatus int
		wantChecks int
		wantNotify int
	}{
		{"bypass is reported", mergedPR("acme", "main"), bypassed, nil, 200, 1, 1},
		{"glob branch is monitored", mergedPR("ACME", "release/1.0"), bypassed, nil, 200, 1, 1},
		{"compliant pr", mergedPR("acme", "main"), &domain.PRComplianceResult{}, nil, 200, 1, 0},
		{"unmonitored branch", mergedPR("acme", "dev"), bypassed, nil, 202, 0, 0},
		{"other org", mergedPR("evil", "main"), bypassed, nil, 202, 0, 0},
		{"api failure", mergedPR("acme", "main"), nil, errors.New("403 resource not accessible"), 502, 1, 0},
		{"app not installed", mergedPR("acme", "main"), nil, domain.ErrInstallationMissing, 503, 1, 0},
		{"not merged", func() map[string]any {
			p := mergedPR("acme", "main")
			p["pull_request"].(map[string]any)["merged"] = false
			return p
		}(), bypassed, nil, 202, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, testConfig(), true)
			h.github.CheckPRComplianceFn = func(context.Context, string, string, int) (*domain.PRComplianceResult, error) {
				return tt.result, tt.resultErr
			}
			rec := h.do(t, webhookRequest(t, "pull_request", tt.payload, testSecret))
			if rec.Code != tt.wantStatus {
				t.Errorf("status %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if got := h.github.Count("CheckPRCompliance"); got != tt.wantChecks {
				t.Errorf("compliance checks = %d, want %d", got, tt.wantChecks)
			}
			if got := len(h.notifier.PRBypasses); got != tt.wantNotify {
				t.Errorf("notifications = %d, want %d", got, tt.wantNotify)
			}
		})
	}
}

func teamEvent(event, action, senderLogin, senderType string) map[string]any {
	p := map[string]any{
		"action":       action,
		"team":         map[string]any{"slug": "eng"},
		"organization": map[string]any{"login": "acme"},
		"sender":       map[string]any{"login": senderLogin, "type": senderType},
	}
	if event == "membership" {
		p["scope"] = "team"
		p["member"] = map[string]any{"login": "carol"}
	}
	return p
}

func TestWebhook_TeamChangesTriggerSync(t *testing.T) {
	tests := []struct {
		name     string
		event    string
		payload  map[string]any
		withOkta bool
		wantSync bool
	}{
		{"human renames team", "team", func() map[string]any {
			p := teamEvent("team", "edited", "alice", "User")
			p["changes"] = map[string]any{"name": map[string]any{"from": "Eng"}}
			return p
		}(), true, true},
		{"human adds member", "membership", teamEvent("membership", "added", "alice", "User"), true, true},
		{"repository access change", "team", teamEvent("team", "added_to_repository", "alice", "User"), true, false},
		{"bot change", "membership", teamEvent("membership", "removed", "renovate[bot]", "Bot"), true, false},
		{"this app's change", "membership", teamEvent("membership", "removed", "ops-app[bot]", "User"), true, false},
		{"okta sync disabled", "team", teamEvent("team", "deleted", "alice", "User"), false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, testConfig(), tt.withOkta)
			rec := h.do(t, webhookRequest(t, tt.event, tt.payload, testSecret))
			if rec.Code >= 300 {
				t.Fatalf("status %d (%s)", rec.Code, rec.Body)
			}
			if synced := h.okta.Count("GetGroupInfo") > 0; synced != tt.wantSync {
				t.Errorf("synced = %v, want %v", synced, tt.wantSync)
			}
		})
	}
}

func TestAdminAuth(t *testing.T) {
	h := newHarness(t, testConfig(), true)
	tests := []struct {
		auth string
		want int
	}{
		{"", http.StatusUnauthorized},
		{"Bearer wrong", http.StatusUnauthorized},
		{"Basic " + testAdminToken, http.StatusUnauthorized},
		{"Bearer " + testAdminToken, http.StatusOK},
		{"bearer " + testAdminToken, http.StatusOK},
	}
	for _, tt := range tests {
		if rec := h.do(t, adminRequest(http.MethodGet, "/server/status", tt.auth)); rec.Code != tt.want {
			t.Errorf("auth %q: status %d, want %d", tt.auth, rec.Code, tt.want)
		}
	}

	rec := h.do(t, adminRequest(http.MethodGet, "/server/config", "Bearer "+testAdminToken))
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), testSecret) || strings.Contains(rec.Body.String(), testAdminToken) {
		t.Errorf("config: status %d body %s", rec.Code, rec.Body)
	}

	cfg := testConfig()
	cfg.AdminToken = ""
	h = newHarness(t, cfg, true)
	if rec := h.do(t, adminRequest(http.MethodPost, "/scheduled/okta-sync", "Bearer ")); rec.Code != http.StatusForbidden {
		t.Errorf("no admin token configured: status %d, want 403", rec.Code)
	}
	if h.okta.Count("GetGroupInfo") != 0 {
		t.Error("scheduled action ran without an admin token")
	}
}

func TestScheduled(t *testing.T) {
	auth := "Bearer " + testAdminToken

	h := newHarness(t, testConfig(), true)
	if rec := h.do(t, adminRequest(http.MethodPost, "/scheduled/nope", auth)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown action: status %d, want 404", rec.Code)
	}

	h.github.ListOrgMembersFn = func(context.Context) ([]string, error) { return []string{"eng", "orphan"}, nil }
	h.github.GetTeamMembersFn = func(context.Context, string) ([]string, error) { return []string{"eng"}, nil }
	if rec := h.do(t, adminRequest(http.MethodPost, "/scheduled/okta-sync", auth)); rec.Code != http.StatusOK {
		t.Fatalf("okta sync: status %d (%s)", rec.Code, rec.Body)
	}
	if len(h.notifier.OktaSyncs) != 1 || len(h.notifier.OrphanedUsers) != 1 || h.notifier.OrphanedUsers[0].OrphanedUsers[0] != "orphan" {
		t.Errorf("notifications: syncs %d, orphaned %+v", len(h.notifier.OktaSyncs), h.notifier.OrphanedUsers)
	}

	h = newHarness(t, testConfig(), false)
	if rec := h.do(t, adminRequest(http.MethodPost, "/scheduled/okta-sync", auth)); rec.Code != http.StatusConflict {
		t.Errorf("okta sync disabled: status %d, want 409", rec.Code)
	}
}

func TestOktaSync_TotalFailureIsReportedAndReturned(t *testing.T) {
	h := newHarness(t, testConfig(), true)
	h.okta.GetGroupInfoFn = func(context.Context, string) (*domain.GroupInfo, error) {
		return nil, errors.New("okta down")
	}
	if err := h.app.RunScheduled(context.Background(), ActionOktaSync); err == nil {
		t.Fatal("total failure returned no error")
	}
	if len(h.notifier.OktaSyncs) != 1 || !h.notifier.OktaSyncs[0][0].HasErrors() {
		t.Error("failed sync was not reported")
	}
	if h.github.Count("ListOrgMembers") != 0 {
		t.Error("orphan detection ran after a failed sync")
	}
}

func TestOktaSync_CoalescesConcurrentRequests(t *testing.T) {
	h := newHarness(t, testConfig(), true)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	h.okta.GetGroupInfoFn = func(_ context.Context, name string) (*domain.GroupInfo, error) {
		started <- struct{}{}
		<-release
		return &domain.GroupInfo{Name: name}, nil
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if err := h.app.RunScheduled(context.Background(), ActionOktaSync); err != nil {
			t.Errorf("first sync: %v", err)
		}
	})
	<-started

	// requests during the running sync return at once and queue one more
	for range 3 {
		if err := h.app.RunScheduled(context.Background(), ActionOktaSync); err != nil {
			t.Fatalf("queued sync: %v", err)
		}
	}
	close(release)
	wg.Wait()

	if got := h.okta.Count("GetGroupInfo"); got != 2 {
		t.Errorf("syncs run = %d, want 2", got)
	}
}

func TestSecurityAlerts(t *testing.T) {
	h := newHarness(t, testConfig(), true)
	h.github.ListSecurityAlertsFn = func(_ context.Context, days int, sev string) (*domain.SecurityAlertsReport, error) {
		if days != 30 || sev != "high" {
			t.Errorf("ListSecurityAlerts(%d, %q)", days, sev)
		}
		return &domain.SecurityAlertsReport{TotalAlerts: 1, AlertsByRepo: map[string][]domain.SecurityAlert{"acme/web": {{CreatedAt: time.Now()}}}}, nil
	}
	if err := h.app.RunScheduled(context.Background(), ActionSecurityAlerts); err != nil {
		t.Fatal(err)
	}
	if len(h.notifier.SecurityAlerts) != 1 {
		t.Errorf("notifications = %d, want 1", len(h.notifier.SecurityAlerts))
	}

	cfg := testConfig()
	cfg.SecurityAlerts.Enabled = false
	h = newHarness(t, cfg, true)
	if err := h.app.RunScheduled(context.Background(), ActionSecurityAlerts); !errors.Is(err, domain.ErrFeatureDisabled) {
		t.Errorf("disabled: error = %v, want ErrFeatureDisabled", err)
	}
}

func TestSlackTest(t *testing.T) {
	h := newHarness(t, testConfig(), true)
	if err := h.app.RunScheduled(context.Background(), ActionSlackTest); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"NotifyPRBypass", "NotifyOktaSync", "NotifyOrphanedUsers", "NotifySecurityAlerts"} {
		if h.notifier.Count(name) != 1 {
			t.Errorf("%s calls = %d, want 1", name, h.notifier.Count(name))
		}
	}

	cfg := testConfig()
	cfg.Okta.Enabled = false
	a, err := New(cfg, slog.New(slog.DiscardHandler), WithGitHubClient(&fakes.GitHub{}))
	if err != nil {
		t.Fatal(err)
	}
	if err := a.RunScheduled(context.Background(), ActionSlackTest); !errors.Is(err, domain.ErrFeatureDisabled) {
		t.Errorf("without slack: error = %v, want ErrFeatureDisabled", err)
	}
}

func TestRouter_BasePathAndErrors(t *testing.T) {
	cfg := testConfig()
	cfg.Server.BasePath = "/prod"
	h := newHarness(t, cfg, true)

	req := webhookRequest(t, "ping", map[string]any{}, testSecret)
	req.URL.Path = "/prod/webhooks"
	if rec := h.do(t, req); rec.Code != http.StatusOK {
		t.Errorf("based webhook: status %d", rec.Code)
	}
	if rec := h.do(t, webhookRequest(t, "ping", map[string]any{}, testSecret)); rec.Code != http.StatusNotFound {
		t.Errorf("unbased webhook: status %d, want 404", rec.Code)
	}
	if rec := h.do(t, adminRequest(http.MethodGet, "/prod/webhooks", "")); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET webhook: status %d, want 405", rec.Code)
	}
}

func TestNew_BuildsRealClients(t *testing.T) {
	cfg := testConfig()
	cfg.GitHub.PrivateKey = []byte("not a key")
	if _, err := New(cfg, nil); err == nil {
		t.Error("New accepted an invalid github private key")
	}
}

func TestOktaSync_FollowUpRunsAfterFailedSync(t *testing.T) {
	h := newHarness(t, testConfig(), true)
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls int
	var mu sync.Mutex
	h.okta.GetGroupInfoFn = func(_ context.Context, name string) (*domain.GroupInfo, error) {
		mu.Lock()
		calls++
		first := calls == 1
		mu.Unlock()
		if first {
			started <- struct{}{}
			<-release
			return nil, errors.New("okta down")
		}
		return &domain.GroupInfo{Name: name}, nil
	}

	var firstErr error
	var wg sync.WaitGroup
	wg.Go(func() { firstErr = h.app.RunScheduled(context.Background(), ActionOktaSync) })
	<-started
	if err := h.app.RunScheduled(context.Background(), ActionOktaSync); err != nil {
		t.Fatalf("queued sync: %v", err)
	}
	close(release)
	wg.Wait()

	if calls != 2 {
		t.Errorf("syncs run = %d, want the queued follow-up to run after the failure", calls)
	}
	if firstErr == nil {
		t.Error("the failed first sync's error was lost")
	}
}

// fakeBackground keeps background jobs so a test can run them.
type fakeBackground struct {
	mu   sync.Mutex
	jobs []func(context.Context)
}

func (b *fakeBackground) Go(f func(context.Context)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.jobs = append(b.jobs, f)
}

func (b *fakeBackground) run() {
	b.mu.Lock()
	jobs := b.jobs
	b.jobs = nil
	b.mu.Unlock()
	for _, job := range jobs {
		job(context.Background())
	}
}

func TestWebhook_SyncRunsInBackground(t *testing.T) {
	gh, okta, bg := &fakes.GitHub{}, &fakes.Okta{}, &fakeBackground{}
	a, err := New(testConfig(), slog.New(slog.DiscardHandler),
		WithGitHubClient(gh), WithOktaClient(okta), WithNotifier(&fakes.Notifier{}), WithBackground(bg))
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, webhookRequest(t, "membership", teamEvent("membership", "added", "alice", "User"), testSecret))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), "queued") {
		t.Fatalf("status %d body %s, want 202 queued", rec.Code, rec.Body)
	}
	if okta.Count("GetGroupInfo") != 0 {
		t.Fatal("sync ran before the response")
	}
	bg.run()
	if okta.Count("GetGroupInfo") != 1 {
		t.Error("background sync did not run")
	}
}

func TestWebhook_IgnoresTeamsNoRuleManages(t *testing.T) {
	h := newHarness(t, testConfig(), true)
	membership := func(team string) map[string]any {
		p := teamEvent("membership", "added", "alice", "User")
		p["team"] = map[string]any{"slug": team}
		return p
	}

	// before a sync, which teams are managed is unknown, so any change syncs
	h.do(t, webhookRequest(t, "membership", membership("design"), testSecret))
	if got := h.okta.Count("GetGroupInfo"); got != 1 {
		t.Fatalf("syncs = %d, want 1", got)
	}

	// the sync managed only "eng"
	rec := h.do(t, webhookRequest(t, "membership", membership("design"), testSecret))
	if rec.Code != http.StatusAccepted || h.okta.Count("GetGroupInfo") != 1 {
		t.Errorf("unmanaged team: status %d, syncs %d; want 202 and no sync", rec.Code, h.okta.Count("GetGroupInfo"))
	}
	h.do(t, webhookRequest(t, "membership", membership("eng"), testSecret))
	if got := h.okta.Count("GetGroupInfo"); got != 2 {
		t.Errorf("managed team: syncs = %d, want 2", got)
	}

	// renaming a managed team is recognized by its previous name
	renamed := teamEvent("team", "edited", "alice", "User")
	renamed["team"] = map[string]any{"slug": "engineering"}
	renamed["changes"] = map[string]any{"name": map[string]any{"from": "eng"}}
	h.do(t, webhookRequest(t, "team", renamed, testSecret))
	if got := h.okta.Count("GetGroupInfo"); got != 3 {
		t.Errorf("renamed team: syncs = %d, want 3", got)
	}

	// a repository permission change is not a membership change
	perms := teamEvent("team", "edited", "alice", "User")
	perms["changes"] = map[string]any{"repository": map[string]any{"permissions": map[string]any{"from": map[string]any{"push": false}}}}
	h.do(t, webhookRequest(t, "team", perms, testSecret))
	if got := h.okta.Count("GetGroupInfo"); got != 3 {
		t.Errorf("permission change: syncs = %d, want 3", got)
	}
}

func TestOktaSync_SkipsOrphanDetectionWhenATeamIsUnknown(t *testing.T) {
	cfg := testConfig()
	cfg.Okta.SyncRules = []domain.SyncRule{{OktaGroupName: "eng"}, {OktaGroupName: "broken"}}
	h := newHarness(t, cfg, true)
	h.okta.GetGroupInfoFn = func(_ context.Context, name string) (*domain.GroupInfo, error) {
		if name == "broken" {
			return nil, errors.New("okta timeout")
		}
		return &domain.GroupInfo{Name: name, Members: []string{"alice"}}, nil
	}

	if err := h.app.RunScheduled(context.Background(), ActionOktaSync); err != nil {
		t.Fatal(err)
	}
	if h.github.Count("ListOrgMembers") != 0 || len(h.notifier.OrphanedUsers) != 0 {
		t.Error("orphaned users were reported although the broken rule's team is unknown")
	}
}
