package commands

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	// acpModelConfigOptionID is the stable id of the model select option. Zed
	// keys per-agent defaults (agent_servers.*.default_config_options) by this
	// id and uses category=model to attach the "Change Model" picker and
	// keybindings.
	acpModelConfigOptionID = "model"
	// acpThoughtLevelConfigOptionID is the stable id of the reasoning-effort
	// select option. The id mirrors the ACP spec category name so clients that
	// key per-agent defaults by id line up with the category-driven picker
	// (Zed renders it as "Change Thinking Effort").
	acpThoughtLevelConfigOptionID = "thought_level"
	// acpProviderConfigOptionID is the stable id of the provider select option.
	// It uses the custom category "_provider": ACP reserves categories starting
	// with "_" for extensions, so spec-compliant clients render it as a plain
	// select without needing to know the name.
	acpProviderConfigOptionID = "provider"
)

// acpConfigOptionsForChat projects the chat session state into ACP config
// options: the permission-mode selector (always available), the model
// selector, the reasoning-effort selector when the model declares efforts,
// plus a provider selector when the session config exposes more than one
// enabled provider. It returns nil when none is known so clients do not render
// empty pickers.
func acpConfigOptionsForChat(chatSession *ChatSession) []acp.SessionConfigOption {
	if chatSession == nil {
		return nil
	}
	options := make([]acp.SessionConfigOption, 0, 4)
	if modeOption, ok := acpModeConfigOption(chatSession); ok {
		options = append(options, modeOption)
	}
	if modelOption, ok := acpModelConfigOption(chatSession); ok {
		options = append(options, modelOption)
	}
	if thoughtLevelOption, ok := acpThoughtLevelConfigOption(chatSession); ok {
		options = append(options, thoughtLevelOption)
	}
	if providerOption, ok := acpProviderConfigOption(chatSession); ok {
		options = append(options, providerOption)
	}
	if len(options) == 0 {
		return nil
	}
	return options
}

func acpModelConfigOption(chatSession *ChatSession) (acp.SessionConfigOption, bool) {
	current := strings.TrimSpace(effectiveRuntimeModel(chatSession))
	models := runtimeModelSelectionOptions(chatSession)
	if current == "" && len(models) == 0 {
		return acp.SessionConfigOption{}, false
	}

	options := make([]acp.SessionConfigSelectOption, 0, len(models)+1)
	seen := make(map[string]struct{}, len(models)+1)
	add := func(model string) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		options = append(options, acp.SessionConfigSelectOption{Value: model, Name: model})
	}
	// Current model first: clients preselect it and it stays selectable even
	// if the catalog is later trimmed.
	add(current)
	for _, model := range models {
		add(model)
	}

	return acp.SessionConfigOption{
		ID:           acpModelConfigOptionID,
		Name:         "Model",
		Description:  "Model used for subsequent turns in this session.",
		Category:     acp.SessionConfigOptionCategoryModel,
		Type:         acp.SessionConfigOptionTypeSelect,
		CurrentValue: current,
		Options:      options,
	}, true
}

// acpThoughtLevelConfigOption projects the current model's reasoning-effort
// catalog into a spec-defined "thought_level" select option. It is only
// advertised when the catalog is known (provider model_capabilities or a
// provider fallback declare reasoning_efforts): with an empty catalog clients
// would have to guess values the runtime cannot validate.
//
// The synthetic "default" choice clears the session override so the provider
// default applies. ACP requires currentValue to be one of the advertised
// values, so the "unset" state needs a real value id.
func acpThoughtLevelConfigOption(chatSession *ChatSession) (acp.SessionConfigOption, bool) {
	if chatSession == nil {
		return acp.SessionConfigOption{}, false
	}
	catalog := reasoningEffortCatalogForModel(chatSession.Provider, effectiveRuntimeModel(chatSession))
	if !catalog.supported || len(catalog.options) == 0 {
		return acp.SessionConfigOption{}, false
	}

	options := make([]acp.SessionConfigSelectOption, 0, len(catalog.options)+2)
	seen := make(map[string]struct{}, len(catalog.options)+2)
	add := func(value, description string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		options = append(options, acp.SessionConfigSelectOption{
			Value:       value,
			Name:        value,
			Description: description,
		})
	}

	current := runtimetypes.NormalizeReasoningEffort(chatSession.ReasoningEffort)
	// Current value first: clients preselect it, and a value that survived a
	// model/provider switch stays selectable even when the new catalog does not
	// list it.
	add(current, "")
	for _, option := range catalog.options {
		add(option, "")
	}
	add(acpReasoningEffortDefaultValue, "Use the provider default reasoning effort.")
	if current == "" {
		current = acpReasoningEffortDefaultValue
	}

	return acp.SessionConfigOption{
		ID:           acpThoughtLevelConfigOptionID,
		Name:         "Thinking Effort",
		Description:  "Reasoning effort used for subsequent turns in this session.",
		Category:     acp.SessionConfigOptionCategoryThoughtLevel,
		Type:         acp.SessionConfigOptionTypeSelect,
		CurrentValue: current,
		Options:      options,
	}, true
}

// acpProviderOptionValues returns the selectable provider names, current
// provider first, or nil when the session exposes no real choice (no config,
// or fewer than two enabled providers). Only enabled providers are advertised
// as choices; the current provider is always kept selectable.
func acpProviderOptionValues(chatSession *ChatSession) []string {
	if chatSession == nil || chatSession.Config == nil {
		return nil
	}
	values := make([]string, 0, len(chatSession.Config.Providers.Items)+1)
	seen := make(map[string]struct{}, len(chatSession.Config.Providers.Items)+1)
	add := func(provider string) {
		provider = strings.TrimSpace(provider)
		if provider == "" {
			return
		}
		key := strings.ToLower(provider)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		values = append(values, provider)
	}

	add(acpCurrentProvider(chatSession))
	for _, name := range listEnabledProviderNames(chatSession.Config) {
		add(name)
	}
	if len(values) < 2 {
		return nil
	}
	return values
}

func acpProviderConfigOption(chatSession *ChatSession) (acp.SessionConfigOption, bool) {
	values := acpProviderOptionValues(chatSession)
	if len(values) < 2 {
		return acp.SessionConfigOption{}, false
	}
	options := make([]acp.SessionConfigSelectOption, 0, len(values))
	for _, name := range values {
		options = append(options, acp.SessionConfigSelectOption{Value: name, Name: name})
	}
	return acp.SessionConfigOption{
		ID:           acpProviderConfigOptionID,
		Name:         "Provider",
		Description:  "Provider used for subsequent turns in this session.",
		Category:     acp.SessionConfigOptionCategoryProvider,
		Type:         acp.SessionConfigOptionTypeSelect,
		CurrentValue: values[0],
		Options:      options,
	}, true
}

// acpCurrentProvider returns the provider currently bound to the session,
// falling back to the configured default so the option keeps a valid
// currentValue before the first turn resolves one.
func acpCurrentProvider(chatSession *ChatSession) string {
	if chatSession == nil {
		return ""
	}
	if provider := strings.TrimSpace(chatSession.ProviderName); provider != "" {
		return provider
	}
	if chatSession.Config != nil {
		return strings.TrimSpace(chatSession.Config.Providers.DefaultProvider)
	}
	return ""
}

// acpProviderOptionAllowed reports whether providerID can be selected in this
// session: either an enabled provider from the config, or the provider the
// session already runs on (a harmless no-op).
func acpProviderOptionAllowed(chatSession *ChatSession, providerID string) bool {
	if chatSession == nil || chatSession.Config == nil {
		return false
	}
	if strings.EqualFold(acpCurrentProvider(chatSession), providerID) {
		return true
	}
	for _, name := range listEnabledProviderNames(chatSession.Config) {
		if strings.EqualFold(name, providerID) {
			return true
		}
	}
	return false
}

// acpModelOptionAllowed reports whether modelID is part of the session catalog.
// An empty catalog (no provider models known) accepts any non-empty value.
func acpModelOptionAllowed(chatSession *ChatSession, modelID string) bool {
	if chatSession == nil {
		return false
	}
	allowed := runtimeModelSelectionOptions(chatSession)
	if len(allowed) == 0 {
		return true
	}
	for _, candidate := range allowed {
		if strings.EqualFold(strings.TrimSpace(candidate), modelID) {
			return true
		}
	}
	return false
}

// acpThoughtLevelOptionAllowed reports whether value can be selected as the
// session reasoning effort: the synthetic default (clear the override), an
// entry of the model's declared catalog, or the value already in effect (a
// harmless no-op). An empty catalog accepts any non-empty value, mirroring
// acpModelOptionAllowed's tolerance for providers without declared catalogs.
func acpThoughtLevelOptionAllowed(chatSession *ChatSession, value string) bool {
	if chatSession == nil {
		return false
	}
	normalized := runtimetypes.NormalizeReasoningEffort(value)
	if normalized == "" {
		return false
	}
	if strings.EqualFold(normalized, acpReasoningEffortDefaultValue) {
		return true
	}
	if strings.EqualFold(runtimetypes.NormalizeReasoningEffort(chatSession.ReasoningEffort), normalized) {
		return true
	}
	catalog := reasoningEffortCatalogForModel(chatSession.Provider, effectiveRuntimeModel(chatSession))
	if len(catalog.options) == 0 {
		return true
	}
	for _, option := range catalog.options {
		if strings.EqualFold(option, normalized) {
			return true
		}
	}
	return false
}

// SessionConfigOptions implements acp.SessionConfigOptionProvider for
// session/load: session/new carries options inline, load has no payload of its
// own so the ACP server asks for them after the history replay.
func (h *acpSessionHost) SessionConfigOptions(ctx context.Context, sessionID string) ([]acp.SessionConfigOption, error) {
	_ = ctx
	if h == nil {
		return nil, fmt.Errorf("acp host is nil")
	}
	h.mu.Lock()
	hostSess := h.sess[strings.TrimSpace(sessionID)]
	h.mu.Unlock()
	if hostSess == nil || hostSess.chat == nil {
		return nil, fmt.Errorf("unknown sessionId %q", sessionID)
	}
	return acpConfigOptionsForChat(hostSess.chat), nil
}

// SetSessionConfigOption implements acp.SessionConfigOptionSetter for
// session/set_config_option. Model, thinking-effort and provider switches
// reuse the same runtime paths as the interactive /model, /reasoning_effort
// and /provider commands and take effect on the next turn; the response
// carries the full option set so the client can refresh its pickers.
func (h *acpSessionHost) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	_ = ctx
	var resp acp.SetSessionConfigOptionResponse
	if h == nil {
		return resp, fmt.Errorf("acp host is nil")
	}
	configID := strings.TrimSpace(req.ConfigID)
	valueID, ok := req.Value.ValueID()
	if !ok {
		return resp, acp.InvalidParams(fmt.Errorf("config option %q expects a value_id string", configID))
	}
	valueID = strings.TrimSpace(valueID)
	if valueID == "" {
		return resp, acp.InvalidParams(fmt.Errorf("config option %q value is empty", configID))
	}

	h.mu.Lock()
	hostSess := h.sess[strings.TrimSpace(req.SessionID)]
	h.mu.Unlock()
	if hostSess == nil || hostSess.chat == nil {
		return resp, acp.InvalidParams(fmt.Errorf("unknown sessionId %q", req.SessionID))
	}

	// Serialize against Prompt. Model, thinking-effort and provider switches
	// race with the running turn reading session fields, so those keep the
	// fail-fast retry contract. The permission mode is evaluated per tool call,
	// so it may be switched mid-turn: the running turn observes it from its
	// next tool evaluation through the live mode source attached at submit
	// time.
	hostSess.mu.Lock()
	defer hostSess.mu.Unlock()
	prompting := hostSess.prompting
	if prompting && !strings.EqualFold(configID, acpModeConfigOptionID) {
		return resp, fmt.Errorf("session %q has an in-flight prompt; retry after it completes", req.SessionID)
	}

	switch {
	case strings.EqualFold(configID, acpModeConfigOptionID):
		if !acpModeOptionAllowed(valueID) {
			return resp, acp.InvalidParams(fmt.Errorf("permission mode %q is not supported", valueID))
		}
		if prompting {
			if _, err := applyRuntimePermissionModeSwitchInFlight(hostSess.chat, valueID); err != nil {
				return resp, err
			}
		} else if err := applyACPConfigOptionSwitch(hostSess.chat, configID, valueID); err != nil {
			return resp, err
		}
		// The response carries the option set, but a client whose pickers are
		// open needs the out-of-band refresh on both channels (config option +
		// legacy modes) to stay in sync while the turn is still streaming.
		h.broadcastACPModeChange(strings.TrimSpace(req.SessionID), hostSess.chat)
	default:
		if err := applyACPConfigOptionSwitch(hostSess.chat, configID, valueID); err != nil {
			return resp, err
		}
	}
	resp.ConfigOptions = acpConfigOptionsForChat(hostSess.chat)
	persistACPConfigOptionPreferences(hostSess.chat)
	return resp, nil
}

// applyACPConfigOptionSwitch validates one config option and performs its
// runtime switch. It deliberately does not gate on an in-flight prompt: callers
// own that decision (session/set_config_option rejects mid-turn model/provider
// switches, while an ACP slash command is itself the running turn). Callers also
// own the post-change broadcasts and preference persistence.
func applyACPConfigOptionSwitch(chat *ChatSession, configID, valueID string) error {
	if chat == nil {
		return fmt.Errorf("no active session")
	}
	switch {
	case strings.EqualFold(configID, acpModeConfigOptionID):
		if !acpModeOptionAllowed(valueID) {
			return acp.InvalidParams(fmt.Errorf("permission mode %q is not supported", valueID))
		}
		_, err := applyRuntimePermissionModeSwitch(chat, valueID)
		return err
	case strings.EqualFold(configID, acpModelConfigOptionID):
		if !acpModelOptionAllowed(chat, valueID) {
			return acp.InvalidParams(fmt.Errorf("model %q is not available for this session", valueID))
		}
		_, err := applyRuntimeModelSwitch(chat, valueID, false)
		return err
	case strings.EqualFold(configID, acpThoughtLevelConfigOptionID):
		if !acpThoughtLevelOptionAllowed(chat, valueID) {
			return acp.InvalidParams(fmt.Errorf("reasoning effort %q is not available for this session", valueID))
		}
		_, err := applyRuntimeReasoningEffortSwitch(chat, valueID)
		return err
	case strings.EqualFold(configID, acpProviderConfigOptionID):
		if !acpProviderOptionAllowed(chat, valueID) {
			return acp.InvalidParams(fmt.Errorf("provider %q is not available for this session", valueID))
		}
		// Re-selecting the current provider is a no-op; skip re-resolution so a
		// disabled-but-current provider still yields a usable response.
		if strings.EqualFold(acpCurrentProvider(chat), valueID) {
			return nil
		}
		_, err := applyRuntimeProviderSwitch(chat, valueID)
		return err
	default:
		return acp.InvalidParams(fmt.Errorf("unknown configId %q", configID))
	}
}

// persistACPConfigOptionPreferences writes provider/model/reasoning-effort
// changes made through the ACP "session/set_config_option" handler into the
// workspace-scoped chat preferences file (chat-prefs.yaml).
//
// Without this the switch only mutates the in-memory ChatSession, so the very
// next session (new session/new request, same cwd) is built from the unchanged
// persisted defaults and clients such as Zed show the selector reset.
func persistACPConfigOptionPreferences(session *ChatSession) {
	if session == nil || session.Config == nil {
		return
	}
	if err := persistChatPreferences(session.Config, session.ProviderName, session.Model, session.ReasoningEffort); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: 保存 ACP 配置选项偏好失败: %v\n", err)
	}
}
