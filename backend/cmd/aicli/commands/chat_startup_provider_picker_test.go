package commands

import (
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// TestBuildStartupProviderPickerItems_AlwaysLeadsWithCreateRow asserts the
// create/login row is first — including the fresh-install zero-provider case
// that previously dead-ended with "没有可用的 providers".
func TestBuildStartupProviderPickerItems_AlwaysLeadsWithCreateRow(t *testing.T) {
	empty := buildStartupProviderPickerItems(&config.Config{})
	if len(empty) != 1 {
		t.Fatalf("expected exactly the create row for an empty config, got %d: %+v", len(empty), empty)
	}
	if empty[0].Title != chatStartupProviderCreateRowTitle {
		t.Fatalf("expected first row to be the create row, got %q", empty[0].Title)
	}

	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.Provider{
				"alpha": {Enabled: true, Protocol: "openai", BaseURL: "https://alpha.example.com", DefaultModel: "gpt-4.1"},
			},
		},
	}
	items := buildStartupProviderPickerItems(cfg)
	if len(items) != 2 {
		t.Fatalf("expected create row + 1 provider, got %d: %+v", len(items), items)
	}
	if items[0].Title != chatStartupProviderCreateRowTitle {
		t.Fatalf("create row must stay first, got %q", items[0].Title)
	}
	if items[1].Title != "alpha" {
		t.Fatalf("expected provider row alpha, got %q", items[1].Title)
	}
}

// TestBuildStartupProviderPickerItems_PendingLoginHint asserts providers
// without resolvable credentials carry the 待登录 hint in their detail row.
func TestBuildStartupProviderPickerItems_PendingLoginHint(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Items: map[string]config.Provider{
				"no-key":   {Enabled: true, Protocol: "openai", BaseURL: "https://needs.example.com"},
				"with-key": {Enabled: true, Protocol: "openai", BaseURL: "https://ok.example.com", APIKey: "sk-test"},
			},
		},
	}
	items := buildStartupProviderPickerItems(cfg)
	var noKey, withKey *struct {
		Title      string
		Detail     string
		SearchText string
	}
	for i := range items {
		switch items[i].Title {
		case "no-key":
			noKey = &struct {
				Title      string
				Detail     string
				SearchText string
			}{Title: items[i].Title, Detail: items[i].Detail, SearchText: items[i].SearchText}
		case "with-key":
			withKey = &struct {
				Title      string
				Detail     string
				SearchText string
			}{Title: items[i].Title, Detail: items[i].Detail, SearchText: items[i].SearchText}
		}
	}
	if noKey == nil || !strings.Contains(noKey.Detail, "待登录") {
		t.Fatalf("expected pending-login hint on no-key, got %+v", noKey)
	}
	if withKey == nil || strings.Contains(withKey.Detail, "待登录") {
		t.Fatalf("did not expect pending-login hint on with-key, got %+v", withKey)
	}
}

// TestProviderNeedsLogin_Matrix covers the credential-resolution matrix used
// by the pending-login routing.
func TestProviderNeedsLogin_Matrix(t *testing.T) {
	cases := []struct {
		name     string
		provider config.Provider
		want     bool
	}{
		{"disabled provider never needs login", config.Provider{Enabled: false}, false},
		{"inline key ok", config.Provider{Enabled: true, APIKey: "sk-test"}, false},
		{"inline key list ok", config.Provider{Enabled: true, APIKeys: []string{"sk-a"}}, false},
		{"oauth ref ok", config.Provider{Enabled: true, AuthMode: "oauth", AuthRef: "codex"}, false},
		{"no credentials at all", config.Provider{Enabled: true, Protocol: "openai", BaseURL: "https://x"}, true},
		{"oauth mode without ref", config.Provider{Enabled: true, AuthMode: "oauth"}, true},
	}
	for _, tc := range cases {
		if got := providerNeedsLogin(tc.provider); got != tc.want {
			t.Fatalf("%s: providerNeedsLogin=%v want %v", tc.name, got, tc.want)
		}
	}
}
