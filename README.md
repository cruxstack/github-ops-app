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

* GitHub App ([setup guide](#github-app-setup))
* Go ≥ 1.24
* **Optional**: Okta API Service app for group sync
* **Optional**: Slack app for notifications

### Deployment Options

The bot can be deployed in multiple ways:

#### Option 1: Standard HTTP Server

Run as a long-lived HTTP server on any VPS, VM, or container platform:

```bash
# build
make build-server

# run (or use systemd, Docker, Kubernetes, etc.)
./dist/server

# server listens on PORT (default: 8080)
# endpoints:
#   POST /webhooks              - GitHub webhook receiver
#   POST /scheduled/okta-sync   - Trigger Okta sync (call via cron)
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

| Variable                 | Description                     |
|--------------------------|---------------------------------|
| `APP_SLACK_TOKEN`        | Bot token (`xoxb-...`)          |
| `APP_SLACK_CHANNEL`      | Default channel ID              |

### Other

| Variable                 | Description                        |
|--------------------------|------------------------------------|
| `APP_DEBUG_ENABLED`      | Verbose logging (default: `false`) |

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

**Rule Fields**:
- `name` - Rule identifier
- `enabled` - Enable/disable rule
- `okta_group_pattern` - Regex to match Okta groups
- `okta_group_name` - Exact Okta group name (alternative to pattern)
- `github_team_prefix` - Prefix for GitHub team names
- `github_team_name` - Exact GitHub team name (overrides pattern)
- `strip_prefix` - Remove this prefix from Okta group name
- `sync_members` - Sync members between Okta and GitHub
- `create_team_if_missing` - Auto-create GitHub teams
- `team_privacy` - `secret` or `closed`

**Sync Safety Features**:
- **Active users only**: Only syncs users with `ACTIVE` status in Okta,
  automatically excluding suspended or deprovisioned accounts
- **External collaborator protection**: Never removes outside collaborators
  (non-org members), preserving contractors and partner access
- **Outage protection**: Safety threshold (default 50%) prevents mass removal
  if Okta/GitHub is experiencing issues. Sync aborts if removal ratio exceeds
  threshold
- **Orphaned user detection**: Identifies organization members not in any
  Okta-synced teams and sends Slack notifications. Enabled by default when
  sync is enabled.

## Okta Setup

Create an API Services application in Okta Admin Console:

1. **Applications** → **Create App Integration** → **API Services**
2. Name: `github-bot-api-service`
3. **Client Credentials**:
   - Authentication: **Public key / Private key**
   - Generate and download private key (PEM format)
   - Note the Client ID
4. **Okta API Scopes**: Grant `okta.groups.read` and `okta.users.read`

Use the Client ID and private key in your environment variables.

## GitHub App Setup

Create a GitHub App in your organization settings:

1. **Developer settings** → **GitHub Apps** → **New GitHub App**
2. **Basic info**:
   - Name: `github-ops-app`
   - Webhook URL: Your API Gateway URL
   - Webhook secret: Generate and save for `APP_GITHUB_WEBHOOK_SECRET`
3. **Permissions**:
   - Repository: Pull requests (Read), Contents (Read)
   - Organization: Members (Read & write), Administration (Read)
4. **Events**: Subscribe to Pull request, Team, Membership
5. Generate and download private key (`.pem` file)
6. Install app to your organization
7. Note: **App ID**, **Installation ID** (from install URL), **Private key**

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

**Okta Sync**: EventBridge triggers sync → Fetch Okta groups → Apply rules →
Update GitHub teams → Detect orphaned users → Send Slack reports. Automatically
reconciles when external team changes are detected. Only syncs ACTIVE Okta
users, skips external collaborators, and prevents mass removal during outages
via safety threshold. Orphaned user detection identifies org members not in any
synced teams.

**PR Compliance**: Webhook on PR merge → Verify signature → Check branch
protection rules → Detect bypasses → Notify Slack if violations found.

## Troubleshooting

**Common issues**:
- Unauthorized from GitHub: Check app installation and permissions
- Group not found from Okta: Verify domain and scopes
- Webhook signature fails: Verify `APP_GITHUB_WEBHOOK_SECRET` matches
- No Slack notifications: Verify token has `chat:write` and bot is in channel

## License

MIT

