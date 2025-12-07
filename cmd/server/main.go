package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/cruxstack/github-ops-app/internal/app"
	"github.com/cruxstack/github-ops-app/internal/config"
)

var (
	appInst *app.App
	logger  *slog.Logger
)

func main() {
	logger = config.NewLogger()
	ctx := context.Background()

	cfg, err := config.NewConfig()
	if err != nil {
		logger.Error("config init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	appInst, err = app.New(ctx, cfg)
	if err != nil {
		logger.Error("app init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", httpHandler)

	port := os.Getenv("APP_PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	done := make(chan bool, 1)
	quit := make(chan os.Signal, 1)

	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		logger.Info("server is shutting down")

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		srv.SetKeepAlivesEnabled(false)
		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("server shutdown failed", slog.String("error", err.Error()))
		}
		close(done)
	}()

	logger.Info("server starting", slog.String("port", port))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("server failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	<-done
	logger.Info("server stopped")
}

// httpHandler converts http.Request to app.Request and handles the response.
func httpHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	headers := make(map[string]string)
	for key, values := range r.Header {
		if len(values) > 0 {
			headers[strings.ToLower(key)] = values[0]
		}
	}

	req := app.Request{
		Type:    app.RequestTypeHTTP,
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: headers,
		Body:    body,
	}

	resp := appInst.HandleRequest(r.Context(), req)

	for key, value := range resp.Headers {
		w.Header().Set(key, value)
	}
	if resp.ContentType != "" && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", resp.ContentType)
	}

	w.WriteHeader(resp.StatusCode)
	if len(resp.Body) > 0 {
		w.Write(resp.Body)
	}
}
