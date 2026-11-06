package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"unicode/utf8"

	awsevents "github.com/aws/aws-lambda-go/events"
)

// scheduler runs scheduled actions.
type scheduler interface {
	RunScheduled(ctx context.Context, action string) error
}

// backend is what the Lambda handler needs from service.Service.
type backend interface {
	scheduler
	Handler() http.Handler
}

type handler struct {
	svc    backend
	logger *slog.Logger
}

func newHandler(svc backend, logger *slog.Logger) *handler {
	return &handler{svc: svc, logger: logger}
}

// event holds the fields that tell the supported event types apart.
type event struct {
	RequestContext struct {
		HTTP struct {
			Method string `json:"method"`
		} `json:"http"`
	} `json:"requestContext"`
	DetailType string `json:"detail-type"`
	Source     string `json:"source"`
}

// scheduledDetail is the detail of an EventBridge event that runs a
// scheduled action.
type scheduledDetail struct {
	Action string `json:"action"`
}

// invoke handles one Lambda invocation.
func (h *handler) invoke(ctx context.Context, raw json.RawMessage) (any, error) {
	var e event
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, fmt.Errorf("failed to decode lambda event: %w", err)
	}

	switch {
	case e.RequestContext.HTTP.Method != "":
		var req awsevents.APIGatewayV2HTTPRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, fmt.Errorf("failed to decode api gateway request: %w", err)
		}
		return h.serveHTTP(ctx, req), nil

	case e.DetailType != "":
		var evt awsevents.CloudWatchEvent
		if err := json.Unmarshal(raw, &evt); err != nil {
			return nil, fmt.Errorf("failed to decode eventbridge event: %w", err)
		}
		return nil, h.runScheduled(ctx, evt)

	default:
		return nil, errors.New("unsupported lambda event: expected an api gateway http api (2.0) request or an eventbridge event")
	}
}

// runScheduled runs the action named in an EventBridge event's detail.
// EventBridge invocations are authorized by AWS, so no admin token is
// needed.
func (h *handler) runScheduled(ctx context.Context, evt awsevents.CloudWatchEvent) error {
	var detail scheduledDetail
	if err := json.Unmarshal(evt.Detail, &detail); err != nil || detail.Action == "" {
		return fmt.Errorf("eventbridge event %s has no action in its detail", evt.ID)
	}
	if err := h.svc.RunScheduled(ctx, detail.Action); err != nil {
		h.logger.ErrorContext(ctx, "scheduled action failed",
			"action", detail.Action,
			"event_id", evt.ID,
			"error", err.Error())
		return fmt.Errorf("scheduled action '%s' failed: %w", detail.Action, err)
	}
	return nil
}

// serveHTTP passes an API Gateway request through the HTTP handler.
func (h *handler) serveHTTP(ctx context.Context, e awsevents.APIGatewayV2HTTPRequest) awsevents.APIGatewayV2HTTPResponse {
	req, err := toHTTPRequest(ctx, e)
	if err != nil {
		h.logger.WarnContext(ctx, "invalid api gateway request", "error", err.Error())
		return awsevents.APIGatewayV2HTTPResponse{
			StatusCode: http.StatusBadRequest,
			Headers:    map[string]string{"Content-Type": "application/json"},
			Body:       `{"error":"invalid request"}`,
		}
	}
	rec := httptest.NewRecorder()
	h.svc.Handler().ServeHTTP(rec, req)
	return toAPIGatewayResponse(rec.Result(), rec.Body.Bytes())
}

// toHTTPRequest converts an API Gateway HTTP API request to an
// http.Request.
func toHTTPRequest(ctx context.Context, e awsevents.APIGatewayV2HTTPRequest) (*http.Request, error) {
	body := []byte(e.Body)
	if e.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(e.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64 body: %w", err)
		}
		body = decoded
	}

	target := e.RawPath
	if target == "" {
		target = "/"
	}
	if e.RawQueryString != "" {
		target += "?" + e.RawQueryString
	}

	req, err := http.NewRequestWithContext(ctx, e.RequestContext.HTTP.Method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// api gateway joins the values of repeated headers with commas
	for name, value := range e.Headers {
		req.Header.Set(name, value)
	}
	if len(e.Cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(e.Cookies, "; "))
	}
	req.Host = req.Header.Get("Host")
	if req.Host == "" {
		req.Host = e.RequestContext.DomainName
	}
	req.RemoteAddr = e.RequestContext.HTTP.SourceIP
	req.RequestURI = target
	return req, nil
}

// toAPIGatewayResponse converts an HTTP response to an API Gateway response.
func toAPIGatewayResponse(res *http.Response, body []byte) awsevents.APIGatewayV2HTTPResponse {
	out := awsevents.APIGatewayV2HTTPResponse{
		StatusCode: res.StatusCode,
		Headers:    make(map[string]string, len(res.Header)),
	}
	for name, values := range res.Header {
		if http.CanonicalHeaderKey(name) == "Set-Cookie" {
			out.Cookies = append(out.Cookies, values...)
			continue
		}
		out.Headers[name] = strings.Join(values, ", ")
	}
	if utf8.Valid(body) {
		out.Body = string(body)
	} else {
		out.Body = base64.StdEncoding.EncodeToString(body)
		out.IsBase64Encoded = true
	}
	return out
}
