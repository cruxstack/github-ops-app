// Command server runs github-ops-app as an HTTP server, for containers,
// Kubernetes, or a VM.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/service"
)

// version is set at build time with -ldflags "-X main.version=v1.0.0".
var version = "dev"

// HTTP server timeouts. Webhook payloads can be large and scheduled actions
// run for minutes, so writes may take as long as a job.
const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = time.Minute
	writeTimeout      = service.JobTimeout + 30*time.Second
	idleTimeout       = 2 * time.Minute
	// shutdownTimeout lets running syncs finish rather than stop between
	// adding and removing members. The platform's grace period, such as
	// Kubernetes' terminationGracePeriodSeconds, caps it.
	shutdownTimeout = service.JobTimeout + 30*time.Second
)

func main() {
	var level slog.LevelVar
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &level}))
	slog.SetDefault(logger)

	if err := run(logger, &level); err != nil {
		logger.Error("server failed", "version", version, "error", err.Error())
		os.Exit(1)
	}
}

func run(logger *slog.Logger, level *slog.LevelVar) error {
	boot, err := config.LoadBootstrap(config.ProcessEnv())
	if err != nil {
		return err
	}
	level.Set(boot.LogLevel)

	svc, err := service.New(boot, service.Options{Mode: service.ModeServer, Logger: logger})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// listen before loading the configuration, so health checks and the
	// installer respond while it loads
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", boot.Server.ListenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", boot.Server.ListenAddr, err)
	}
	srv := &http.Server{
		Handler:           svc.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	logger.InfoContext(ctx, "listening",
		"version", version,
		"addr", ln.Addr().String(),
		"base_path", boot.Server.BasePath,
		"installer", boot.Installer.Enabled)

	if err := svc.Start(ctx); err != nil {
		_ = srv.Close()
		return err
	}

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}

	logger.InfoContext(ctx, "shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("failed to shut down: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	if err := svc.Wait(shutdownCtx); err != nil {
		return fmt.Errorf("background jobs did not finish: %w", err)
	}
	logger.InfoContext(ctx, "stopped")
	return nil
}
