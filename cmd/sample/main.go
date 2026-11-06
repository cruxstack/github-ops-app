// Command sample sends the samples in fixtures/samples.json through the
// application with the configuration in .env.
//
// WARNING: it uses live credentials and calls the real GitHub, Okta, and
// Slack APIs, which can change team membership. Use the e2e tests for
// offline testing instead.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"

	"github.com/joho/godotenv"

	"github.com/cruxstack/github-ops-app/internal/app"
	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/github/webhooks"
)

type sample struct {
	EventType   string          `json:"event_type"`
	Description string          `json:"description"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug}))
	if err := run(context.Background(), logger); err != nil {
		logger.Error("sample run failed", "error", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, logger *slog.Logger) error {
	if err := godotenv.Load(".env"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to read .env: %w", err)
	}
	cfg, err := config.Load(ctx, config.ProcessEnv())
	if err != nil {
		return err
	}
	a, err := app.New(cfg, logger)
	if err != nil {
		return err
	}

	raw, err := os.ReadFile("fixtures/samples.json")
	if err != nil {
		return err
	}
	var samples []sample
	if err := json.Unmarshal(raw, &samples); err != nil {
		return fmt.Errorf("failed to parse samples: %w", err)
	}

	for i, s := range samples {
		logger.Info("running sample", "sample", i, "type", s.EventType, "description", s.Description)
		switch s.EventType {
		case "okta_sync":
			if err := a.RunScheduled(ctx, app.ActionOktaSync); err != nil {
				return fmt.Errorf("sample %d: %w", i, err)
			}
		case "pr_webhook":
			req := httptest.NewRequestWithContext(ctx, http.MethodPost, cfg.Server.BasePath+"/webhooks", bytes.NewReader(s.Payload))
			req.Header.Set("X-GitHub-Event", webhooks.EventPullRequest)
			req.Header.Set("X-Hub-Signature-256", webhooks.Sign(s.Payload, cfg.GitHub.WebhookSecret))
			rec := httptest.NewRecorder()
			a.Handler().ServeHTTP(rec, req)
			if rec.Code >= http.StatusBadRequest {
				return fmt.Errorf("sample %d: status %d: %s", i, rec.Code, rec.Body)
			}
		default:
			logger.Warn("skipping unknown sample type", "type", s.EventType)
		}
	}
	return nil
}
