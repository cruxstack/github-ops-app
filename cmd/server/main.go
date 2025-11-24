// Package main provides a standard HTTP server entry point for the GitHub bot.
// runs as a long-lived HTTP server suitable for deployment on any VPS, container,
// or Kubernetes cluster.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
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
	mux.HandleFunc("/webhooks", appInst.WebhookHandler)
	mux.HandleFunc("/server/status", appInst.StatusHandler)
	mux.HandleFunc("/server/config", appInst.ConfigHandler)
	mux.HandleFunc("/scheduled/okta-sync", scheduledOktaSyncHandler)

	port := os.Getenv("PORT")
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

// scheduledOktaSyncHandler handles HTTP-triggered Okta sync operations.
// can be invoked by external cron services or schedulers.
func scheduledOktaSyncHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	evt := app.ScheduledEvent{
		Action: "okta-sync",
	}

	if err := appInst.ProcessScheduledEvent(r.Context(), evt); err != nil {
		logger.Error("scheduled event processing failed",
			slog.String("action", evt.Action),
			slog.String("error", err.Error()))
		http.Error(w, "event processing failed", http.StatusInternalServerError)
		return
	}

	response := map[string]string{
		"status":  "success",
		"message": "okta sync completed",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Error("failed to encode response", slog.String("error", err.Error()))
	}
}
