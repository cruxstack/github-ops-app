# AWS Lambda Deployment

This directory contains the AWS Lambda adapter for the GitHub bot. Lambda is one
of several deployment options - see the [main README](../../README.md) for
alternatives like standard HTTP servers.

## Overview

The Lambda adapter translates AWS-specific events into the unified
`app.HandleRequest()` interface:
- **API Gateway** (webhooks, status, config) → `app.Request{Type: HTTP}`
- **EventBridge** (scheduled sync) → `app.Request{Type: Scheduled}`

## Build

```bash
# from repository root
make build-lambda

# creates dist/bootstrap (Lambda requires this exact name)
```

## Deploy to AWS Lambda

### 1. Create Function

* **Runtime**: `provided.al2023`
* **Handler**: `bootstrap`
* **Architecture**: `x86_64`
* **Memory**: 256 MB
* **Timeout**: 30 seconds
* **IAM Role**: `AWSLambdaBasicExecutionRole` (no additional permissions needed)

### 2. Upload Code

Upload `dist/bootstrap` to your Lambda function via:
- AWS Console (function code upload)
- AWS CLI: `aws lambda update-function-code --function-name github-ops-app
  --zip-file fileb://dist/bootstrap.zip`
- Infrastructure as Code (Terraform, CDK, CloudFormation)

### 3. Configure Environment Variables

Set all required environment variables (see
[Configuration](../../README.md#configuration) in main README).

### 4. Setup Triggers

#### API Gateway (for GitHub Webhooks)

Create an HTTP API Gateway:

1. **Create API**: HTTP API (not REST API)
2. **Add Route**: `POST /webhooks` (or `ANY /{proxy+}` for universal handler)
3. **Integration**: Lambda function (proxy integration enabled)
4. **Deploy**: Note the invoke URL
5. **GitHub App**: Set webhook URL to `https://<api-gateway-url>/webhooks`

**Headers**: API Gateway automatically forwards all headers including
`X-GitHub-Event` and `X-Hub-Signature-256`.

#### EventBridge (for Scheduled Okta Sync)

Create an EventBridge rule:

1. **Rule Type**: Schedule
2. **Schedule**: 
   - Rate: `rate(1 hour)` or `rate(6 hours)`
   - Cron: `cron(0 */6 * * ? *)` (every 6 hours)
3. **Target**: Lambda function
4. **Input**: Configure constant (JSON):
```json
{
  "source": "aws.events",
  "detail-type": "Scheduled Event",
  "detail": {
    "action": "okta-sync"
  }
}
```

## Architecture

### Universal Handler

The Lambda function uses a universal handler that detects event types and
converts them to the unified `app.Request` format:

```go
func UniversalHandler(ctx context.Context, event json.RawMessage) (any, error)
```

**Supported Events**:
- `APIGatewayV2HTTPRequest` → Converts to `app.Request{Type: HTTP}`
- `CloudWatchEvent` (EventBridge) → Converts to `app.Request{Type: Scheduled}`

All requests are then processed by `app.HandleRequest()` which routes based on
request type and path.

### Endpoints

When invoked via API Gateway:

| Method | Path                   | Description                       |
|--------|------------------------|-----------------------------------|
| POST   | `/webhooks`            | GitHub webhook receiver           |
| POST   | `/scheduled/okta-sync` | Trigger Okta sync                 |
| POST   | `/scheduled/slack-test`| Send test notification to Slack   |
| GET    | `/server/status`       | Health check and feature flags    |
| GET    | `/server/config`       | Config inspection (secrets hidden)|

## Monitoring

### CloudWatch Logs

View logs:
```bash
aws logs tail /aws/lambda/your-function-name --follow
```

### Metrics

Lambda automatically tracks:
- Invocations
- Duration
- Errors
- Throttles

### Debug Mode

Enable verbose logging:
```bash
aws lambda update-function-configuration \
  --function-name github-ops-app \
  --environment Variables="{APP_DEBUG_ENABLED=true,...}"
```

## Cost Optimization

### Memory Sizing

Start with 256 MB and adjust based on CloudWatch metrics:
- **Under-provisioned**: Slow response times, timeouts
- **Over-provisioned**: Wasted cost
- **Right-sized**: <100ms execution time for webhooks, <5s for sync

### Timeout

- **Webhooks**: 15 seconds (GitHub timeout is 10s)
- **Scheduled sync**: 30-60 seconds (depends on org size)

### Invocation Frequency

**Webhooks**: Pay-per-use (only when events occur)

**Scheduled Sync**: Runs on schedule regardless of changes
- Hourly sync: ~730 invocations/month
- 6-hour sync: ~120 invocations/month

**Cost Example** (us-east-1, 256MB, 5s avg):
- Free tier: 1M requests, 400,000 GB-seconds/month
- Typical usage: <1000 invocations/month → **free**

## Troubleshooting

### Webhook Signature Validation Fails

**Symptom**: `401 unauthorized` responses, logs show "webhook signature
validation failed"

**Solutions**:
- Verify `APP_GITHUB_WEBHOOK_SECRET` matches GitHub App settings
- Check API Gateway forwards `X-Hub-Signature-256` header
- Ensure payload isn't modified by API Gateway (use proxy integration)

### EventBridge Sync Not Running

**Symptom**: No sync activity in logs

**Solutions**:
- Verify EventBridge rule is **enabled**
- Check rule target is configured correctly
- Verify input payload has correct structure
- Check Lambda has no concurrent execution limits

### Lambda Timeout

**Symptom**: Function execution exceeds configured timeout

**Solutions**:
- Increase timeout (max 15 minutes)
- Reduce Okta sync scope (fewer rules/groups)
- Check for slow API responses from GitHub/Okta
- Enable debug logging to identify bottleneck

### Cold Start Latency

**Symptom**: First webhook after idle period is slow

**Solutions**:
- Accept it (typically 100-500ms, GitHub webhooks tolerate this)
- Use Lambda provisioned concurrency (increases cost)
- Consider standard HTTP server for consistently low latency

## Migration from Lambda

To migrate to a standard HTTP server deployment:

1. Build server: `make build-server`
2. Deploy to VPS/container/K8s
3. Update GitHub webhook URL to new server
4. Setup external cron for `/scheduled/okta-sync` endpoint
5. Delete Lambda function and API Gateway

No code changes needed - the core app is deployment-agnostic.

## Alternatives

Consider non-Lambda deployments if you:
- Need consistently low latency (<50ms)
- Want to avoid AWS vendor lock-in
- Prefer traditional server management
- Have existing container infrastructure

See [main README](../../README.md) for standard HTTP server deployment.
