package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

func newThoughtLevelTestHost(sessionID, model string, efforts []string) (*acpSessionHost, *ChatSession) {
	chat := &ChatSession{
		NoInteractive: true,
		Model:         model,
		ProviderName:  "test-provider",
		Provider: config.Provider{
			DefaultModel: model,
			ModelCapabilities: map[string]config.ModelCapabilitySpec{
				model: {ReasoningModel: true, ReasoningEfforts: append([]string(nil), efforts...)},
			},
		},
	}
	host := &acpSessionHost{sess: map[string]*acpHostSession{}}
	host.sess[sessionID] = &acpHostSession{id: sessionID, chat: chat}
	return host, chat
}

func thoughtLevelOptionFrom(t *testing.T, options []acp.SessionConfigOption) acp.SessionConfigOption {
	t.Helper()
	for _, option := range options {
		if option.ID == acpThoughtLevelConfigOptionID {
			return option
		}
	}
	t.Fatalf("thought_level option missing from %+v", options)
	return acp.SessionConfigOption{}
}

func optionValues(option acp.SessionConfigOption) []string {
	values := make([]string, 0, len(option.Options))
	for _, choice := range option.Options {
		values = append(values, choice.Value)
	}
	return values
}

func containsOptionValue(option acp.SessionConfigOption, value string) bool {
	for _, choice := range option.Options {
		if strings.EqualFold(choice.Value, value) {
			return true
		}
	}
	return false
}

func TestACPThoughtLevelOption_AdvertisedForKnownCatalog(t *testing.T) {
	host, _ := newThoughtLevelTestHost("sess-1", "m1", []string{"low", "medium", "high"})
	options, err := host.SessionConfigOptions(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("SessionConfigOptions failed: %v", err)
	}
	option := thoughtLevelOptionFrom(t, options)
	if option.Category != acp.SessionConfigOptionCategoryThoughtLevel {
		t.Fatalf("category = %q, want %q", option.Category, acp.SessionConfigOptionCategoryThoughtLevel)
	}
	if option.Type != acp.SessionConfigOptionTypeSelect {
		t.Fatalf("type = %q, want select", option.Type)
	}
	// No session override yet: clients must see the explicit "provider default"
	// state, because ACP requires currentValue to be an advertised value.
	if option.CurrentValue != acpReasoningEffortDefaultValue {
		t.Fatalf("currentValue = %q, want %q", option.CurrentValue, acpReasoningEffortDefaultValue)
	}
	for _, want := range []string{acpReasoningEffortDefaultValue, "low", "medium", "high"} {
		if !containsOptionValue(option, want) {
			t.Fatalf("option values = %v, missing %q", optionValues(option), want)
		}
	}
}

func TestACPThoughtLevelOption_HiddenWithoutCatalog(t *testing.T) {
	host, _ := newThoughtLevelTestHost("sess-1", "m1", nil)
	options, err := host.SessionConfigOptions(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("SessionConfigOptions failed: %v", err)
	}
	for _, option := range options {
		if option.ID == acpThoughtLevelConfigOptionID {
			t.Fatalf("thought_level advertised without a declared catalog: %+v", option)
		}
	}
}

func TestACPThoughtLevelOption_KeepsStaleEffortSelectable(t *testing.T) {
	host, chat := newThoughtLevelTestHost("sess-1", "m1", []string{"low", "medium"})
	chat.ReasoningEffort = "high" // survived a model switch; new catalog lacks it
	options, err := host.SessionConfigOptions(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("SessionConfigOptions failed: %v", err)
	}
	option := thoughtLevelOptionFrom(t, options)
	if option.CurrentValue != "high" {
		t.Fatalf("currentValue = %q, want high", option.CurrentValue)
	}
	if !containsOptionValue(option, "high") || !containsOptionValue(option, acpReasoningEffortDefaultValue) {
		t.Fatalf("option values = %v, want stale value plus default", optionValues(option))
	}
}

func TestACPHostSetSessionConfigOption_SwitchesReasoningEffort(t *testing.T) {
	host, chat := newThoughtLevelTestHost("sess-1", "m1", []string{"low", "medium", "high"})
	resp, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpThoughtLevelConfigOptionID,
		Value:     configOptionTestValue(t, `"high"`),
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption failed: %v", err)
	}
	if chat.ReasoningEffort != "high" {
		t.Fatalf("session reasoning effort = %q, want high", chat.ReasoningEffort)
	}
	option := thoughtLevelOptionFrom(t, resp.ConfigOptions)
	if option.CurrentValue != "high" {
		t.Fatalf("currentValue = %q, want high", option.CurrentValue)
	}
	// Current value first so clients preselect it.
	if len(option.Options) == 0 || option.Options[0].Value != "high" {
		t.Fatalf("options = %v, want current effort first", optionValues(option))
	}

	// Selecting the synthetic default clears the override.
	resp, err = host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpThoughtLevelConfigOptionID,
		Value:     configOptionTestValue(t, `"default"`),
	})
	if err != nil {
		t.Fatalf("clearing reasoning effort failed: %v", err)
	}
	if chat.ReasoningEffort != "" {
		t.Fatalf("session reasoning effort = %q, want cleared", chat.ReasoningEffort)
	}
	if got := thoughtLevelOptionFrom(t, resp.ConfigOptions).CurrentValue; got != acpReasoningEffortDefaultValue {
		t.Fatalf("currentValue = %q, want %q", got, acpReasoningEffortDefaultValue)
	}
}

func TestACPHostSetSessionConfigOption_RejectsUnavailableReasoningEffort(t *testing.T) {
	host, chat := newThoughtLevelTestHost("sess-1", "m1", []string{"low", "medium"})
	_, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpThoughtLevelConfigOptionID,
		Value:     configOptionTestValue(t, `"ultra"`),
	})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want unavailable reasoning-effort error", err)
	}
	if chat.ReasoningEffort != "" {
		t.Fatalf("session reasoning effort changed to %q on rejected switch", chat.ReasoningEffort)
	}
}

// TestACPThoughtLevelSwitch_ReachesProviderRequest is the end-to-end guard for
// the switch: the value accepted by session/set_config_option must show up in
// the request config the next turn sends upstream.
func TestACPThoughtLevelSwitch_ReachesProviderRequest(t *testing.T) {
	host, chat := newThoughtLevelTestHost("sess-1", "m1", []string{"low", "medium", "high"})
	chat.Provider.Protocol = "anthropic"
	chat.Adapter = adapter.GetAdapterOrDefault("anthropic")

	if _, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpThoughtLevelConfigOptionID,
		Value:     configOptionTestValue(t, `"high"`),
	}); err != nil {
		t.Fatalf("SetSessionConfigOption failed: %v", err)
	}
	cfg := adapterRequestConfig(chat, nil, runtimechatcore.ProviderTurnRequest{Stream: false})
	if cfg.ReasoningEffort != "high" {
		t.Fatalf("provider request reasoning effort = %q, want high", cfg.ReasoningEffort)
	}

	if _, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpThoughtLevelConfigOptionID,
		Value:     configOptionTestValue(t, `"default"`),
	}); err != nil {
		t.Fatalf("clearing reasoning effort failed: %v", err)
	}
	cfg = adapterRequestConfig(chat, nil, runtimechatcore.ProviderTurnRequest{Stream: false})
	if cfg.ReasoningEffort != "" {
		t.Fatalf("provider request reasoning effort = %q, want cleared", cfg.ReasoningEffort)
	}
}
