// Command lambda runs github-ops-app as an AWS Lambda function. It handles
// API Gateway HTTP API (payload format 2.0) requests, which carry GitHub
// webhooks and admin requests, and EventBridge events, which run scheduled
// actions.
package main

import (
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/cruxstack/github-ops-app/internal/config"
	"github.com/cruxstack/github-ops-app/internal/service"
)

// version is set at build time with -ldflags "-X main.version=v1.0.0".
var version = "dev"

func main() {
	var level slog.LevelVar
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: &level}))
	slog.SetDefault(logger)

	boot, err := config.LoadBootstrap(config.ProcessEnv())
	if err != nil {
		logger.Error("invalid configuration", "version", version, "error", err.Error())
		os.Exit(1)
	}
	level.Set(boot.LogLevel)

	// the application configuration loads on the first invocation, and is
	// retried on later ones until it succeeds
	svc, err := service.New(boot, service.Options{Mode: service.ModeLambda, Logger: logger})
	if err != nil {
		logger.Error("failed to create service", "version", version, "error", err.Error())
		os.Exit(1)
	}

	logger.Info("starting", "version", version)
	lambda.Start(newHandler(svc, logger).invoke)
}
