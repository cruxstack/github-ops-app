// Package app provides HTTP handlers for webhook and status endpoints.
package app

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"github.com/cruxstack/github-ops-app/internal/github"
)

// WebhookHandler processes incoming GitHub webhook POST requests.
// validates webhook signatures before processing events.
func (a *App) WebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")
	signature := r.Header.Get("X-Hub-Signature-256")

	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.Logger.Warn("failed to read request body", slog.String("error", err.Error()))
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	if err := github.ValidateWebhookSignature(
		body,
		signature,
		a.Config.GitHubWebhookSecret,
	); err != nil {
		a.Logger.Warn("webhook signature validation failed", slog.String("error", err.Error()))
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if err := a.ProcessWebhook(r.Context(), body, eventType); err != nil {
		a.Logger.Error("webhook processing failed",
			slog.String("event_type", eventType),
			slog.String("error", err.Error()))
		http.Error(w, "webhook processing failed", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// StatusHandler returns the application status and feature flags.
// responds with JSON containing configuration state and enabled features.
func (a *App) StatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status := a.GetStatus()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(status); err != nil {
		a.Logger.Error("failed to encode status response", slog.String("error", err.Error()))
	}
}

// ConfigHandler returns the application configuration with secrets redacted.
// useful for debugging and verifying environment settings.
func (a *App) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	redacted := a.Config.Redacted()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)

	if err := json.NewEncoder(w).Encode(redacted); err != nil {
		a.Logger.Error("failed to encode config response", slog.String("error", err.Error()))
	}
}
