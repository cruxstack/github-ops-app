package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/cruxstack/github-ops-app/internal/app"
	"github.com/cruxstack/github-ops-app/internal/config"
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
		return fmt.Errorf("generate cert: %w", err)
	}

	githubAppKey, err := generateOAuthPrivateKey()
	if err != nil {
		return fmt.Errorf("generate github app key: %w", err)
	}
	os.Setenv("APP_GITHUB_APP_PRIVATE_KEY", string(githubAppKey))

	oauthKey, err := generateOAuthPrivateKey()
	if err != nil {
		return fmt.Errorf("generate oauth key: %w", err)
	}
	os.Setenv("APP_OKTA_CLIENT_ID", "test-client-id")
	os.Setenv("APP_OKTA_PRIVATE_KEY", string(oauthKey))

	githubServer := &http.Server{
		Addr:    "localhost:9001",
		Handler: githubMock,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
		},
	}
	oktaServer := &http.Server{
		Addr:    "localhost:9002",
		Handler: oktaMock,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
		},
	}
	slackServer := &http.Server{
		Addr:    "localhost:9003",
		Handler: slackMock,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
		},
	}

	githubReady := make(chan bool)
	oktaReady := make(chan bool)
	slackReady := make(chan bool)

	go func() {
		githubReady <- true
		if err := githubServer.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
			logger.Error("github mock server error", slog.String("error", err.Error()))
		}
	}()

	go func() {
		oktaReady <- true
		if err := oktaServer.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
			logger.Error("okta mock server error", slog.String("error", err.Error()))
		}
	}()

	go func() {
		slackReady <- true
		if err := slackServer.ListenAndServeTLS("", ""); err != http.ErrServerClosed {
			logger.Error("slack mock server error", slog.String("error", err.Error()))
		}
	}()

	<-githubReady
	<-oktaReady
	<-slackReady
	time.Sleep(100 * time.Millisecond)

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		githubServer.Shutdown(shutdownCtx)
		oktaServer.Shutdown(shutdownCtx)
		slackServer.Shutdown(shutdownCtx)
	}()

	http.DefaultTransport = &http.Transport{
		TLSClientConfig: &tls.Config{
			RootCAs: certPool,
		},
	}

	os.Setenv("APP_GITHUB_BASE_URL", "https://localhost:9001/")
	os.Setenv("APP_SLACK_API_URL", "https://localhost:9003/")
	os.Setenv("APP_OKTA_BASE_URL", "https://localhost:9002")

	ctx = context.WithValue(ctx, "okta_tls_cert_pool", certPool)

	if os.Getenv("APP_OKTA_ORPHANED_USER_NOTIFICATIONS") == "" {
		os.Setenv("APP_OKTA_ORPHANED_USER_NOTIFICATIONS", "false")
	}

	for key, value := range scenario.ConfigOverrides {
		os.Setenv(key, value)
	}

	cfg, err := config.NewConfig()
	if err != nil {
		return fmt.Errorf("config creation failed: %w", err)
	}

	a, err := app.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("app creation failed: %w", err)
	}

	if verbose {
		fmt.Printf("\n  Application Output:\n")
	}

	appLogger := slog.New(&testHandler{prefix: "  ", verbose: verbose, w: os.Stdout})
	a.Logger = appLogger

	var req app.Request
	switch scenario.EventType {
	case "scheduled_event":
		var evt app.ScheduledEvent
		if err := json.Unmarshal(scenario.EventPayload, &evt); err != nil {
			return fmt.Errorf("unmarshal event payload failed: %w", err)
		}
		req = app.Request{
			Type:            app.RequestTypeScheduled,
			ScheduledAction: evt.Action,
			ScheduledData:   evt.Data,
		}

	case "webhook":
		req = app.Request{
			Type:   app.RequestTypeHTTP,
			Method: "POST",
			Path:   "/webhooks",
			Headers: map[string]string{
				"x-github-event":      scenario.WebhookType,
				"x-hub-signature-256": "", // signature validated separately in tests
			},
			Body: scenario.WebhookPayload,
		}

	default:
		return fmt.Errorf("unknown event type: %s", scenario.EventType)
	}

	resp := a.HandleRequest(ctx, req)

	var processErr error
	if resp.StatusCode >= 400 {
		processErr = fmt.Errorf("request failed with status %d: %s", resp.StatusCode, string(resp.Body))
	}

	if scenario.ExpectError {
		if processErr == nil {
			return fmt.Errorf("expected error but processing succeeded")
		}
		if verbose {
			fmt.Printf("  ✓ Expected error occurred: %v\n", processErr)
		}
	} else {
		if processErr != nil {
			return fmt.Errorf("process event failed: %w", processErr)
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
			return fmt.Errorf("expected call not found: %s %s %s", exp.Service, exp.Method, exp.Path)
		}
	}
	return nil
}
