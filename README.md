# github-ops-app

A GitHub App that syncs Okta groups to GitHub teams, reports pull requests
merged without meeting their branch's requirements, and reports stale security
alerts, with Slack notifications. Run it as an HTTP server, a container, or an
AWS Lambda function.

## Features

- **Okta group sync** - Sync Okta group members to GitHub teams, by group name
  or name pattern
- **Orphaned user detection** - Report organization members in no synced team
- **Automatic reconciliation** - Sync again when someone changes a team outside
  Okta
- **PR compliance monitoring** - Report PRs merged into monitored branches
  without the reviews or status checks that branch protection and rulesets
  require
- **Security alerts monitoring** - Report open Dependabot, code scanning, and
  secret scanning alerts older than a threshold
- **GitHub App installer** - Optionally create the GitHub App from a manifest
  with one click, using
  [github-app-setup-go](https://github.com/cruxstack/github-app-setup-go)
- **Slack notifications** - Reports for each feature, in per-feature channels

## Quick Start

### Prerequisites

- Go 1.27 or later, to build from source
- A GitHub App ([setup guide](docs/github-app-setup.md)), or the built-in
  installer
- **Optional**: an Okta API Services app ([setup guide](docs/okta-setup.md))
- **Optional**: a Slack app ([setup guide](docs/slack-setup.md))

Prebuilt binaries for the server and the Lambda function are attached to each
[release](https://github.com/cruxstack/github-ops-app/releases).

### HTTP Server or Container

```bash
cp .env.example .env   # then edit it
make run               # or: make up, to run in docker compose
```

Or build the binary or image:

```bash
make build   # dist/github-ops-app
make image   # github-ops-app:dev
```

On shutdown the server lets running syncs finish, for up to 10 minutes; give
it a grace period that long, such as Kubernetes'
`terminationGracePeriodSeconds: 630`, or a sync may stop partway and finish on
the next run.

Schedule the Okta sync and security alert reports with any scheduler, such as
a Kubernetes CronJob, by calling the scheduled endpoints with the admin token:

```bash
curl -X POST -H "Authorization: Bearer $APP_ADMIN_TOKEN" \
  https://github-ops.example.com/scheduled/okta-sync
```

### AWS Lambda

```bash
make build-lambda   # dist/bootstrap, for the provided.al2023 runtime
```

API Gateway delivers webhooks and EventBridge runs the scheduled actions. See
[cmd/lambda/README.md](cmd/lambda/README.md), or deploy with
[terraform-aws-github-ops-app](https://github.com/cruxstack/terraform-aws-github-ops-app).

## Endpoints

Every route is served under `APP_BASE_PATH`, if set.

| Method | Path                         | Auth           | Description                          |
| ------ | ---------------------------- | -------------- | ------------------------------------ |
| POST   | `/webhooks`                  | Signature      | GitHub webhooks                      |
| GET    | `/healthz`                   | None           | Liveness; always `200`               |
| GET    | `/readyz`                    | None           | `200` once the configuration loads   |
| POST   | `/scheduled/okta-sync`       | Admin token    | Sync Okta groups to teams            |
| POST   | `/scheduled/security-alerts` | Admin token    | Report stale security alerts         |
| POST   | `/scheduled/slack-test`      | Admin token    | Send a sample of every notification  |
| GET    | `/server/status`             | Admin token    | Status and enabled features          |
| GET    | `/server/config`             | Admin token    | Configuration, secrets redacted      |
| GET    | `/setup`                     | None           | GitHub App installer, when enabled   |

Webhooks must carry a valid `X-Hub-Signature-256`; there is no unsigned mode.
The admin endpoints require `Authorization: Bearer <APP_ADMIN_TOKEN>` and are
disabled when no admin token is set. Webhooks from organizations other than
`APP_GITHUB_ORG` are ignored.

## Configuration

See [`.env.example`](.env.example) for a complete example. The configuration
is validated at startup: an invalid value, or a partly configured feature, is
an error, and every problem is reported at once.

Any `APP_` value may be an AWS SSM Parameter Store ARN, in any partition,
which is replaced by the parameter's decrypted value. The role needs
`ssm:GetParameter` (and `kms:Decrypt` for a customer managed key).

```bash
APP_GITHUB_WEBHOOK_SECRET=arn:aws:ssm:us-east-1:123456789012:parameter/github-ops-app/webhook-secret
```

### GitHub App (required)

| Variable                          | Description                                        |
| --------------------------------- | -------------------------------------------------- |
| `APP_GITHUB_ORG`                  | Organization the app manages                       |
| `APP_GITHUB_APP_ID`               | GitHub App ID                                      |
| `APP_GITHUB_APP_PRIVATE_KEY`      | Private key (PEM; `\n` escapes are accepted)       |
| `APP_GITHUB_APP_PRIVATE_KEY_PATH` | Or a path to the private key file                  |
| `APP_GITHUB_WEBHOOK_SECRET`       | Webhook secret                                     |
| `APP_GITHUB_INSTALLATION_ID`      | Installation ID; looked up from the org when unset |
| `APP_GITHUB_BASE_URL`             | API URL for GitHub Enterprise Server               |

### GitHub App Installer (optional)

With the installer enabled, the service starts without GitHub App credentials
and serves `/setup`, which creates the app from a manifest with the right
permissions and saves its credentials. The service then loads them without a
restart. See [GitHub App Setup](docs/github-app-setup.md#option-a-installer).
These settings use the names of github-app-setup-go.

| Variable                       | Description                                          |
| ------------------------------ | ---------------------------------------------------- |
| `GITHUB_APP_INSTALLER_ENABLED` | `true` to serve the installer                        |
| `STORAGE_MODE`                 | `envfile` (default) or `aws-ssm`                     |
| `STORAGE_DIR`                  | Env file the `envfile` mode writes (`./.env`)        |
| `AWS_SSM_PARAMETER_PREFIX`     | Parameter path prefix for `aws-ssm`                  |
| `AWS_SSM_KMS_KEY_ID`           | KMS key for `aws-ssm` (default: AWS managed key)     |
| `APP_PUBLIC_URL`               | External URL of the service, including the base path |

### Okta Sync (optional)

Setting any of these enables the sync and requires the domain, client ID,
private key, and rules.

| Variable                               | Description                                          |
| -------------------------------------- | ---------------------------------------------------- |
| `APP_OKTA_DOMAIN`                      | Okta domain, such as `acme.okta.com`                 |
| `APP_OKTA_CLIENT_ID`                   | OAuth 2.0 client ID                                  |
| `APP_OKTA_PRIVATE_KEY`                 | Private key (PEM, PKCS#1 or PKCS#8)                  |
| `APP_OKTA_PRIVATE_KEY_PATH`            | Or a path to the private key file                    |
| `APP_OKTA_PRIVATE_KEY_ID`              | Key ID (`kid`) registered with the OAuth app         |
| `APP_OKTA_SCOPES`                      | Scopes (default: `okta.groups.read,okta.users.read`) |
| `APP_OKTA_GITHUB_USER_FIELD`           | Profile attribute with the GitHub username           |
| `APP_OKTA_SYNC_RULES`                  | JSON array of rules (see [below](#okta-sync-rules))  |
| `APP_OKTA_SYNC_SAFETY_THRESHOLD`       | Max share of a team to remove (default: `0.5`)       |
| `APP_OKTA_ORPHANED_USER_NOTIFICATIONS` | Report orphaned users (default: `true`)              |

### PR Compliance (optional)

| Variable                    | Description                                                 |
| --------------------------- | ----------------------------------------------------------- |
| `APP_PR_COMPLIANCE_ENABLED` | `true` to check merged PRs                                  |
| `APP_PR_MONITORED_BRANCHES` | Branches or globs, such as `main,release/*` (`main,master`) |

### Security Alerts (optional)

| Variable                           | Description                                      |
| ---------------------------------- | ------------------------------------------------ |
| `APP_SECURITY_ALERTS_ENABLED`      | `true` to enable the report                      |
| `APP_SECURITY_ALERTS_MIN_AGE_DAYS` | Minimum alert age in days (default: `30`)        |
| `APP_SECURITY_ALERTS_MIN_SEVERITY` | `critical`, `high` (default), `medium`, or `low` |

Code scanning alerts use their security severity, or for rules without one,
`error` as high, `warning` as medium, and `note` as low. Secret scanning alerts
have no severity and count as high.

### Slack (optional)

The token and channel are required together.

| Variable                            | Description                               |
| ----------------------------------- | ----------------------------------------- |
| `APP_SLACK_TOKEN`                   | Bot token (`xoxb-...`)                    |
| `APP_SLACK_CHANNEL`                 | Default channel ID                        |
| `APP_SLACK_CHANNEL_PR_BYPASS`       | Channel for PR bypass reports             |
| `APP_SLACK_CHANNEL_OKTA_SYNC`       | Channel for sync reports                  |
| `APP_SLACK_CHANNEL_ORPHANED_USERS`  | Channel for orphaned user reports         |
| `APP_SLACK_CHANNEL_SECURITY_ALERTS` | Channel for security alert reports        |
| `APP_SLACK_FOOTER_NOTE_PR_BYPASS`   | Footer for PR bypass reports, in mrkdwn   |

### Service

| Variable          | Description                                                  |
| ----------------- | ------------------------------------------------------------ |
| `APP_ADMIN_TOKEN` | Token for the admin endpoints, at least 16 characters        |
| `APP_LOG_LEVEL`   | `debug`, `info` (default), `warn`, or `error`                |
| `APP_LISTEN_ADDR` | Server address (default: `:8080`)                            |
| `APP_BASE_PATH`   | Path every route is served under, such as an API stage       |

### Okta Sync Rules

Each rule maps one Okta group, by name, or every group matching a pattern, to
a GitHub team:

```json
[
  {
    "name": "engineering-teams",
    "okta_group_pattern": "^github-eng-.*",
    "strip_prefix": "github-eng-",
    "github_team_prefix": "eng-",
    "create_team_if_missing": true
  },
  {
    "name": "platform-team",
    "okta_group_name": "Platform Engineers",
    "github_team_name": "platform",
    "team_privacy": "secret"
  }
]
```

| Field                    | Description                                                          |
| ------------------------ | -------------------------------------------------------------------- |
| `name`                   | Name used in reports                                                 |
| `enabled`                | `false` to skip the rule (default: `true`)                           |
| `okta_group_name`        | Exact name of one Okta group                                         |
| `okta_group_pattern`     | Regular expression matching Okta group names                         |
| `github_team_name`       | Team name, for `okta_group_name` rules                               |
| `strip_prefix`           | Prefix removed from the group name to derive the team name           |
| `github_team_prefix`     | Prefix added to the derived team name                                |
| `sync_members`           | `false` to only create the team (default: `true`)                    |
| `create_team_if_missing` | Create missing teams; otherwise they are reported (default: `false`) |
| `team_privacy`           | `closed` (default) or `secret`, for created teams                    |

A rule sets exactly one of `okta_group_name` and `okta_group_pattern`. Unknown
fields are rejected, so a misspelled field is an error instead of being
ignored.

**Sync safety**:

- Only members who can sign in to Okta are synced: `ACTIVE`, and
  `PASSWORD_EXPIRED`, `LOCKED_OUT`, and `RECOVERY`, so an expired password
  does not remove anyone. Suspended and deprovisioned users are removed.
- No one is removed from a team if more than the safety threshold of its
  members would be, which guards against an empty or partial group from Okta.
- Users who are not organization members are never removed.
- A group whose members cannot be read from Okta is reported and not synced.
- Two groups that map to the same team in one run are reported; only the
  first is synced, so they cannot remove each other's members.
- Okta usernames are validated as GitHub logins before they are used.

## How It Works

```
+------------+      +------------------------------------+      +-----------+
|  GitHub    | ---> |           github-ops-app           | ---> |   Slack   |
|  webhooks  |      |                                    |      |  reports  |
+------------+      |  +------------------------------+  |      +-----------+
                    |  |     PR Compliance Check      |  |
+------------+      |  |  - required reviews          |  |      +-----------+
|    Okta    | ---> |  |  - required status checks    |  | ---> |  GitHub   |
|   groups   |      |  +------------------------------+  |      | Teams API |
+------------+      |                                    |      +-----------+
                    |  +------------------------------+  |
+------------+      |  |      Okta Sync Engine        |  |
| Scheduler  | ---> |  |  - map groups to teams       |  |
| (cron/EB)  |      |  |  - sync membership           |  |
+------------+      |  +------------------------------+  |
                    |                                    |
                    |  +------------------------------+  |
                    |  |   Security Alerts Monitor    |  |
                    |  |  - dependabot alerts         |  |
                    |  |  - code scanning alerts      |  |
                    |  |  - secret scanning alerts    |  |
                    |  +------------------------------+  |
                    +------------------------------------+
```

### Okta Sync

1. **Trigger**: the schedule, or a `team` or `membership` webhook from a
   person, not a bot, about a team a rule manages
2. **Fetch**: read the Okta groups each rule matches, with their members
3. **Map**: derive each group's GitHub team, creating it if allowed
4. **Sync**: add and remove team members to match the group
5. **Report**: post the changes, errors, and orphaned users to Slack

A sync requested while one is running does not run concurrently; the running
sync runs once more when it finishes. The server answers webhooks at once
(`202`) and syncs afterwards, since GitHub waits only 10 seconds; Lambda syncs
before it answers. Members of a team's child teams are never removed from the
parent team, where GitHub also lists them.

Orphaned user detection is skipped when a rule's team could not be found,
since its members would otherwise be reported as orphaned.

### PR Compliance

1. **Receive**: a `pull_request` webhook for a PR merged into a monitored
   branch of the organization's repositories
2. **Check**: compare the PR's approving reviews and its head commit's status
   checks with what branch protection and rulesets require
3. **Report**: post the PR to Slack when it did not meet them, with the
   merger's repository role when it explains the bypass

A reviewer counts once, by their latest review that approved, requested
changes, or was dismissed. Check runs that are successful, neutral, or skipped
pass, as on GitHub.

### Security Alerts

1. **Trigger**: the schedule
2. **Fetch**: read every page of open Dependabot, code scanning, and secret
   scanning alerts in the organization, oldest first
3. **Filter**: keep alerts older than the minimum age at or above the minimum
   severity
4. **Report**: post the alerts by repository to Slack, with any source that
   failed

## Development

```bash
make help       # list targets
make test       # all tests, with the race detector
make test-e2e   # the scenarios in fixtures/scenarios.json against mock apis
make lint       # golangci-lint
make vuln       # govulncheck
make check      # everything ci runs
```

The end-to-end tests run the real service against mock GitHub, Okta, and
Slack APIs. Add a scenario to [fixtures/scenarios.json](fixtures/scenarios.json)
to cover a new behavior.

## Troubleshooting

- **`401` on webhooks**: `APP_GITHUB_WEBHOOK_SECRET` doesn't match the app's
  webhook secret.
- **`503 service not configured`**: the configuration did not load; the logs
  list every problem. With the installer enabled, finish the setup at `/setup`.
- **`403` from GitHub when checking a PR**: the app lacks a permission, such
  as **Administration: read** for branch protection. See the
  [permissions](docs/github-app-setup.md#permissions).
- **Group not found from Okta**: check the domain, scopes, and the group name.
- **No Slack messages**: check the token has `chat:write` and the bot is in
  the channel, or add `chat:write.public`.

Upgrading from v0? See [Upgrading to v1](docs/upgrading-to-v1.md).

## License

MIT
