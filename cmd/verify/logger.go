package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// testHandler implements slog.Handler for capturing application logs during
// tests. only displays logs in verbose mode.
type testHandler struct {
	prefix  string
	verbose bool
	w       io.Writer
}

// Enabled returns true for all log levels when verbose mode is enabled.
func (h *testHandler) Enabled(_ context.Context, _ slog.Level) bool {
	return true
}

// Handle formats and writes log records to output with test-appropriate
// formatting.
func (h *testHandler) Handle(_ context.Context, r slog.Record) error {
	if !h.verbose {
		return nil
	}

	prefix := h.prefix + "› "
	msg := r.Message

	var attrs []string
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, fmt.Sprintf("%s=%v", a.Key, a.Value))
		return true
	})

	if len(attrs) > 0 {
		msg = fmt.Sprintf("%s %s", msg, strings.Join(attrs, " "))
	}

	fmt.Fprintf(h.w, "%s%s\n", prefix, msg)
	return nil
}

// WithAttrs returns the handler unchanged.
func (h *testHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h
}

// WithGroup returns the handler unchanged.
func (h *testHandler) WithGroup(name string) slog.Handler {
	return h
}
