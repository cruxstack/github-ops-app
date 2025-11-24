# Agent Guidelines

## Architecture Overview
- **Deployment**: Standard HTTP server OR AWS Lambda (both supported)
- **GitHub**: GitHub App required (JWT + installation token authentication)
- **Features**: Okta group sync + PR compliance monitoring (both optional)
- **Entry points**:
  - `cmd/server/main.go` - Standard HTTP server (VPS, container, K8s)
  - `cmd/lambda/main.go` - Lambda adapter (API Gateway + EventBridge)
  - `cmd/verify/main.go` - Integration tests with HTTP mock servers
  - `cmd/sample/main.go` - **DO NOT RUN** (requires live credentials)
- **Packages**:
  - `internal/app/` - Core logic, configuration, and HTTP handlers (no AWS
    dependencies)
  - `internal/github/` - API client, webhooks, PR checks, team mgmt, auth
  - `internal/okta/` - API client, group sync
  - `internal/notifiers/` - Slack formatting for events and reports
  - `internal/errors/` - Sentinel errors

## Build & Test
- **Build server**: `make build-server` (creates `dist/server`)
- **Build Lambda**: `make build-lambda` (creates `dist/bootstrap`)
- **Run server locally**: `make server`
- **Test all**: `make test` (runs with `-race -count=1`)
- **Test single package**: `go test -race -count=1 ./internal/github`
- **Test single function**: `go test -race -count=1 ./internal/okta -run
  TestGroupSync`
- **Integration tests**: `make test-verify` (offline tests using HTTP mock
  servers)
- **Verbose integration tests**: `make test-verify-verbose` (shows all HTTP
  requests during testing)
- **Lint**: `go vet ./...` and `gofmt -l .`

IMPORTANT: DO NOT run `go run cmd/sample/main.go` as it requires live
credentials and makes real API calls to GitHub/Okta/Slack.

## Code Style
- **Imports**: stdlib, blank line, third-party, local (e.g., `internal/`)
- **Naming**: `PascalCase` exports, `camelCase` private, `ALL_CAPS` env vars (prefixed `APP_`)
- **Structs**: define types in package; constructors as `New()` or `NewTypeName()`; methods public (PascalCase)
- **Formatting**: `gofmt` (tabs for indentation)
- **Comments**: rare, lowercase, short; prefer self-documenting code
- **Error handling**: return errors up stack; wrap with `fmt.Errorf` (see Error Handling below)

## Error Handling

### Error Message Format
- **Style**: lowercase, action-focused, concise
- **Pattern**: `"failed to {action} {object}: {context}"`
- **Always include**: specific identifiers (PR numbers, team names, IDs)
- **Examples**:
  - ✅ `"failed to fetch pr #123 from owner/repo: %w"`
  - ✅ `"failed to create team 'engineers' in org 'myorg': %w"`
  - ✅ `"required check 'ci' did not pass"`
  - ❌ `"PR is nil"` (no context)
  - ❌ `"Failed to Get Team"` (capitalized, generic)

### Error Wrapping
- Use `github.com/cockroachdb/errors` package for all error handling
- **Wrap errors**: `errors.Wrap(err, "context")` or `errors.Wrapf(err,
  "failed to sync team '%s'", teamName)`
- **Create new errors**: `errors.New("message")` or `errors.Newf("error:
  %s", context)`
- Automatically captures stack traces for debugging Lambda issues
- Preserve original error context while adding specific details

### Sentinel Errors
- Define common errors in `internal/errors/errors.go`
- Each sentinel error is marked with a domain type (ValidationError,
  AuthError, APIError, ConfigError)
- Domain markers enable error classification and monitoring
- Use `errors.Is()` to check for sentinel errors in tests
- Use `errors.HasType()` to check for error domains
- Examples: `ErrMissingPRData`, `ErrInvalidSignature`, `ErrClientNotInit`

### Stack Traces
- Automatically captured when wrapping errors with cockroachdb/errors
- No performance overhead unless error is formatted
- Critical for debugging serverless Lambda executions
- Use `errors.WithDetailf()` to add structured context to auth/API errors

### Validation
- Validate at parse time, not during processing
- Webhook events validated in `ParseXxxEvent()` functions
- Return detailed validation errors immediately
- Prevents nil pointer issues downstream

### Error Logging vs Returning
- **Fatal errors** (config, init): return immediately
- **Recoverable errors** (individual items in batch): collect and continue
- **Optional features** (notifications): log only, don't fail parent operation
- Lambda handlers: log detailed errors, return sanitized messages to client

### Batch Operation Errors
- Collect errors in result structs (e.g., `SyncReport.Errors`)
- Continue processing remaining items
- Return aggregated results with partial success
- Helper methods: `HasErrors()`, `HasChanges()`

## Authentication & Integration

### GitHub
- **Required**: GitHub App (JWT + installation tokens, automatic rotation)
- **Auth flow**: JWT signed with private key → exchange for installation token → cached with auto-refresh
- **Webhooks**: HMAC-SHA256 signature verification

### Okta
- OAuth 2.0 with private key authentication
- **Required scopes**: `okta.groups.read` and `okta.users.read`
- Sync uses slug-based GitHub Teams API

### Slack
- Optional notifications for PR events and sync reports
- Configuration in `internal/notifiers/`

## Markdown Style
- **Line length**: Max 80 characters for prose text
- **Exceptions**: Code blocks, links, ASCII art, tables
- **Table alignment**: Align columns in plaintext for readability
- **Wrapping**: Break at natural boundaries (commas, periods, conjunctions)
- **Lists**: Indent continuation lines with 2 spaces

NOTE: AGENTS.md itself is exempt from the 80-character line limit.
