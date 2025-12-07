# Slack Setup

This guide walks through creating a Slack app for github-ops-app notifications.

## Prerequisites

- Slack workspace with admin access (or ability to request app approval)
- Channel where notifications will be posted

## Step 1: Create Slack App

### Option A: Use App Manifest (Recommended)

1. Go to [api.slack.com/apps](https://api.slack.com/apps)
2. Click **Create New App**
3. Select **From an app manifest**
4. Select your workspace
5. Copy the contents of [`assets/slack/manifest.json`](../assets/slack/manifest.json)
   and paste into the manifest editor
6. Click **Create**

### Option B: Manual Setup

1. Go to [api.slack.com/apps](https://api.slack.com/apps)
2. Click **Create New App** → **From scratch**
3. Enter app name: `GitHub Ops Bot`
4. Select your workspace
5. Click **Create App**

Then continue to configure OAuth scopes manually (Step 2).

## Step 2: Configure OAuth Scopes

If you used the manifest, scopes are pre-configured. Otherwise:

1. Go to **OAuth & Permissions** in the sidebar
2. Scroll to **Scopes** → **Bot Token Scopes**
3. Add the following scopes:

   | Scope               | Purpose                                      |
   |---------------------|----------------------------------------------|
   | `chat:write`        | Post messages to channels bot is member of   |
   | `chat:write.public` | Post to public channels without joining      |
   | `channels:read`     | View basic channel info                      |
   | `channels:join`     | Join public channels                         |

## Step 3: Install to Workspace

1. Go to **OAuth & Permissions**
2. Click **Install to Workspace**
3. Review permissions and click **Allow**

If your workspace requires admin approval:
- Submit the app for approval
- Wait for workspace admin to approve
- Return to install after approval

## Step 4: Get Bot Token

After installation:

1. Go to **OAuth & Permissions**
2. Copy the **Bot User OAuth Token**
   - Starts with `xoxb-`
   - Example: `xoxb-1234567890-...`

This is your `APP_SLACK_TOKEN`.

## Step 5: Get Channel ID

You need the channel ID (not the name) for `APP_SLACK_CHANNEL`.

### Method 1: From Slack UI

1. Open Slack in a browser or desktop app
2. Right-click the channel name
3. Select **View channel details** (or **Open channel details**)
4. Scroll to the bottom - Channel ID is shown (e.g., `C01ABC2DEFG`)

### Method 2: From Channel Link

1. Right-click the channel name
2. Select **Copy link**
3. The link contains the channel ID:
   `https://workspace.slack.com/archives/C01ABC2DEFG`

### Method 3: Using Slack API

```bash
curl -H "Authorization: Bearer xoxb-your-token" \
  "https://slack.com/api/conversations.list?types=public_channel,private_channel" \
  | jq '.channels[] | select(.name=="your-channel-name") | .id'
```

## Step 6: Add Bot to Private Channels

For private channels, you must invite the bot:

1. Open the private channel
2. Type `/invite @GitHub Ops Bot` (or your bot's display name)
3. Or click the channel name → **Integrations** → **Add apps**

Public channels work without invitation when using `chat:write.public`.

## Step 7: Configure Environment Variables

```bash
# Required Slack configuration
APP_SLACK_TOKEN=xoxb-1234567890-...
APP_SLACK_CHANNEL=C01ABC2DEFG

# Optional: per-notification-type channels (override APP_SLACK_CHANNEL)
APP_SLACK_CHANNEL_PR_BYPASS=C01234ABCDE
APP_SLACK_CHANNEL_OKTA_SYNC=C01234ABCDE
APP_SLACK_CHANNEL_ORPHANED_USERS=C01234ABCDE
```

For AWS deployments, use SSM parameters:

```bash
APP_SLACK_TOKEN=arn:aws:ssm:us-east-1:123456789:parameter/github-bot/slack-token
```

## Step 8: Customize App Appearance (Optional)

Make notifications more recognizable:

1. Go to **Basic Information**
2. Under **Display Information**:
   - **App name**: `GitHub Ops Bot` (or your preference)
   - **Short description**: Brief description of the bot
   - **App icon**: Upload a custom icon (use `assets/slack/icon.png` or your own)
   - **Background color**: `#10203B` or your brand color

## Verification

Test your Slack configuration:

```bash
# Test message posting
curl -X POST https://slack.com/api/chat.postMessage \
  -H "Authorization: Bearer xoxb-your-token" \
  -H "Content-Type: application/json" \
  -d '{
    "channel": "C01ABC2DEFG",
    "text": "GitHub Bot test message"
  }'
```

Expected response:
```json
{
  "ok": true,
  "channel": "C01ABC2DEFG",
  "ts": "1234567890.123456",
  "message": { ... }
}
```

## Notification Types

The bot sends these notification types:

| Event                 | Description                                    |
|-----------------------|------------------------------------------------|
| PR Compliance Alert   | PR merged bypassing branch protection          |
| Okta Sync Report      | Summary of team membership changes             |
| Orphaned Users Alert  | Org members not in any synced teams            |
| Sync Error            | Errors during Okta sync process                |

## Troubleshooting

### "not_in_channel" error

- Bot needs to be in the channel to post
- For private channels: `/invite @GitHub Bot`
- For public channels: Ensure `chat:write.public` scope is granted

### "invalid_auth" error

- Token may be expired or revoked
- Regenerate token: **OAuth & Permissions** → **Reinstall to Workspace**
- Verify token starts with `xoxb-`

### "channel_not_found" error

- Verify channel ID is correct (not channel name)
- Channel IDs start with `C` (public), `G` (private), or `D` (DM)
- Check the channel still exists

### "missing_scope" error

- Add the required scope in **OAuth & Permissions**
- Reinstall the app after adding scopes

### Messages not appearing

- Check the channel ID is correct
- Verify bot is in the channel (for private channels)
- Check app logs for API response errors
- Test with curl command above to isolate the issue

### Rate limiting

Slack has rate limits (typically 1 message per second per channel). The app
handles rate limits gracefully, but if you see delays:
- Notifications are queued and retried
- Consider consolidating notifications for high-volume events

## Security Considerations

- **Token security**: Store `APP_SLACK_TOKEN` securely (environment variable,
  SSM parameter, or secrets manager)
- **Channel access**: Bot can only post to channels it has access to
- **No incoming webhooks**: This setup uses bot tokens, not incoming webhooks,
  providing better security and audit trails
- **Minimal scopes**: Only request scopes actually needed
