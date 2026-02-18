package client

import (
	"context"
	"math"
	"time"
	"unicode/utf8"

	"github.com/cockroachdb/errors"
	"github.com/cruxstack/github-ops-app/internal/domain"
	"github.com/google/go-github/v79/github"
)

// severityRank maps severity strings to numeric values for comparison.
// higher values indicate greater severity.
var severityRank = map[string]int{
	"critical": 4,
	"high":     3,
	"medium":   2,
	"low":      1,
}

// meetsMinSeverity returns true if the alert severity is at or above
// the minimum threshold.
func meetsMinSeverity(alertSeverity, minSeverity string) bool {
	return severityRank[alertSeverity] >= severityRank[minSeverity]
}

// ListSecurityAlerts fetches open security alerts across the org that
// are older than minAgeDays and at or above minSeverity. covers
// dependabot, code scanning, and secret scanning alerts.
func (c *Client) ListSecurityAlerts(ctx context.Context, minAgeDays int, minSeverity string) (*domain.SecurityAlertsReport, error) {
	if err := c.ensureValidToken(ctx); err != nil {
		return nil, err
	}

	cutoff := time.Now().AddDate(0, 0, -minAgeDays)

	report := &domain.SecurityAlertsReport{
		MinAgeDays:   minAgeDays,
		MinSeverity:  minSeverity,
		AlertsByRepo: make(map[string][]domain.SecurityAlert),
	}

	c.fetchDependabotAlerts(ctx, cutoff, minSeverity, report)
	c.fetchCodeScanningAlerts(ctx, cutoff, minSeverity, report)
	c.fetchSecretScanningAlerts(ctx, cutoff, report)

	total := 0
	for _, alerts := range report.AlertsByRepo {
		total += len(alerts)
	}
	report.TotalAlerts = total

	return report, nil
}

// fetchDependabotAlerts fetches open dependabot alerts for the org,
// filtering by severity and age. results are sorted by creation date
// ascending so the pagination early-exit works correctly: once a page
// contains only alerts newer than the cutoff, all subsequent pages
// will also be newer.
func (c *Client) fetchDependabotAlerts(ctx context.Context, cutoff time.Time, minSeverity string, report *domain.SecurityAlertsReport) {
	state := "open"
	opts := &github.ListAlertsOptions{
		State:     &state,
		Sort:      github.Ptr("created"),
		Direction: github.Ptr("asc"),
		ListCursorOptions: github.ListCursorOptions{
			PerPage: 100,
		},
	}

	for {
		alerts, resp, err := c.client.Dependabot.ListOrgAlerts(ctx, c.org, opts)
		if err != nil {
			report.Errors = append(report.Errors,
				errors.Wrapf(err, "failed to fetch dependabot alerts for org '%s'", c.org).Error())
			return
		}

		pastCutoff := false
		for _, alert := range alerts {
			created := alert.GetCreatedAt().Time
			if !created.Before(cutoff) {
				pastCutoff = true
				break
			}

			sev := ""
			if alert.SecurityAdvisory != nil {
				sev = alert.SecurityAdvisory.GetSeverity()
			}
			if sev == "" && alert.SecurityVulnerability != nil &&
				alert.SecurityVulnerability.Severity != nil {
				sev = *alert.SecurityVulnerability.Severity
			}

			if !meetsMinSeverity(sev, minSeverity) {
				continue
			}

			repo := ""
			if alert.Repository != nil {
				repo = alert.Repository.GetFullName()
			}

			ageDays := int(math.Floor(
				time.Since(created).Hours() / 24,
			))

			report.AlertsByRepo[repo] = append(
				report.AlertsByRepo[repo],
				domain.SecurityAlert{
					Type:      "dependabot",
					Repo:      repo,
					Number:    alert.GetNumber(),
					Severity:  sev,
					Summary:   truncate(alert.SecurityAdvisory.GetSummary(), 120),
					HTMLURL:   alert.GetHTMLURL(),
					CreatedAt: created,
					AgeDays:   ageDays,
				},
			)
		}

		if resp.NextPageToken == "" || pastCutoff {
			break
		}
		opts.ListCursorOptions.After = resp.NextPageToken
	}
}

// fetchCodeScanningAlerts fetches open code scanning alerts for the
// org, filtering by severity and age. results are sorted ascending
// by creation date so the early-exit optimization works correctly.
// the code scanning API only accepts a single severity value, so we
// fetch all severities and filter client-side via meetsMinSeverity.
func (c *Client) fetchCodeScanningAlerts(ctx context.Context, cutoff time.Time, minSeverity string, report *domain.SecurityAlertsReport) {
	opts := &github.AlertListOptions{
		State:     "open",
		Sort:      "created",
		Direction: "asc",
		ListCursorOptions: github.ListCursorOptions{
			PerPage: 100,
		},
	}

	for {
		alerts, resp, err := c.client.CodeScanning.ListAlertsForOrg(ctx, c.org, opts)
		if err != nil {
			report.Errors = append(report.Errors,
				errors.Wrapf(err, "failed to fetch code scanning alerts for org '%s'", c.org).Error())
			return
		}

		pastCutoff := false
		for _, alert := range alerts {
			created := alert.GetCreatedAt().Time
			if !created.Before(cutoff) {
				pastCutoff = true
				break
			}

			sev := ""
			if alert.Rule != nil {
				sev = alert.Rule.GetSecuritySeverityLevel()
				if sev == "" {
					sev = alert.Rule.GetSeverity()
				}
			}

			// alerts with unknown severity are included
			// conservatively; we only exclude alerts that have a
			// known severity below the threshold.
			if sev != "" && !meetsMinSeverity(sev, minSeverity) {
				continue
			}

			repo := ""
			if alert.Repository != nil {
				repo = alert.Repository.GetFullName()
			}

			summary := ""
			if alert.Rule != nil {
				summary = alert.Rule.GetDescription()
			}

			ageDays := int(math.Floor(
				time.Since(created).Hours() / 24,
			))

			report.AlertsByRepo[repo] = append(
				report.AlertsByRepo[repo],
				domain.SecurityAlert{
					Type:      "code_scanning",
					Repo:      repo,
					Number:    int(alert.GetNumber()),
					Severity:  sev,
					Summary:   truncate(summary, 120),
					HTMLURL:   alert.GetHTMLURL(),
					CreatedAt: created,
					AgeDays:   ageDays,
				},
			)
		}

		if resp.NextPageToken == "" || pastCutoff {
			break
		}
		opts.ListCursorOptions.After = resp.NextPageToken
	}
}

// fetchSecretScanningAlerts fetches open secret scanning alerts for
// the org, filtering by age only. results are sorted ascending by
// creation date so the early-exit optimization works correctly.
// secret scanning alerts have no severity in the GitHub API, so we
// assign "high" to ensure they are included when minSeverity is
// "high" or below but excluded when minSeverity is "critical".
func (c *Client) fetchSecretScanningAlerts(ctx context.Context, cutoff time.Time, report *domain.SecurityAlertsReport) {
	opts := &github.SecretScanningAlertListOptions{
		State:     "open",
		Sort:      "created",
		Direction: "asc",
		ListCursorOptions: github.ListCursorOptions{
			PerPage: 100,
		},
	}

	for {
		alerts, resp, err := c.client.SecretScanning.ListAlertsForOrg(ctx, c.org, opts)
		if err != nil {
			report.Errors = append(report.Errors,
				errors.Wrapf(err, "failed to fetch secret scanning alerts for org '%s'", c.org).Error())
			return
		}

		pastCutoff := false
		for _, alert := range alerts {
			created := alert.GetCreatedAt().Time
			if !created.Before(cutoff) {
				pastCutoff = true
				break
			}

			repo := ""
			if alert.Repository != nil {
				repo = alert.Repository.GetFullName()
			}

			ageDays := int(math.Floor(
				time.Since(created).Hours() / 24,
			))

			report.AlertsByRepo[repo] = append(
				report.AlertsByRepo[repo],
				domain.SecurityAlert{
					Type:      "secret_scanning",
					Repo:      repo,
					Number:    alert.GetNumber(),
					Severity:  "high",
					Summary:   truncate(alert.GetSecretTypeDisplayName(), 120),
					HTMLURL:   alert.GetHTMLURL(),
					CreatedAt: created,
					AgeDays:   ageDays,
				},
			)
		}

		if resp.NextPageToken == "" || pastCutoff {
			break
		}
		opts.ListCursorOptions.After = resp.NextPageToken
	}
}

// truncate shortens a string to max runes, appending "..." if
// needed. operates on rune count so multi-byte characters are not
// split mid-codepoint.
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
