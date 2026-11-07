# GitHub App Setup

github-ops-app runs as a GitHub App installed on your organization. Create
the app with the built-in installer, or by hand.

## Prerequisites

- Owner access to the GitHub organization
- The URL where GitHub can reach the service, for webhooks

## Option A: Installer

The installer, from
[github-app-setup-go](https://github.com/cruxstack/github-app-setup-go),
creates the app from a manifest with the permissions and events below, then
saves its credentials where the service reads them. The service loads them
without a restart.

1. Configure the service without GitHub App credentials, and enable the
   installer:

   ```bash
   APP_GITHUB_ORG=your-org
   GITHUB_APP_INSTALLER_ENABLED=true
   # where the credentials are saved: envfile (default) or aws-ssm
   STORAGE_MODE=envfile
   STORAGE_DIR=./.env
   # the external url of the service, including APP_BASE_PATH
   APP_PUBLIC_URL=https://github-ops.example.com
   ```

   With `STORAGE_MODE=aws-ssm`, also set `AWS_SSM_PARAMETER_PREFIX`, and set
   `APP_GITHUB_APP_ID`, `APP_GITHUB_APP_PRIVATE_KEY`, and
   `APP_GITHUB_WEBHOOK_SECRET` to the ARNs of the parameters the installer
   will create under that prefix, such as
   `arn:aws:ssm:us-east-1:123456789012:parameter/github-ops-app/APP_GITHUB_APP_ID`.

2. Start the service. It serves the installer while it waits for credentials,
   and `/readyz` returns `503` until they load.

3. Visit `https://github-ops.example.com/setup`, check the webhook URL ends in
   `/webhooks`, and create the app. GitHub asks you to confirm.

4. On the success page, follow the link to install the app on your
   organization.

5. Disable the installer: use the button on the success page, or unset
   `GITHUB_APP_INSTALLER_ENABLED`.

Once an app is registered, the service refuses to exchange new installer
codes, so no one can replace its credentials. Still, only enable the
installer while you set the app up, and keep `/setup` off the public internet
if you can.

The installer saves the credentials under both the names github-app-setup-go
uses, such as `GITHUB_APP_ID`, and the names this service reads, such as
`APP_GITHUB_APP_ID`.

## Option B: By Hand

### 1. Create the App

1. Go to `https://github.com/organizations/YOUR_ORG/settings/apps/new`.
2. Fill in:

   | Field                                   | Value                                |
   | --------------------------------------- | ------------------------------------ |
   | GitHub App name                         | `github-ops-app` (must be unique)    |
   | Homepage URL                            | Your organization's or the repo URL  |
   | Webhook URL                             | `https://YOUR_HOST/webhooks`         |
   | Webhook secret                          | A long random string; save it        |
   | Where can this GitHub App be installed? | **Only on this account**             |

3. Set the [permissions](#permissions) and [events](#events) below.
4. Click **Create GitHub App**, and note the **App ID**.

[assets/github/manifest.json](../assets/github/manifest.json) holds the same
settings as a manifest.

### 2. Generate a Private Key

Under **Private keys**, click **Generate a private key** and keep the `.pem`
file secret.

### 3. Install the App

Click **Install App**, choose your organization, and give it access to all
repositories, or the ones whose PRs and alerts it should check.

### 4. Configure the Service

```bash
APP_GITHUB_ORG=your-org
APP_GITHUB_APP_ID=123456
APP_GITHUB_WEBHOOK_SECRET=your-webhook-secret

# the private key: a file, the pem itself, or an ssm parameter arn
APP_GITHUB_APP_PRIVATE_KEY_PATH=/path/to/private-key.pem
# APP_GITHUB_APP_PRIVATE_KEY="-----BEGIN RSA PRIVATE KEY-----\n...\n-----END RSA PRIVATE KEY-----"
# APP_GITHUB_APP_PRIVATE_KEY=arn:aws:ssm:us-east-1:123456789012:parameter/github-ops-app/private-key
```

The installation ID is looked up from the organization. To skip the lookup,
set `APP_GITHUB_INSTALLATION_ID` to the number at the end of the installation
page's URL, `https://github.com/organizations/YOUR_ORG/settings/installations/12345678`.

## Permissions

The app needs these permissions and no others. Grant only those of the
features you use, if you prefer.

### Repository Permissions

| Permission             | Access | Used for                                       |
| ---------------------- | ------ | ---------------------------------------------- |
| Administration         | Read   | PR compliance: branch protection rules         |
| Checks                 | Read   | PR compliance: check runs on the merged commit |
| Commit statuses        | Read   | PR compliance: statuses on the merged commit   |
| Metadata               | Read   | PR compliance: rulesets and merger's role      |
| Pull requests          | Read   | PR compliance: PRs and their reviews           |
| Code scanning alerts   | Read   | Security alerts                                |
| Dependabot alerts      | Read   | Security alerts                                |
| Secret scanning alerts | Read   | Security alerts                                |

### Organization Permissions

| Permission | Access     | Used for                                |
| ---------- | ---------- | --------------------------------------- |
| Members    | Read/Write | Okta sync: teams and team membership    |

Without **Administration: read**, the PR compliance check cannot read branch
protection, and fails with a `403` instead of silently treating the branch as
unprotected.

## Events

| Event        | Used for                                                            |
| ------------ | ------------------------------------------------------------------- |
| Pull request | PR compliance                                                       |
| Team         | Okta sync after a team is created, renamed, or deleted outside Okta |
| Membership   | Okta sync after a team's members change outside Okta                |

The service answers GitHub's `ping` event, and accepts and ignores events it
doesn't handle and events from other organizations.

## Verification

1. In the app's settings, open **Advanced** → **Recent Deliveries**; the
   `ping` delivery should have succeeded.
2. Merge a PR into a monitored branch and check the service's logs.
3. With the admin token set, call `GET /server/status`.

## Troubleshooting

### Webhook signature verification failed

- `APP_GITHUB_WEBHOOK_SECRET` must match the app's webhook secret exactly.

### `403` or `404` from the GitHub API

- Check the app has the [permissions](#permissions) above. After changing
  them, an organization owner must approve them on the installation.
- Check the app is installed on `APP_GITHUB_ORG` and can access the repository.

### `github app is not installed on the organization`

- Install the app on `APP_GITHUB_ORG`, or set `APP_GITHUB_INSTALLATION_ID`.

### Webhook not received

- Check the webhook URL is reachable from the internet and ends in
  `/webhooks`, after any `APP_BASE_PATH`.
- Review **Recent Deliveries** for the response GitHub got.
