# Agent Guidelines

## Architecture Overview
- **Deployment**: HTTP server (container, K8s, VM) or AWS Lambda; both serve the same router
- **GitHub**: GitHub App (JWT + installation tokens); created by hand or with the github-app-setup-go installer
- **Features**: Okta group sync, PR compliance monitoring, stale security alert reports, Slack notifications (all optional)
- **Entry points**:
  - `cmd/server/main.go` - HTTP server with graceful shutdown
  - `cmd/lambda/` - Lambda adapter: API Gateway HTTP API (2.0) requests and EventBridge scheduled events
  - `cmd/sample/main.go` - **DO NOT RUN** (requires live credentials)
- **Packages**:
  - `internal/service/` - composition root shared by both entry points: github-app-setup-go runtime (load, retry, reload), health and installer routes, swaps the app on reload
  - `internal/app/` - the application: builds clients from config, router (`/webhooks`, admin, `/scheduled/{action}`), feature handlers
  - `internal/config/` - reads and validates all configuration; resolves SSM parameter ARNs
  - `internal/domain/` - shared types, interfaces, and error kinds; imports no internal package
  - `internal/github/client/` - GitHub API client: auth, PR compliance, teams, security alerts
  - `internal/github/webhooks/` - webhook signature validation and event parsing
  - `internal/okta/` - Okta API client: groups and members
  - `internal/teamsync/` - Okta group to GitHub team sync and orphaned user detection
  - `internal/notifiers/` - Slack messages
  - `internal/setup/` - GitHub App manifest and installer credential store
  - `internal/fakes/` - in-memory domain fakes for tests
- **Tests**: unit tests beside each package; `e2e/` runs `fixtures/scenarios.json` against the real service with mock GitHub, Okta, and Slack servers

## Build & Test
- **List targets**: `make help`
- **Build server**: `make build` (creates `dist/github-ops-app`)
- **Build Lambda**: `make build-lambda` (creates `dist/bootstrap`)
- **Run server locally**: `make run` (reads `.env`)
- **Test all**: `make test` (runs with `-race -count=1`)
- **Test single package**: `go test -race -count=1 ./internal/github/client`
- **Test single function**: `go test -race -count=1 ./internal/teamsync -run TestSync_NamedGroup`
- **End-to-end scenarios**: `make test-e2e`
- **Lint**: `make lint` (golangci-lint), `make fmt`
- **Vulnerabilities**: `make vuln` (govulncheck)
- **Everything CI runs**: `make check`

IMPORTANT: DO NOT run `go run ./cmd/sample` or `make sample` as they require
live credentials and make real API calls to GitHub/Okta/Slack.

## Code Style
- **Imports**: stdlib, blank line, third-party, blank line, local (`github.com/cruxstack/github-ops-app/...`); `make fmt` enforces it
- **Naming**: `PascalCase` exports, `camelCase` private
- **Constructors**: `New(Options)` or `New(cfg, ...Option)`; constructors make no network calls
- **Comments**: lowercase inline comments; doc comments are full sentences
- **Logging**: `log/slog` with key-value pairs (`"team", slug`), snake_case keys, `*Context` methods where a context exists
- **Formatting**: `gofmt` and `goimports` (tabs for indentation)

## Configuration
- All variables are read in `internal/config`; nothing else calls `os.Getenv`
- App settings use the `APP_` prefix; installer settings keep github-app-setup-go's names (`GITHUB_APP_INSTALLER_ENABLED`, `STORAGE_MODE`, ...)
- Validate at load time and report every problem at once; invalid or partial configuration is an error, never a silent default
- Renamed variables go in `removed` in `config.go`, so setting the old name is an error
- Any `APP_` value may be an SSM parameter ARN; installer variables are read as github-app-setup-go reads them

## Error Handling

### Error Messages
- **Style**: lowercase, action-focused, concise
- **Pattern**: `"failed to {action} {object}: {context}"`
- **Always include**: specific identifiers (PR numbers, team names, IDs)
- **Examples**:
  - ✅ `fmt.Errorf("failed to fetch pr #%d from %s/%s: %w", n, owner, repo, err)`
  - ✅ `fmt.Errorf("failed to create team '%s' in org '%s': %w", name, org, err)`
  - ❌ `errors.New("PR is nil")` (no context)
  - ❌ `errors.New("Failed to Get Team")` (capitalized, generic)

### Wrapping
- Use the standard library: `fmt.Errorf("...: %w", err)`, `errors.Is`, `errors.As`, `errors.Join`
- Return errors up the stack with context; don't log and return the same error

### Error Kinds
- `internal/domain/errors.go` defines kinds (`ErrValidation`, `ErrAuth`, `ErrAPI`, `ErrConfig`) and sentinels that belong to a kind (`ErrInvalidSignature`, `ErrTeamNotFound`, ...)
- `errors.Is(err, domain.ErrAuth)` matches any auth sentinel, through wrapping
- The HTTP layer maps kinds to status codes in `writeDomainError` and never returns error details to clients

### Logging vs Returning
- **Fatal errors** (config, init): return immediately
- **Recoverable errors** (one user, team, or alert source in a batch): collect in the result (`SyncReport.Errors`, `SecurityAlertsReport.Errors`) and continue
- **Optional features** (notifications): log, don't fail the parent operation

### Batch Operations
- Collect errors in result structs and continue with the remaining items
- Never remove team members based on incomplete data: a failed Okta read skips the group, the safety threshold caps removals, and child-team members are never removed from a parent
- Never report orphaned users from incomplete data: detection is skipped unless every rule's team was found

## Security
- Webhooks are always signature-checked; there is no unsigned mode
- Admin endpoints require `APP_ADMIN_TOKEN` (constant-time compare) and are disabled without it
- Only events from `APP_GITHUB_ORG` are acted on
- Escape GitHub and Okta text in Slack mrkdwn with `escape`/`link` in `internal/notifiers`
- Validate GitHub logins (`domain.IsValidGitHubLogin`) before putting them in API paths
- Auth headers are only sent to the configured GitHub API host

## Markdown Style
- **Line length**: Max 80 characters for prose text
- **Exceptions**: Code blocks, links, ASCII art, tables
- **Table alignment**: Align columns in plaintext for readability
- **Wrapping**: Break at natural boundaries (commas, periods, conjunctions)
- **Lists**: Indent continuation lines with 2 spaces

NOTE: AGENTS.md itself is exempt from the 80-character line limit.
