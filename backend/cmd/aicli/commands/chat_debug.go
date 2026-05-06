package commands

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
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
	printChatSessionMetaRow("Agent Target:", chatDebugValueOrNone(strings.TrimSpace(session.SelectedAgentTarget)))
	if session.Surface != nil {
		printChatSessionMetaRow("Surface:", chatDebugBool(session.Surface.Enabled()))
	} else {
		printChatSessionMetaRow("Surface:", "<none>")
	}
	printChatDebugAgentGraph(session)
	printChatDebugMailbox(session)
}

func printChatAgents(session *ChatSession) {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return
	}
	fmt.Println("Agent Graph:")
	for _, line := range chatAgentGraphLines(session) {
		fmt.Println(line)
	}
}

func handleChatAgentsCommand(session *ChatSession, command string) {
	arg := strings.TrimSpace(extractCommandArgument(command))
	verb := strings.ToLower(firstChatAgentsArgToken(arg))
	switch verb {
	case "pick", "select":
		if err := pickChatAgent(session); err != nil {
			if err == io.EOF {
				fmt.Println("已取消 agent 选择")
				return
			}
			fmt.Printf("错误: %v\n", err)
		}
	case "send":
		if err := sendChatAgentMessageCommand(session, arg, false); err != nil {
			fmt.Printf("错误: %v\n", err)
		}
	case "followup", "task":
		if err := sendChatAgentMessageCommand(session, arg, true); err != nil {
			fmt.Printf("错误: %v\n", err)
		}
	case "target":
		if err := handleChatAgentTargetCommand(session, arg); err != nil {
			fmt.Printf("错误: %v\n", err)
		}
	default:
		printChatAgents(session)
	}
}

func firstChatAgentsArgToken(argument string) string {
	fields := strings.Fields(strings.TrimSpace(argument))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func sendChatAgentMessageCommand(session *ChatSession, argument string, trigger bool) error {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.ActorRegistry == nil {
		return fmt.Errorf("agent registry not configured")
	}
	target, message := parseChatAgentMessageCommand(argument)
	if target != "" && strings.TrimSpace(session.SelectedAgentTarget) != "" {
		if _, err := resolveChatAgentTarget(session, target); err != nil {
			message = strings.TrimSpace(strings.TrimSpace(target) + " " + strings.TrimSpace(message))
			target = ""
		}
	}
	if target == "" {
		target = strings.TrimSpace(session.SelectedAgentTarget)
	}
	if target == "" || message == "" {
		if trigger {
			return fmt.Errorf("用法: /agents followup [target] <message>")
		}
		return fmt.Errorf("用法: /agents send [target] <message>")
	}
	fromSessionID := ""
	if session.RuntimeSession != nil {
		fromSessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	args := toolbroker.AgentMessageArgs{Target: target, Message: message}
	var (
		result *toolbroker.AgentMessageResult
		err    error
	)
	if trigger {
		result, err = session.LocalRuntimeHost.ActorRegistry.FollowupTask(context.Background(), fromSessionID, args)
	} else {
		result, err = session.LocalRuntimeHost.ActorRegistry.SendMessage(context.Background(), fromSessionID, args)
	}
	if err != nil {
		return err
	}
	printChatAgentMessageResult(trigger, result)
	return nil
}

func parseChatAgentMessageCommand(argument string) (string, string) {
	argument = strings.TrimSpace(argument)
	fields := strings.Fields(argument)
	if len(fields) < 2 {
		return "", ""
	}
	verb := fields[0]
	rest := strings.TrimSpace(argument[len(verb):])
	if rest == "" {
		return "", ""
	}
	if len(fields) == 2 {
		return "", rest
	}
	target := fields[1]
	rest = strings.TrimSpace(rest[len(target):])
	return strings.TrimSpace(target), strings.TrimSpace(rest)
}

func printChatAgentMessageResult(trigger bool, result *toolbroker.AgentMessageResult) {
	action := "sent"
	if trigger {
		action = "followup"
	}
	if result == nil {
		fmt.Printf("Agent Message: %s\n", action)
		return
	}
	mode := "queued"
	if result.Triggered {
		mode = "triggered"
	} else if result.Delivered {
		mode = "delivered"
	}
	fmt.Printf("Agent Message: %s target=%s mode=%s\n", action, firstNonEmptyChatValue(result.TargetSessionID, "<none>"), mode)
}

func handleChatAgentTargetCommand(session *ChatSession, argument string) error {
	if session == nil {
		return fmt.Errorf("当前没有活动会话")
	}
	fields := strings.Fields(strings.TrimSpace(argument))
	if len(fields) < 2 {
		if strings.TrimSpace(session.SelectedAgentTarget) == "" {
			fmt.Println("Selected Agent Target: <none>")
		} else {
			fmt.Printf("Selected Agent Target: %s\n", strings.TrimSpace(session.SelectedAgentTarget))
		}
		return nil
	}
	target := strings.TrimSpace(fields[1])
	if strings.EqualFold(target, "clear") || strings.EqualFold(target, "none") {
		session.SelectedAgentTarget = ""
		warnIfChatSessionSyncFails(session, "clear selected agent target", syncRuntimeSessionFromChat(session))
		fmt.Println("Selected Agent Target: <none>")
		return nil
	}
	resolved, err := resolveChatAgentTarget(session, target)
	if err != nil {
		return err
	}
	session.SelectedAgentTarget = firstNonEmptyChatValue(resolved.Path, resolved.SessionID, resolved.ID)
	warnIfChatSessionSyncFails(session, "set selected agent target", syncRuntimeSessionFromChat(session))
	fmt.Printf("Selected Agent Target: %s\n", session.SelectedAgentTarget)
	return nil
}

func resolveChatAgentTarget(session *ChatSession, target string) (*toolbroker.AgentStatusResult, error) {
	agents, err := chatAgentPickerItems(session)
	if err != nil {
		return nil, err
	}
	if selected := resolveChatAgentPickerChoice(target, agents); selected != nil {
		return selected, nil
	}
	return nil, fmt.Errorf("unknown agent target: %s", strings.TrimSpace(target))
}

func pickChatAgent(session *ChatSession) error {
	agents, err := chatAgentPickerItems(session)
	if err != nil {
		return err
	}
	if len(agents) == 0 {
		fmt.Println("Agent Picker: <none>")
		return nil
	}
	selected, err := readChatAgentPickerChoice(session, agents)
	if err != nil || selected == nil {
		return err
	}
	session.SelectedAgentTarget = firstNonEmptyChatValue(selected.Path, selected.SessionID, selected.ID)
	warnIfChatSessionSyncFails(session, "set selected agent target", syncRuntimeSessionFromChat(session))
	fmt.Println("Selected Agent:")
	for _, line := range chatAgentPickerSelectionLines(*selected) {
		fmt.Println(line)
	}
	return nil
}

func chatAgentPickerItems(session *ChatSession) ([]toolbroker.AgentStatusResult, error) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.ActorRegistry == nil {
		return nil, nil
	}
	parentSessionID := ""
	if session.RuntimeSession != nil {
		parentSessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	list, err := session.LocalRuntimeHost.ActorRegistry.List(context.Background(), parentSessionID, toolbroker.ListAgentsArgs{IncludeClosed: true})
	if err != nil {
		return nil, err
	}
	if list == nil || len(list.Agents) == 0 {
		return nil, nil
	}
	return list.Agents, nil
}

func readChatAgentPickerChoice(session *ChatSession, agents []toolbroker.AgentStatusResult) (*toolbroker.AgentStatusResult, error) {
	prompt := "Agent (回车=1, q取消): "
	usePopup := useRuntimeSelectionPopup(session)
	if usePopup {
		defer clearRuntimeSelectionPopup(session)
	}
	lines := chatAgentPickerPopupLines(agents, "")
	if !usePopup {
		fmt.Println("Agent Picker:")
		for _, line := range lines {
			fmt.Println(line)
		}
	}
	warning := ""
	for {
		if usePopup {
			showRuntimeSelectionPopup(session, chatAgentPickerPopupLines(agents, warning), prompt)
		} else {
			fmt.Print(prompt)
		}
		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if !usePopup {
			fmt.Println()
		}
		if err != nil {
			return nil, err
		}
		choice := strings.TrimSpace(normalizeQueuedInputLine(text))
		if choice == "" {
			choice = "1"
		}
		if choice == "q" || choice == "quit" || choice == "cancel" || choice == "exit" {
			return nil, nil
		}
		if selected := resolveChatAgentPickerChoice(choice, agents); selected != nil {
			return selected, nil
		}
		if usePopup {
			warning = "  无效的选择，请重新输入"
		} else {
			fmt.Println("无效的选择，请重新输入")
		}
	}
}

func chatAgentPickerPopupLines(agents []toolbroker.AgentStatusResult, warning string) []string {
	lines := []string{"Agent Picker:"}
	if strings.TrimSpace(warning) != "" {
		lines = append(lines, warning)
	}
	for index, agent := range agents {
		lines = append(lines, fmt.Sprintf("  [%d] %s", index+1, chatAgentPickerOptionLine(agent)))
	}
	lines = append(lines, "  提示: 输入编号、path 或 session，q 取消")
	return lines
}

func chatAgentPickerOptionLine(agent toolbroker.AgentStatusResult) string {
	path := firstNonEmptyChatValue(agent.Path, agent.SessionID, agent.ID)
	status := firstNonEmptyChatValue(agent.Status, "unknown")
	sessionID := firstNonEmptyChatValue(agent.SessionID, agent.ID)
	parts := []string{path, "status=" + status, "session=" + sessionID}
	if agent.AgentType != "" {
		parts = append(parts, "type="+agent.AgentType)
	}
	return strings.Join(parts, " ")
}

func resolveChatAgentPickerChoice(choice string, agents []toolbroker.AgentStatusResult) *toolbroker.AgentStatusResult {
	choice = strings.TrimSpace(choice)
	if choice == "" {
		return nil
	}
	if index, err := strconv.Atoi(choice); err == nil {
		if index >= 1 && index <= len(agents) {
			return &agents[index-1]
		}
		return nil
	}
	for index := range agents {
		agent := &agents[index]
		for _, value := range []string{agent.Path, agent.SessionID, agent.ID} {
			if strings.EqualFold(strings.TrimSpace(value), choice) {
				return agent
			}
		}
	}
	return nil
}

func chatAgentPickerSelectionLines(agent toolbroker.AgentStatusResult) []string {
	lines := []string{
		"  path=" + firstNonEmptyChatValue(agent.Path, "<none>"),
		"  session=" + firstNonEmptyChatValue(agent.SessionID, agent.ID, "<none>"),
		"  status=" + firstNonEmptyChatValue(agent.Status, "unknown"),
	}
	if agent.SessionState != "" {
		lines = append(lines, "  session_state="+agent.SessionState)
	}
	if agent.ParentSessionID != "" {
		lines = append(lines, "  parent="+agent.ParentSessionID)
	}
	if agent.Depth > 0 {
		lines = append(lines, fmt.Sprintf("  depth=%d", agent.Depth))
	}
	if agent.AgentType != "" {
		lines = append(lines, "  type="+agent.AgentType)
	}
	return lines
}

func printChatDebugAgentGraph(session *ChatSession) {
	fmt.Println("Agent Graph:")
	for _, line := range chatAgentGraphLines(session) {
		fmt.Println(line)
	}
}

func chatDebugAgentGraphLines(session *ChatSession) []string {
	return chatAgentGraphLines(session)
}

func chatAgentGraphLines(session *ChatSession) []string {
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
	if target := strings.TrimSpace(session.SelectedAgentTarget); target != "" {
		lines = append(lines, "  selected="+target)
	}
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

func printChatTimeline(session *ChatSession, command string) {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return
	}
	fmt.Println("Collab Timeline:")
	for _, line := range chatTimelineLines(session, parseChatTimelineLimit(command, 20)) {
		fmt.Println(line)
	}
}

func printChatCollab(session *ChatSession, command string) {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return
	}
	fmt.Println("Parent Mailbox Timeline:")
	for _, line := range chatCollabLines(session, parseChatTimelineLimit(command, 20)) {
		fmt.Println(line)
	}
}

func parseChatTimelineLimit(command string, fallback int) int {
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		return fallback
	}
	limit, err := strconv.Atoi(arg)
	if err != nil || limit <= 0 {
		return fallback
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func chatTimelineLines(session *ChatSession, limit int) []string {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.TeamStore == nil || session.ActiveTeam == nil {
		return []string{"  <none>"}
	}
	teamID := strings.TrimSpace(session.ActiveTeam.TeamID)
	if teamID == "" {
		return []string{"  <none>"}
	}
	if limit <= 0 {
		limit = 20
	}
	events, err := session.LocalRuntimeHost.TeamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{
		TeamID: teamID,
	})
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	if len(events) == 0 {
		return []string{fmt.Sprintf("  team=%s events=0", teamID)}
	}
	total := len(events)
	if total > limit {
		events = events[total-limit:]
	}
	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Seq < events[j].Seq
	})
	lines := []string{fmt.Sprintf("  team=%s events=%d shown=%d", teamID, total, len(events))}
	for _, event := range events {
		line := chatTimelineEventLine(event)
		if strings.TrimSpace(line) != "" {
			lines = append(lines, "  - "+line)
		}
	}
	if len(lines) == 1 {
		return []string{fmt.Sprintf("  team=%s events=0", teamID)}
	}
	return lines
}

func chatTimelineEventLine(event team.TeamEventRecord) string {
	payload := map[string]interface{}{}
	if event.Payload != nil {
		payload = event.Payload
	}
	parts := []string{fmt.Sprintf("#%d", event.Seq), strings.TrimSpace(event.Type)}
	if taskID := payloadStringValue(payload["task_id"]); taskID != "" {
		parts = append(parts, "task="+taskID)
	}
	if sessionID := payloadStringValue(payload["session_id"]); sessionID != "" {
		parts = append(parts, "session="+sessionID)
	}
	if assignee := payloadStringValue(payload["assignee"]); assignee != "" {
		parts = append(parts, "assignee="+assignee)
	}
	if status := payloadStringValue(payload["status"]); status != "" {
		parts = append(parts, "status="+status)
	}
	if via := payloadStringValue(payload["via"]); via != "" {
		parts = append(parts, "via="+via)
	}
	if _, ok := payload["success"]; ok {
		parts = append(parts, "success="+strconv.FormatBool(payloadBoolValue(payload, "success")))
	}
	if traceID := payloadStringValue(payload["trace_id"]); traceID != "" {
		parts = append(parts, "trace="+traceID)
	}
	if summary := truncateChatRuntimeText(payloadStringValue(payload["summary"]), 140); summary != "" {
		parts = append(parts, "summary="+summary)
	}
	if errorText := truncateChatRuntimeText(payloadStringValue(payload["error"]), 140); errorText != "" {
		parts = append(parts, "error="+errorText)
	}
	return strings.Join(parts, " ")
}

func chatCollabLines(session *ChatSession, limit int) []string {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.EventStore == nil || session.RuntimeSession == nil {
		return []string{"  <none>"}
	}
	sessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if sessionID == "" {
		return []string{"  <none>"}
	}
	if limit <= 0 {
		limit = 20
	}
	if reader, ok := session.LocalRuntimeHost.EventStore.(runtimechat.MailboxReaderStore); ok {
		return chatCollabMailboxSubstrateLines(context.Background(), reader, session.LocalRuntimeHost.EventStore, sessionID, limit)
	}
	events, err := session.LocalRuntimeHost.EventStore.ListEvents(context.Background(), sessionID, 0, 0)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	filtered := make([]runtimeevents.Event, 0, len(events))
	for _, event := range events {
		if !isChatCollabEvent(event) {
			continue
		}
		filtered = append(filtered, event)
	}
	if len(filtered) == 0 {
		return []string{fmt.Sprintf("  session=%s events=0", sessionID)}
	}
	total := len(filtered)
	if total > limit {
		filtered = filtered[total-limit:]
	}
	lines := []string{fmt.Sprintf("  session=%s events=%d shown=%d", sessionID, total, len(filtered))}
	for _, event := range filtered {
		line := chatCollabEventLine(event)
		if strings.TrimSpace(line) != "" {
			lines = append(lines, "  - "+line)
		}
	}
	return lines
}

func chatCollabMailboxSubstrateLines(ctx context.Context, reader runtimechat.MailboxReaderStore, eventStore interface {
	ListEvents(context.Context, string, int64, int) ([]runtimeevents.Event, error)
}, sessionID string, limit int) []string {
	messages, err := reader.ListMailbox(ctx, sessionID, 0, 0)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	extras, err := listChatCollabNonMailboxEvents(ctx, eventStore, sessionID)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	total := len(messages) + len(extras)
	if total == 0 {
		return []string{fmt.Sprintf("  session=%s events=0", sessionID)}
	}
	lines := []string{fmt.Sprintf("  session=%s events=%d shown=%d source=mailbox", sessionID, total, minChatTimelineLimit(total, limit))}
	for _, entry := range recentChatCollabMailboxEntries(messages, extras, limit) {
		line := strings.TrimSpace(entry)
		if line != "" {
			lines = append(lines, "  - "+line)
		}
	}
	return lines
}

func recentChatCollabMailboxEntries(messages []team.MailMessage, extras []runtimeevents.Event, limit int) []string {
	entries := make([]string, 0, len(messages)+len(extras))
	for _, message := range messages {
		entries = append(entries, chatCollabMailboxLine(message))
	}
	for _, event := range extras {
		entries = append(entries, chatCollabEventLine(event))
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	return entries
}

func listChatCollabNonMailboxEvents(ctx context.Context, eventStore interface {
	ListEvents(context.Context, string, int64, int) ([]runtimeevents.Event, error)
}, sessionID string) ([]runtimeevents.Event, error) {
	if eventStore == nil {
		return nil, nil
	}
	events, err := eventStore.ListEvents(ctx, sessionID, 0, 0)
	if err != nil {
		return nil, err
	}
	filtered := make([]runtimeevents.Event, 0, len(events))
	for _, event := range events {
		if isChatCollabMailboxMirrorEvent(event) || !isChatCollabEvent(event) {
			continue
		}
		filtered = append(filtered, event)
	}
	return filtered, nil
}

func chatCollabMailboxLine(message team.MailMessage) string {
	parts := []string{fmt.Sprintf("#%d", message.Seq), runtimechat.EventMailboxReceived}
	if kind := strings.TrimSpace(message.Kind); kind != "" {
		parts = append(parts, "kind="+kind)
	}
	if from := strings.TrimSpace(message.FromAgent); from != "" {
		parts = append(parts, "from="+from)
	}
	if to := strings.TrimSpace(message.ToAgent); to != "" {
		parts = append(parts, "to="+to)
	}
	if message.TaskID != nil && strings.TrimSpace(*message.TaskID) != "" {
		parts = append(parts, "task="+strings.TrimSpace(*message.TaskID))
	}
	if teamID := strings.TrimSpace(message.TeamID); teamID != "" {
		parts = append(parts, "team="+teamID)
	}
	if body := truncateChatRuntimeText(strings.TrimSpace(message.Body), 140); body != "" {
		parts = append(parts, "body="+body)
	}
	if status := payloadStringValue(message.Metadata["status"]); status != "" {
		parts = append(parts, "status="+status)
	}
	return strings.Join(parts, " ")
}

func minChatTimelineLimit(total, limit int) int {
	if limit <= 0 || total <= limit {
		return total
	}
	return limit
}

func isChatCollabEvent(event runtimeevents.Event) bool {
	switch strings.TrimSpace(event.Type) {
	case "mailbox_received", "subagent.completed", "team.completed", "team.summary":
		return true
	default:
		return false
	}
}

func isChatCollabMailboxMirrorEvent(event runtimeevents.Event) bool {
	return strings.TrimSpace(event.Type) == runtimechat.EventMailboxReceived
}

func chatCollabEventLine(event runtimeevents.Event) string {
	payload := map[string]interface{}{}
	if event.Payload != nil {
		payload = event.Payload
	}
	parts := []string{fmt.Sprintf("#%d", chatCollabEventSeq(event)), strings.TrimSpace(event.Type)}
	for _, key := range []string{"kind", "from_agent", "to_agent", "task_id", "team_id"} {
		if value := payloadStringValue(payload[key]); value != "" {
			parts = append(parts, strings.TrimSuffix(key, "_agent")+"="+value)
		}
	}
	if body := truncateChatRuntimeText(payloadStringValue(payload["body"]), 140); body != "" {
		parts = append(parts, "body="+body)
	}
	if metadata, ok := payload["metadata"].(map[string]interface{}); ok {
		if childSession := payloadStringValue(metadata["session_id"]); childSession != "" {
			parts = append(parts, "child="+childSession)
		}
		if status := payloadStringValue(metadata["status"]); status != "" {
			parts = append(parts, "status="+status)
		}
	}
	return strings.Join(parts, " ")
}

func chatCollabEventSeq(event runtimeevents.Event) int64 {
	if event.Payload == nil {
		return 0
	}
	switch value := event.Payload["seq"].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	default:
		return 0
	}
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
