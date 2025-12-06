# Integration Tests

Offline integration tests that validate the bot's HTTP interactions with GitHub, Okta, and Slack APIs using local mock servers.

## How It Works

Tests run the actual bot code against local HTTPS servers instead of
production APIs:

1. **Mock servers** start on localhost (ports 9001-9003) with self-signed
   TLS certificates
2. **Bot clients** (go-github, okta-sdk, slack-go) are configured via
   environment variables to use mock base URLs
3. **Requests** are captured and matched against expected API calls
4. **Responses** return predefined JSON from scenario definitions
5. **Validation** ensures all expected calls were made with correct
   parameters

**Key advantage**: Tests run against production code paths with real SDK
clients—no mocking of `internal/` packages required.

## Running Tests

```bash
# all tests
make test-verify

# verbose output (shows HTTP requests)
make test-verify-verbose

# single scenario
go run cmd/verify/main.go -filter="okta_sync"

# custom scenarios file
go run cmd/verify/main.go -scenarios=path/to/scenarios.json
```

### Setup

Copy `.env.example` to `.env` (dummy credentials—never sent to real APIs):
```bash
cp cmd/verify/.env.example cmd/verify/.env
```

## Test Scenarios

Scenarios are defined in `fixtures/scenarios.json` with:

- **Input event**: Webhook or EventBridge payload
- **Expected API calls**: Method, path, query params, request body
- **Mock responses**: Status code and response body

### Example Scenario

```jsonc
{
  "name": "pr_compliance_check",
  "event_type": "webhook",
  "webhook_type": "pull_request",
  "webhook_payload": {"action": "closed", "pull_request": {...}},
  "expected_calls": [
    {
      "service": "github",
      "method": "GET",
      "path": "/repos/*/pulls/*"
    }
  ],
  "mock_responses": [
    {
      "service": "github",
      "method": "GET",
      "path": "/repos/owner/repo/pulls/123",
      "status_code": 200,
      "body": "{\"number\":123,\"merged\":true}"
    }
  ]
}
```

Path matching supports wildcards (`*`) for dynamic segments like org names, repo names, or IDs.

## Adding Tests

1. Add scenario to `fixtures/scenarios.json`
2. Define input event (webhook or scheduled)
3. List expected API calls with paths
4. Provide mock responses
5. Run with `make test-verify`

## Architecture

```
Test Scenario → app.HandleRequest() → SDK Client → Mock Server (localhost:9001-9003)
                                                          ↓
                                                     Record Request
                                                          ↓
                                                     Return Mock Response
                                                          ↓
                                               Validate Expected Calls Made
```

Tests use the unified `app.HandleRequest()` interface, ensuring the same code
paths are exercised as production deployments.

Base URLs configured via environment (all HTTPS with self-signed certs):
- `APP_GITHUB_BASE_URL` → `https://localhost:9001/`
- `APP_OKTA_BASE_URL` → `https://localhost:9002/`
- `APP_SLACK_API_URL` → `https://localhost:9003/`

## Debugging

Failed tests show:
- All captured HTTP requests (method, path, headers, body)
- Missing expected calls
- Unexpected calls made

Use `-verbose` for real-time request logging during execution.

## Limitations

- Fixed ports (9001-9003) prevent parallel test execution
- Tests run serially
- All services use self-signed TLS certificates (auto-generated per test)
- GitHub base URL requires trailing slash
