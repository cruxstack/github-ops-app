# github-ops-app

Bot that automates GitHub operations with Okta integration and Slack
notifications. Deploy as AWS Lambda, standard HTTP server, or container.

## Features

* **Okta group sync** - Automatically sync Okta groups to GitHub teams
* **Orphaned user detection** - Identify org members not in any synced teams
* **PR compliance monitoring** - Detect and notify when PRs bypass branch
  protection
* **Automatic reconciliation** - Detects external team changes and triggers
  sync
* **Flexible configuration** - Enable only what you need via environment
  variables
* **Slack notifications** - Rich messages for violations and sync reports

## Quick Start

### Prerequisites

* Go ≥ 1.24
* GitHub App ([setup guide](docs/github-app-setup.md))
* **Optional**: Okta API Service app ([setup guide](docs/okta-setup.md))
* **Optional**: Slack app ([setup guide](docs/slack-setup.md))

### Deployment Options

The bot can be deployed in multiple ways:

#### Option 1: Standard HTTP Server

Run as a long-lived HTTP server on any VPS, VM, or container platform:

```bash
# build
make build-server

# run (or use systemd, Docker, Kubernetes, etc.)
./dist/server

# server listens on APP_PORT (default: 8080)
# endpoints:
#   POST /webhooks              - GitHub webhook receiver
#   POST /scheduled/okta-sync   - Trigger Okta sync (call via cron)
#   POST /scheduled/slack-test  - Send test notification to Slack
#   GET  /server/status         - Health check
#   GET  /server/config         - Config (secrets redacted)
```

**Scheduling Okta Sync**: Use any cron service or scheduler to POST to
`/scheduled/okta-sync` periodically. No EventBridge required.

#### Option 2: AWS Lambda

Deploy as serverless function with automatic scaling:

```bash
# build for Lambda
make build-lambda  # creates dist/bootstrap
```

See [cmd/lambda/README.md](cmd/lambda/README.md) for complete Lambda deployment
instructions including API Gateway and EventBridge configuration.

## Configuration

See [`.env.example`](.env.example) for a complete configuration reference.

All configuration values support direct values or AWS SSM parameter references.
For sensitive values like secrets and private keys, use SSM parameters with
automatic decryption:

```bash
# Direct value
APP_GITHUB_WEBHOOK_SECRET=my-secret

# SSM parameter (automatically decrypted if SecureString)
APP_GITHUB_WEBHOOK_SECRET=arn:aws:ssm:us-east-1:123456789012:parameter/github-bot/webhook-secret
```

**Requirements for SSM parameters**:
- Valid AWS credentials with `ssm:GetParameter` permission
- Full SSM parameter ARN in format:
  `arn:aws:ssm:REGION:ACCOUNT:parameter/path/to/param`
- SecureString parameters are automatically decrypted

### Required: GitHub

| Variable                            | Description                     |
|-------------------------------------|---------------------------------|
| `APP_GITHUB_APP_ID`                 | GitHub App ID                   |
| `APP_GITHUB_APP_PRIVATE_KEY`        | Private key (PEM)               |
| `APP_GITHUB_APP_PRIVATE_KEY_PATH`   | Path to private key file        |
| `APP_GITHUB_INSTALLATION_ID`        | Installation ID                 |
| `APP_GITHUB_ORG`                    | Organization name               |
| `APP_GITHUB_WEBHOOK_SECRET`         | Webhook signature secret        |

### Optional: Okta Sync

| Variable                               | Description                                   |
|----------------------------------------|-----------------------------------------------|
| `APP_OKTA_DOMAIN`                      | Okta domain                                   |
| `APP_OKTA_CLIENT_ID`                   | OAuth 2.0 client ID                           |
| `APP_OKTA_PRIVATE_KEY`                 | Private key (PEM) or use                      |
| `APP_OKTA_PRIVATE_KEY_PATH`            | Path to private key file                      |
| `APP_OKTA_GITHUB_USER_FIELD`           | User profile field for username               |
| `APP_OKTA_SYNC_RULES`                  | JSON array (see [examples](#okta-sync-rules)) |
| `APP_OKTA_SYNC_SAFETY_THRESHOLD`       | Max removal ratio (default: `0.5` = 50%)      |
| `APP_OKTA_ORPHANED_USER_NOTIFICATIONS` | Notify about orphaned users                   |

### Optional: PR Compliance

| Variable                         | Description                               |
|----------------------------------|-------------------------------------------|
| `APP_PR_COMPLIANCE_ENABLED`      | Enable monitoring (`true`)                |
| `APP_PR_MONITORED_BRANCHES`      | Branches to monitor (e.g., `main,master`) |

### Optional: Slack

| Variable                          | Description                              |
|-----------------------------------|------------------------------------------|
| `APP_SLACK_TOKEN`                 | Bot token (`xoxb-...`)                   |
| `APP_SLACK_CHANNEL`               | Default channel ID                       |
| `APP_SLACK_CHANNEL_PR_BYPASS`     | Channel for PR bypass alerts (optional)  |
| `APP_SLACK_CHANNEL_OKTA_SYNC`     | Channel for sync reports (optional)      |
| `APP_SLACK_CHANNEL_ORPHANED_USERS`| Channel for orphan alerts (optional)     |

### Other

| Variable                 | Description                                    |
|--------------------------|------------------------------------------------|
| `APP_PORT`               | Server port (default: `8080`)                  |
| `APP_DEBUG_ENABLED`      | Verbose logging (default: `false`)             |
| `APP_BASE_PATH`          | URL prefix to strip (e.g., `/api/v1`)          |

### Okta Sync Rules

Map Okta groups to GitHub teams using JSON rules:

```json
[
  {
    "name": "sync-engineering-teams",
    "enabled": true,
    "okta_group_pattern": "^github-eng-.*",
    "github_team_prefix": "eng-",
    "strip_prefix": "github-eng-",
    "sync_members": true,
    "create_team_if_missing": true
  },
  {
    "name": "sync-platform-team",
    "enabled": true,
    "okta_group_name": "platform-team",
    "github_team_name": "platform",
    "sync_members": true,
    "team_privacy": "closed"
  }
]
```

See [Okta Setup - Sync Rules](docs/okta-setup.md#step-10-configure-sync-rules)
for detailed rule field documentation.

**Sync Safety Features**:
- Only syncs `ACTIVE` Okta users; never removes outside collaborators
- Safety threshold (default 50%) aborts sync if too many removals detected
- Orphaned user detection alerts when org members aren't in any synced teams

## Integration Setup

Detailed setup guides for each integration:

- [GitHub App Setup](docs/github-app-setup.md) - Create and install the GitHub
  App with required permissions
- [Okta Setup](docs/okta-setup.md) - Configure API Services app for group sync
- [Slack Setup](docs/slack-setup.md) - Create Slack app for notifications

## Development

```bash
# run server locally
make server

# run all tests
make test

# integration tests (offline, uses mock servers)
make test-verify

# specific package
go test -race -count=1 ./internal/github

# specific test
go test -race -count=1 ./internal/okta -run TestGroupSync
```

### Docker Deployment

```dockerfile
FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY . .
RUN apk add --no-cache make && make build-server

FROM alpine:latest
RUN apk --no-cache add ca-certificates
COPY --from=builder /app/dist/server /server
EXPOSE 8080
CMD ["/server"]
```

## How It Works

```
+------------+      +------------------------------------+      +-----------+
|  GitHub    | ---> |           github-ops-app           | ---> |   Slack   |
|  webhooks  |      |                                    |      |  alerts   |
+------------+      |  +------------------------------+  |      +-----------+
                    |  |     PR Compliance Check      |  |
+------------+      |  |  - verify branch protection  |  |      +-----------+
|    Okta    | ---> |  |  - detect bypasses           |  | ---> |  GitHub   |
|   groups   |      |  +------------------------------+  |      | Teams API |
+------------+      |                                    |      +-----------+
                    |  +------------------------------+  |
                    |  |      Okta Sync Engine        |  |
                    |  |  - map groups to teams       |  |
                    |  |  - sync membership           |  |
                    |  +------------------------------+  |
                    +------------------------------------+
```

### Okta Sync Flow

1. **Trigger**: Scheduled cron/EventBridge or team membership webhook
2. **Fetch**: Query Okta groups matching configured rules
3. **Match**: Apply sync rules to map Okta groups → GitHub teams
4. **Sync**: Add/remove GitHub team members (ACTIVE Okta users only)
5. **Safety**: Abort if removal ratio exceeds threshold (default 50%)
6. **Report**: Send Slack notification with changes and orphaned users

### PR Compliance Flow

1. **Receive**: GitHub webhook on PR merge to monitored branch
2. **Verify**: Validate webhook signature (HMAC-SHA256)
3. **Check**: Query branch protection rules and required status checks
4. **Detect**: Identify bypasses (admin override, missing reviews, failed checks)
5. **Notify**: Send Slack alert with violation details

## Troubleshooting

**Common issues**:
- Unauthorized from GitHub: Check app installation and permissions
- Group not found from Okta: Verify domain and scopes
- Webhook signature fails: Verify `APP_GITHUB_WEBHOOK_SECRET` matches
- No Slack notifications: Verify token has `chat:write` and bot is in channel

## License

MIT

