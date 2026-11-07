SHELL := /bin/bash
.DEFAULT_GOAL := help

# build settings; release builds pass GOOS=linux and each GOARCH
GOOS    ?= $(shell go env GOOS)
GOARCH  ?= $(shell go env GOARCH)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
DIST    := dist
BINARY  := github-ops-app
LDFLAGS := -s -w -X main.version=$(VERSION)

# tool versions, run with "go run" so they are built with this module's go
GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
GOVULNCHECK   := go run golang.org/x/vuln/cmd/govulncheck@v1.9.0

# the race detector on macos needs this linker flag with some xcode versions
TEST_ENV   := $(if $(filter Darwin,$(shell uname -s)),CGO_LDFLAGS=-w,)
TEST_FLAGS := -race -count=1

# local runs
COMPOSE    ?= docker compose
BASE_URL   ?= http://localhost:8080
URL_PATH    = $(patsubst %/,%,$(APP_BASE_PATH))

.PHONY: help
help: ## show this help
	@awk 'BEGIN {FS = ":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

##@ build

.PHONY: build
build: ## build the server binary (dist/github-ops-app) for GOOS and GOARCH
	GOOS=$(GOOS) GOARCH=$(GOARCH) CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$(BINARY) ./cmd/server

.PHONY: build-lambda
build-lambda: ## build the lambda bootstrap binary (dist/bootstrap) for linux and GOARCH
	GOOS=linux GOARCH=$(GOARCH) CGO_ENABLED=0 go build -trimpath -tags lambda.norpc -ldflags "$(LDFLAGS)" -o $(DIST)/bootstrap ./cmd/lambda

.PHONY: image
image: ## build the container image (github-ops-app:dev)
	docker build --build-arg VERSION=$(VERSION) -t $(BINARY):dev .

.PHONY: clean
clean: ## remove build output
	rm -rf $(DIST) coverage.out

##@ test

.PHONY: test
test: ## run all tests with the race detector
	$(TEST_ENV) go test $(TEST_FLAGS) ./...

.PHONY: test-unit
test-unit: ## run unit tests
	$(TEST_ENV) go test $(TEST_FLAGS) ./internal/... ./cmd/...

.PHONY: test-e2e
test-e2e: ## run the scenarios in fixtures/scenarios.json against mock apis
	$(TEST_ENV) go test $(TEST_FLAGS) -v ./e2e

.PHONY: cover
cover: ## run tests with coverage and print a summary
	go test -count=1 -coverprofile=coverage.out -coverpkg=./internal/... ./...
	go tool cover -func=coverage.out | tail -1

##@ quality

.PHONY: lint
lint: ## run golangci-lint
	$(GOLANGCI_LINT) run ./...

.PHONY: fmt
fmt: ## format code
	$(GOLANGCI_LINT) fmt ./...

.PHONY: vuln
vuln: ## check for known vulnerabilities
	$(GOVULNCHECK) ./...

.PHONY: tidy
tidy: ## tidy go.mod and go.sum
	go mod tidy

.PHONY: check
check: lint test vuln ## run lint, tests, and the vulnerability check, as CI does
	go mod tidy -diff

##@ local runs

.PHONY: run
run: ## run the server locally with go run, reading .env
	go run github.com/joho/godotenv/cmd/godotenv@v1.5.1 -f .env go run ./cmd/server

.PHONY: up
up: ## start the server in docker compose and wait until it is healthy
	$(COMPOSE) up --build --detach --wait server

.PHONY: down
down: ## stop docker compose
	$(COMPOSE) down --remove-orphans

.PHONY: logs
logs: ## follow the server's output
	$(COMPOSE) logs --follow server

.PHONY: health
health: ## check a running server's readiness (BASE_URL, APP_BASE_PATH)
	curl -fsS "$(BASE_URL)$(URL_PATH)/readyz"

.PHONY: okta-sync
okta-sync: ## run an okta sync on a running server (BASE_URL, APP_ADMIN_TOKEN)
	curl -fsS -X POST -H "Authorization: Bearer $(APP_ADMIN_TOKEN)" "$(BASE_URL)$(URL_PATH)/scheduled/okta-sync"

.PHONY: slack-test
slack-test: ## send sample notifications from a running server (BASE_URL, APP_ADMIN_TOKEN)
	curl -fsS -X POST -H "Authorization: Bearer $(APP_ADMIN_TOKEN)" "$(BASE_URL)$(URL_PATH)/scheduled/slack-test"

.PHONY: sample
sample: ## send fixtures/samples.json through the app with .env; calls live apis
	go run ./cmd/sample
