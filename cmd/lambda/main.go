// Package main provides the AWS Lambda entry point for the GitHub bot.
// This Lambda handler supports API Gateway HTTP API (v2.0) and EventBridge
// (scheduled sync) events.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	awsevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/cruxstack/github-ops-app/internal/app"
	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/github"
)

var (
	initOnce sync.Once
	appInst  *app.App
	logger   *slog.Logger
	initErr  error
)

// initApp initializes the application instance once using sync.Once.
// stores any initialization error in the initErr global variable.
func initApp() {
	initOnce.Do(func() {
		logger = config.NewLogger()

		cfg, err := config.NewConfig()
		if err != nil {
			initErr = fmt.Errorf("config init failed: %w", err)
			return
		}
		appInst, initErr = app.New(context.Background(), cfg)
	})
}

// APIGatewayHandler processes incoming API Gateway HTTP API (v2.0) requests.
// handles GitHub webhook events, status checks, and config endpoints.
// validates webhook signatures before processing events.
func APIGatewayHandler(ctx context.Context, req awsevents.APIGatewayV2HTTPRequest) (awsevents.APIGatewayV2HTTPResponse, error) {
	initApp()
	if initErr != nil {
		logger.Error("initialization failed", slog.String("error", initErr.Error()))
		return awsevents.APIGatewayV2HTTPResponse{
			StatusCode: 500,
			Body:       "service initialization failed",
		}, nil
	}

	if appInst.Config.DebugEnabled {
		j, _ := json.Marshal(req)
		logger.Debug("received api gateway request", slog.String("request", string(j)))
	}

	path := req.RawPath
	if appInst.Config.BasePath != "" {
		path = strings.TrimPrefix(path, appInst.Config.BasePath)
		if path == "" {
			path = "/"
		}
	}

	if path == "/server/status" {
		return handleServerStatus(ctx, req)
	}

	if path == "/server/config" {
		return handleServerConfig(ctx, req)
	}

	if req.RequestContext.HTTP.Method != "POST" {
		return awsevents.APIGatewayV2HTTPResponse{
			StatusCode: 405,
			Body:       "method not allowed",
		}, nil
	}

	eventType := req.Headers["x-github-event"]
	signature := req.Headers["x-hub-signature-256"]

	if err := github.ValidateWebhookSignature(
		[]byte(req.Body),
		signature,
		appInst.Config.GitHubWebhookSecret,
	); err != nil {
		logger.Warn("webhook signature validation failed", slog.String("error", err.Error()))
		return awsevents.APIGatewayV2HTTPResponse{
			StatusCode: 401,
			Body:       "unauthorized",
		}, nil
	}

	if err := appInst.ProcessWebhook(ctx, []byte(req.Body), eventType); err != nil {
		logger.Error("webhook processing failed",
			slog.String("event_type", eventType),
			slog.String("error", err.Error()))
		return awsevents.APIGatewayV2HTTPResponse{
			StatusCode: 500,
			Body:       "webhook processing failed",
		}, nil
	}

	return awsevents.APIGatewayV2HTTPResponse{
		StatusCode: 200,
		Body:       "ok",
	}, nil
}

// handleServerStatus returns the application status and feature flags.
// responds with JSON containing configuration state and enabled features.
func handleServerStatus(ctx context.Context, req awsevents.APIGatewayV2HTTPRequest) (awsevents.APIGatewayV2HTTPResponse, error) {
	if req.RequestContext.HTTP.Method != "GET" {
		return awsevents.APIGatewayV2HTTPResponse{
			StatusCode: 405,
			Body:       "method not allowed",
		}, nil
	}

	status := appInst.GetStatus()
	body, err := json.Marshal(status)
	if err != nil {
		logger.Error("failed to marshal status response", slog.String("error", err.Error()))
		return awsevents.APIGatewayV2HTTPResponse{
			StatusCode: 500,
			Body:       "failed to generate status response",
		}, nil
	}

	return awsevents.APIGatewayV2HTTPResponse{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       string(body),
	}, nil
}

// handleServerConfig returns the application configuration with secrets
// redacted. useful for debugging and verifying environment settings.
func handleServerConfig(ctx context.Context, req awsevents.APIGatewayV2HTTPRequest) (awsevents.APIGatewayV2HTTPResponse, error) {
	if req.RequestContext.HTTP.Method != "GET" {
		return awsevents.APIGatewayV2HTTPResponse{
			StatusCode: 405,
			Body:       "method not allowed",
		}, nil
	}

	redacted := appInst.Config.Redacted()
	body, err := json.Marshal(redacted)
	if err != nil {
		logger.Error("failed to marshal config response", slog.String("error", err.Error()))
		return awsevents.APIGatewayV2HTTPResponse{
			StatusCode: 500,
			Body:       "failed to generate config response",
		}, nil
	}

	return awsevents.APIGatewayV2HTTPResponse{
		StatusCode: 200,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       string(body),
	}, nil
}

// EventBridgeHandler processes EventBridge scheduled events.
// typically handles scheduled Okta group sync operations.
func EventBridgeHandler(ctx context.Context, evt awsevents.CloudWatchEvent) error {
	initApp()
	if initErr != nil {
		return initErr
	}

	if appInst.Config.DebugEnabled {
		j, _ := json.Marshal(evt)
		logger.Debug("received eventbridge event", slog.String("event", string(j)))
	}

	var detail app.ScheduledEvent
	if err := json.Unmarshal(evt.Detail, &detail); err != nil {
		logger.Error("failed to parse event detail", slog.String("error", err.Error()))
		return err
	}

	return appInst.ProcessScheduledEvent(ctx, detail)
}

// UniversalHandler detects the event type and routes to the appropriate
// handler. supports API Gateway HTTP API (v2.0) and EventBridge events.
func UniversalHandler(ctx context.Context, event json.RawMessage) (any, error) {
	if initErr != nil {
		return nil, initErr
	}

	// try API Gateway HTTP API (v2.0)
	var apiGatewayReq awsevents.APIGatewayV2HTTPRequest
	if err := json.Unmarshal(event, &apiGatewayReq); err == nil && apiGatewayReq.RequestContext.HTTP.Method != "" {
		return APIGatewayHandler(ctx, apiGatewayReq)
	}

	// try EventBridge
	var eventBridgeEvent awsevents.CloudWatchEvent
	if err := json.Unmarshal(event, &eventBridgeEvent); err == nil && eventBridgeEvent.DetailType != "" {
		return nil, EventBridgeHandler(ctx, eventBridgeEvent)
	}

	return nil, fmt.Errorf("unknown lambda event type")
}

func main() {
	lambda.Start(UniversalHandler)
}
