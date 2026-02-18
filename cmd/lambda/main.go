package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	awsevents "github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/app"
	"github.com/cruxstack/github-ops-app/internal/config"
)

var (
	initOnce sync.Once
	appInst  *app.App
	router   http.Handler
	logger   *slog.Logger
	initErr  error
)

func initApp() {
	initOnce.Do(func() {
		logger = config.NewLogger()

		cfg, err := config.NewConfig()
		if err != nil {
			initErr = errors.Wrap(err, "config init failed")
			return
		}
		appInst, initErr = app.NewApp(context.Background(), cfg, logger)
		if initErr != nil {
			return
		}
		router = appInst.Handler()
	})
}

// APIGatewayHandler converts API Gateway requests to stdlib *http.Request
// and routes them through the chi router.
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

	httpReq, err := http.NewRequestWithContext(
		ctx,
		req.RequestContext.HTTP.Method,
		req.RawPath,
		strings.NewReader(req.Body),
	)
	if err != nil {
		return awsevents.APIGatewayV2HTTPResponse{
			StatusCode: 500,
			Body:       "failed to construct http request",
		}, nil
	}

	for key, value := range req.Headers {
		httpReq.Header.Set(key, value)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httpReq)

	return awsevents.APIGatewayV2HTTPResponse{
		StatusCode: rec.Code,
		Headers:    flattenHeaders(rec.Header()),
		Body:       rec.Body.String(),
	}, nil
}

// EventBridgeHandler converts EventBridge events to POST /scheduled/{action}
// requests and routes them through the chi router.
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

	path := fmt.Sprintf("%s/scheduled/%s", appInst.Config.BasePath, detail.Action)

	var body []byte
	if detail.Data != nil {
		body = detail.Data
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, path, bytes.NewReader(body))
	if err != nil {
		return errors.Wrap(err, "failed to construct http request")
	}

	if appInst.Config.AdminToken != "" {
		httpReq.Header.Set("Authorization", "Bearer "+appInst.Config.AdminToken)
	}
	if len(body) > 0 {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httpReq)

	if rec.Code >= 400 {
		return errors.Newf("scheduled event failed: %s", rec.Body.String())
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

	return nil, errors.New("unknown lambda event type")
}

// flattenHeaders converts multi-value http.Header to single-value map.
func flattenHeaders(h http.Header) map[string]string {
	flat := make(map[string]string, len(h))
	for key, values := range h {
		if len(values) > 0 {
			flat[key] = values[0]
		}
	}
	return flat
}

func main() {
	lambda.Start(UniversalHandler)
}
