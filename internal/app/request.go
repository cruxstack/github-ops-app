package app

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/cruxstack/github-ops-app/internal/github/webhooks"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Handler returns the HTTP handler for the application.
// this is the single entry point for all HTTP request processing.
// both the server and lambda entry points feed requests into this handler.
func (a *App) Handler() http.Handler {
	a.routerOnce.Do(func() {
		a.router = a.buildRouter()
	})
	return a.router
}

// buildRouter constructs the chi router with all routes and middleware.
func (a *App) buildRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	basePath := a.Config.BasePath
	if basePath == "" {
		basePath = "/"
	}

	r.Route(basePath, func(r chi.Router) {
		r.Post("/webhooks", a.handleWebhookHTTP)

		r.Group(func(r chi.Router) {
			r.Use(a.adminAuthMiddleware)
			r.Get("/server/status", a.handleStatusHTTP)
			r.Get("/server/config", a.handleConfigHTTP)
			r.Post("/scheduled/{action}", a.handleScheduledHTTP)
		})
	})

	return r
}

// adminAuthMiddleware validates the admin bearer token on protected routes.
// if no admin token is configured, all requests pass through.
func (a *App) adminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.Config.AdminToken == "" {
			next.ServeHTTP(w, r)
			return
		}

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == authHeader {
			token = strings.TrimPrefix(authHeader, "bearer ")
		}

		if token != a.Config.AdminToken {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// handleStatusHTTP returns application status.
func (a *App) handleStatusHTTP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.GetStatus())
}

// handleConfigHTTP returns redacted configuration.
func (a *App) handleConfigHTTP(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.Config.Redacted())
}

// handleWebhookHTTP processes GitHub webhook POST requests.
func (a *App) handleWebhookHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // 10MB limit
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")
	signature := r.Header.Get("X-Hub-Signature-256")

	if err := webhooks.ValidateWebhookSignature(
		body,
		signature,
		a.Config.GitHubWebhookSecret,
	); err != nil {
		a.Logger.Warn("webhook signature validation failed",
			slog.String("error", err.Error()))
		writeErrorFromDomain(w, err, "unauthorized")
		return
	}

	if err := a.processWebhook(r.Context(), body, eventType); err != nil {
		a.Logger.Error("webhook processing failed",
			slog.String("event_type", eventType),
			slog.String("error", err.Error()))
		writeErrorFromDomain(w, err, "webhook processing failed")
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// handleScheduledHTTP processes scheduled events via HTTP POST.
// the action is extracted from the chi URL parameter.
func (a *App) handleScheduledHTTP(w http.ResponseWriter, r *http.Request) {
	action := chi.URLParam(r, "action")
	if action == "" {
		writeError(w, http.StatusBadRequest, "missing scheduled action")
		return
	}

	evt := ScheduledEvent{Action: action}

	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeError(w, http.StatusBadRequest, "failed to read request body")
			return
		}
		if len(body) > 0 {
			evt.Data = json.RawMessage(body)
		}
	}

	if err := a.processScheduledEvent(r.Context(), evt); err != nil {
		a.Logger.Error("scheduled event processing failed",
			slog.String("action", evt.Action),
			slog.String("error", err.Error()))
		writeErrorFromDomain(w, err, "scheduled event processing failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "success",
		"message": evt.Action + " completed",
	})
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to marshal response")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(body)
}

// writeError writes a plain text error response.
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(status)
	w.Write([]byte(message))
}

// writeErrorFromDomain translates domain error types to HTTP status codes
// and writes the response. centralizes error-to-HTTP mapping.
func writeErrorFromDomain(w http.ResponseWriter, err error, fallbackMsg string) {
	switch {
	case errors.Is(err, domain.AuthError):
		writeError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, domain.ValidationError):
		writeError(w, http.StatusBadRequest, fallbackMsg)
	case errors.Is(err, domain.ConfigError):
		writeError(w, http.StatusServiceUnavailable, "service not configured")
	case errors.Is(err, domain.APIError):
		writeError(w, http.StatusBadGateway, fallbackMsg)
	default:
		writeError(w, http.StatusInternalServerError, fallbackMsg)
	}
}
