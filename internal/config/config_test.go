package config

import (
	"context"
	"testing"
)

func TestResolveEnvValue(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		key       string
		value     string
		wantSSM   bool
		wantError bool
	}{
		{
			name:      "empty value",
			key:       "TEST_KEY",
			value:     "",
			wantSSM:   false,
			wantError: false,
		},
		{
			name:      "plain text value",
			key:       "TEST_KEY",
			value:     "plain-text-secret",
			wantSSM:   false,
			wantError: false,
		},
		{
			name:      "valid ssm arn",
			key:       "TEST_KEY",
			value:     "arn:aws:ssm:us-east-1:123456789012:parameter/test/param",
			wantSSM:   true,
			wantError: true, // will error in test env without AWS creds
		},
		{
			name:      "invalid ssm arn missing parameter prefix",
			key:       "TEST_KEY",
			value:     "arn:aws:ssm:us-east-1:123456789012:test/param",
			wantSSM:   true,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := resolveEnvValue(ctx, tt.key, tt.value)

			if tt.wantError && err == nil {
				t.Errorf("expected error but got none")
			}

			if !tt.wantError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if !tt.wantSSM && err == nil && result != tt.value {
				t.Errorf("expected result %q, got %q", tt.value, result)
			}
		})
	}
}
