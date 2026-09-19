package commands

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func newConfigOptionTestHost(sessionID, model string, supported ...string) (*acpSessionHost, *ChatSession) {
	chat := &ChatSession{
		NoInteractive: true,
		Model:         model,
		ProviderName:  "test-provider",
		Provider: config.Provider{
			DefaultModel:    model,
			SupportedModels: append([]string(nil), supported...),
		},
	}
	host := &acpSessionHost{sess: map[string]*acpHostSession{}}
	host.sess[sessionID] = &acpHostSession{id: sessionID, chat: chat}
	return host, chat
}

func configOptionTestValue(t *testing.T, raw string) acp.SessionConfigOptionValue {
	t.Helper()
	var value acp.SessionConfigOptionValue
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		t.Fatalf("unmarshal value %s: %v", raw, err)
	}
	return value
}

// withoutModeConfigOption drops the permission-mode option so the model and
// provider assertions keep their historical positions. Mode-option behavior is
// not asserted here; its coverage is tracked in the ACP capability plan (§11-B2).
func withoutModeConfigOption(options []acp.SessionConfigOption) []acp.SessionConfigOption {
	filtered := make([]acp.SessionConfigOption, 0, len(options))
	for _, option := range options {
		if option.ID == acpModeConfigOptionID {
			continue
		}
		filtered = append(filtered, option)
	}
	return filtered
}

func findModeConfigOption(t *testing.T, options []acp.SessionConfigOption) acp.SessionConfigOption {
	t.Helper()
	for _, option := range options {
		if option.ID == acpModeConfigOptionID {
			return option
		}
	}
	t.Fatalf("mode config option missing from %+v", options)
	return acp.SessionConfigOption{}
}

func TestACPConfigOptionsForChat_NilSession(t *testing.T) {
	if options := acpConfigOptionsForChat(nil); options != nil {
		t.Fatalf("options = %+v, want nil", options)
	}
}

func TestACPHostSessionConfigOptions_ModelShape(t *testing.T) {
	host, _ := newConfigOptionTestHost("sess-1", "m1", "m1", "m2", "m3")
	options, err := host.SessionConfigOptions(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("SessionConfigOptions failed: %v", err)
	}
	options = withoutModeConfigOption(options)
	if len(options) != 1 {
		t.Fatalf("options = %+v, want exactly the model option", options)
	}
	option := options[0]
	if option.ID != acpModelConfigOptionID {
		t.Fatalf("id = %q, want %q", option.ID, acpModelConfigOptionID)
	}
	if option.Category != acp.SessionConfigOptionCategoryModel {
		t.Fatalf("category = %q, want %q", option.Category, acp.SessionConfigOptionCategoryModel)
	}
	if option.Type != acp.SessionConfigOptionTypeSelect {
		t.Fatalf("type = %q, want %q", option.Type, acp.SessionConfigOptionTypeSelect)
	}
	if option.CurrentValue != "m1" {
		t.Fatalf("currentValue = %q, want m1", option.CurrentValue)
	}
	if len(option.Options) != 3 {
		t.Fatalf("options = %+v, want 3 choices", option.Options)
	}
	// Current model is advertised first so clients preselect it.
	if option.Options[0].Value != "m1" {
		t.Fatalf("first choice = %q, want current model m1", option.Options[0].Value)
	}
}

func TestACPHostSessionConfigOptions_UnknownSession(t *testing.T) {
	host, _ := newConfigOptionTestHost("sess-1", "m1", "m1")
	if _, err := host.SessionConfigOptions(context.Background(), "missing"); err == nil {
		t.Fatal("expected error for unknown session")
	}
}

func TestACPHostSetSessionConfigOption_SwitchesModel(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1", "m2")
	resp, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpModelConfigOptionID,
		Value:     configOptionTestValue(t, `"m2"`),
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption failed: %v", err)
	}
	if chat.Model != "m2" {
		t.Fatalf("session model = %q, want m2", chat.Model)
	}
	resp.ConfigOptions = withoutModeConfigOption(resp.ConfigOptions)
	if len(resp.ConfigOptions) != 1 {
		t.Fatalf("response configOptions = %+v", resp.ConfigOptions)
	}
	if resp.ConfigOptions[0].CurrentValue != "m2" {
		t.Fatalf("currentValue = %q, want m2", resp.ConfigOptions[0].CurrentValue)
	}
}

func TestACPHostSetSessionConfigOption_RejectsUnavailableModel(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1", "m2")
	_, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpModelConfigOptionID,
		Value:     configOptionTestValue(t, `"m9"`),
	})
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want unavailable-model error", err)
	}
	if chat.Model != "m1" {
		t.Fatalf("session model changed to %q on rejected switch", chat.Model)
	}
}

func TestACPHostSetSessionConfigOption_ValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		req  acp.SetSessionConfigOptionRequest
	}{
		{
			name: "unknown config id",
			req: acp.SetSessionConfigOptionRequest{
				SessionID: "sess-1",
				ConfigID:  "temperature",
				Value:     configOptionTestValue(t, `"high"`),
			},
		},
		{
			name: "unknown session",
			req: acp.SetSessionConfigOptionRequest{
				SessionID: "missing",
				ConfigID:  acpModelConfigOptionID,
				Value:     configOptionTestValue(t, `"m2"`),
			},
		},
		{
			name: "non-string value",
			req: acp.SetSessionConfigOptionRequest{
				SessionID: "sess-1",
				ConfigID:  acpModelConfigOptionID,
				Value:     configOptionTestValue(t, `true`),
			},
		},
		{
			name: "empty value",
			req: acp.SetSessionConfigOptionRequest{
				SessionID: "sess-1",
				ConfigID:  acpModelConfigOptionID,
				Value:     configOptionTestValue(t, `"  "`),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, chat := newConfigOptionTestHost("sess-1", "m1", "m1", "m2")
			if _, err := host.SetSessionConfigOption(context.Background(), tc.req); err == nil {
				t.Fatal("expected error")
			}
			if chat.Model != "m1" {
				t.Fatalf("session model changed to %q", chat.Model)
			}
		})
	}
}

func TestACPHostSetSessionConfigOption_RejectsWhilePrompting(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1", "m2")
	hostSess := host.sess["sess-1"]
	hostSess.mu.Lock()
	hostSess.prompting = true
	hostSess.mu.Unlock()
	defer func() {
		hostSess.mu.Lock()
		hostSess.prompting = false
		hostSess.mu.Unlock()
	}()

	_, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpModelConfigOptionID,
		Value:     configOptionTestValue(t, `"m2"`),
	})
	if err == nil || !strings.Contains(err.Error(), "in-flight prompt") {
		t.Fatalf("err = %v, want in-flight prompt error", err)
	}
	if chat.Model != "m1" {
		t.Fatalf("session model changed to %q during prompt", chat.Model)
	}
}

// The approval mode is evaluated per tool call, so it may be switched while a
// prompt is in flight; the response must already advertise the new value so the
// client picker does not wait for the turn to finish.
func TestACPHostSetSessionConfigOption_AllowsModeWhilePrompting(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1", "m2")
	hostSess := host.sess["sess-1"]
	hostSess.mu.Lock()
	hostSess.prompting = true
	hostSess.mu.Unlock()
	defer func() {
		hostSess.mu.Lock()
		hostSess.prompting = false
		hostSess.mu.Unlock()
	}()

	resp, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpModeConfigOptionID,
		Value:     configOptionTestValue(t, `"bypass_permissions"`),
	})
	if err != nil {
		t.Fatalf("in-flight mode switch failed: %v", err)
	}
	if chat.PermissionMode != runtimepolicy.ModeBypassPermissions {
		t.Fatalf("session permission mode = %q, want %q", chat.PermissionMode, runtimepolicy.ModeBypassPermissions)
	}
	if got := findModeConfigOption(t, resp.ConfigOptions).CurrentValue; got != string(runtimepolicy.ModeBypassPermissions) {
		t.Fatalf("mode option currentValue = %q, want %q", got, runtimepolicy.ModeBypassPermissions)
	}
}

// plan is a durable lifecycle with its own artifact/exit flow, so entering it
// mid-turn keeps the retry-after-completion contract.
func TestACPHostSetSessionConfigOption_RejectsPlanModeWhilePrompting(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1")
	hostSess := host.sess["sess-1"]
	hostSess.mu.Lock()
	hostSess.prompting = true
	hostSess.mu.Unlock()
	defer func() {
		hostSess.mu.Lock()
		hostSess.prompting = false
		hostSess.mu.Unlock()
	}()

	_, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpModeConfigOptionID,
		Value:     configOptionTestValue(t, `"plan"`),
	})
	if err == nil || !strings.Contains(err.Error(), "in flight") {
		t.Fatalf("err = %v, want plan-in-flight error", err)
	}
	if chat.PermissionMode == runtimepolicy.ModePlan {
		t.Fatal("session entered plan mode while a prompt was in flight")
	}
}

// The legacy session/set_mode extension shares the mode switch semantics of the
// "mode" config option, including the in-flight allowance.
func TestACPHostSetSessionMode_AllowsModeWhilePrompting(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1")
	hostSess := host.sess["sess-1"]
	hostSess.mu.Lock()
	hostSess.prompting = true
	hostSess.mu.Unlock()
	defer func() {
		hostSess.mu.Lock()
		hostSess.prompting = false
		hostSess.mu.Unlock()
	}()

	if _, err := host.SetSessionMode(context.Background(), acp.SetSessionModeRequest{
		SessionID: "sess-1",
		ModeID:    string(runtimepolicy.ModeBypassPermissions),
	}); err != nil {
		t.Fatalf("in-flight legacy mode switch failed: %v", err)
	}
	if chat.PermissionMode != runtimepolicy.ModeBypassPermissions {
		t.Fatalf("session permission mode = %q, want %q", chat.PermissionMode, runtimepolicy.ModeBypassPermissions)
	}
}

func newProviderConfigOptionTestHost(sessionID, provider, model string) (*acpSessionHost, *ChatSession) {
	cfg := &config.Config{
		ConfigFilePath: "mem://acp-provider-option-test.yaml",
		Providers: config.ProvidersConfig{
			DefaultProvider: "alpha",
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:         true,
					Protocol:        "openai",
					BaseURL:         "https://alpha.example.com",
					DefaultModel:    "alpha-model",
					SupportedModels: []string{"alpha-model", "alpha-mini"},
				},
				"beta": {
					Enabled:         true,
					Protocol:        "codex",
					BaseURL:         "https://beta.example.com",
					DefaultModel:    "beta-model",
					SupportedModels: []string{"beta-model"},
				},
				"gamma": {
					Enabled:      false,
					Protocol:     "anthropic",
					DefaultModel: "gamma-model",
				},
			},
		},
	}
	chat := &ChatSession{
		NoInteractive: true,
		Config:        cfg,
		ProviderName:  provider,
		Model:         model,
		Provider:      cfg.Providers.Items[provider],
	}
	host := &acpSessionHost{sess: map[string]*acpHostSession{}}
	host.sess[sessionID] = &acpHostSession{id: sessionID, chat: chat}
	return host, chat
}

func TestACPConfigOptionsForChat_IncludesProviderOption(t *testing.T) {
	host, _ := newProviderConfigOptionTestHost("sess-1", "alpha", "alpha-model")
	options, err := host.SessionConfigOptions(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("SessionConfigOptions failed: %v", err)
	}
	options = withoutModeConfigOption(options)
	if len(options) != 2 {
		t.Fatalf("options = %+v, want model + provider", options)
	}
	if options[0].ID != acpModelConfigOptionID {
		t.Fatalf("first option id = %q, want model first", options[0].ID)
	}
	providerOption := options[1]
	if providerOption.ID != acpProviderConfigOptionID {
		t.Fatalf("provider id = %q, want %q", providerOption.ID, acpProviderConfigOptionID)
	}
	if providerOption.Category != acp.SessionConfigOptionCategoryProvider {
		t.Fatalf("provider category = %q, want %q", providerOption.Category, acp.SessionConfigOptionCategoryProvider)
	}
	if providerOption.CurrentValue != "alpha" {
		t.Fatalf("provider currentValue = %q, want alpha", providerOption.CurrentValue)
	}
	// Only enabled providers are offered; the current one stays first.
	if len(providerOption.Options) != 2 {
		t.Fatalf("provider choices = %+v, want alpha + beta", providerOption.Options)
	}
	if providerOption.Options[0].Value != "alpha" || providerOption.Options[1].Value != "beta" {
		t.Fatalf("provider choices = %+v, want [alpha beta]", providerOption.Options)
	}
}

func TestACPConfigOptionsForChat_ProviderOptionHiddenWithoutChoice(t *testing.T) {
	host, chat := newProviderConfigOptionTestHost("sess-1", "alpha", "alpha-model")
	delete(chat.Config.Providers.Items, "beta")
	options, err := host.SessionConfigOptions(context.Background(), "sess-1")
	if err != nil {
		t.Fatalf("SessionConfigOptions failed: %v", err)
	}
	options = withoutModeConfigOption(options)
	if len(options) != 1 || options[0].ID != acpModelConfigOptionID {
		t.Fatalf("options = %+v, want only the model option", options)
	}
}

func TestACPHostSetSessionConfigOption_SwitchesProvider(t *testing.T) {
	host, chat := newProviderConfigOptionTestHost("sess-1", "alpha", "alpha-model")
	resp, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpProviderConfigOptionID,
		Value:     configOptionTestValue(t, `"beta"`),
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption failed: %v", err)
	}
	if chat.ProviderName != "beta" {
		t.Fatalf("session provider = %q, want beta", chat.ProviderName)
	}
	if chat.Provider.DefaultModel != "beta-model" {
		t.Fatalf("session provider struct not swapped: %+v", chat.Provider)
	}
	if chat.Model != "beta-model" {
		t.Fatalf("session model = %q, want the beta default model", chat.Model)
	}
	if chat.RequestedProvider != "beta" || chat.RequestedModel != "beta-model" {
		t.Fatalf("requested state = %q/%q, want beta/beta-model", chat.RequestedProvider, chat.RequestedModel)
	}
	resp.ConfigOptions = withoutModeConfigOption(resp.ConfigOptions)
	if len(resp.ConfigOptions) != 2 {
		t.Fatalf("response configOptions = %+v", resp.ConfigOptions)
	}
	providerOption, modelOption := resp.ConfigOptions[1], resp.ConfigOptions[0]
	if providerOption.CurrentValue != "beta" {
		t.Fatalf("provider currentValue = %q, want beta", providerOption.CurrentValue)
	}
	if providerOption.Options[0].Value != "beta" {
		t.Fatalf("provider choices = %+v, want beta first", providerOption.Options)
	}
	if modelOption.CurrentValue != "beta-model" {
		t.Fatalf("model currentValue = %q, want beta-model", modelOption.CurrentValue)
	}
}

func TestACPHostSetSessionConfigOption_RejectsUnavailableProvider(t *testing.T) {
	for _, provider := range []string{"gamma", "delta"} {
		t.Run(provider, func(t *testing.T) {
			host, chat := newProviderConfigOptionTestHost("sess-1", "alpha", "alpha-model")
			_, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
				SessionID: "sess-1",
				ConfigID:  acpProviderConfigOptionID,
				Value:     configOptionTestValue(t, `"`+provider+`"`),
			})
			if err == nil || !strings.Contains(err.Error(), "not available") {
				t.Fatalf("err = %v, want unavailable-provider error", err)
			}
			if chat.ProviderName != "alpha" {
				t.Fatalf("session provider changed to %q on rejected switch", chat.ProviderName)
			}
		})
	}
}

func TestACPHostSetSessionConfigOption_CurrentProviderIsNoop(t *testing.T) {
	host, chat := newProviderConfigOptionTestHost("sess-1", "alpha", "alpha-model")
	resp, err := host.SetSessionConfigOption(context.Background(), acp.SetSessionConfigOptionRequest{
		SessionID: "sess-1",
		ConfigID:  acpProviderConfigOptionID,
		Value:     configOptionTestValue(t, `"alpha"`),
	})
	if err != nil {
		t.Fatalf("SetSessionConfigOption failed: %v", err)
	}
	if chat.ProviderName != "alpha" || chat.Model != "alpha-model" {
		t.Fatalf("no-op switch changed session: %q/%q", chat.ProviderName, chat.Model)
	}
	resp.ConfigOptions = withoutModeConfigOption(resp.ConfigOptions)
	if len(resp.ConfigOptions) != 2 || resp.ConfigOptions[1].CurrentValue != "alpha" {
		t.Fatalf("response configOptions = %+v", resp.ConfigOptions)
	}
}

// TestACPModeOptionAllowed_AgreesWithPolicyParser pins the mode validator used
// by the ACP "mode" config option and the legacy session/set_mode extension to
// the runtime policy parser. The documented contract is that unknown values are
// rejected rather than silently falling back to default, so a typo in a client
// cannot weaken the effective policy.
func TestACPModeOptionAllowed_AgreesWithPolicyParser(t *testing.T) {
	candidates := []string{
		string(runtimepolicy.ModeBypassPermissions),
		"default",
		"acceptEdits",
		"accept_edits",
		"plan",
		"bypassPermissions",
		"",
		"   ",
		"bogus",
		"DEFAULT",
		"yolo",
	}
	for _, candidate := range candidates {
		_, want := runtimepolicy.ParseMode(candidate)
		if got := acpModeOptionAllowed(candidate); got != want {
			t.Fatalf("acpModeOptionAllowed(%q) = %v, want %v (policy parser agreement)", candidate, got, want)
		}
	}
}

// TestACPModeOptionAllowed_RejectsUnknownModeIDs guards the weakening path
// directly: an unsupported mode id must never be accepted.
func TestACPModeOptionAllowed_RejectsUnknownModeIDs(t *testing.T) {
	for _, modeID := range []string{"", "bogus", "yolo", "sess-1"} {
		if _, known := runtimepolicy.ParseMode(modeID); known {
			// Skip values that the policy layer itself recognises; the point of
			// this test is only the negative case.
			continue
		}
		if acpModeOptionAllowed(modeID) {
			t.Fatalf("acpModeOptionAllowed(%q) = true, want false for an unknown mode", modeID)
		}
	}
}

// TestACPModeOptionAllowed_AcceptsEveryCanonicalModeID walks the canonical mode
// ids advertised by the mode config option and asserts each one validates.
func TestACPModeOptionAllowed_AcceptsEveryCanonicalModeID(t *testing.T) {
	canonical := []string{
		"default",
		"acceptEdits",
		"plan",
		string(runtimepolicy.ModeBypassPermissions),
	}
	accepted := 0
	for _, modeID := range canonical {
		if _, known := runtimepolicy.ParseMode(modeID); !known {
			continue
		}
		if !acpModeOptionAllowed(modeID) {
			t.Fatalf("acpModeOptionAllowed(%q) = false, want true for a canonical mode", modeID)
		}
		accepted++
	}
	if accepted == 0 {
		t.Fatal("no canonical mode id validated; mode vocabulary drifted from the policy package")
	}
}
