package setup

import (
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cruxstack/github-app-setup-go/configstore"
)

// TestManifestMatchesAsset keeps the manifest in assets/github, for creating
// the app by hand, the same as the one the installer uses.
func TestManifestMatchesAsset(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "assets", "github", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var asset struct {
		Name        string            `json:"name"`
		Public      bool              `json:"public"`
		Permissions map[string]string `json:"default_permissions"`
		Events      []string          `json:"default_events"`
	}
	if err := json.Unmarshal(raw, &asset); err != nil {
		t.Fatal(err)
	}

	m := Manifest()
	if asset.Name != m.Name || asset.Public != m.Public {
		t.Errorf("asset name/public = %q/%v, manifest %q/%v", asset.Name, asset.Public, m.Name, m.Public)
	}
	if !maps.Equal(asset.Permissions, m.DefaultPerms) {
		t.Errorf("asset permissions = %v\nmanifest permissions = %v", asset.Permissions, m.DefaultPerms)
	}
	if !slices.Equal(asset.Events, m.DefaultEvents) {
		t.Errorf("asset events = %v, manifest events = %v", asset.Events, m.DefaultEvents)
	}
	if m.Public {
		t.Error("the app must be private so other accounts cannot install it")
	}
}

type recordingStore struct {
	NopStore
	saved *configstore.AppCredentials
}

func (s *recordingStore) Save(_ context.Context, creds *configstore.AppCredentials) error {
	s.saved = creds
	return nil
}

func TestStoreSavesTheAppsNames(t *testing.T) {
	inner := &recordingStore{}
	creds := &configstore.AppCredentials{AppID: 42, PrivateKey: "-----BEGIN-----\nabc\n-----END-----\n", WebhookSecret: "whsec"}
	if err := (Store{Store: inner}).Save(context.Background(), creds); err != nil {
		t.Fatal(err)
	}

	got := inner.saved.CustomFields
	if got[EnvAppID] != "42" || got[EnvWebhookSecret] != "whsec" {
		t.Errorf("custom fields = %v", got)
	}
	if strings.Contains(got[EnvPrivateKey], "\n") || !strings.Contains(got[EnvPrivateKey], `\n`) {
		t.Errorf("private key is not on one line: %q", got[EnvPrivateKey])
	}
}
