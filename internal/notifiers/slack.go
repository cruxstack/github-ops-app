// Package notifiers formats and sends Slack notifications.
package notifiers

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/slack-go/slack"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// requestTimeout bounds every Slack API request.
const requestTimeout = 30 * time.Second

// Slack limits a message to 50 blocks and a section's text to 3000
// characters. Messages stay under both so long reports are never rejected.
const (
	maxBlocks      = 50
	maxSectionText = 3000
	// maxListItems is the most items listed under one heading; the rest are
	// summarized as "...and N more".
	maxListItems = 100
)

// SlackChannels holds channel IDs for different notification types.
// Empty values fall back to the default channel.
type SlackChannels struct {
	Default        string
	PRBypass       string
	OktaSync       string
	OrphanedUsers  string
	SecurityAlerts string
}

// SlackMessages holds optional custom text for notifications. Empty values
// are left out.
type SlackMessages struct {
	// PRBypassFooterNote is Slack mrkdwn written by the operator, so it is
	// sent as is.
	PRBypassFooterNote string
}

// Options configures a SlackNotifier.
type Options struct {
	Token    string
	Channels SlackChannels
	Messages SlackMessages
	// APIURL overrides the Slack API URL, for tests.
	APIURL string
	// GitHubWebURL is the web URL of GitHub links, such as
	// https://github.com or a GitHub Enterprise Server URL.
	GitHubWebURL string
	HTTPClient   *http.Client
}

// SlackNotifier sends formatted messages to Slack channels.
type SlackNotifier struct {
	client       *slack.Client
	channels     SlackChannels
	messages     SlackMessages
	githubWebURL string
}

var _ domain.Notifier = (*SlackNotifier)(nil)

// New creates a SlackNotifier.
func New(opts Options) *SlackNotifier {
	var slackOpts []slack.Option
	if opts.APIURL != "" {
		slackOpts = append(slackOpts, slack.OptionAPIURL(opts.APIURL))
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	slackOpts = append(slackOpts, slack.OptionHTTPClient(httpClient))
	webURL := strings.TrimRight(opts.GitHubWebURL, "/")
	if webURL == "" {
		webURL = "https://github.com"
	}
	return &SlackNotifier{
		client:       slack.New(opts.Token, slackOpts...),
		channels:     opts.Channels,
		messages:     opts.Messages,
		githubWebURL: webURL,
	}
}

// channelFor returns the channel for a notification type, falling back to
// the default if the type-specific channel is empty.
func (s *SlackNotifier) channelFor(typeChannel string) string {
	if typeChannel != "" {
		return typeChannel
	}
	return s.channels.Default
}

// escape makes untrusted text safe to include in Slack mrkdwn, so text such
// as a PR title cannot mention channels or users or add links.
func escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// link returns a mrkdwn link with escaped text.
func link(url, text string) string {
	if url == "" {
		return escape(text)
	}
	return fmt.Sprintf("<%s|%s>", escape(url), escape(text))
}

func mrkdwn(text string) *slack.TextBlockObject {
	return slack.NewTextBlockObject(slack.MarkdownType, text, false, false)
}

func section(text string) slack.Block {
	return slack.NewSectionBlock(mrkdwn(text), nil, nil)
}

// listSections returns sections with a heading followed by one line per
// item, split so each section's text fits Slack's limit. Items beyond
// maxListItems are summarized.
func listSections(heading string, lines []string) []slack.Block {
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > maxListItems {
		more := len(lines) - maxListItems
		lines = append(lines[:maxListItems:maxListItems], fmt.Sprintf("_...and %d more_", more))
	}

	var blocks []slack.Block
	var b strings.Builder
	b.WriteString(heading)
	for _, line := range lines {
		line = truncateRunes(line, maxSectionText-len(heading)-2)
		if b.Len()+len(line)+1 > maxSectionText {
			blocks = append(blocks, section(b.String()))
			b.Reset()
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(line)
	}
	return append(blocks, section(b.String()))
}

// truncateRunes shortens s to at most n bytes without splitting a rune.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool {
	return b&0xC0 != 0x80
}

// limitBlocks keeps a message under Slack's block limit, replacing the
// blocks that don't fit with a note.
func limitBlocks(blocks []slack.Block) []slack.Block {
	if len(blocks) <= maxBlocks {
		return blocks
	}
	kept := blocks[: maxBlocks-1 : maxBlocks-1]
	return append(kept, slack.NewContextBlock("", mrkdwn(
		fmt.Sprintf("_%d more sections were left out; see the application logs for the full report._", len(blocks)-len(kept)))))
}
