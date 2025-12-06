// Package app provides unified request/response handling for all runtimes.
package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/cruxstack/github-ops-app/internal/github"
)

// RequestType identifies the category of incoming request.
type RequestType string

const (
	// RequestTypeHTTP represents HTTP requests (webhooks, status, config).
	RequestTypeHTTP RequestType = "http"
	// RequestTypeScheduled represents scheduled/cron events.
	RequestTypeScheduled RequestType = "scheduled"
)

// Request is a unified request type that abstracts HTTP and scheduled events.
// Runtimes (server, lambda) convert their native formats to this type.
type Request struct {
	Type    RequestType       `json:"type"`
	Method  string            `json:"method,omitempty"`
	Path    string            `json:"path,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    []byte            `json:"body,omitempty"`

	// ScheduledAction is used for scheduled events (e.g., "okta-sync").
	ScheduledAction string `json:"scheduled_action,omitempty"`
	// ScheduledData contains optional payload for scheduled events.
	ScheduledData json.RawMessage `json:"scheduled_data,omitempty"`
}

// Response is a unified response type returned by HandleRequest.
// Runtimes convert this to their native response format.
type Response struct {
	StatusCode  int               `json:"status_code"`
	Headers     map[string]string `json:"headers,omitempty"`
	Body        []byte            `json:"body,omitempty"`
	ContentType string            `json:"content_type,omitempty"`
}

// HandleRequest routes incoming requests to the appropriate handler.
// This is the single entry point for all request processing.
func (a *App) HandleRequest(ctx context.Context, req Request) Response {
	if a.Config.DebugEnabled {
		j, _ := json.Marshal(req)
		a.Logger.Debug("handling request", slog.String("request", string(j)))
	}

	switch req.Type {
	case RequestTypeScheduled:
		return a.handleScheduledRequest(ctx, req)
	case RequestTypeHTTP:
		return a.handleHTTPRequest(ctx, req)
	default:
		return errorResponse(400, "unknown request type")
	}
}

// handleScheduledRequest processes scheduled/cron events.
func (a *App) handleScheduledRequest(ctx context.Context, req Request) Response {
	evt := ScheduledEvent{
		Action: req.ScheduledAction,
		Data:   req.ScheduledData,
	}

	if err := a.ProcessScheduledEvent(ctx, evt); err != nil {
		a.Logger.Error("scheduled event processing failed",
			slog.String("action", evt.Action),
			slog.String("error", err.Error()))
		return errorResponse(500, "scheduled event processing failed")
	}

	return jsonResponse(200, map[string]string{
		"status":  "success",
		"message": evt.Action + " completed",
	})
}

// handleHTTPRequest routes HTTP requests based on path.
// strips BasePath prefix if configured (e.g., "/api/v1" -> "/").
func (a *App) handleHTTPRequest(ctx context.Context, req Request) Response {
	path := req.Path
	if a.Config.BasePath != "" {
		path = strings.TrimPrefix(path, a.Config.BasePath)
		if path == "" {
			path = "/"
		}
	}

	switch path {
	case "/server/status":
		return a.handleStatusRequest(req)
	case "/server/config":
		return a.handleConfigRequest(req)
	case "/webhooks", "/":
		return a.handleWebhookRequest(ctx, req)
	default:
		if strings.HasPrefix(path, "/scheduled/") {
			return a.handleScheduledHTTPRequest(ctx, req, path)
		}
		return errorResponse(404, "not found")
	}
}

// handleStatusRequest returns application status.
func (a *App) handleStatusRequest(req Request) Response {
	if req.Method != "GET" {
		return errorResponse(405, "method not allowed")
	}
	return jsonResponse(200, a.GetStatus())
}

// handleConfigRequest returns redacted configuration.
func (a *App) handleConfigRequest(req Request) Response {
	if req.Method != "GET" {
		return errorResponse(405, "method not allowed")
	}
	return jsonResponse(200, a.Config.Redacted())
}

// handleWebhookRequest processes GitHub webhook POST requests.
func (a *App) handleWebhookRequest(ctx context.Context, req Request) Response {
	if req.Method != "POST" {
		return errorResponse(405, "method not allowed")
	}

	eventType := req.Headers["x-github-event"]
	signature := req.Headers["x-hub-signature-256"]

	if err := github.ValidateWebhookSignature(
		req.Body,
		signature,
		a.Config.GitHubWebhookSecret,
	); err != nil {
		a.Logger.Warn("webhook signature validation failed",
			slog.String("error", err.Error()))
		return errorResponse(401, "unauthorized")
	}

	if err := a.ProcessWebhook(ctx, req.Body, eventType); err != nil {
		a.Logger.Error("webhook processing failed",
			slog.String("event_type", eventType),
			slog.String("error", err.Error()))
		return errorResponse(500, "webhook processing failed")
	}

	return Response{
		StatusCode:  200,
		ContentType: "text/plain",
		Body:        []byte("ok"),
	}
}

// handleScheduledHTTPRequest processes scheduled events via HTTP POST.
// path is the normalized path with BasePath already stripped.
func (a *App) handleScheduledHTTPRequest(ctx context.Context, req Request, path string) Response {
	if req.Method != "POST" {
		return errorResponse(405, "method not allowed")
	}

	// extract action from path (e.g., "/scheduled/okta-sync" -> "okta-sync")
	action := strings.TrimPrefix(path, "/scheduled/")
	if action == "" {
		return errorResponse(400, "missing scheduled action")
	}

	scheduledReq := Request{
		Type:            RequestTypeScheduled,
		ScheduledAction: action,
	}

	return a.handleScheduledRequest(ctx, scheduledReq)
}

// jsonResponse creates a JSON response with the given status and data.
func jsonResponse(status int, data any) Response {
	body, err := json.Marshal(data)
	if err != nil {
		return errorResponse(500, "failed to marshal response")
	}
	return Response{
		StatusCode:  status,
		ContentType: "application/json",
		Headers:     map[string]string{"Content-Type": "application/json"},
		Body:        body,
	}
}

// errorResponse creates an error response with the given status and message.
func errorResponse(status int, message string) Response {
	return Response{
		StatusCode:  status,
		ContentType: "text/plain",
		Body:        []byte(message),
	}
}
