// Package e2e runs the scenarios in fixtures/scenarios.json against the
// service with its real wiring and real HTTP servers, with the GitHub, Okta,
// and Slack APIs replaced by mock servers.
package e2e

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/cruxstack/github-ops-app/internal/app"
	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/github/webhooks"
	"github.com/cruxstack/github-ops-app/internal/service"
)

const (
	webhookSecret = "test_webhook_secret"
	adminToken    = "e2e-admin-token-0123456789"
	unset         = "<unset>"
)

type call struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	Path    string `json:"path"`
	Query   string `json:"query,omitempty"`
}

func (c call) String() string {
	return fmt.Sprintf("%s %s %s?%s", c.Service, c.Method, c.Path, c.Query)
}

func (c call) matches(service string, r request) bool {
	return (c.Service == service || c.Service == "*") &&
		(c.Method == r.Method || c.Method == "*") &&
		matchPath(r.Path, c.Path) &&
		strings.Contains(r.Query, c.Query)
}

type scenario struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// EventType is "webhook" or "scheduled_event".
	EventType      string          `json:"event_type"`
	EventPayload   json.RawMessage `json:"event_payload,omitempty"`
	WebhookType    string          `json:"webhook_type,omitempty"`
	WebhookPayload json.RawMessage `json:"webhook_payload,omitempty"`
	// Signature is empty to sign webhooks, "invalid", or "none".
	Signature string `json:"signature,omitempty"`
	// AdminAuth is empty to send the admin token, or "none".
	AdminAuth       string            `json:"admin_auth,omitempty"`
	ConfigOverrides map[string]string `json:"config_overrides,omitempty"`
	MockResponses   []mockResponse    `json:"mock_responses"`
	ExpectedCalls   []call            `json:"expected_calls"`
	ForbiddenCalls  []call            `json:"forbidden_calls,omitempty"`
	ExpectError     bool              `json:"expect_error,omitempty"`
	ExpectStatus    int               `json:"expect_status,omitempty"`
}

var (
	keysOnce           sync.Once
	githubKey, oktaKey string
)

func testKeys(t *testing.T) (string, string) {
	t.Helper()
	keysOnce.Do(func() {
		gen := func() string {
			k, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				panic(err)
			}
			return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}))
		}
		githubKey, oktaKey = gen(), gen()
	})
	return githubKey, oktaKey
}

func loadScenarios(t *testing.T) []scenario {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "fixtures", "scenarios.json"))
	if err != nil {
		t.Fatal(err)
	}
	var scenarios []scenario
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&scenarios); err != nil {
		t.Fatalf("invalid scenarios: %v", err)
	}
	return scenarios
}

func TestScenarios(t *testing.T) {
	for _, sc := range loadScenarios(t) {
		t.Run(sc.Name, func(t *testing.T) { runScenario(t, sc) })
	}
}

func runScenario(t *testing.T, sc scenario) {
	if sc.Description != "" {
		t.Log(sc.Description)
	}

	byService := map[string][]mockResponse{}
	for _, m := range sc.MockResponses {
		byService[m.Service] = append(byService[m.Service], m)
	}
	mocks := map[string]*mockServer{}
	urls := map[string]string{}
	var client *http.Client
	for _, name := range []string{"github", "okta", "slack"} {
		mocks[name] = newMockServer(name, byService[name], t.Logf)
		srv := httptest.NewTLSServer(mocks[name])
		t.Cleanup(srv.Close)
		urls[name] = srv.URL
		// every test server shares one certificate, so any of their clients
		// trusts all of them
		client = srv.Client()
	}

	ghKey, oktaPEM := testKeys(t)
	env := map[string]string{
		"APP_LOG_LEVEL":                        "debug",
		"APP_ADMIN_TOKEN":                      adminToken,
		"APP_GITHUB_ORG":                       "acme-ghorg",
		"APP_GITHUB_APP_ID":                    "123456",
		"APP_GITHUB_INSTALLATION_ID":           "987654",
		"APP_GITHUB_APP_PRIVATE_KEY":           ghKey,
		"APP_GITHUB_WEBHOOK_SECRET":            webhookSecret,
		"APP_GITHUB_BASE_URL":                  urls["github"],
		"APP_PR_COMPLIANCE_ENABLED":            "true",
		"APP_PR_MONITORED_BRANCHES":            "main,master",
		"APP_OKTA_DOMAIN":                      "dev-12345.okta.com",
		"APP_OKTA_BASE_URL":                    urls["okta"],
		"APP_OKTA_CLIENT_ID":                   "test-client-id",
		"APP_OKTA_PRIVATE_KEY":                 oktaPEM,
		"APP_OKTA_GITHUB_USER_FIELD":           "githubUsername",
		"APP_OKTA_SYNC_RULES":                  `[{"okta_group_name":"Engineering","github_team_name":"engineering"}]`,
		"APP_OKTA_ORPHANED_USER_NOTIFICATIONS": "false",
		"APP_SLACK_TOKEN":                      "xoxb-test-token",
		"APP_SLACK_CHANNEL":                    "C01234TEST",
		"APP_SLACK_API_URL":                    urls["slack"],
	}
	for k, v := range sc.ConfigOverrides {
		if v == unset {
			delete(env, k)
		} else {
			env[k] = v
		}
	}
	cfgEnv := config.Env{Lookup: func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}}

	logger := slog.New(slog.NewTextHandler(t.Output(), &slog.HandlerOptions{Level: slog.LevelDebug}))
	boot, err := config.LoadBootstrap(cfgEnv)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := service.New(boot, service.Options{
		Mode:       service.ModeServer,
		Logger:     logger,
		Env:        cfgEnv,
		AppOptions: []app.Option{app.WithHTTPClient(client)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Start(t.Context()); err != nil {
		t.Fatalf("service failed to start: %v", err)
	}
	appSrv := httptest.NewServer(svc.Handler())
	t.Cleanup(appSrv.Close)

	req := scenarioRequest(t, sc, appSrv.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	// webhook-triggered syncs run after the response
	if err := svc.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}

	switch {
	case sc.ExpectStatus != 0:
		if resp.StatusCode != sc.ExpectStatus {
			t.Errorf("status %d, want %d: %s", resp.StatusCode, sc.ExpectStatus, respBody)
		}
	case sc.ExpectError:
		if resp.StatusCode < 400 {
			t.Errorf("status %d, want an error: %s", resp.StatusCode, respBody)
		}
	default:
		if resp.StatusCode >= 300 {
			t.Errorf("status %d, want success: %s", resp.StatusCode, respBody)
		}
	}

	received := map[string][]request{}
	for name, m := range mocks {
		received[name] = m.received()
	}
	found := func(c call) bool {
		for service, reqs := range received {
			for _, r := range reqs {
				if c.matches(service, r) {
					return true
				}
			}
		}
		return false
	}

	for _, c := range sc.ExpectedCalls {
		if !found(c) {
			t.Errorf("expected call not made: %s", c)
		}
	}
	for _, c := range sc.ForbiddenCalls {
		if found(c) {
			t.Errorf("forbidden call made: %s", c)
		}
	}
	// no request may delete anything a scenario doesn't expect deleted
	for service, reqs := range received {
		for _, r := range reqs {
			if r.Method != http.MethodDelete {
				continue
			}
			expected := false
			for _, c := range sc.ExpectedCalls {
				expected = expected || c.matches(service, r)
			}
			if !expected {
				t.Errorf("unexpected destructive call: %s DELETE %s", service, r.Path)
			}
		}
	}
	if t.Failed() {
		for service, reqs := range received {
			for _, r := range reqs {
				t.Logf("received %s %s %s?%s", service, r.Method, r.Path, r.Query)
			}
		}
	}
}

func scenarioRequest(t *testing.T, sc scenario, baseURL string) *http.Request {
	t.Helper()
	switch sc.EventType {
	case "webhook":
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/webhooks", bytes.NewReader(sc.WebhookPayload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-GitHub-Event", sc.WebhookType)
		req.Header.Set("X-GitHub-Delivery", sc.Name)
		switch sc.Signature {
		case "":
			req.Header.Set("X-Hub-Signature-256", webhooks.Sign(sc.WebhookPayload, webhookSecret))
		case "invalid":
			req.Header.Set("X-Hub-Signature-256", webhooks.Sign(sc.WebhookPayload, "wrong-secret"))
		}
		return req

	case "scheduled_event":
		var evt struct {
			Action string `json:"action"`
		}
		if err := json.Unmarshal(sc.EventPayload, &evt); err != nil || evt.Action == "" {
			t.Fatalf("scheduled event has no action: %s", sc.EventPayload)
		}
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/scheduled/"+evt.Action, nil)
		if err != nil {
			t.Fatal(err)
		}
		if sc.AdminAuth != "none" {
			req.Header.Set("Authorization", "Bearer "+adminToken)
		}
		return req

	default:
		t.Fatalf("unknown event type %q", sc.EventType)
		return nil
	}
}

func TestMatchPath(t *testing.T) {
	tests := []struct {
		path, pattern string
		want          bool
	}{
		{"/a/b", "/a/b", true},
		{"/a/b", "*", true},
		{"/orgs/x/teams/y", "/orgs/*/teams/*", true},
		{"/orgs/x/members", "/orgs/*/teams/*", false},
		{"/a/b/c", "/a/*/c", true},
		{"/a/b/d", "/a/*/c", false},
	}
	for _, tt := range tests {
		if got := matchPath(tt.path, tt.pattern); got != tt.want {
			t.Errorf("matchPath(%q, %q) = %v", tt.path, tt.pattern, got)
		}
	}
}
