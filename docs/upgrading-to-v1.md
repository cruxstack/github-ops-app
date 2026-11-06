# Upgrading to v1

v1 fixes security and correctness problems, some of which change behavior,
and validates the configuration at startup. Read this list before you
upgrade; the service refuses to start on most configuration problems and
logs every one.

## Configuration

| v0                       | v1                                                      |
| ------------------------ | ------------------------------------------------------- |
| `APP_DEBUG_ENABLED=true` | `APP_LOG_LEVEL=debug`                                   |
| `APP_PORT=8080`          | `APP_LISTEN_ADDR=:8080`                                 |
| `APP_OKTA_KEY_ID`        | `APP_OKTA_PRIVATE_KEY_ID`; the old name was never read  |

Setting a removed variable is an error, so it can't be silently ignored.

- `APP_GITHUB_ORG`, `APP_GITHUB_APP_ID`, a private key, and
  `APP_GITHUB_WEBHOOK_SECRET` are required.
- `APP_GITHUB_INSTALLATION_ID` is optional; the installation is looked up
  from the organization.
- Invalid values are errors instead of falling back to defaults: booleans,
  numbers, the safety threshold, the alert age and severity, URLs, and the
  base path.
- A partly configured feature is an error instead of being disabled: any Okta
  variable requires the domain, client ID, private key, and rules, and the
  Slack token and channel are required together.
- Sync rules reject unknown fields, rules that set both or neither of
  `okta_group_name` and `okta_group_pattern`, and `github_team_name` combined
  with a pattern.
- `APP_ADMIN_TOKEN` must be at least 16 characters.
- `APP_PR_MONITORED_BRANCHES` accepts glob patterns, such as `release/*`.
- Logs are always JSON.

## Security

- **Webhooks must be signed.** v0 accepted unsigned webhooks when no secret was
  set.
- **The admin endpoints are disabled without `APP_ADMIN_TOKEN`.** In v0 they
  were open to anyone. EventBridge invocations in Lambda don't need the token.
- **Webhooks from other organizations are ignored.**
- **The app manifest is private** (`"public": false`), so other accounts can't
  install it. If you created a public app from the v0 manifest, make it
  private under **Advanced** → **Make private** in its settings.
- Slack messages escape text from GitHub and Okta, so a PR title can't
  mention `@channel` or add a link.
- Okta usernames are validated as GitHub logins before use.

## GitHub App Permissions

The v0 manifest asked for permissions under names GitHub doesn't use, and
missed two the PR compliance check needs, so it silently skipped legacy branch
protection and commit statuses. Update an existing app's permissions to match
[the list](github-app-setup.md#permissions), then approve them on the
installation:

- Add **Administration: read** and **Commit statuses: read**.
- Make sure **Dependabot alerts** and **Code scanning alerts** are granted, if
  you use security alerts.
- Remove **Organization administration** and **Contents**, which nothing uses.

Errors reading branch protection or rulesets now fail the check, with a
`502`, instead of treating the branch as unprotected.

## Behavior

### Okta Sync

- `create_team_if_missing` is honored and defaults to `false`. v0 created
  missing teams regardless. Set it to `true` on rules that should create
  teams.
- Users whose Okta status is `PASSWORD_EXPIRED`, `LOCKED_OUT`, or `RECOVERY`
  stay in their teams. v0 removed them until they signed in again.
- A group whose members can't be read is reported and not synced, and the
  other groups of its rule still sync.
- Teams are found by name even when the slug differs, so `github_team_name`
  can be a display name such as `Platform Team`.
- Pending invitations count as members, so they aren't invited again and
  reported as added on every sync.
- Orphaned user detection doesn't run when the sync failed, and fails rather
  than report the members of a team it couldn't read.
- Syncs requested while one is running are combined into one more sync.
- Webhooks only trigger a sync for teams a rule manages, and `team` events
  only when a team is created, deleted, or renamed, not for description,
  privacy, or repository permission changes.
- The server answers `team` and `membership` webhooks with `202` and syncs
  afterwards, instead of making GitHub wait past its 10-second timeout.
- Members of a team's child teams, which GitHub also lists as members of the
  parent team, are no longer removed from the parent team.

### PR Compliance

- Every merged PR that didn't meet its branch's requirements is reported, with
  the merger's role when it is admin or maintain. v0 reported only merges by
  admins, missing ruleset bypass actors; it also never recognized maintainers.
- Reviews are read across every page, and a comment after an approval keeps
  the approval.
- Check runs that are neutral or skipped pass, as on GitHub, and check runs and
  statuses are read across every page.

### Security Alerts

- Alerts are read across every page; v0 read only the first 100 of each kind.
- Code scanning alerts without a security severity use their rule's severity:
  `error` as high, `warning` as medium, and `note` as low.
- A report is posted when a source fails, even with no alerts.

## HTTP API

- `GET /healthz` (liveness) and `GET /readyz` (readiness) are new.
- Responses are JSON: `{"status": ...}` or `{"error": ...}`.
- The `ping` event returns `200`; events the app doesn't handle, and events
  from other organizations, return `202`. v0 returned `400`, which GitHub
  shows as a failed delivery.
- An unknown scheduled action returns `404`, and a disabled feature `409`.
- Webhook payloads up to 25 MB are accepted, as GitHub sends.

## Lambda

- The EventBridge event's `detail` is `{"action": "..."}`; `data` is ignored.
- The binary is built with the `lambda.norpc` tag, for the `provided.al2023`
  runtime; the deprecated `go1.x` runtime is not supported.
- A configuration that fails to load, such as from an SSM outage, is retried
  on the next invocation. v0 failed every invocation until the container was
  recycled.
- Webhooks and scheduled actions run for up to 10 minutes; raise the
  function's timeout so a sync can finish.

## Building

- Go 1.27 or later is required.
- Releases attach server and Lambda binaries for amd64 and arm64.
- The container image runs as user `65532`, with `/data` for the installer's
  env file.
- On `SIGTERM` the server waits up to 10 minutes for running syncs; set the
  platform's grace period to match.
