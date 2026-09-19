package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

const (
	// acpModeConfigOptionID is the stable id of the permission-mode select
	// option. ACP v1 defines the "mode" category for exactly this selector;
	// the id mirrors the category name so clients that key per-agent defaults
	// by id line up with the category-driven picker.
	acpModeConfigOptionID = "mode"
)

// acpPermissionModeChoice is one selectable permission mode as advertised to
// ACP clients.
type acpPermissionModeChoice struct {
	Value       string
	Name        string
	Description string
}

// acpPermissionModeChoices returns the selectable permission modes in display
// order, from most restrictive to most permissive. Values are the canonical
// runtime permission-mode ids (the same strings /permission-mode accepts); the
// display names surface the CLI aliases users already know from the TUI, so
// "yolo" is discoverable as bypass_permissions.
func acpPermissionModeChoices() []acpPermissionModeChoice {
	return []acpPermissionModeChoice{
		{
			Value:       string(runtimepolicy.ModeDefault),
			Name:        "Default",
			Description: "Ask before writes, shell commands and network access.",
		},
		{
			Value:       string(runtimepolicy.ModeAcceptEdits),
			Name:        "Accept Edits",
			Description: "Auto-approve file edits; still ask for shell and network access.",
		},
		{
			Value:       string(runtimepolicy.ModePlan),
			Name:        "Plan",
			Description: "Investigate read-only and propose a plan before changing anything.",
		},
		{
			Value:       string(runtimepolicy.ModeBypassPermissions),
			Name:        "Bypass Permissions (yolo)",
			Description: "Auto-approve every tool call without prompting. Equivalent to --yolo.",
		},
	}
}

// acpModeIDForSession normalizes the session permission mode to one of the
// advertised values. ACP requires currentValue to be a value the agent
// actually offered, so an unset (empty) mode is reported as "default" rather
// than as an empty string.
func acpModeIDForSession(chatSession *ChatSession) string {
	if chatSession == nil {
		return string(runtimepolicy.ModeDefault)
	}
	mode, ok := runtimepolicy.ParseMode(string(chatSessionPermissionMode(chatSession)))
	if !ok {
		return string(runtimepolicy.ModeDefault)
	}
	return string(mode)
}

// acpModeConfigOption projects the session permission mode into a spec-defined
// "mode" select option. It is always advertised: unlike the model or provider
// pickers it needs no catalog, because the runtime supports a fixed mode set.
func acpModeConfigOption(chatSession *ChatSession) (acp.SessionConfigOption, bool) {
	if chatSession == nil {
		return acp.SessionConfigOption{}, false
	}
	choices := acpPermissionModeChoices()
	options := make([]acp.SessionConfigSelectOption, 0, len(choices))
	for _, choice := range choices {
		options = append(options, acp.SessionConfigSelectOption{
			Value:       choice.Value,
			Name:        choice.Name,
			Description: choice.Description,
		})
	}
	return acp.SessionConfigOption{
		ID:           acpModeConfigOptionID,
		Name:         "Mode",
		Description:  "Permission mode for subsequent turns in this session.",
		Category:     acp.SessionConfigOptionCategoryMode,
		Type:         acp.SessionConfigOptionTypeSelect,
		CurrentValue: acpModeIDForSession(chatSession),
		Options:      options,
	}, true
}

// acpModesForChat projects the permission modes onto the legacy `modes` state
// carried by session/new and session/load. ACP v1 marks the dedicated modes
// API as superseded by the "mode" config option but recommends offering both
// for backwards compatibility, so both are driven from the same choice list.
func acpModesForChat(chatSession *ChatSession) *acp.SessionModeState {
	if chatSession == nil {
		return nil
	}
	choices := acpPermissionModeChoices()
	modes := make([]acp.SessionMode, 0, len(choices))
	for _, choice := range choices {
		modes = append(modes, acp.SessionMode{
			ID:          choice.Value,
			Name:        choice.Name,
			Description: choice.Description,
		})
	}
	return &acp.SessionModeState{
		CurrentModeID:  acpModeIDForSession(chatSession),
		AvailableModes: modes,
	}
}

// acpModeOptionAllowed reports whether modeID names a permission mode the
// runtime supports. Unknown values are rejected instead of silently falling
// back to default, so a typo cannot weaken the effective policy.
func acpModeOptionAllowed(modeID string) bool {
	_, ok := runtimepolicy.ParseMode(modeID)
	return ok
}

// applyRuntimePermissionModeSwitch applies an ACP mode switch to the session.
//
// It writes the same session state as the interactive /mode, /yolo and
// /permission-mode commands, so the permission engine and the approval bridge
// observe the new mode on the next turn; the change is session-scoped and is
// never written to the global config.
//
// Unlike the interactive /yolo command there is no extra confirmation step:
// ACP has no confirmation channel of its own, and the request always comes
// from the user's own client UI, so picking bypass_permissions there is the
// user's explicit action (the same way an IDE's own "always allow" is).
//
// plan is a durable lifecycle rather than a plain mode, so switching into it
// goes through the same enter path as /plan (plan artifact + exit_plan_mode
// flow), and switching out of it closes that lifecycle instead of leaving the
// runtime denying writes behind the client's back.
func applyRuntimePermissionModeSwitch(chatSession *ChatSession, modeID string) (runtimepolicy.Mode, error) {
	if chatSession == nil {
		return runtimepolicy.ModeDefault, fmt.Errorf("no active session")
	}
	mode, ok := runtimepolicy.ParseMode(modeID)
	if !ok {
		return runtimepolicy.ModeDefault, acp.InvalidParams(fmt.Errorf("permission mode %q is not supported", modeID))
	}

	if mode == runtimepolicy.ModePlan {
		if !planmode.IsActive(loadChatPlanMode(chatSession)) {
			result, err := enterChatPlanModeWithResult(chatSession, "")
			if err != nil {
				return runtimepolicy.ModeDefault, err
			}
			if result.SyncErr != nil {
				return runtimepolicy.ModePlan, result.SyncErr
			}
		}
		return runtimepolicy.ModePlan, nil
	}

	if planmode.IsActive(loadChatPlanMode(chatSession)) {
		// ExitQuit restores the pre-plan mode; the requested mode is applied
		// right after, so the client's choice always wins.
		result, err := exitChatPlanModeWithResult(chatSession, string(planmode.ExitQuit), "")
		if err != nil {
			return runtimepolicy.ModeDefault, err
		}
		if result.SyncErr != nil {
			return mode, result.SyncErr
		}
	}

	setChatPermissionMode(chatSession, mode)
	if err := syncRuntimeSessionFromChat(chatSession); err != nil {
		return mode, err
	}
	return mode, nil
}

// applyRuntimePermissionModeSwitchInFlight applies a permission-mode switch to
// a session whose turn is already running.
//
// Unlike applyRuntimePermissionModeSwitch it never rewrites the durable session
// snapshot: mid-turn that would replace the stored history while the running
// turn is still appending to it. The session state written here is persisted by
// the same turn's end-of-turn sync and is already live for the permission
// engine, which re-reads the session mode on every tool evaluation.
//
// plan is a durable lifecycle with its own artifact and exit flow, which cannot
// be entered or left safely while a turn is in flight, so plan transitions keep
// the retry-after-completion contract.
func applyRuntimePermissionModeSwitchInFlight(chatSession *ChatSession, modeID string) (runtimepolicy.Mode, error) {
	if chatSession == nil {
		return runtimepolicy.ModeDefault, fmt.Errorf("no active session")
	}
	mode, ok := runtimepolicy.ParseMode(modeID)
	if !ok {
		return runtimepolicy.ModeDefault, acp.InvalidParams(fmt.Errorf("permission mode %q is not supported", modeID))
	}
	if mode == runtimepolicy.ModePlan || planmode.IsActive(loadChatPlanMode(chatSession)) {
		return runtimepolicy.ModeDefault, fmt.Errorf("permission mode %q cannot be applied while a prompt is in flight; retry after it completes", modeID)
	}
	setChatPermissionMode(chatSession, mode)
	return mode, nil
}

// SetSessionMode implements the legacy acp.SessionModeSetter extension for
// session/set_mode. The dedicated modes API is superseded by the "mode" config
// option, so the response carries no payload beyond the empty object ACP v1
// types for this method; clients that need the refreshed state read it from
// the current_mode_update pushed right after the switch.
func (h *acpSessionHost) SetSessionMode(ctx context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	_ = ctx
	var resp acp.SetSessionModeResponse
	if h == nil {
		return resp, fmt.Errorf("acp host is nil")
	}
	modeID := strings.TrimSpace(req.ModeID)
	if !acpModeOptionAllowed(modeID) {
		return resp, acp.InvalidParams(fmt.Errorf("permission mode %q is not supported", modeID))
	}

	sessionID := strings.TrimSpace(req.SessionID)
	h.mu.Lock()
	hostSess := h.sess[sessionID]
	h.mu.Unlock()
	if hostSess == nil || hostSess.chat == nil {
		return resp, acp.InvalidParams(fmt.Errorf("unknown sessionId %q", req.SessionID))
	}

	// Serialize against Prompt: the switch mutates session state while the
	// running turn may be reading it, so it goes through the host lock.
	hostSess.mu.Lock()
	defer hostSess.mu.Unlock()
	if hostSess.prompting {
		// A mode switch is safe mid-turn: the permission engine re-reads the
		// session mode on every tool evaluation (see
		// withLivePermissionModeSource), so the change applies from the next
		// tool call instead of the next turn. Plan transitions still need the
		// durable lifecycle and are rejected by the in-flight variant.
		if _, err := applyRuntimePermissionModeSwitchInFlight(hostSess.chat, modeID); err != nil {
			return resp, err
		}
	} else if _, err := applyRuntimePermissionModeSwitch(hostSess.chat, modeID); err != nil {
		return resp, err
	}
	h.broadcastACPModeChange(sessionID, hostSess.chat)
	return resp, nil
}

// SessionModes implements acp.SessionModeProvider for session/load: session/new
// carries the legacy modes state inline, load has no payload of its own so the
// ACP server asks for it after the history replay.
func (h *acpSessionHost) SessionModes(ctx context.Context, sessionID string) (*acp.SessionModeState, error) {
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
	return acpModesForChat(hostSess.chat), nil
}

// broadcastACPModeChange pushes a refreshed mode state to a client whose mode
// selector is open. Both channels are refreshed because both are advertised:
// config_option_update carries the authoritative currentValue for the "mode"
// config option, and current_mode_update drives the legacy modes selector.
// Notifications are best-effort: the caller's RPC response already carries the
// authoritative option set, so a dropped notification only leaves a stale
// picker until the next turn.
func (h *acpSessionHost) broadcastACPModeChange(sessionID string, chatSession *ChatSession) {
	if h == nil || chatSession == nil {
		return
	}
	h.broadcastSessionUpdate(sessionID, acp.SessionUpdate{
		SessionUpdate: acp.SessionUpdateConfigOptionUpdate,
		ConfigOptions: acpConfigOptionsForChat(chatSession),
	})
	h.broadcastSessionUpdate(sessionID, acp.CurrentModeUpdate(acpModeIDForSession(chatSession)))
}
