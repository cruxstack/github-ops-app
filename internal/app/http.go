package app

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/cruxstack/github-ops-app/internal/github/webhooks"
)

// maxWebhookBytes is the largest webhook payload accepted. GitHub caps
// payloads at 25 MB.
const maxWebhookBytes = 25 << 20

// newRouter builds the application's routes under the base path:
//
//	POST /webhooks             GitHub webhooks, verified by signature
//	GET  /server/status        status and enabled features (admin)
//	GET  /server/config        configuration, secrets redacted (admin)
//	POST /scheduled/{action}   run a scheduled action (admin)
func (a *App) newRouter() http.Handler {
	r := chi.NewRouter()
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	})

	mount := a.cfg.Server.BasePath
	if mount == "" {
		mount = "/"
	}
	r.Route(mount, func(r chi.Router) {
		r.Post("/webhooks", a.handleWebhook)

		r.Group(func(r chi.Router) {
			r.Use(a.requireAdmin)
			r.Get("/server/status", a.handleStatus)
			r.Get("/server/config", a.handleConfig)
			r.Post("/scheduled/{action}", a.handleScheduled)
		})
	})
	return r
}

// requireAdmin allows a request only with the admin bearer token. When no
// token is configured the admin endpoints are disabled.
func (a *App) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.cfg.AdminToken == "" {
			writeError(w, http.StatusForbidden, "admin api is disabled: set APP_ADMIN_TOKEN to enable it")
			return
		}
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || !tokensEqual(strings.TrimSpace(token), a.cfg.AdminToken) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="github-ops-app"`)
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokensEqual compares two tokens in constant time, including their length.
func tokensEqual(a, b string) bool {
	ha, hb := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

func (a *App) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.Status())
}

func (a *App) handleConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, a.cfg.Redacted())
}

func (a *App) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	eventType := r.Header.Get("X-GitHub-Event")
	log := a.logger.With(
		"event_type", eventType,
		"delivery_id", r.Header.Get("X-GitHub-Delivery"))

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
			return
		}
		writeError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	if err := webhooks.ValidateSignature(body, r.Header.Get("X-Hub-Signature-256"), a.cfg.GitHub.WebhookSecret); err != nil {
		log.WarnContext(ctx, "rejected webhook with invalid signature", "error", err.Error())
		writeDomainError(w, err, "unauthorized")
		return
	}

	result, err := a.processWebhook(ctx, eventType, body)
	if err != nil {
		log.ErrorContext(ctx, "webhook processing failed", "error", err.Error())
		writeDomainError(w, err, "webhook processing failed")
		return
	}

	status := http.StatusOK
	if result == WebhookIgnored || result == WebhookQueued {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]string{"status": string(result)})
}

func (a *App) handleScheduled(w http.ResponseWriter, r *http.Request) {
	action := chi.URLParam(r, "action")
	if err := a.RunScheduled(r.Context(), action); err != nil {
		a.logger.ErrorContext(r.Context(), "scheduled action failed",
			"action", action,
			"error", err.Error())
		writeDomainError(w, err, "scheduled action failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "success", "action": action})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeDomainError responds with the status for err's kind. The message
// never includes err itself, which can hold internal details.
func writeDomainError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, domain.ErrAuth):
		writeError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, domain.ErrUnknownAction):
		writeError(w, http.StatusNotFound, "unknown scheduled action")
	case errors.Is(err, domain.ErrValidation):
		writeError(w, http.StatusBadRequest, "invalid request")
	case errors.Is(err, domain.ErrFeatureDisabled):
		writeError(w, http.StatusConflict, "feature is not enabled")
	case errors.Is(err, domain.ErrConfig):
		writeError(w, http.StatusServiceUnavailable, "service not configured")
	case errors.Is(err, domain.ErrAPI):
		writeError(w, http.StatusBadGateway, fallback)
	default:
		writeError(w, http.StatusInternalServerError, fallback)
	}
}
