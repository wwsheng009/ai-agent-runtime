package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
)

// ACP v1 carries slash commands as ordinary prompt text: available_commands_update
// only publishes the catalog, and the client then sends "/model gpt-5" as a
// normal session/prompt. A headless host that forwards that text to the model
// would have the model answer a command the user never asked a question about,
// so the advertised commands are claimed here before the turn reaches the model.
//
// Only the catalog published by acpAvailableCommands is claimed. Anything else
// ("/foo", a POSIX path, a quoted slash) stays an ordinary prompt and reaches
// the model unchanged, so a client may still send text the agent never
// advertised as a command.
func (h *acpSessionHost) dispatchACPSlashCommand(ctx context.Context, sessionID string, hostSess *acpHostSession, text string, emit acp.Emitter) bool {
	if h == nil || hostSess == nil || hostSess.chat == nil {
		return false
	}
	name, args := splitACPSlashCommand(text)
	if name == "" {
		return false
	}
	switch name {
	case "help", "status":
		// Both are already implemented as structured command documents by the
		// shared dispatcher; the ACP host only redirects where they render.
		h.emitACPStructuredCommand(sessionID, hostSess, emit, "/"+name)
		return true
	case "clear":
		// There is no TTY to run the interactive confirmation on: the client's
		// user typing /clear is the confirmation.
		result := applyStructuredClear(hostSess.chat)
		h.emitACPMessage(sessionID, hostSess, emit, ui.RenderDocumentPlain(result.Document()))
		return true
	case "model", "provider", "reasoning_effort", "thought_level", "mode", "permission-mode":
		h.emitACPConfigOptionCommand(ctx, sessionID, hostSess, name, args, emit)
		return true
	default:
		return false
	}
}

// emitACPConfigOptionCommand applies "/model <id>", "/provider <id>",
// "/reasoning_effort <level>" and "/mode <mode>" to the session. Bare commands
// answer with the currently selectable values instead of opening the TUI
// pickers, which a headless client cannot render.
func (h *acpSessionHost) emitACPConfigOptionCommand(ctx context.Context, sessionID string, hostSess *acpHostSession, name, args string, emit acp.Emitter) {
	_ = ctx
	configID := name
	if strings.EqualFold(name, "permission-mode") {
		configID = acpModeConfigOptionID
	}
	if strings.EqualFold(name, "thought_level") {
		configID = acpThoughtLevelConfigOptionID
	}
	valueID := strings.TrimSpace(args)
	if valueID == "" {
		h.emitACPMessage(sessionID, hostSess, emit, acpConfigOptionChoicesText(hostSess.chat, configID))
		return
	}

	// This runs inside the session's own prompt turn, so the in-flight gate of
	// session/set_config_option must not apply: the slash command *is* the turn.
	hostSess.mu.Lock()
	err := applyACPConfigOptionSwitch(hostSess.chat, configID, valueID)
	hostSess.mu.Unlock()
	if err != nil {
		h.emitACPMessage(sessionID, hostSess, emit, fmt.Sprintf("命令 /%s 失败: %v", name, err))
		return
	}
	persistACPConfigOptionPreferences(hostSess.chat)
	if strings.EqualFold(configID, acpModeConfigOptionID) {
		// The mode channel refreshes both the config option and the legacy modes
		// selector itself.
		h.broadcastACPModeChange(sessionID, hostSess.chat)
	} else {
		h.broadcastSessionUpdate(sessionID, acp.ConfigOptionUpdate(acpConfigOptionsForChat(hostSess.chat)))
	}
	h.emitACPMessage(sessionID, hostSess, emit, acpConfigOptionConfirmationText(configID, valueID))
}

// emitACPStructuredCommand runs one of the shared structured commands and pushes
// its rendered document as a single agent message. Commands that need an
// interactive surface (pickers, alternate-screen viewers) are not reachable
// here because they are not advertised in the catalog.
func (h *acpSessionHost) emitACPStructuredCommand(sessionID string, hostSess *acpHostSession, emit acp.Emitter, command string) {
	result, handled, err := tryExecuteStructuredChatCommand(hostSess.chat, command)
	if err != nil {
		h.emitACPMessage(sessionID, hostSess, emit, fmt.Sprintf("命令 %s 失败: %v", command, err))
		return
	}
	if !handled {
		h.emitACPMessage(sessionID, hostSess, emit, fmt.Sprintf("命令 %s 在 ACP 会话中不可用", command))
		return
	}
	h.emitACPMessage(sessionID, hostSess, emit, ui.RenderDocumentPlain(result.Document()))
}

// emitACPMessage pushes one complete message to the client. The prompt-scoped
// bridge is preferred because it owns the message ids used for streaming
// dedupe; the raw emitter is only a fallback for hosts without a bridge.
func (h *acpSessionHost) emitACPMessage(sessionID string, hostSess *acpHostSession, emit acp.Emitter, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if hostSess != nil && hostSess.bridge != nil {
		if err := hostSess.bridge.EmitAssistant(text); err == nil {
			return
		}
	}
	if emit != nil {
		_ = emit.SessionUpdate(sessionID, acp.AgentMessageChunk(text))
	}
}

// splitACPSlashCommand parses "/model gpt-5" into ("model", "gpt-5"). A bare
// "/" is not a command (empty name), and the name is lower-cased to match the
// TUI dispatcher's case-insensitive handling.
func splitACPSlashCommand(text string) (string, string) {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "/") {
		return "", ""
	}
	body := strings.TrimPrefix(trimmed, "/")
	if body == "" {
		return "", ""
	}
	name := body
	args := ""
	if idx := strings.IndexAny(body, " \t\r\n"); idx >= 0 {
		name = body[:idx]
		args = strings.TrimSpace(body[idx+1:])
	}
	return strings.ToLower(strings.TrimSpace(name)), args
}

// acpConfigOptionChoicesText renders "current value + selectable values" for one
// config option. It replaces the TUI picker: prompt text is the only channel a
// headless client has.
func acpConfigOptionChoicesText(chat *ChatSession, configID string) string {
	for _, option := range acpConfigOptionsForChat(chat) {
		if !strings.EqualFold(option.ID, configID) {
			continue
		}
		lines := []string{fmt.Sprintf("%s（当前: %s）", option.Name, option.CurrentValue)}
		for _, choice := range option.Options {
			marker := "  "
			if strings.EqualFold(choice.Value, option.CurrentValue) {
				marker = "* "
			}
			lines = append(lines, fmt.Sprintf("%s%s - %s", marker, choice.Value, choice.Name))
		}
		return strings.Join(lines, "\n")
	}
	return fmt.Sprintf("配置项 %s 不可用", configID)
}

// acpConfigOptionConfirmationText is the user-facing acknowledgement of a
// config switch. The authoritative state is pushed separately (config option /
// mode notification), so this stays deliberately short.
func acpConfigOptionConfirmationText(configID, valueID string) string {
	switch {
	case strings.EqualFold(configID, acpModelConfigOptionID):
		return fmt.Sprintf("已切换模型: %s", valueID)
	case strings.EqualFold(configID, acpProviderConfigOptionID):
		return fmt.Sprintf("已切换提供方: %s", valueID)
	case strings.EqualFold(configID, acpThoughtLevelConfigOptionID):
		return fmt.Sprintf("已切换推理强度: %s", valueID)
	case strings.EqualFold(configID, acpModeConfigOptionID):
		return fmt.Sprintf("已切换权限模式: %s", valueID)
	default:
		return fmt.Sprintf("已更新配置项 %s: %s", configID, valueID)
	}
}
