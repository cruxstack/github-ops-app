package e2e

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// mockResponse is a canned response for matching requests. Responses are
// tried in order; the first whose method, path, and query match is used.
type mockResponse struct {
	Description string `json:"description,omitempty"`

	Service    string            `json:"service"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Query      string            `json:"query,omitempty"`
	StatusCode int               `json:"status_code"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body"`
}

// request is a request a mock server received.
type request struct {
	Method string
	Path   string
	Query  string
	Body   string
}

// mockServer is a fake API that records requests and replies with canned
// responses.
type mockServer struct {
	name      string
	responses []mockResponse
	logf      func(format string, args ...any)

	mu       sync.Mutex
	requests []request
}

func newMockServer(name string, responses []mockResponse, logf func(string, ...any)) *mockServer {
	return &mockServer{name: name, responses: responses, logf: logf}
}

func (m *mockServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	m.mu.Lock()
	m.requests = append(m.requests, request{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(body)})
	m.mu.Unlock()

	for _, resp := range m.responses {
		if resp.Method != r.Method || !matchPath(r.URL.Path, resp.Path) || !strings.Contains(r.URL.RawQuery, resp.Query) {
			continue
		}
		m.logf("%-6s %s %s?%s -> %d", m.name, r.Method, r.URL.Path, r.URL.RawQuery, resp.StatusCode)
		for k, v := range resp.Headers {
			w.Header().Set(k, strings.ReplaceAll(v, "{host}", r.Host))
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write([]byte(resp.Body))
		return
	}

	m.logf("%-6s %s %s?%s -> no mock, 404", m.name, r.Method, r.URL.Path, r.URL.RawQuery)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprint(w, `{"message":"not found in mock"}`)
}

func (m *mockServer) received() []request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]request(nil), m.requests...)
}

// matchPath reports whether path matches pattern, in which "*" matches any
// run of characters.
func matchPath(path, pattern string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return path == pattern
	}
	if !strings.HasPrefix(path, parts[0]) {
		return false
	}
	path = path[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(path, part)
		if i < 0 {
			return false
		}
		path = path[i+len(part):]
	}
	return strings.HasSuffix(path, parts[len(parts)-1])
}
