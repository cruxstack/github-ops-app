package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

type fakeService struct {
	handler   http.HandlerFunc
	actions   []string
	actionErr error
}

func (f *fakeService) Handler() http.Handler { return f.handler }

func (f *fakeService) RunScheduled(_ context.Context, action string) error {
	f.actions = append(f.actions, action)
	return f.actionErr
}

func invoke(t *testing.T, svc *fakeService, event string) (any, error) {
	t.Helper()
	h := newHandler(svc, slog.New(slog.DiscardHandler))
	return h.invoke(context.Background(), json.RawMessage(event))
}

func TestInvoke_APIGateway(t *testing.T) {
	var got *http.Request
	var gotBody string
	svc := &fakeService{handler: func(w http.ResponseWriter, r *http.Request) {
		got = r
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Add("X-Multi", "a")
		w.Header().Add("X-Multi", "b")
		http.SetCookie(w, &http.Cookie{Name: "c1", Value: "v1"})
		http.SetCookie(w, &http.Cookie{Name: "c2", Value: "v2"})
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte{0xff, 0xfe})
	}}

	event := `{
		"version": "2.0",
		"rawPath": "/prod/a%20b",
		"rawQueryString": "x=1&y=2",
		"cookies": ["s=1", "t=2"],
		"headers": {"host": "api.example.com", "x-github-event": "ping", "x-forwarded-proto": "https"},
		"body": "` + base64.StdEncoding.EncodeToString([]byte(`{"zen":"hi"}`)) + `",
		"isBase64Encoded": true,
		"requestContext": {"domainName": "fallback.example.com", "http": {"method": "POST", "sourceIp": "203.0.113.9"}}
	}`
	out, err := invoke(t, svc, event)
	if err != nil {
		t.Fatal(err)
	}

	if got.Method != http.MethodPost || got.URL.Path != "/prod/a b" || got.URL.RawQuery != "x=1&y=2" {
		t.Errorf("request = %s %s ? %s", got.Method, got.URL.Path, got.URL.RawQuery)
	}
	if gotBody != `{"zen":"hi"}` || got.Host != "api.example.com" || got.RemoteAddr != "203.0.113.9" {
		t.Errorf("body %q host %q remote %q", gotBody, got.Host, got.RemoteAddr)
	}
	if got.Header.Get("X-GitHub-Event") != "ping" || got.Header.Get("Cookie") != "s=1; t=2" {
		t.Errorf("headers = %v", got.Header)
	}

	raw, _ := json.Marshal(out)
	var resp struct {
		StatusCode      int               `json:"statusCode"`
		Headers         map[string]string `json:"headers"`
		Cookies         []string          `json:"cookies"`
		Body            string            `json:"body"`
		IsBase64Encoded bool              `json:"isBase64Encoded"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated || resp.Headers["X-Multi"] != "a, b" || len(resp.Cookies) != 2 {
		t.Errorf("response = %+v", resp)
	}
	if !resp.IsBase64Encoded || resp.Body != base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe}) {
		t.Errorf("binary body not base64 encoded: %+v", resp)
	}
}

func TestInvoke_APIGatewayBadBody(t *testing.T) {
	svc := &fakeService{handler: func(http.ResponseWriter, *http.Request) { t.Error("handler called") }}
	out, err := invoke(t, svc, `{"rawPath":"/","body":"%%%","isBase64Encoded":true,"requestContext":{"http":{"method":"POST"}}}`)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), `"statusCode":400`) {
		t.Errorf("response = %s, want 400", raw)
	}
}

func TestInvoke_EventBridge(t *testing.T) {
	svc := &fakeService{}
	event := `{"id":"e1","source":"aws.events","detail-type":"Scheduled Event","detail":{"action":"okta-sync"}}`
	if _, err := invoke(t, svc, event); err != nil {
		t.Fatal(err)
	}
	if strings.Join(svc.actions, ",") != "okta-sync" {
		t.Errorf("actions = %v", svc.actions)
	}

	svc.actionErr = errors.New("boom")
	if _, err := invoke(t, svc, event); err == nil {
		t.Error("failed action returned no error")
	}

	if _, err := invoke(t, svc, `{"source":"aws.events","detail-type":"Scheduled Event","detail":{}}`); err == nil {
		t.Error("event without an action returned no error")
	}
}

func TestInvoke_Unsupported(t *testing.T) {
	if _, err := invoke(t, &fakeService{}, `{"Records":[]}`); err == nil {
		t.Error("unsupported event returned no error")
	}
	if _, err := invoke(t, &fakeService{}, `not json`); err == nil {
		t.Error("invalid json returned no error")
	}
}
