// Package notifiers provides Slack notification formatting and sending.
package notifiers

import (
	"github.com/slack-go/slack"
)

// SlackChannels holds channel IDs for different notification types.
// empty values fall back to the default channel.
type SlackChannels struct {
	Default       string
	PRBypass      string
	OktaSync      string
	OrphanedUsers string
}

// SlackNotifier sends formatted messages to Slack channels.
type SlackNotifier struct {
	client   *slack.Client
	channels SlackChannels
}

// NewSlackNotifier creates a Slack notifier with default API URL.
func NewSlackNotifier(token string, channels SlackChannels) *SlackNotifier {
	return NewSlackNotifierWithAPIURL(token, channels, "")
}

// NewSlackNotifierWithAPIURL creates a Slack notifier with custom API URL.
// useful for testing with mock servers.
func NewSlackNotifierWithAPIURL(token string, channels SlackChannels, apiURL string) *SlackNotifier {
	var opts []slack.Option
	if apiURL != "" {
		opts = append(opts, slack.OptionAPIURL(apiURL))
	}
	return &SlackNotifier{
		client:   slack.New(token, opts...),
		channels: channels,
	}
}

// channelFor returns the channel for a notification type, falling back to
// default if the type-specific channel is empty.
func (s *SlackNotifier) channelFor(typeChannel string) string {
	if typeChannel != "" {
		return typeChannel
	}
	return s.channels.Default
}
