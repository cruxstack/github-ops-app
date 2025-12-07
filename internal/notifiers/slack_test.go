package notifiers

import "testing"

func TestChannelFor(t *testing.T) {
	defaultChannel := "C_DEFAULT"
	prBypassChannel := "C_PR_BYPASS"
	oktaSyncChannel := "C_OKTA_SYNC"

	tests := []struct {
		name        string
		channels    SlackChannels
		typeChannel string
		want        string
	}{
		{
			name: "uses type-specific channel when set",
			channels: SlackChannels{
				Default:  defaultChannel,
				PRBypass: prBypassChannel,
			},
			typeChannel: prBypassChannel,
			want:        prBypassChannel,
		},
		{
			name: "falls back to default when type-specific is empty",
			channels: SlackChannels{
				Default:  defaultChannel,
				PRBypass: "",
			},
			typeChannel: "",
			want:        defaultChannel,
		},
		{
			name: "all channels set independently",
			channels: SlackChannels{
				Default:       defaultChannel,
				PRBypass:      prBypassChannel,
				OktaSync:      oktaSyncChannel,
				OrphanedUsers: "C_ORPHANED",
			},
			typeChannel: oktaSyncChannel,
			want:        oktaSyncChannel,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := &SlackNotifier{channels: tt.channels}
			got := n.channelFor(tt.typeChannel)
			if got != tt.want {
				t.Errorf("channelFor() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSlackChannels_AllFallbackToDefault(t *testing.T) {
	defaultChannel := "C_DEFAULT"
	n := &SlackNotifier{
		channels: SlackChannels{
			Default: defaultChannel,
		},
	}

	// all type-specific channels should fall back to default
	if got := n.channelFor(n.channels.PRBypass); got != defaultChannel {
		t.Errorf("PRBypass channel = %q, want %q", got, defaultChannel)
	}
	if got := n.channelFor(n.channels.OktaSync); got != defaultChannel {
		t.Errorf("OktaSync channel = %q, want %q", got, defaultChannel)
	}
	if got := n.channelFor(n.channels.OrphanedUsers); got != defaultChannel {
		t.Errorf("OrphanedUsers channel = %q, want %q", got, defaultChannel)
	}
}
