package client

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/go-github/v93/github"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// Alert types in a security alerts report.
const (
	AlertTypeDependabot     = "dependabot"
	AlertTypeCodeScanning   = "code_scanning"
	AlertTypeSecretScanning = "secret_scanning"
)

// secretScanningSeverity is the severity given to secret scanning alerts,
// which have none in the GitHub API. They are reported unless the minimum
// severity is critical.
const secretScanningSeverity = domain.SeverityHigh

// maxSummaryLen is the longest alert summary kept, in runes.
const maxSummaryLen = 120

// codeScanningRuleSeverity maps the severity of a code scanning rule without
// a security severity level to one.
var codeScanningRuleSeverity = map[string]string{
	"error":   domain.SeverityHigh,
	"warning": domain.SeverityMedium,
	"note":    domain.SeverityLow,
}

// ListSecurityAlerts fetches open security alerts across the org that were
// created more than minAgeDays ago and are at or above minSeverity. It
// covers dependabot, code scanning, and secret scanning alerts. A source that
// fails is recorded in the report's errors and the others still run.
func (c *Client) ListSecurityAlerts(ctx context.Context, minAgeDays int, minSeverity string) (*domain.SecurityAlertsReport, error) {
	now := time.Now()
	cutoff := now.AddDate(0, 0, -minAgeDays)

	report := &domain.SecurityAlertsReport{
		MinAgeDays:   minAgeDays,
		MinSeverity:  minSeverity,
		AlertsByRepo: make(map[string][]domain.SecurityAlert),
	}
	add := func(alert domain.SecurityAlert) {
		alert.AgeDays = int(now.Sub(alert.CreatedAt).Hours() / 24)
		alert.Summary = truncate(alert.Summary, maxSummaryLen)
		report.AlertsByRepo[alert.Repo] = append(report.AlertsByRepo[alert.Repo], alert)
		report.TotalAlerts++
	}

	for _, fetch := range []func(context.Context, time.Time, string, func(domain.SecurityAlert)) error{
		c.fetchDependabotAlerts,
		c.fetchCodeScanningAlerts,
		c.fetchSecretScanningAlerts,
	} {
		if err := fetch(ctx, cutoff, minSeverity, add); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			report.Errors = append(report.Errors, err.Error())
		}
	}

	return report, nil
}

// The fetchers below request alerts oldest first, so they stop paging at
// the first alert created after the cutoff.

func (c *Client) fetchDependabotAlerts(ctx context.Context, cutoff time.Time, minSeverity string, add func(domain.SecurityAlert)) error {
	opts := &github.ListAlertsOptions{
		State:             new("open"),
		Sort:              new("created"),
		Direction:         new("asc"),
		ListCursorOptions: github.ListCursorOptions{PerPage: 100},
	}
	for alert, err := range c.gh.Dependabot.ListOrgAlertsIter(ctx, c.org, opts) {
		if err != nil {
			return fmt.Errorf("failed to fetch dependabot alerts for org '%s': %w", c.org, err)
		}
		created := alert.GetCreatedAt().Time
		if !created.Before(cutoff) {
			break
		}

		severity := alert.GetSecurityAdvisory().GetSeverity()
		if severity == "" {
			severity = alert.GetSecurityVulnerability().GetSeverity()
		}
		if !domain.MeetsSeverity(severity, minSeverity) {
			continue
		}

		add(domain.SecurityAlert{
			Type:      AlertTypeDependabot,
			Repo:      alert.GetRepository().GetFullName(),
			Number:    alert.GetNumber(),
			Severity:  strings.ToLower(severity),
			Summary:   alert.GetSecurityAdvisory().GetSummary(),
			HTMLURL:   alert.GetHTMLURL(),
			CreatedAt: created,
		})
	}
	return nil
}

// codeScanningSeverity returns an alert's security severity level, or a
// severity mapped from its rule's severity for rules without one.
func codeScanningSeverity(alert *github.Alert) string {
	if level := alert.GetRule().GetSecuritySeverityLevel(); level != "" {
		return strings.ToLower(level)
	}
	return codeScanningRuleSeverity[strings.ToLower(alert.GetRule().GetSeverity())]
}

func (c *Client) fetchCodeScanningAlerts(ctx context.Context, cutoff time.Time, minSeverity string, add func(domain.SecurityAlert)) error {
	// the API filters by a single severity, so severities are filtered here
	opts := &github.AlertListOptions{
		State:             "open",
		Sort:              "created",
		Direction:         "asc",
		ListCursorOptions: github.ListCursorOptions{PerPage: 100},
	}
	for alert, err := range c.gh.CodeScanning.ListAlertsForOrgIter(ctx, c.org, opts) {
		if err != nil {
			return fmt.Errorf("failed to fetch code scanning alerts for org '%s': %w", c.org, err)
		}
		created := alert.GetCreatedAt().Time
		if !created.Before(cutoff) {
			break
		}

		severity := codeScanningSeverity(alert)
		if !domain.MeetsSeverity(severity, minSeverity) {
			continue
		}

		add(domain.SecurityAlert{
			Type:      AlertTypeCodeScanning,
			Repo:      alert.GetRepository().GetFullName(),
			Number:    alert.GetNumber(),
			Severity:  severity,
			Summary:   alert.GetRule().GetDescription(),
			HTMLURL:   alert.GetHTMLURL(),
			CreatedAt: created,
		})
	}
	return nil
}

func (c *Client) fetchSecretScanningAlerts(ctx context.Context, cutoff time.Time, minSeverity string, add func(domain.SecurityAlert)) error {
	if !domain.MeetsSeverity(secretScanningSeverity, minSeverity) {
		return nil
	}

	opts := &github.SecretScanningAlertListOptions{
		State:             "open",
		Sort:              "created",
		Direction:         "asc",
		ListCursorOptions: github.ListCursorOptions{PerPage: 100},
	}
	for alert, err := range c.gh.SecretScanning.ListAlertsForOrgIter(ctx, c.org, opts) {
		if err != nil {
			return fmt.Errorf("failed to fetch secret scanning alerts for org '%s': %w", c.org, err)
		}
		created := alert.GetCreatedAt().Time
		if !created.Before(cutoff) {
			break
		}

		add(domain.SecurityAlert{
			Type:      AlertTypeSecretScanning,
			Repo:      alert.GetRepository().GetFullName(),
			Number:    alert.GetNumber(),
			Severity:  secretScanningSeverity,
			Summary:   alert.GetSecretTypeDisplayName(),
			HTMLURL:   alert.GetHTMLURL(),
			CreatedAt: created,
		})
	}
	return nil
}

// truncate shortens s to maxLen runes, ending with "..." when it was cut.
func truncate(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
