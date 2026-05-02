package commands

import (
	"fmt"
	"strings"
)

func printChatDebugInfo(session *ChatSession) {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return
	}

	printSessionInfo(session)

	fmt.Println("会话文件与目录:")
	printChatSessionMetaRow("Session:", chatDebugSessionLabel(session))
	printChatSessionMetaRow("Session Store:", chatDebugValueOrNone(currentRuntimeSessionStoreSummary(session)))
	printChatSessionMetaRow("Session File:", chatDebugValueOrNone(currentRuntimeSessionPath(session)))
	printChatSessionMetaRow("Chat Log File:", chatDebugValueOrNone(currentChatLogFile(session)))
	printChatSessionMetaRow("Debug Log File:", chatDebugValueOrNone(currentDebugLogFile(session)))
	printChatSessionMetaRow("HTTP Artifact Dir:", chatDebugValueOrNone(currentRuntimeHTTPArtifactDir(session)))
	printChatSessionMetaRow("Shell Artifact Dir:", chatDebugValueOrNone(currentLocalShellArtifactDir(session)))
	printChatSessionMetaRow("Generated Image Artifact Dir:", chatDebugValueOrNone(currentGeneratedImageArtifactDir(session)))
	printChatSessionMetaRow("Last HTTP Req:", chatDebugValueOrNone(chatDebugLastHTTPArtifactPath(session, true)))
	printChatSessionMetaRow("Last HTTP Resp:", chatDebugValueOrNone(chatDebugLastHTTPArtifactPath(session, false)))
	printChatSessionMetaRow("Last Shell Out:", chatDebugValueOrNone(currentLastLocalShellArtifactPath(session)))
	if session.RuntimeSession != nil {
		preview := session.RuntimeSession.BuildPreview()
		if preview.Title != "" {
			printChatSessionMetaRow("Title:", preview.Title)
		}
		if preview.MessageCount > 0 {
			printChatSessionMetaRow("History:", fmt.Sprintf("%d messages", preview.MessageCount))
		}
	}

	fmt.Println("运行时调试:")
	printChatSessionMetaRow("Profile Root:", chatDebugValueOrNone(resolveAbsoluteChatPath(session.ProfileRoot)))
	printChatSessionMetaRow("Runtime Config Path:", chatDebugValueOrNone(resolveAbsoluteChatPath(session.RuntimeConfigPath)))
	printChatSessionMetaRow("MCP Config Path:", chatDebugValueOrNone(resolveAbsoluteChatPath(session.MCPConfigPath)))
	printChatSessionMetaRow("Resolved Skill Dirs:", chatDebugJoinedPaths(session.ResolvedSkillDirs))
	printChatSessionMetaRow("Output Format:", chatDebugValueOrNone(session.OutputFormat))
	printChatSessionMetaRow("No Interactive:", chatDebugBool(session.NoInteractive))
	printChatSessionMetaRow("JSON Output:", chatDebugBool(session.JSONOutput))
	printChatSessionMetaRow("JSON Envelope:", chatDebugBool(session.JSONEnvelope))
	printChatSessionMetaRow("MCP Enabled:", chatDebugBool(session.MCPEnabled))
	printChatSessionMetaRow("Skills Debug:", chatDebugBool(session.SkillsDebug))
	if session.LocalRuntimeHost == nil && (strings.TrimSpace(string(session.PermissionMode)) != "" || strings.TrimSpace(string(session.ApprovalReuseMode)) != "") {
		printChatSessionMetaRow("Permission Mode:", chatDebugValueOrNone(string(session.PermissionMode)))
		printChatSessionMetaRow("Approval Reuse:", chatDebugValueOrNone(formatChatApprovalReuseMode(session.ApprovalReuseMode)))
	}
	if session.InputQueue != nil {
		queuedCount, draining := queuedInteractiveInputState(session)
		if queuedCount == 0 && !draining {
			printChatSessionMetaRow("Queued Input:", "0 pending")
		}
	}
	if session.queuedInputDrain && session.InputQueue == nil {
		printChatSessionMetaRow("Queued Input:", "0 pending (draining)")
	}
	if session.Interaction != nil {
		printChatSessionMetaRow("Interaction:", session.Interaction.DebugSummary())
	} else {
		printChatSessionMetaRow("Interaction:", "<none>")
	}
	if session.Surface != nil {
		printChatSessionMetaRow("Surface:", chatDebugBool(session.Surface.Enabled()))
	} else {
		printChatSessionMetaRow("Surface:", "<none>")
	}
}

func chatDebugSessionLabel(session *ChatSession) string {
	if session == nil || session.RuntimeSession == nil {
		return "<none>"
	}
	return fmt.Sprintf("%s [%s]", session.RuntimeSession.ID, session.RuntimeSession.State)
}

func chatDebugLastHTTPArtifactPath(session *ChatSession, request bool) string {
	if session == nil || session.runtimeHTTPCapture == nil {
		return ""
	}
	snapshot := session.runtimeHTTPCapture.Snapshot()
	if request {
		return resolveAbsoluteChatPath(snapshot.RequestArtifactPath)
	}
	return resolveAbsoluteChatPath(snapshot.ResponseArtifactPath)
}

func chatDebugValueOrNone(value string) string {
	if strings.TrimSpace(value) == "" {
		return "<none>"
	}
	return value
}

func chatDebugBool(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func chatDebugJoinedPaths(paths []string) string {
	if len(paths) == 0 {
		return "<none>"
	}
	values := make([]string, 0, len(paths))
	for _, path := range paths {
		if resolved := resolveAbsoluteChatPath(path); resolved != "" {
			values = append(values, resolved)
		}
	}
	if len(values) == 0 {
		return "<none>"
	}
	return strings.Join(values, ", ")
}
