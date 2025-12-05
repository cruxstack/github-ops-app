# Okta Setup

This guide walks through creating an Okta API Services application for
github-ops-app to sync Okta groups with GitHub teams.

## Prerequisites

- Okta organization with admin access
- Super Admin or Application Admin role

## Step 1: Create API Services Application

1. Log in to your **Okta Admin Console**
2. Navigate to **Applications** → **Applications**
3. Click **Create App Integration**
4. Select **API Services** and click **Next**

   > API Services apps use OAuth 2.0 client credentials flow with no user
   > context, ideal for server-to-server integrations.

5. Enter application name: `github-ops-app` (or similar)
6. Click **Save**

## Step 2: Configure Client Authentication

After creating the app:

1. Go to the **General** tab
2. Under **Client Credentials**, click **Edit**
3. Set **Client authentication** to **Public key / Private key**
4. Click **Save**

## Step 3: Generate Key Pair

### Option A: Generate in Okta (Recommended)

1. Under **PUBLIC KEYS**, click **Add Key**
2. Click **Generate new key**
3. Click **Download PEM** to save the private key
4. Click **Save**

The downloaded file contains your private key for `APP_OKTA_PRIVATE_KEY`.

### Option B: Generate Your Own Key

```bash
# Generate private key
openssl genpkey -algorithm RSA -out okta-private-key.pem -pkeyopt rsa_keygen_bits:2048

# Extract public key in JWK format (for Okta)
# You'll need to convert PEM to JWK - use a tool like:
# https://8gwifi.org/jwkconvertfunctions.jsp
# Or use the node jose library
```

Then upload the public JWK to Okta under **PUBLIC KEYS** → **Add Key**.

## Step 4: Note Client ID

On the **General** tab, find and save:

- **Client ID** - alphanumeric string (e.g., `0oa1abc2def3ghi4j5k6`)

## Step 5: Grant API Scopes

1. Go to the **Okta API Scopes** tab
2. Grant the following scopes:

   | Scope              | Purpose                       |
   |--------------------|-------------------------------|
   | `okta.groups.read` | Read group names and members  |
   | `okta.users.read`  | Read user profiles            |

3. Click **Grant** for each scope

These scopes allow read-only access to groups and users - no write access to
Okta is required.

## Step 6: Identify Your Okta Domain

Your Okta domain is the URL you use to access the admin console:

- Production: `your-org.okta.com`
- Preview/Dev: `your-org.oktapreview.com` or `dev-123456.okta.com`

Use the domain without `https://` prefix for `APP_OKTA_DOMAIN`.

## Step 7: Configure User Profile Field

The app needs to map Okta users to GitHub usernames. Determine which Okta user
profile field contains GitHub usernames:

| Common Fields      | Description                              |
|--------------------|------------------------------------------|
| `login`            | Okta username (often email)              |
| `email`            | User's email address                     |
| `githubUsername`   | Custom field (recommended)               |
| `nickName`         | Sometimes used for GitHub username       |

### Adding a Custom GitHub Username Field (Recommended)

1. Go to **Directory** → **Profile Editor**
2. Select **Okta** (or your user profile)
3. Click **Add Attribute**
4. Configure:
   - Data type: `string`
   - Display name: `GitHub Username`
   - Variable name: `githubUsername`
   - Description: `User's GitHub username for team sync`
5. Click **Save**

Then set `APP_OKTA_GITHUB_USER_FIELD=githubUsername`.

## Step 8: Prepare Okta Groups

Ensure your Okta groups follow a naming convention that can be matched by sync
rules:

**Example naming conventions:**

| Pattern              | Example Groups                               |
|----------------------|----------------------------------------------|
| `github-{team}`      | `github-engineering`, `github-platform`      |
| `gh-eng-{team}`      | `gh-eng-frontend`, `gh-eng-backend`          |
| `Team - {name}`      | `Team - Platform`, `Team - Security`         |

Groups can be:
- Okta groups (manually managed)
- Groups synced from Active Directory
- Groups from other identity providers

## Step 9: Configure Environment Variables

```bash
# Required Okta configuration
APP_OKTA_DOMAIN=your-org.okta.com
APP_OKTA_CLIENT_ID=0oa1abc2def3ghi4j5k6
APP_OKTA_GITHUB_USER_FIELD=githubUsername

# Private key (choose one method)
APP_OKTA_PRIVATE_KEY="-----BEGIN PRIVATE KEY-----
...
-----END PRIVATE KEY-----"

# Or use a file path
APP_OKTA_PRIVATE_KEY_PATH=/path/to/okta-private-key.pem

# Or use AWS SSM parameter
APP_OKTA_PRIVATE_KEY=arn:aws:ssm:us-east-1:123456789:parameter/github-bot/okta-key
```

## Step 10: Configure Sync Rules

Define how Okta groups map to GitHub teams:

```bash
APP_OKTA_SYNC_RULES='[
  {
    "name": "engineering-teams",
    "enabled": true,
    "okta_group_pattern": "^github-eng-.*",
    "github_team_prefix": "eng-",
    "strip_prefix": "github-eng-",
    "sync_members": true,
    "create_team_if_missing": true
  }
]'
```

See the main README for complete sync rule documentation.

## Verification

Test your Okta configuration:

```bash
# Test OAuth token retrieval (manual verification)
# The app will automatically authenticate on startup

# Check app logs for:
# - "okta client initialized"
# - No authentication errors during sync
```

Trigger a sync and verify:
1. POST to `/scheduled/okta-sync` endpoint
2. Check logs for groups discovered and teams synced
3. Verify GitHub team memberships match Okta groups

## Troubleshooting

### Authentication failed / Invalid client

- Verify `APP_OKTA_CLIENT_ID` matches the Client ID in Okta
- Check the private key is the one generated for this specific app
- Ensure the key format is correct (PEM with proper headers)

### No groups found

- Verify `okta.groups.read` scope is granted
- Check your sync rule patterns match actual group names
- Test the regex pattern against your group names

### Users not syncing

- Verify `okta.users.read` scope is granted
- Check `APP_OKTA_GITHUB_USER_FIELD` points to a valid profile field
- Ensure users have the GitHub username field populated
- Only `ACTIVE` users are synced - suspended users are skipped

### Rate limiting

Okta has API rate limits. If you hit limits:
- Reduce sync frequency
- The app handles rate limit responses gracefully

### Permission denied errors

- API Services apps need explicit scope grants
- Check that scopes were granted (not just requested)
- Super Admin role may be required to grant certain scopes
