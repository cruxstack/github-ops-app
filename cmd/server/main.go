package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cruxstack/github-ops-app/internal/app"
	"github.com/cruxstack/github-ops-app/internal/config"
)

func main() {
	logger := config.NewLogger()
	ctx := context.Background()

	cfg, err := config.NewConfig()
	if err != nil {
		logger.Error("config init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	appInst, err := app.NewApp(ctx, cfg, logger)
	if err != nil {
		logger.Error("app init failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	port := os.Getenv("APP_PORT")
	if port == "" {
		port = "8080"
	}

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      appInst.Handler(),
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
