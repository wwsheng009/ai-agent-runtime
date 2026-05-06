package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
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
	printChatDebugAgentGraph(session)
	printChatDebugMailbox(session)
}

func printChatDebugAgentGraph(session *ChatSession) {
	fmt.Println("Agent Graph:")
	for _, line := range chatDebugAgentGraphLines(session) {
		fmt.Println(line)
	}
}

func chatDebugAgentGraphLines(session *ChatSession) []string {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.ActorRegistry == nil {
		return []string{"  <none>"}
	}
	parentSessionID := ""
	if session.RuntimeSession != nil {
		parentSessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	list, err := session.LocalRuntimeHost.ActorRegistry.List(context.Background(), parentSessionID, toolbroker.ListAgentsArgs{IncludeClosed: true})
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	if list == nil || len(list.Agents) == 0 {
		return []string{"  <none>"}
	}
	lines := make([]string, 0, len(list.Agents)+1)
	lines = append(lines, fmt.Sprintf("  count=%d", list.Count))
	for _, agent := range list.Agents {
		path := firstNonEmptyChatValue(agent.Path, agent.SessionID, agent.ID)
		status := firstNonEmptyChatValue(agent.Status, "unknown")
		sessionID := firstNonEmptyChatValue(agent.SessionID, agent.ID)
		parts := []string{fmt.Sprintf("  %s status=%s session=%s", path, status, sessionID)}
		if agent.SessionState != "" {
			parts = append(parts, "state="+agent.SessionState)
		}
		if agent.ParentSessionID != "" {
			parts = append(parts, "parent="+agent.ParentSessionID)
		}
		if agent.Depth > 0 {
			parts = append(parts, fmt.Sprintf("depth=%d", agent.Depth))
		}
		if agent.AgentType != "" {
			parts = append(parts, "type="+agent.AgentType)
		}
		if agent.PendingApproval {
			parts = append(parts, "approval=pending")
		}
		if agent.PendingQuestion {
			parts = append(parts, "question=pending")
		}
		if agent.PendingToolName != "" {
			parts = append(parts, "tool="+agent.PendingToolName)
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	return lines
}

func printChatDebugMailbox(session *ChatSession) {
	fmt.Println("Mailbox Pending:")
	for _, line := range chatDebugMailboxLines(session) {
		fmt.Println(line)
	}
}

func chatDebugMailboxLines(session *ChatSession) []string {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.TeamStore == nil || session.ActiveTeam == nil {
		return []string{"  <none>"}
	}
	teamID := strings.TrimSpace(session.ActiveTeam.TeamID)
	if teamID == "" {
		return []string{"  <none>"}
	}
	agentID := firstNonEmptyChatValue(session.ActiveTeam.AgentID, "lead")
	messages, err := session.LocalRuntimeHost.TeamStore.ListMail(context.Background(), team.MailFilter{
		TeamID:           teamID,
		ToAgent:          agentID,
		UnreadOnly:       true,
		IncludeBroadcast: true,
		Limit:            5,
	})
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	if len(messages) == 0 {
		return []string{fmt.Sprintf("  team=%s agent=%s unread=0", teamID, agentID)}
	}
	lines := []string{fmt.Sprintf("  team=%s agent=%s unread=%d shown=%d", teamID, agentID, len(messages), len(messages))}
	for _, message := range messages {
		parts := []string{fmt.Sprintf("  - %s", firstNonEmptyChatValue(message.ID, "<no-id>"))}
		if message.Kind != "" {
			parts = append(parts, "kind="+strings.TrimSpace(message.Kind))
		}
		if message.FromAgent != "" {
			parts = append(parts, "from="+strings.TrimSpace(message.FromAgent))
		}
		if message.ToAgent != "" {
			parts = append(parts, "to="+strings.TrimSpace(message.ToAgent))
		}
		if message.TaskID != nil && strings.TrimSpace(*message.TaskID) != "" {
			parts = append(parts, "task="+strings.TrimSpace(*message.TaskID))
		}
		if body := truncateChatRuntimeText(message.Body, 120); body != "" {
			parts = append(parts, "body="+body)
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	return lines
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
