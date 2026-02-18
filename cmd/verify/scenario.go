package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/app"
	"github.com/cruxstack/github-ops-app/internal/config"
	oktaclient "github.com/cruxstack/github-ops-app/internal/okta"
)

// TestScenario defines a test case with input events and expected outcomes.
type TestScenario struct {
	Name            string            `json:"name"`
	Description     string            `json:"description,omitempty"`
	EventType       string            `json:"event_type"`
	EventPayload    json.RawMessage   `json:"event_payload,omitempty"`
	WebhookType     string            `json:"webhook_type,omitempty"`
	WebhookPayload  json.RawMessage   `json:"webhook_payload,omitempty"`
	ConfigOverrides map[string]string `json:"config_overrides,omitempty"`
	ExpectedCalls   []ExpectedCall    `json:"expected_calls"`
	MockResponses   []MockResponse    `json:"mock_responses"`
	ExpectError     bool              `json:"expect_error,omitempty"`
}

// ExpectedCall defines an HTTP API call the test expects the application to
// make.
type ExpectedCall struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	Path    string `json:"path"`
}

// runScenario executes a single test scenario with mock HTTP servers and
// validates that expected API calls were made.
func runScenario(ctx context.Context, scenario TestScenario, verbose bool, logger *slog.Logger) error {
	startTime := time.Now()

	fmt.Printf("\n▶ Running: %s\n", scenario.Name)
	if scenario.Description != "" {
		fmt.Printf("  %s\n", scenario.Description)
	}

	githubResponses := []MockResponse{}
	oktaResponses := []MockResponse{}
	slackResponses := []MockResponse{}
	for _, resp := range scenario.MockResponses {
		if resp.Service == "github" {
			githubResponses = append(githubResponses, resp)
		} else if resp.Service == "okta" {
			oktaResponses = append(oktaResponses, resp)
		} else if resp.Service == "slack" {
			slackResponses = append(slackResponses, resp)
		}
	}

	githubMock := NewMockServer("GitHub", githubResponses, verbose)
	oktaMock := NewMockServer("Okta", oktaResponses, verbose)
	slackMock := NewMockServer("Slack", slackResponses, verbose)

	tlsCert, certPool, err := generateSelfSignedCert()
	if err != nil {
		return errors.Wrap(err, "failed to generate cert")
	}

	githubAppKey, err := generateOAuthPrivateKey()
	if err != nil {
		return errors.Wrap(err, "failed to generate github app key")
	}

	oauthKey, err := generateOAuthPrivateKey()
	if err != nil {
		return errors.Wrap(err, "failed to generate oauth key")
	}

	// use dynamic ports: bind to :0 and extract the assigned port
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{tlsCert}}

	githubListener, err := tls.Listen("tcp", "localhost:0", tlsConfig)
	if err != nil {
		return errors.Wrap(err, "failed to listen github")
	}
	oktaListener, err := tls.Listen("tcp", "localhost:0", tlsConfig)
	if err != nil {
		githubListener.Close()
		return errors.Wrap(err, "failed to listen okta")
	}
	slackListener, err := tls.Listen("tcp", "localhost:0", tlsConfig)
	if err != nil {
		githubListener.Close()
		oktaListener.Close()
		return errors.Wrap(err, "failed to listen slack")
	}

	githubServer := &http.Server{Handler: githubMock}
	oktaServer := &http.Server{Handler: oktaMock}
	slackServer := &http.Server{Handler: slackMock}

	go func() {
		if err := githubServer.Serve(githubListener); err != http.ErrServerClosed {
			logger.Error("github mock server error", slog.String("error", err.Error()))
		}
	}()
	go func() {
		if err := oktaServer.Serve(oktaListener); err != http.ErrServerClosed {
			logger.Error("okta mock server error", slog.String("error", err.Error()))
		}
	}()
	go func() {
		if err := slackServer.Serve(slackListener); err != http.ErrServerClosed {
			logger.Error("slack mock server error", slog.String("error", err.Error()))
		}
	}()

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		githubServer.Shutdown(shutdownCtx)
		oktaServer.Shutdown(shutdownCtx)
		slackServer.Shutdown(shutdownCtx)
	}()

	githubAddr := fmt.Sprintf("https://%s/", githubListener.Addr().String())
	oktaAddr := fmt.Sprintf("https://%s", oktaListener.Addr().String())
	slackAddr := fmt.Sprintf("https://%s/", slackListener.Addr().String())

	// save and restore environment variables for isolation between scenarios
	envKeys := []string{
		"APP_GITHUB_APP_PRIVATE_KEY", "APP_OKTA_CLIENT_ID", "APP_OKTA_PRIVATE_KEY",
		"APP_GITHUB_BASE_URL", "APP_SLACK_API_URL", "APP_OKTA_BASE_URL",
		"APP_OKTA_ORPHANED_USER_NOTIFICATIONS",
	}
	for key := range scenario.ConfigOverrides {
		envKeys = append(envKeys, key)
	}
	savedEnv := make(map[string]string, len(envKeys))
	for _, key := range envKeys {
		savedEnv[key] = os.Getenv(key)
	}
	defer func() {
		for key, value := range savedEnv {
			if value == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, value)
			}
		}
	}()

	os.Setenv("APP_GITHUB_APP_PRIVATE_KEY", string(githubAppKey))
	os.Setenv("APP_OKTA_CLIENT_ID", "test-client-id")
	os.Setenv("APP_OKTA_PRIVATE_KEY", string(oauthKey))
	os.Setenv("APP_GITHUB_BASE_URL", githubAddr)
	os.Setenv("APP_SLACK_API_URL", slackAddr)
	os.Setenv("APP_OKTA_BASE_URL", oktaAddr)

	// save and restore http.DefaultTransport
	savedTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = savedTransport }()

	http.DefaultTransport = &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs: certPool,
		},
	}

	ctx = oktaclient.WithCertPool(ctx, certPool)

	if os.Getenv("APP_OKTA_ORPHANED_USER_NOTIFICATIONS") == "" {
		os.Setenv("APP_OKTA_ORPHANED_USER_NOTIFICATIONS", "false")
	}

	for key, value := range scenario.ConfigOverrides {
		os.Setenv(key, value)
	}

	cfg, err := config.NewConfig()
	if err != nil {
		return errors.Wrap(err, "config creation failed")
	}

	appLogger := slog.New(&testHandler{prefix: "  ", verbose: verbose, w: os.Stdout})

	a, err := app.NewApp(ctx, cfg, appLogger)
	if err != nil {
		return errors.Wrap(err, "app creation failed")
	}

	if verbose {
		fmt.Printf("\n  Application Output:\n")
	}

	router := a.Handler()

	var httpReq *http.Request
	switch scenario.EventType {
	case "scheduled_event":
		var evt app.ScheduledEvent
		if err := json.Unmarshal(scenario.EventPayload, &evt); err != nil {
			return errors.Wrap(err, "failed to unmarshal event payload")
		}
		path := fmt.Sprintf("%s/scheduled/%s", cfg.BasePath, evt.Action)
		var body []byte
		if evt.Data != nil {
			body = evt.Data
		}
		var err error
		httpReq, err = http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
		if err != nil {
			return errors.Wrap(err, "failed to construct scheduled http request")
		}
		if cfg.AdminToken != "" {
			httpReq.Header.Set("Authorization", "Bearer "+cfg.AdminToken)
		}
		if len(body) > 0 {
			httpReq.Header.Set("Content-Type", "application/json")
		}

	case "webhook":
		var err error
		httpReq, err = http.NewRequestWithContext(ctx, http.MethodPost, cfg.BasePath+"/webhooks", bytes.NewReader(scenario.WebhookPayload))
		if err != nil {
			return errors.Wrap(err, "failed to construct webhook http request")
		}
		httpReq.Header.Set("X-GitHub-Event", scenario.WebhookType)
		httpReq.Header.Set("X-Hub-Signature-256", "") // signature validated separately in tests

	default:
		return errors.Newf("unknown event type: %s", scenario.EventType)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httpReq)

	var processErr error
	if rec.Code >= 400 {
		processErr = errors.Newf("request failed with status %d: %s", rec.Code, rec.Body.String())
	}

	if scenario.ExpectError {
		if processErr == nil {
			return errors.New("expected error but processing succeeded")
		}
		if verbose {
			fmt.Printf("  ✓ Expected error occurred: %v\n", processErr)
		}
	} else {
		if processErr != nil {
			return errors.Wrap(processErr, "process event failed")
		}
	}

	time.Sleep(500 * time.Millisecond)

	githubReqs := githubMock.GetRequests()
	oktaReqs := oktaMock.GetRequests()
	slackReqs := slackMock.GetRequests()

	allReqs := make(map[string][]RequestRecord)
	allReqs["github"] = githubReqs
	allReqs["okta"] = oktaReqs
	allReqs["slack"] = slackReqs

	totalCalls := len(githubReqs) + len(oktaReqs) + len(slackReqs)

	if verbose {
		fmt.Printf("\n")
	}

	if err := validateNoUnexpectedCalls(scenario.ExpectedCalls, allReqs); err != nil {
		fmt.Printf("\n  Validation:\n")
		fmt.Printf("  ✗ FAILED: %v\n", err)
		return err
	}

	if err := validateExpectedCalls(scenario.ExpectedCalls, allReqs); err != nil {
		fmt.Printf("\n  Validation:\n")
		fmt.Printf("  ✗ FAILED: %v\n", err)
		fmt.Printf("\n  All captured requests:\n")
		if len(githubReqs) > 0 {
			fmt.Printf("    GitHub (%d):\n", len(githubReqs))
			for i, req := range githubReqs {
				fmt.Printf("      [%d] %s %s\n", i+1, req.Method, req.Path)
			}
		}
		if len(oktaReqs) > 0 {
			fmt.Printf("    Okta (%d):\n", len(oktaReqs))
			for i, req := range oktaReqs {
				fmt.Printf("      [%d] %s %s\n", i+1, req.Method, req.Path)
			}
		}
		if len(slackReqs) > 0 {
			fmt.Printf("    Slack (%d):\n", len(slackReqs))
			for i, req := range slackReqs {
				fmt.Printf("      [%d] %s %s\n", i+1, req.Method, req.Path)
			}
		}
		return err
	}

	duration := time.Since(startTime)

	if verbose {
		fmt.Printf("  Validation:\n")
		fmt.Printf("  ✓ All expected calls verified (%d total)\n", totalCalls)
		fmt.Printf("\n")
	}

	fmt.Printf("✓ PASSED (Duration: %.2fs)\n", duration.Seconds())
	return nil
}

// validateExpectedCalls verifies that all expected HTTP calls were captured
// by the mock servers.
func validateExpectedCalls(expected []ExpectedCall, allReqs map[string][]RequestRecord) error {
	for _, exp := range expected {
		reqs := allReqs[exp.Service]
		found := false
		for _, req := range reqs {
			if req.Method == exp.Method && matchPath(req.Path, exp.Path) {
				found = true
				break
			}
		}
		if !found {
			return errors.Newf("expected call not found: %s %s %s", exp.Service, exp.Method, exp.Path)
		}
	}
	return nil
}

// validateNoUnexpectedCalls checks that no unexpected destructive API calls
// were made. only flags DELETE calls to catch unintended member removal or
// resource deletion.
func validateNoUnexpectedCalls(expected []ExpectedCall, allReqs map[string][]RequestRecord) error {
	for service, reqs := range allReqs {
		for _, req := range reqs {
			// only flag unexpected destructive calls
			if req.Method != "DELETE" {
				continue
			}
			matched := false
			for _, exp := range expected {
				if exp.Service == service && exp.Method == req.Method && matchPath(req.Path, exp.Path) {
					matched = true
					break
				}
			}
			if !matched {
				return errors.Newf("unexpected destructive call: %s %s %s", service, req.Method, req.Path)
			}
		}
	}
	return nil
}
