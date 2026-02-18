// Package main provides a sample test runner for live API testing.
// WARNING: DO NOT RUN - requires live credentials and makes real API calls
// to GitHub, Okta, and Slack. Use cmd/verify for offline testing instead.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"

	"github.com/cruxstack/github-ops-app/internal/app"
	"github.com/cruxstack/github-ops-app/internal/config"
)

func main() {
	logger := config.NewLogger()
	ctx := context.Background()

	envpath := filepath.Join(".env")
	logger.Debug("loading environment", slog.String("path", envpath))
	if _, err := os.Stat(envpath); err == nil {
		_ = godotenv.Load(envpath)
	}

	cfg, err := config.NewConfig()
	if err != nil {
		logger.Error("failed to load config", slog.String("error", err.Error()))
		os.Exit(1)
	}

	a, err := app.NewApp(ctx, cfg, logger)
	if err != nil {
		logger.Error("failed to initialize app", slog.String("error", err.Error()))
		os.Exit(1)
	}

	router := a.Handler()

	path := filepath.Join("fixtures", "samples.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		logger.Error("failed to read samples file", slog.String("error", err.Error()))
		os.Exit(1)
	}

	var samples []map[string]any
	if err := json.Unmarshal(raw, &samples); err != nil {
		logger.Error("failed to parse samples json", slog.String("error", err.Error()))
		os.Exit(1)
	}

	for i, sample := range samples {
		eventType, ok := sample["event_type"].(string)
		if !ok {
			logger.Error("missing or invalid event_type", slog.Int("sample", i))
			os.Exit(1)
		}

		switch eventType {
		case "okta_sync":
			reqPath := fmt.Sprintf("%s/scheduled/okta-sync", cfg.BasePath)
			httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqPath, nil)
			if err != nil {
				logger.Error("failed to construct http request",
					slog.Int("sample", i),
					slog.String("error", err.Error()))
				os.Exit(1)
			}
			if cfg.AdminToken != "" {
				httpReq.Header.Set("Authorization", "Bearer "+cfg.AdminToken)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httpReq)
			if rec.Code >= 400 {
				logger.Error("failed to process okta_sync sample",
					slog.Int("sample", i),
					slog.String("response", rec.Body.String()))
				os.Exit(1)
			}

		case "pr_webhook":
			payload, _ := json.Marshal(sample["payload"])
			reqPath := fmt.Sprintf("%s/webhooks", cfg.BasePath)
			httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, reqPath, bytes.NewReader(payload))
			if err != nil {
				logger.Error("failed to construct http request",
					slog.Int("sample", i),
					slog.String("error", err.Error()))
				os.Exit(1)
			}
			httpReq.Header.Set("X-GitHub-Event", "pull_request")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httpReq)
			if rec.Code >= 400 {
				logger.Error("failed to process pr_webhook sample",
					slog.Int("sample", i),
					slog.String("response", rec.Body.String()))
				os.Exit(1)
			}

		default:
			logger.Info("skipping unknown event type", slog.String("event_type", eventType))
		}

		logger.Info("processed sample successfully", slog.Int("sample", i))
	}
}
