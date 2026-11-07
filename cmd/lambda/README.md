# AWS Lambda Deployment

This command runs github-ops-app as an AWS Lambda function. It serves the same
routes as the HTTP server; see the [main README](../../README.md) for the
configuration and the other ways to run it. The
[terraform-aws-github-ops-app](https://github.com/cruxstack/terraform-aws-github-ops-app)
module deploys everything below.

## Events

- **API Gateway HTTP API** (payload format 2.0) requests carry GitHub webhooks
  and admin requests. They are converted to HTTP requests, with their query
  string, headers, cookies, and base64 bodies, and served by the same router
  as the server.
- **EventBridge** events run a scheduled action named in their detail. AWS
  authorizes these invocations, so they don't need the admin token.

The configuration loads on the first invocation. If it fails, such as when an
SSM parameter is unavailable, it is retried on the next invocation instead of
leaving the container broken until it is recycled.

## Build

```bash
# from the repository root; GOARCH=arm64 for arm64 functions
make build-lambda
cd dist && zip ../bootstrap.zip bootstrap
```

Releases attach `github-ops-app_lambda_amd64.zip` and
`github-ops-app_lambda_arm64.zip`.

## Deploy

### Function

| Setting      | Value                                                  |
| ------------ | ------------------------------------------------------ |
| Runtime      | `provided.al2023`                                      |
| Handler      | `bootstrap`                                            |
| Architecture | `x86_64` or `arm64`, matching the build                |
| Memory       | 256 MB                                                 |
| Timeout      | 5 minutes or more, so a full Okta sync can finish      |
| Role         | `AWSLambdaBasicExecutionRole`, plus `ssm:GetParameter` |

Set the [configuration](../../README.md#configuration) as environment
variables. Keep secrets in SSM Parameter Store and set their variables to the
parameter ARNs, so they don't appear in the function's configuration.

### API Gateway

1. Create an **HTTP API** with a Lambda proxy integration.
2. Add the route `ANY /{proxy+}`, or at least `POST /webhooks`.
3. If the stage is not `$default`, set `APP_BASE_PATH` to `/<stage>`.
4. Set the GitHub App's webhook URL to `https://<api-url>/<stage>/webhooks`.

### EventBridge

Add a schedule rule per scheduled feature, with the Lambda function as the
target and an input whose detail names the action:

```json
{
  "source": "aws.events",
  "detail-type": "Scheduled Event",
  "detail": { "action": "okta-sync" }
}
```

| Action            | Suggested schedule           |
| ----------------- | ---------------------------- |
| `okta-sync`       | `rate(1 hour)`               |
| `security-alerts` | `cron(0 13 ? * MON *)`       |

A failed action fails the invocation, so EventBridge retries it and it shows
in the function's error metrics.

## Installer

The [installer](../../docs/github-app-setup.md#option-a-installer) works in
Lambda with `STORAGE_MODE=aws-ssm`:

1. Set `GITHUB_APP_INSTALLER_ENABLED=true`, `STORAGE_MODE=aws-ssm`, and
   `AWS_SSM_PARAMETER_PREFIX`, such as `/github-ops-app/`.
2. Set `APP_GITHUB_APP_ID`, `APP_GITHUB_APP_PRIVATE_KEY`, and
   `APP_GITHUB_WEBHOOK_SECRET` to the ARNs of the parameters the installer
   will create, such as
   `arn:aws:ssm:us-east-1:123456789012:parameter/github-ops-app/APP_GITHUB_APP_ID`.
3. Grant the role `ssm:GetParameter` and `ssm:PutParameter` (and
   `ssm:AddTagsToResource` with `AWS_SSM_TAGS`) on the prefix.
4. Visit `https://<api-url>/<stage>/setup` and create the app.

The next invocation after the app is created loads its credentials. When
`APP_BASE_PATH` is set, the installer's "disable" button posts to a path
without the stage; set `GITHUB_APP_INSTALLER_ENABLED=false` instead.

## Troubleshooting

- **`401` on webhooks**: check `APP_GITHUB_WEBHOOK_SECRET`, and that API
  Gateway uses a proxy integration, which passes the body unchanged.
- **`503 service not configured`**: the configuration did not load; the logs
  list every problem.
- **`404` on every route**: `APP_BASE_PATH` doesn't match the stage.
- **Timeouts**: raise the timeout; a sync of many large groups takes minutes.
