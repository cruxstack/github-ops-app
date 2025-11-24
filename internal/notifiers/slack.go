// Package notifiers provides Slack notification formatting and sending.
package notifiers

import (
	"github.com/slack-go/slack"
)

// SlackNotifier sends formatted messages to Slack channels.
type SlackNotifier struct {
	client  *slack.Client
	channel string
}

// NewSlackNotifier creates a Slack notifier with default API URL.
func NewSlackNotifier(token, channel string) *SlackNotifier {
	return NewSlackNotifierWithAPIURL(token, channel, "")
}

// NewSlackNotifierWithAPIURL creates a Slack notifier with custom API URL.
// useful for testing with mock servers.
func NewSlackNotifierWithAPIURL(token, channel, apiURL string) *SlackNotifier {
	var opts []slack.Option
	if apiURL != "" {
		opts = append(opts, slack.OptionAPIURL(apiURL))
	}
	return &SlackNotifier{
		client:  slack.New(token, opts...),
		channel: channel,
	}
}
