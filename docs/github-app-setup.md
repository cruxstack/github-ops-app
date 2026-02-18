# GitHub App Setup

This guide walks through creating and installing a GitHub App for
github-ops-app.

## Prerequisites

- GitHub organization with admin access

## Step 1: Create the GitHub App

1. Navigate to your organization's settings:

   - Go to `https://github.com/organizations/YOUR_ORG/settings/apps`
   - Or: **Organization** → **Settings** → **Developer settings** → **GitHub
     Apps**

2. Click **New GitHub App**

3. Fill in the basic information:

   | Field                 | Value                                           |
   | --------------------- | ----------------------------------------------- |
   | GitHub App name       | `github-ops-app` (must be unique across GitHub) |
   | Homepage URL          | Your organization's URL or repo URL             |
   | Webhook > Webhook URL | Leave blank for now                             |
   | Webhook > Secret      | Generate a strong secret (save this for later)  |
   | Webhook > Active      | **Uncheck** to disable webhooks initially       |

   > **Note**: Disable webhooks during creation since you may not know your
   > endpoint URL until after deployment. You'll configure webhooks and
   > subscribe to events in [Step 7](#step-7-configure-webhook-and-events).

### Configure Permissions

Under **Permissions**, set the following:

### Repository Permissions

| Permission             | Access | Purpose                           |
| ---------------------- | ------ | --------------------------------- |
| Checks                 | Read   | Read status checks on PRs         |
| Code scanning alerts   | Read   | Fetch open code scanning alerts   |
| Contents               | Read   | Read branch protection rules      |
| Dependabot alerts      | Read   | Fetch open Dependabot alerts      |
| Pull requests          | Read   | Access PR details for compliance  |
| Secret scanning alerts | Read   | Fetch open secret scanning alerts |

#### Organization Permissions

| Permission     | Access     | Purpose                    |
| -------------- | ---------- | -------------------------- |
| Members        | Read/Write | Manage team membership     |
| Administration | Read       | Read organization settings |

> **Note**: The three security alert permissions are only required if you enable
> security alerts monitoring (`APP_SECURITY_ALERTS_ENABLED=true`). You can omit
> them if you don't use that feature.

4. Set installation scope:

   | Setting                                 | Value                |
   | --------------------------------------- | -------------------- |
   | Where can this GitHub App be installed? | Only on this account |

5. Click **Create GitHub App**

## Step 2: Generate Private Key

After creating the app:

1. Scroll to **Private keys** section
2. Click **Generate a private key**
3. Save the downloaded `.pem` file securely
4. This file is used for `APP_GITHUB_APP_PRIVATE_KEY` or
   `APP_GITHUB_APP_PRIVATE_KEY_PATH`

## Step 3: Note Your App ID

On the app's settings page, find and save:

- **App ID** - numeric ID displayed near the top (e.g., `123456`)

## Step 4: Install the App

1. In the left sidebar, click **Install App**
2. Select your organization
3. Choose repository access:
   - **All repositories** - recommended for org-wide PR compliance
   - **Only select repositories** - if limiting scope
4. Click **Install**

## Step 5: Get Installation ID

After installation, you'll be redirected to a URL like:

```
https://github.com/organizations/YOUR_ORG/settings/installations/12345678
```

The number at the end (`12345678`) is your **Installation ID**.

Alternatively, use the GitHub API:

```bash
# List installations (requires app JWT authentication)
curl -H "Authorization: Bearer YOUR_JWT" \
  https://api.github.com/app/installations
```

## Step 6: Configure Environment Variables

Set these environment variables in your deployment:

```bash
# Required GitHub configuration
APP_GITHUB_APP_ID=123456
APP_GITHUB_INSTALLATION_ID=12345678
APP_GITHUB_ORG=your-org-name
APP_GITHUB_WEBHOOK_SECRET=your-webhook-secret

# Private key (choose one method)
APP_GITHUB_APP_PRIVATE_KEY="-----BEGIN RSA PRIVATE KEY-----
...
-----END RSA PRIVATE KEY-----"

# Or use a file path
APP_GITHUB_APP_PRIVATE_KEY_PATH=/path/to/private-key.pem

# Or use AWS SSM parameter
APP_GITHUB_APP_PRIVATE_KEY=arn:aws:ssm:us-east-1:123456789:parameter/github-bot/private-key
```

## Step 7: Configure Webhook and Events

After deploying your server, configure and enable webhooks:

1. Go to your GitHub App settings:
   `https://github.com/organizations/YOUR_ORG/settings/apps/YOUR_APP`

2. Set **Webhook URL** to your endpoint:

   - Lambda: `https://xxx.execute-api.region.amazonaws.com/webhooks`
   - Server: `https://your-domain.com/webhooks`

3. Check **Active** to enable webhooks

4. Click **Save changes**

5. Under **Subscribe to events**, check:

   - [x] **Pull request** - PR open, close, merge events
   - [x] **Team** - Team creation, deletion, changes
   - [x] **Membership** - Team membership changes

6. Click **Save changes**

## Using the App Manifest (Alternative)

For automated setup, use the manifest at `assets/github/manifest.json`:

1. Go to `https://github.com/settings/apps/new`
2. Append `?manifest=` with URL-encoded manifest JSON
3. Or use the manifest creation API

The manifest pre-configures all required permissions and events.

## Verification

Test your setup:

1. **Webhook delivery**: Check **Settings** → **Developer settings** → **GitHub
   Apps** → your app → **Advanced** → **Recent Deliveries**

2. **Create a test PR**: Open and merge a PR to a monitored branch to verify
   webhook reception

3. **Check logs**: Verify your application receives and processes the webhook

## Troubleshooting

### Webhook signature verification failed

- Verify `APP_GITHUB_WEBHOOK_SECRET` matches the secret in GitHub App settings
- Check for whitespace or encoding issues in the secret

### 401 Unauthorized from GitHub API

- Verify the private key matches the one generated for this app
- Check that the app is installed on the target organization
- Ensure `APP_GITHUB_INSTALLATION_ID` is correct

### Missing permissions error

- Re-check the app's permission settings
- After changing permissions, organization admins may need to re-approve

### Webhook not received

- Verify the webhook URL is accessible from the internet
- Check the webhook URL doesn't have a trailing slash mismatch
- Review recent deliveries in GitHub App settings for error details
