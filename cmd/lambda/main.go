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
)

var (
	initOnce sync.Once
	appInst  *app.App
	logger   *slog.Logger
	initErr  error
)

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

// APIGatewayHandler converts API Gateway requests to unified app.Request.
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

	headers := make(map[string]string)
	for key, value := range req.Headers {
		headers[strings.ToLower(key)] = value
	}

	appReq := app.Request{
		Type:    app.RequestTypeHTTP,
		Method:  req.RequestContext.HTTP.Method,
		Path:    req.RawPath,
		Headers: headers,
		Body:    []byte(req.Body),
	}

	resp := appInst.HandleRequest(ctx, appReq)

	return awsevents.APIGatewayV2HTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    resp.Headers,
		Body:       string(resp.Body),
	}, nil
}

// EventBridgeHandler converts EventBridge events to unified app.Request.
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

	req := app.Request{
		Type:            app.RequestTypeScheduled,
		ScheduledAction: detail.Action,
		ScheduledData:   detail.Data,
	}

	resp := appInst.HandleRequest(ctx, req)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("scheduled event failed: %s", string(resp.Body))
	}

	return nil
}

// UniversalHandler detects event type and routes to the appropriate handler.
func UniversalHandler(ctx context.Context, event json.RawMessage) (any, error) {
	initApp()
	if initErr != nil {
		return nil, initErr
	}

	var apiGatewayReq awsevents.APIGatewayV2HTTPRequest
	if err := json.Unmarshal(event, &apiGatewayReq); err == nil && apiGatewayReq.RequestContext.HTTP.Method != "" {
		return APIGatewayHandler(ctx, apiGatewayReq)
	}

	var eventBridgeEvent awsevents.CloudWatchEvent
	if err := json.Unmarshal(event, &eventBridgeEvent); err == nil && eventBridgeEvent.DetailType != "" {
		return nil, EventBridgeHandler(ctx, eventBridgeEvent)
	}

	return nil, fmt.Errorf("unknown lambda event type")
}

func main() {
	lambda.Start(UniversalHandler)
}
