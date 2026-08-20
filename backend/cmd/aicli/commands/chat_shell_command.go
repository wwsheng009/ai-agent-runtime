package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// executeStructuredShellCommand is the unified interactive entry point for
// /shell and /cmd (and the ! shorthand, which dispatches here). It reuses the
// legacy execution chain in captured mode and renders one Scene-backed command
// cell, then shares the output with the AI as its own turn through the
// post-commit SendMessageAfterCommit effect. The legacy live-streaming path
// (raw stdout writes) is never reachable while a TerminalSession owns the TTY.
func executeStructuredShellCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	argument := strings.TrimSpace(extractCommandArgument(command))
	if argument == "" {
		return commandTextResult("错误: 需要指定 shell 命令\n用法: /shell [--output-bytes-cap <bytes> | --disable-output-cap] <命令>\n      /cmd   [--output-bytes-cap <bytes> | --disable-output-cap] <命令>")
	}
	result, err := executeShellCommandDetailedMode(session, argument, false)
	if err != nil {
		return commandErrorResult(err)
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildStructuredShellCommandDocument(result)}},
		Action: CommandContinue,
		// Commit the captured command cell first; the output then streams to
		// the AI through the normal send pipeline as its own turn.
		SendMessageAfterCommit: buildShellCommandAIInput(result),
	}
}

// buildStructuredShellCommandDocument renders the captured shell execution as
// one plain command cell, mirroring the header the legacy streaming path
// printed ("执行命令: ..." / "--- 输出 ---") plus the truncation/artifact
// hints that used to go to raw stdout.
func buildStructuredShellCommandDocument(result shellCommandResult) render.Document {
	lines := []string{
		fmt.Sprintf("执行命令: %s", result.ExecutedCommand),
		"--- 输出 ---",
	}
	if result.Output != "" {
		lines = append(lines, result.Output)
	} else {
		lines = append(lines, "(无输出)")
	}
	if result.Capture.Truncated {
		lines = append(lines, fmt.Sprintf("[提示] 命令输出较大，传递给 AI 的内容已按 capture limit 截断：total=%dB retained=%dB omitted=%dB limit=%dB",
			result.Capture.TotalBytes, result.Capture.RetainedBytes, result.Capture.OmittedBytes, result.Capture.CaptureLimitBytes))
		if strings.TrimSpace(result.RawOutputArtifactPath) != "" {
			lines = append(lines, fmt.Sprintf("[提示] 完整原始输出已保存到: %s", resolveAbsoluteChatPath(result.RawOutputArtifactPath)))
		}
	}
	return textLinesDocument(lines)
}

// sendChatMessageAfterCommit streams a post-commit message through the normal
// send pipeline after the command cell is committed. It mirrors
// sendGoalObjectiveRequest without goal-specific gating.
func sendChatMessageAfterCommit(session *ChatSession, message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}
	response, err := sendMessage(session, message)
	if err != nil {
		return err
	}
	finishSuccessfulChatSend(session, response, session.NoInteractive)
	return nil
}
