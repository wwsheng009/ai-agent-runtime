package commands

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// dispatchUnmigratedUnifiedChatCommand is the hard command-side cutover
// boundary. A TerminalSession-owned interactive TTY must never enter a legacy
// handler: many of those handlers still own prompts, modal reads, or direct
// stdout writes. Commands become available here only after their side effects
// and visible result are represented by typed UI actions / CommandResult.
//
// Returning true requests process exit. Every other branch consumes the
// command, including unknown commands, so dispatch cannot fall through to
// handleCommand while the unified primary is active.
func dispatchUnmigratedUnifiedChatCommand(session *ChatSession, command string) bool {
	commandName := unifiedChatCommandName(command)
	switch commandName {
	case "/exit", "/quit", "/q":
		renderUnifiedCommandGateResult(session, "再见！")
		return true
	case "/help", "/?":
		renderUnifiedCommandGateDocument(session, textLinesDocument(buildChatSlashHelpLines()))
		return false
	default:
		if commandName == "" {
			commandName = strings.TrimSpace(command)
		}
		if commandName == "" {
			commandName = "该命令"
		}
		renderUnifiedCommandGateResult(session,
			"错误: "+commandName+" 尚未迁移到统一渲染命令通道，已在 interactive TTY 中禁用。")
		return false
	}
}

// rejectUnmigratedUnifiedChatCommand protects non-dispatch entry points such
// as the ! shell shorthand and direct tool invocations. The caller receives
// true when the command was consumed by the semantic gate and must not invoke
// its legacy implementation.
func rejectUnmigratedUnifiedChatCommand(session *ChatSession, command string) bool {
	if !unifiedDirectInteractiveOutput(session) {
		return false
	}
	_ = dispatchUnmigratedUnifiedChatCommand(session, command)
	return true
}

func unifiedChatCommandName(command string) string {
	name, _ := splitFirstToken(strings.TrimSpace(command))
	return strings.ToLower(strings.TrimSpace(name))
}

func renderUnifiedCommandGateResult(session *ChatSession, message string) {
	renderUnifiedCommandGateDocument(session, render.SingleLineDoc(render.TextSpan(message)))
}

func renderUnifiedCommandGateDocument(session *ChatSession, doc render.Document) {
	// renderChatCommandResult owns the plain/JSON projection separately. This
	// gate is called only after unifiedDirectInteractiveOutput, so an error
	// (for example during teardown) must remain fail-closed rather than using
	// a legacy stdout fallback.
	_ = renderChatCommandResult(session, CommandResult{
		Blocks: []RenderBlock{{Document: doc}},
	}, false)
}
