package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

const chatAgentPanelPopupOwner = "agent_panel"

var chatAgentControlHealthCache sync.Map

type chatAgentControlHealthCacheEntry struct {
	expiresAt time.Time
	parts     []string
}

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
	printChatDebugAgentControl(session)
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
	case "panel", "pane", "dashboard":
		printChatAgentPanel(session, arg)
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
		for _, line := range chatAgentTargetLines(session) {
			fmt.Println(line)
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

func chatAgentTargetLines(session *ChatSession) []string {
	selected := strings.TrimSpace(session.SelectedAgentTarget)
	if selected == "" {
		selected = "<none>"
	}
	lines := []string{"Selected Agent Target: " + selected}
	agents, err := chatAgentPickerItems(session)
	if err != nil {
		return append(lines, "Agent Targets: <error: "+err.Error()+">")
	}
	if len(agents) == 0 {
		return append(lines, "Agent Targets: <none>")
	}
	lines = append(lines, "Agent Targets:")
	for index, agent := range agents {
		marker := " "
		if chatAgentTargetMatchesSelected(session.SelectedAgentTarget, agent) {
			marker = "*"
		}
		lines = append(lines, fmt.Sprintf("  [%d] %s %s", index+1, marker, chatAgentPickerOptionLine(agent)))
	}
	return lines
}

func chatAgentTargetMatchesSelected(selected string, agent toolbroker.AgentStatusResult) bool {
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return false
	}
	for _, value := range []string{agent.Path, agent.SessionID, agent.ID} {
		if strings.EqualFold(strings.TrimSpace(value), selected) {
			return true
		}
	}
	return false
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
	return chatAgentItems(session, false)
}

func chatAgentGraphItems(session *ChatSession) ([]toolbroker.AgentStatusResult, error) {
	return chatAgentItems(session, true)
}

func chatAgentItems(session *ChatSession, includeClosed bool) ([]toolbroker.AgentStatusResult, error) {
	if agents, ok, err := chatAgentItemsFast(session, includeClosed); ok || err != nil {
		return agents, err
	}
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.ActorRegistry == nil {
		return nil, nil
	}
	parentSessionID := ""
	if session.RuntimeSession != nil {
		parentSessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	list, err := session.LocalRuntimeHost.ActorRegistry.List(context.Background(), parentSessionID, toolbroker.ListAgentsArgs{IncludeClosed: includeClosed})
	if err != nil {
		return nil, err
	}
	if list == nil || len(list.Agents) == 0 {
		return nil, nil
	}
	return list.Agents, nil
}

func chatAgentItemsFast(session *ChatSession, includeClosed bool) ([]toolbroker.AgentStatusResult, bool, error) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.ActorRegistry == nil {
		return nil, true, nil
	}
	parentSessionID := ""
	if session.RuntimeSession != nil {
		parentSessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	if agents, ok, err := chatAgentItemsFromRegistryStore(session, parentSessionID, includeClosed); ok || err != nil {
		return agents, ok, err
	}
	return chatAgentItemsFromSessionStore(session, parentSessionID, includeClosed)
}

func chatAgentItemsFromRegistryStore(session *ChatSession, parentSessionID string, includeClosed bool) ([]toolbroker.AgentStatusResult, bool, error) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.ActorRegistry == nil {
		return nil, true, nil
	}
	host := session.LocalRuntimeHost
	store := host.ActorRegistry.localAgentRegistryStore()
	if store == nil || host.SessionStore == nil {
		return nil, false, nil
	}
	rootSessionID, parentPath, err := host.ActorRegistry.localAgentRegistryRootAndPath(context.Background(), parentSessionID)
	if err != nil {
		return nil, true, err
	}
	if rootSessionID == "" {
		return nil, false, nil
	}
	pathPrefix := ""
	if parentPath != "" && parentPath != "/root" {
		pathPrefix = parentPath
	}
	records, err := store.ListAgentControlAgents(context.Background(), agentcontrol.AgentFilter{
		RootSessionID: rootSessionID,
		PathPrefix:    pathPrefix,
		IncludeClosed: includeClosed,
	})
	if err != nil {
		return nil, true, err
	}
	if len(records) == 0 {
		return nil, false, nil
	}
	agents := make([]toolbroker.AgentStatusResult, 0, len(records))
	for _, record := range dedupeLocalAgentRecords(records) {
		record = record.Normalize()
		if record.AgentPath == "/root" || strings.EqualFold(record.AgentType, agentcontrol.AgentTypeRoot) {
			continue
		}
		agents = append(agents, chatAgentPickerStatusFromRecord(host, record))
	}
	sort.SliceStable(agents, func(i, j int) bool {
		left := firstNonEmptyChatValue(agents[i].Path, agents[i].SessionID, agents[i].ID)
		right := firstNonEmptyChatValue(agents[j].Path, agents[j].SessionID, agents[j].ID)
		return left < right
	})
	return agents, true, nil
}

func chatAgentItemsFromSessionStore(session *ChatSession, parentSessionID string, includeClosed bool) ([]toolbroker.AgentStatusResult, bool, error) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.ActorRegistry == nil || session.LocalRuntimeHost.SessionStore == nil {
		return nil, true, nil
	}
	host := session.LocalRuntimeHost
	sessions, err := host.ActorRegistry.listLocalAgentSessions(context.Background())
	if err != nil {
		return nil, true, err
	}
	if len(sessions) == 0 {
		return nil, true, nil
	}
	byID := make(map[string]*runtimechat.Session, len(sessions))
	for _, item := range sessions {
		if item != nil && strings.TrimSpace(item.ID) != "" {
			byID[strings.TrimSpace(item.ID)] = item
		}
	}
	rootSessionID := strings.TrimSpace(parentSessionID)
	if parent := byID[parentSessionID]; parent != nil {
		rootSessionID = localAgentRootSessionID(parent, parentSessionID)
	}
	agents := make([]toolbroker.AgentStatusResult, 0)
	for _, item := range sessions {
		if item == nil || !isLocalAgentSession(item) {
			continue
		}
		if !includeClosed && isClosedLocalAgentSession(item) {
			continue
		}
		if rootSessionID != "" && localAgentRootSessionID(item, "") != rootSessionID && !localAgentHasAncestor(item, parentSessionID, byID) {
			continue
		}
		agents = append(agents, chatAgentPickerStatusFromSession(host, item))
	}
	sort.SliceStable(agents, func(i, j int) bool {
		left := firstNonEmptyChatValue(agents[i].Path, agents[i].SessionID, agents[i].ID)
		right := firstNonEmptyChatValue(agents[j].Path, agents[j].SessionID, agents[j].ID)
		return left < right
	})
	return agents, true, nil
}

func chatAgentPickerStatusFromRecord(host *localChatRuntimeHost, record agentcontrol.AgentRecord) toolbroker.AgentStatusResult {
	record = record.Normalize()
	status := firstNonEmptyChatValue(record.Status, "active")
	sessionState := ""
	if record.Closed() {
		status = string(runtimechat.SessionStopped)
		sessionState = string(runtimechat.StateClosed)
	}
	result := toolbroker.AgentStatusResult{
		ID:              firstNonEmptyChatValue(record.AgentID, record.SessionID),
		SessionID:       record.SessionID,
		ParentSessionID: record.ParentSessionID,
		Path:            record.AgentPath,
		Depth:           record.Depth,
		AgentType:       record.AgentType,
		TeamID:          record.TeamID,
		TeammateID:      record.TeammateID,
		Status:          status,
		SessionState:    sessionState,
		Exists:          record.SessionID != "",
	}
	chatAgentPickerApplyLiveStatus(host, &result)
	return result
}

func chatAgentPickerStatusFromSession(host *localChatRuntimeHost, session *runtimechat.Session) toolbroker.AgentStatusResult {
	sessionID := ""
	if session != nil {
		sessionID = strings.TrimSpace(session.ID)
	}
	sessionState := ""
	if session != nil {
		sessionState = string(session.State)
	}
	result := toolbroker.AgentStatusResult{
		ID:           sessionID,
		SessionID:    sessionID,
		Path:         localAgentSessionPath(session),
		Depth:        localAgentSessionDepth(session),
		Status:       string(runtimechat.SessionIdle),
		Exists:       session != nil,
		SessionState: sessionState,
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextParentSessionID); ok {
		if text, ok := value.(string); ok {
			result.ParentSessionID = strings.TrimSpace(text)
		}
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextAgentType); ok {
		if text, ok := value.(string); ok {
			result.AgentType = strings.TrimSpace(text)
		}
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextTeamID); ok {
		if text, ok := value.(string); ok {
			result.TeamID = strings.TrimSpace(text)
		}
	}
	if value, ok := session.GetContext(toolbroker.AgentSessionContextTeammateID); ok {
		if text, ok := value.(string); ok {
			result.TeammateID = strings.TrimSpace(text)
		}
	}
	if isClosedLocalAgentSession(session) {
		result.Status = string(runtimechat.SessionStopped)
		result.SessionState = string(runtimechat.StateClosed)
	}
	chatAgentPickerApplyLiveStatus(host, &result)
	return result
}

func chatAgentPickerApplyLiveStatus(host *localChatRuntimeHost, result *toolbroker.AgentStatusResult) {
	if host == nil || host.SessionHub == nil || result == nil || strings.TrimSpace(result.SessionID) == "" {
		return
	}
	if actor, ok := host.SessionHub.Get(strings.TrimSpace(result.SessionID)); ok && actor != nil {
		state := actor.State()
		if state != nil {
			result.Status = string(state.Status)
			result.PendingApproval = state.PendingApproval != nil
			result.PendingQuestion = state.PendingQuestion != nil
			result.CurrentTurnID = strings.TrimSpace(state.CurrentTurnID)
			if state.PendingTool != nil {
				result.PendingToolName = strings.TrimSpace(state.PendingTool.ToolName)
				result.PendingToolCallID = strings.TrimSpace(state.PendingTool.ToolCallID)
			}
		}
	}
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
	parts = appendAgentTeamTaskParts(parts, agent)
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
	if agent.TeamID != "" {
		lines = append(lines, "  team="+agent.TeamID)
	}
	if agent.TeammateID != "" {
		lines = append(lines, "  teammate="+agent.TeammateID)
	}
	if agent.CurrentTaskID != "" {
		taskLine := "  task=" + agent.CurrentTaskID
		if agent.CurrentTaskStatus != "" {
			taskLine += " status=" + agent.CurrentTaskStatus
		}
		lines = append(lines, taskLine)
	}
	return lines
}

func printChatDebugAgentGraph(session *ChatSession) {
	fmt.Println("Agent Graph:")
	for _, line := range chatAgentGraphLines(session) {
		fmt.Println(line)
	}
}

func printChatDebugAgentControl(session *ChatSession) {
	fmt.Println("AgentControl Registry:")
	fmt.Println(chatAgentPanelRegistryLine(session))
}

func chatDebugAgentGraphLines(session *ChatSession) []string {
	return chatAgentGraphLines(session)
}

func chatAgentGraphLines(session *ChatSession) []string {
	selected := ""
	if session != nil {
		selected = strings.TrimSpace(session.SelectedAgentTarget)
	}
	lines, _ := chatAgentGraphLinesAndSelectedSession(session, selected)
	return lines
}

func chatAgentGraphLinesAndSelectedSession(session *ChatSession, selected string) ([]string, string) {
	agents, err := chatAgentGraphItems(session)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}, ""
	}
	if len(agents) == 0 {
		return []string{"  <none>"}, ""
	}
	selected = strings.TrimSpace(selected)
	selectedSessionID := ""
	lines := make([]string, 0, len(agents)+1)
	if selected != "" {
		lines = append(lines, "  selected="+selected)
	}
	lines = append(lines, fmt.Sprintf("  count=%d", len(agents)))
	for _, agent := range agents {
		if selectedSessionID == "" && chatAgentTargetMatchesSelected(selected, agent) {
			selectedSessionID = firstNonEmptyChatValue(agent.SessionID, agent.ID)
		}
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
		parts = appendAgentTeamTaskParts(parts, agent)
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
	return lines, strings.TrimSpace(selectedSessionID)
}

func printChatAgentPanel(session *ChatSession, argument string) {
	opts := parseChatAgentPanelOptions(argument, 8)
	if changed, err := applyChatAgentPanelNavigation(session, opts); err != nil {
		fmt.Printf("错误: %v\n", err)
		return
	} else if changed {
		warnIfChatSessionSyncFails(session, "set panel selected agent target", syncRuntimeSessionFromChat(session))
	}
	if opts.Follow && shouldUseChatAgentPanelModal(session) {
		if err := runChatAgentPanelModal(session, opts); err != nil {
			if err == io.EOF {
				return
			}
			fmt.Printf("错误: %v\n", err)
		}
		return
	}
	if useRuntimeSelectionPopup(session) {
		showChatAgentPanelPopup(session, chatAgentPanelLoadingLines(session))
	}
	lines := chatAgentPanelLines(session, opts.Limit)
	if opts.Follow {
		lines = append(lines, chatAgentPanelFollowLines(session, opts)...)
	}
	if useRuntimeSelectionPopup(session) {
		showChatAgentPanelPopup(session, lines)
		if session.Interaction != nil {
			session.Interaction.RefreshStatus("Agent Panel")
		}
		return
	}
	for _, line := range lines {
		fmt.Println(line)
	}
}

func showChatAgentPanelPopup(session *ChatSession, lines []string) {
	if session == nil || session.Surface == nil || !session.Surface.Enabled() {
		return
	}
	session.Surface.ShowPopupPreserveCursorForOwner(lines, chatAgentPanelPopupOwner)
}

func chatAgentPanelLoadingLines(session *ChatSession) []string {
	lines := []string{
		"Agent Control Panel:",
		"  loading=true",
	}
	selected := "<none>"
	if session != nil && strings.TrimSpace(session.SelectedAgentTarget) != "" {
		selected = strings.TrimSpace(session.SelectedAgentTarget)
	}
	lines = append(lines, "  selected="+selected)
	if session != nil && session.RuntimeSession != nil {
		lines = append(lines, "  parent_session="+strings.TrimSpace(session.RuntimeSession.ID)+" state="+strings.TrimSpace(string(session.RuntimeSession.State)))
	}
	lines = append(lines, "Agents:")
	lines = append(lines, "  <loading>")
	lines = append(lines, "Mailbox:")
	lines = append(lines, "  <loading>")
	lines = append(lines, "Timeline:")
	lines = append(lines, "  <loading>")
	return lines
}

func shouldUseChatAgentPanelModal(session *ChatSession) bool {
	return useRuntimeSelectionPopup(session) && shouldUseInteractiveLineEditor(session)
}

func parseChatAgentPanelLimit(argument string, fallback int) int {
	return parseChatAgentPanelOptions(argument, fallback).Limit
}

type chatAgentPanelOptions struct {
	Limit   int
	Follow  bool
	Timeout time.Duration
	Nav     string
	Target  string
}

func parseChatAgentPanelOptions(argument string, fallback int) chatAgentPanelOptions {
	limit := fallback
	follow := false
	timeout := 10 * time.Second
	for _, field := range strings.Fields(strings.TrimSpace(argument)) {
		if strings.EqualFold(field, "panel") || strings.EqualFold(field, "pane") || strings.EqualFold(field, "dashboard") {
			continue
		}
		if strings.EqualFold(field, "next") || strings.EqualFold(field, "prev") || strings.EqualFold(field, "previous") {
			if strings.EqualFold(field, "previous") {
				field = "prev"
			}
			return chatAgentPanelOptions{Limit: limit, Follow: follow, Timeout: timeout, Nav: strings.ToLower(field)}
		}
		if strings.EqualFold(field, "target") {
			continue
		}
		if strings.EqualFold(field, "follow") || strings.EqualFold(field, "watch") {
			follow = true
			continue
		}
		if duration, ok := chatCollabTimeoutToken(field); ok {
			if duration > 0 {
				timeout = duration
			}
			continue
		}
		value, err := strconv.Atoi(field)
		if err != nil || value <= 0 {
			if !strings.HasPrefix(field, "timeout=") && !strings.HasPrefix(field, "wait=") {
				return chatAgentPanelOptions{Limit: limit, Follow: follow, Timeout: timeout, Nav: "target", Target: strings.TrimSpace(field)}
			}
			continue
		}
		if value > 50 {
			value = 50
		}
		limit = value
	}
	return chatAgentPanelOptions{Limit: limit, Follow: follow, Timeout: timeout}
}

func applyChatAgentPanelNavigation(session *ChatSession, opts chatAgentPanelOptions) (bool, error) {
	if strings.TrimSpace(opts.Nav) == "" {
		return false, nil
	}
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.ActorRegistry == nil {
		return false, fmt.Errorf("agent registry not configured")
	}
	switch strings.ToLower(strings.TrimSpace(opts.Nav)) {
	case "target":
		if strings.TrimSpace(opts.Target) == "" {
			return false, nil
		}
		resolved, err := resolveChatAgentTarget(session, opts.Target)
		if err != nil {
			return false, err
		}
		session.SelectedAgentTarget = firstNonEmptyChatValue(resolved.Path, resolved.SessionID, resolved.ID)
		return true, nil
	case "next", "prev":
		target, err := nextChatAgentPanelTarget(session, strings.EqualFold(opts.Nav, "prev"))
		if err != nil {
			return false, err
		}
		if target == "" {
			return false, nil
		}
		session.SelectedAgentTarget = target
		return true, nil
	default:
		return false, nil
	}
}

func nextChatAgentPanelTarget(session *ChatSession, previous bool) (string, error) {
	agents, err := chatAgentPickerItems(session)
	if err != nil {
		return "", err
	}
	if len(agents) == 0 {
		return "", nil
	}
	targets := make([]string, 0, len(agents))
	for _, agent := range agents {
		target := firstNonEmptyChatValue(agent.Path, agent.SessionID, agent.ID)
		if strings.TrimSpace(target) != "" {
			targets = append(targets, strings.TrimSpace(target))
		}
	}
	if len(targets) == 0 {
		return "", nil
	}
	current := ""
	if session != nil {
		current = strings.TrimSpace(session.SelectedAgentTarget)
	}
	index := -1
	for i, target := range targets {
		if strings.EqualFold(target, current) {
			index = i
			break
		}
	}
	if previous {
		if index <= 0 {
			return targets[len(targets)-1], nil
		}
		return targets[index-1], nil
	}
	if index < 0 || index+1 >= len(targets) {
		return targets[0], nil
	}
	return targets[index+1], nil
}

type chatAgentPanelPane int

const (
	chatAgentPanelPaneAgents chatAgentPanelPane = iota
	chatAgentPanelPaneMailbox
	chatAgentPanelPaneTimeline
)

func (p chatAgentPanelPane) String() string {
	switch p {
	case chatAgentPanelPaneMailbox:
		return "mailbox"
	case chatAgentPanelPaneTimeline:
		return "timeline"
	default:
		return "agents"
	}
}

type chatAgentPanelModalState struct {
	Limit  int
	Pane   chatAgentPanelPane
	Cursor int
}

func newChatAgentPanelModalState(limit int) chatAgentPanelModalState {
	if limit <= 0 {
		limit = 8
	}
	return chatAgentPanelModalState{Limit: limit}
}

func (s *chatAgentPanelModalState) MoveCursor(delta int, total int) {
	if s == nil || delta == 0 || total <= 0 {
		return
	}
	s.Cursor += delta
	if s.Cursor < 0 {
		s.Cursor = total - 1
	}
	if s.Cursor >= total {
		s.Cursor = 0
	}
}

func (s *chatAgentPanelModalState) MovePane(delta int) {
	if s == nil || delta == 0 {
		return
	}
	next := int(s.Pane) + delta
	for next < 0 {
		next += 3
	}
	s.Pane = chatAgentPanelPane(next % 3)
}

func runChatAgentPanelModal(session *ChatSession, opts chatAgentPanelOptions) error {
	if session == nil || session.InputBox == nil {
		return io.EOF
	}
	state := newChatAgentPanelModalState(opts.Limit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer clearRuntimeSelectionPopup(session)
	beginDirectInteractiveOutput(session)
	controller := newChatAgentPanelModalController(session, &state, "Agent Panel> ")
	controller.Start(ctx)
	defer controller.Stop()
	controller.Render()
	_, err := session.InputBox.ReadTransientPromptWithHooks("Agent Panel> ", ui.LineEditorHooks{
		OnNavigate: func(snapshot ui.LineEditorSnapshot, delta int) bool {
			controller.Navigate(delta)
			return true
		},
		OnMove: func(snapshot ui.LineEditorSnapshot, delta int) bool {
			controller.MovePane(delta)
			return true
		},
		OnSubmit: func(snapshot ui.LineEditorSnapshot) (ui.LineEditorReplacement, bool) {
			controller.Select()
			return ui.LineEditorReplacement{}, true
		},
		OnCancel: func(snapshot ui.LineEditorSnapshot) bool {
			return true
		},
	})
	if handledErr := handleChatAgentPanelModalInputError(session, err); handledErr != err {
		return handledErr
	}
	return err
}

func handleChatAgentPanelModalInputError(session *ChatSession, err error) error {
	if errors.Is(err, ui.ErrInteractiveInputInterrupted) || errors.Is(err, ui.ErrInteractiveInputExitRequested) {
		if session == nil {
			return io.EOF
		}
		session.Interrupt()
		if session.Interaction != nil {
			session.Interaction.ResetPromptState()
		}
		return io.EOF
	}
	return err
}

type chatAgentPanelModalController struct {
	session  *ChatSession
	state    *chatAgentPanelModalState
	prompt   string
	mu       sync.Mutex
	cancel   context.CancelFunc
	rendered bool
}

func newChatAgentPanelModalController(session *ChatSession, state *chatAgentPanelModalState, prompt string) *chatAgentPanelModalController {
	return &chatAgentPanelModalController{session: session, state: state, prompt: prompt}
}

func (c *chatAgentPanelModalController) Start(ctx context.Context) {
	if c == nil {
		return
	}
	watchCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	updates := watchChatAgentPanelModalUpdates(watchCtx, c.session)
	go func() {
		for {
			select {
			case <-watchCtx.Done():
				return
			case _, ok := <-updates:
				if !ok {
					return
				}
				c.Render()
			}
		}
	}()
}

func (c *chatAgentPanelModalController) Stop() {
	if c == nil {
		return
	}
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *chatAgentPanelModalController) Navigate(delta int) {
	if c == nil || c.state == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	agents, _ := chatAgentPickerItems(c.session)
	c.state.MoveCursor(delta, len(agents))
	c.renderLocked()
}

func (c *chatAgentPanelModalController) MovePane(delta int) {
	if c == nil || c.state == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state.MovePane(delta)
	c.renderLocked()
}

func (c *chatAgentPanelModalController) Select() {
	if c == nil || c.state == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	agents, err := chatAgentPickerItems(c.session)
	if err != nil || len(agents) == 0 {
		c.renderLocked()
		return
	}
	if c.state.Cursor < 0 {
		c.state.Cursor = 0
	}
	if c.state.Cursor >= len(agents) {
		c.state.Cursor = len(agents) - 1
	}
	selected := agents[c.state.Cursor]
	c.session.SelectedAgentTarget = firstNonEmptyChatValue(selected.Path, selected.SessionID, selected.ID)
	warnIfChatSessionSyncFails(c.session, "set panel selected agent target", syncRuntimeSessionFromChat(c.session))
	c.renderLocked()
}

func (c *chatAgentPanelModalController) Render() {
	if c == nil || c.session == nil || c.session.Surface == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.renderLocked()
}

func (c *chatAgentPanelModalController) renderLocked() {
	if c == nil || c.session == nil || c.session.Surface == nil {
		return
	}
	lines := chatAgentPanelModalLines(c.session, c.state)
	if !c.rendered {
		c.session.Surface.ShowPopupInput(lines, c.prompt)
		c.rendered = true
		return
	}
	c.session.Surface.ShowPopupInputPreserveCursor(lines, c.prompt)
}

func chatAgentPanelModalLines(session *ChatSession, state *chatAgentPanelModalState) []string {
	if state == nil {
		s := newChatAgentPanelModalState(8)
		state = &s
	}
	lines := []string{
		"Agent Control Panel:",
		fmt.Sprintf("  mode=follow pane=%s cursor=%d", state.Pane.String(), state.Cursor+1),
	}
	selected := "<none>"
	if session != nil && strings.TrimSpace(session.SelectedAgentTarget) != "" {
		selected = strings.TrimSpace(session.SelectedAgentTarget)
	}
	lines = append(lines, "  selected="+selected)
	if session != nil && session.RuntimeSession != nil {
		lines = append(lines, "  parent_session="+strings.TrimSpace(session.RuntimeSession.ID)+" state="+strings.TrimSpace(string(session.RuntimeSession.State)))
	}
	if session != nil && session.ActiveTeam != nil {
		lines = append(lines, "  active_team="+strings.TrimSpace(session.ActiveTeam.TeamID)+" agent="+strings.TrimSpace(session.ActiveTeam.AgentID))
	}
	lines = append(lines, chatAgentPanelRegistryLine(session))
	lines = append(lines, chatAgentPanelModalAgentLines(session, state)...)
	lines = append(lines, chatAgentPanelModalMailboxLines(session, state)...)
	lines = append(lines, chatAgentPanelModalTimelineLines(session, state)...)
	lines = append(lines, "  提示: ↑↓ 选择 agent，←→ 切换 pane，Enter 设为 target，Esc 关闭")
	return compactChatAgentPanelLines(lines, 80)
}

func chatAgentPanelModalAgentLines(session *ChatSession, state *chatAgentPanelModalState) []string {
	lines := []string{"Agents:"}
	agents, err := chatAgentPickerItems(session)
	if err != nil {
		return append(lines, "  <error: "+err.Error()+">")
	}
	if len(agents) == 0 {
		return append(lines, "  <none>")
	}
	cursor := state.Cursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(agents) {
		cursor = len(agents) - 1
	}
	for index, agent := range agents {
		marker := " "
		if index == cursor {
			marker = ">"
		}
		selected := " "
		if session != nil && chatAgentTargetMatchesSelected(session.SelectedAgentTarget, agent) {
			selected = "*"
		}
		lines = append(lines, fmt.Sprintf("  %s%s [%d] %s", marker, selected, index+1, chatAgentPickerOptionLine(agent)))
	}
	return lines
}

func chatAgentPanelModalMailboxLines(session *ChatSession, state *chatAgentPanelModalState) []string {
	header := "Mailbox:"
	if state.Pane == chatAgentPanelPaneMailbox {
		header += " <focused>"
	}
	lines := []string{header}
	target := ""
	if session != nil && strings.TrimSpace(session.SelectedAgentTarget) != "" {
		target = "selected"
	}
	return append(lines, chatAgentPanelSnapshotLines(session, target, state.Limit)...)
}

func chatAgentPanelModalTimelineLines(session *ChatSession, state *chatAgentPanelModalState) []string {
	header := "Timeline:"
	if state.Pane == chatAgentPanelPaneTimeline {
		header += " <focused>"
	}
	lines := []string{header}
	if session != nil && session.ActiveTeam != nil {
		return append(lines, chatAgentPanelTimelineLines(session, state.Limit)...)
	}
	return append(lines, "  <none>")
}

func watchChatAgentPanelModalUpdates(ctx context.Context, session *ChatSession) <-chan struct{} {
	out := make(chan struct{}, 1)
	if session == nil || session.LocalRuntimeHost == nil {
		return out
	}
	watchChatAgentPanelMailboxUpdates(ctx, session, out)
	watchChatAgentPanelAgentUpdates(ctx, session, out)
	watchChatAgentPanelTaskUpdates(ctx, session, out)
	return out
}

func watchChatAgentPanelMailboxUpdates(ctx context.Context, session *ChatSession, out chan<- struct{}) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.EventStore == nil {
		return
	}
	sessionIDs, err := chatCollabFollowSessionIDs(session, "all")
	if err != nil || len(sessionIDs) == 0 {
		return
	}
	for _, sessionID := range sessionIDs {
		watch, unwatch, ok := runtimechat.WatchMailboxAgentControlFirst(ctx, session.LocalRuntimeHost.EventStore, sessionID)
		if ok {
			go forwardChatAgentPanelModalUpdates(ctx, watch, unwatch, out)
		}
	}
}

func watchChatAgentPanelAgentUpdates(ctx context.Context, session *ChatSession, out chan<- struct{}) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.AgentRegistryStore == nil {
		return
	}
	source, ok := session.LocalRuntimeHost.AgentRegistryStore.(agentcontrol.AgentWakeSource)
	if !ok || source == nil {
		return
	}
	filter := agentcontrol.AgentWakeFilter{}
	if session.RuntimeSession != nil {
		filter.RootSessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	watch, unwatch := source.WatchAgentControlAgentWake(ctx, filter)
	go forwardChatAgentPanelModalUpdates(ctx, watch, unwatch, out)
}

func watchChatAgentPanelTaskUpdates(ctx context.Context, session *ChatSession, out chan<- struct{}) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.TeamStore == nil || session.ActiveTeam == nil {
		return
	}
	watchSource := team.NewAgentControlTaskRegistry(session.LocalRuntimeHost.TeamStore)
	watch, unwatch := watchSource.WatchAgentControlTaskWake(ctx, agentcontrol.TaskWakeFilter{
		Workflow: agentcontrol.WorkflowSpawnTeam,
		TeamID:   strings.TrimSpace(session.ActiveTeam.TeamID),
	})
	go forwardChatAgentPanelModalUpdates(ctx, watch, unwatch, out)
}

func forwardChatAgentPanelModalUpdates[T any](ctx context.Context, watch <-chan T, unwatch func(), out chan<- struct{}) {
	defer func() {
		if unwatch != nil {
			unwatch()
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-watch:
			if !ok {
				return
			}
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}
}

func chatAgentPanelFollowLines(session *ChatSession, opts chatAgentPanelOptions) []string {
	target := ""
	if session != nil {
		target = strings.TrimSpace(session.SelectedAgentTarget)
	}
	followOpts := chatCollabCommandConfig{
		Target:  target,
		Limit:   opts.Limit,
		Follow:  true,
		Timeout: opts.Timeout,
	}
	if followOpts.Target == "" {
		followOpts.Target = "parent"
	}
	lines := []string{"Panel Follow:"}
	lines = append(lines, chatCollabFollowUpdateLines(session, followOpts)...)
	return lines
}

func chatAgentPanelLines(session *ChatSession, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	lines := []string{"Agent Control Panel:"}
	selected := ""
	if session != nil {
		selected = strings.TrimSpace(session.SelectedAgentTarget)
	}
	displaySelected := selected
	if displaySelected == "" {
		displaySelected = "<none>"
	}
	lines = append(lines, "  selected="+displaySelected)
	if session != nil && session.RuntimeSession != nil {
		lines = append(lines, "  parent_session="+strings.TrimSpace(session.RuntimeSession.ID)+" state="+strings.TrimSpace(string(session.RuntimeSession.State)))
	}
	if session != nil && session.ActiveTeam != nil {
		lines = append(lines, "  active_team="+strings.TrimSpace(session.ActiveTeam.TeamID)+" agent="+strings.TrimSpace(session.ActiveTeam.AgentID))
	}
	lines = append(lines, chatAgentPanelRegistryLine(session))
	lines = append(lines, "Agents:")
	agentLines, selectedSessionID := chatAgentGraphLinesAndSelectedSession(session, selected)
	lines = append(lines, agentLines...)
	lines = append(lines, "Mailbox:")
	if selected != "" {
		lines = append(lines, "  target=parent")
		lines = append(lines, chatAgentPanelSnapshotLines(session, "", limit)...)
		lines = append(lines, "  target=selected")
		if selectedSessionID != "" {
			lines = append(lines, chatAgentPanelSnapshotLinesForSession(session, selectedSessionID, limit)...)
		} else {
			lines = append(lines, chatAgentPanelSnapshotLines(session, "selected", limit)...)
		}
	} else {
		lines = append(lines, chatAgentPanelSnapshotLines(session, "", limit)...)
	}
	lines = append(lines, "Timeline:")
	if session != nil && session.ActiveTeam != nil {
		lines = append(lines, chatAgentPanelTimelineLines(session, limit)...)
	} else {
		lines = append(lines, "  <none>")
	}
	return compactChatAgentPanelLines(lines, 80)
}

func chatAgentPanelRegistryLine(session *ChatSession) string {
	parts := []string{"  registry=local"}
	if session == nil || session.LocalRuntimeHost == nil {
		return strings.Join(parts, " ")
	}
	if session.LocalRuntimeHost.AgentControl != nil {
		parts = append(parts, chatAgentControlHealthParts(session.LocalRuntimeHost.AgentControl)...)
	}
	if session.LocalRuntimeHost.AgentRegistryStore != nil {
		parts = append(parts, "agents=durable")
	}
	if session.LocalRuntimeHost.EventStore != nil {
		parts = append(parts, "mailbox=durable")
	}
	if session.LocalRuntimeHost.TeamStore != nil {
		parts = append(parts, "tasks=durable")
	}
	if projection := chatMailboxProjectionStatusPart("runtime_projection", session.LocalRuntimeHost.EventStore); projection != "" {
		parts = append(parts, projection)
	}
	if projection := chatMailboxProjectionStatusPart("team_projection", session.LocalRuntimeHost.TeamStore); projection != "" {
		parts = append(parts, projection)
	}
	return strings.Join(parts, " ")
}

func chatAgentControlHealthParts(service *agentcontrol.RegistryService) []string {
	if service == nil {
		return nil
	}
	now := time.Now()
	if cached, ok := chatAgentControlHealthCache.Load(service); ok {
		entry, ok := cached.(chatAgentControlHealthCacheEntry)
		if ok && now.Before(entry.expiresAt) {
			return append([]string(nil), entry.parts...)
		}
	}
	parts := []string{"service=on"}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	health, err := service.Health(ctx)
	if err != nil {
		parts = append(parts, "service_health=error")
		parts = append(parts, "mode="+service.Mode())
	} else {
		parts = append(parts, "service_health=ok")
		parts = append(parts, "mode="+health.Mode)
		parts = append(parts, "shared_db="+strconv.FormatBool(health.SharedDB))
	}
	chatAgentControlHealthCache.Store(service, chatAgentControlHealthCacheEntry{
		expiresAt: now.Add(750 * time.Millisecond),
		parts:     append([]string(nil), parts...),
	})
	return parts
}

func chatMailboxProjectionStatusPart(label string, store interface{}) string {
	reporter, ok := store.(agentcontrol.MailboxProjectionReporter)
	if !ok || reporter == nil {
		return ""
	}
	status := reporter.AgentControlMailboxProjectionStatus().Normalize()
	value := status.Mode
	if status.Reason != "" {
		value += ":" + status.Reason
	}
	if status.Store != "" {
		value += "@" + status.Store
	}
	return label + "=" + value
}

func compactChatAgentPanelLines(lines []string, maxLines int) []string {
	if maxLines <= 0 || len(lines) <= maxLines {
		return lines
	}
	out := make([]string, 0, maxLines)
	head := maxLines / 2
	tail := maxLines - head - 1
	out = append(out, lines[:head]...)
	out = append(out, fmt.Sprintf("  ... %d lines hidden ...", len(lines)-head-tail))
	out = append(out, lines[len(lines)-tail:]...)
	return out
}

func chatAgentPanelSnapshotLines(session *ChatSession, target string, limit int) []string {
	return chatCollabSnapshotLinesWithReadWindow(session, target, limit, "", chatAgentPanelReadWindowLimit(limit))
}

func chatAgentPanelSnapshotLinesForSession(session *ChatSession, sessionID string, limit int) []string {
	return chatCollabLinesForSessionWithReadWindow(session, strings.TrimSpace(sessionID), limit, chatAgentPanelReadWindowLimit(limit))
}

func chatAgentPanelTimelineLines(session *ChatSession, limit int) []string {
	if session == nil || session.ActiveTeam == nil {
		return []string{"  <none>"}
	}
	return chatTimelineLinesForTeamWithReadWindow(session, strings.TrimSpace(session.ActiveTeam.TeamID), limit, chatAgentPanelReadWindowLimit(limit))
}

func chatAgentPanelReadWindowLimit(limit int) int {
	if limit <= 0 {
		limit = 8
	}
	window := limit * 8
	if window < 64 {
		return 64
	}
	if window > 512 {
		return 512
	}
	return window
}

func appendAgentTeamTaskParts(parts []string, agent toolbroker.AgentStatusResult) []string {
	if agent.TeamID != "" {
		parts = append(parts, "team="+agent.TeamID)
	}
	if agent.TeammateID != "" {
		parts = append(parts, "teammate="+agent.TeammateID)
	}
	if agent.CurrentTaskID != "" {
		parts = append(parts, "task="+agent.CurrentTaskID)
	}
	if agent.CurrentTaskStatus != "" {
		parts = append(parts, "task_status="+agent.CurrentTaskStatus)
	}
	return parts
}

func printChatTimeline(session *ChatSession, command string) {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return
	}
	fmt.Println("Collab Timeline:")
	for _, line := range chatTimelineCommandLines(session, command) {
		fmt.Println(line)
	}
}

func printChatCollab(session *ChatSession, command string) {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return
	}
	target, _ := parseChatCollabTargetAndLimit(command, 20)
	if isChatCollabAllTarget(target) {
		fmt.Println("All Mailbox Timelines:")
	} else if strings.TrimSpace(target) == "" {
		fmt.Println("Parent Mailbox Timeline:")
	} else {
		fmt.Println("Agent Mailbox Timeline:")
	}
	for _, line := range chatCollabCommandLines(session, command) {
		fmt.Println(line)
	}
}

func parseChatTimelineLimit(command string, fallback int) int {
	_, limit := parseChatTimelineTargetAndLimit(command, fallback)
	return limit
}

func parseChatTimelineTargetAndLimit(command string, fallback int) (string, int) {
	opts := parseChatTimelineCommandConfig(command, fallback)
	return opts.Target, opts.Limit
}

type chatTimelineCommandConfig struct {
	Target string
	Limit  int
	Filter string
}

func parseChatTimelineCommandConfig(command string, fallback int) chatTimelineCommandConfig {
	limit := fallback
	target := ""
	filterParts := []string{}
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		return chatTimelineCommandConfig{Limit: limit}
	}
	for _, field := range strings.Fields(arg) {
		if value, err := strconv.Atoi(field); err == nil && value > 0 {
			if value > 100 {
				value = 100
			}
			limit = value
			continue
		}
		if filter, ok := chatCollabFilterToken(field); ok {
			if filter != "" {
				filterParts = append(filterParts, filter)
			}
			continue
		}
		if target == "" {
			target = strings.TrimSpace(field)
			continue
		}
		filterParts = append(filterParts, strings.TrimSpace(field))
	}
	return chatTimelineCommandConfig{
		Target: target,
		Limit:  limit,
		Filter: strings.TrimSpace(strings.Join(filterParts, " ")),
	}
}

func chatTimelineLines(session *ChatSession, limit int) []string {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.TeamStore == nil || session.ActiveTeam == nil {
		return []string{"  <none>"}
	}
	teamID := strings.TrimSpace(session.ActiveTeam.TeamID)
	return chatTimelineLinesForTeam(session, teamID, limit)
}

func chatTimelineCommandLines(session *ChatSession, command string) []string {
	opts := parseChatTimelineCommandConfig(command, 20)
	teamID := strings.TrimSpace(opts.Target)
	if teamID == "" || strings.EqualFold(teamID, "active") || strings.EqualFold(teamID, "current") {
		if session != nil && session.ActiveTeam != nil {
			teamID = strings.TrimSpace(session.ActiveTeam.TeamID)
		}
	}
	return filterChatTimelineLines(chatTimelineLinesForTeam(session, teamID, opts.Limit), opts.Filter)
}

func chatTimelineLinesForTeam(session *ChatSession, teamID string, limit int) []string {
	return chatTimelineLinesForTeamWithReadWindow(session, teamID, limit, 0)
}

func chatTimelineLinesForTeamWithReadWindow(session *ChatSession, teamID string, limit int, readLimit int) []string {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.TeamStore == nil {
		return []string{"  <none>"}
	}
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return []string{"  <none>"}
	}
	if limit <= 0 {
		limit = 20
	}
	afterSeq, readCount, err := chatTeamEventReadArgs(context.Background(), session.LocalRuntimeHost.TeamStore, teamID, readLimit)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	events, err := session.LocalRuntimeHost.TeamStore.ListTeamEvents(context.Background(), team.TeamEventFilter{
		TeamID:   teamID,
		AfterSeq: afterSeq,
		Limit:    readCount,
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
	lines := []string{fmt.Sprintf("  team=%s %s shown=%d", teamID, chatCollabEventCountPart(total, readLimit), len(events))}
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

func chatTeamEventReadArgs(ctx context.Context, store team.Store, teamID string, readLimit int) (int64, int, error) {
	if readLimit <= 0 {
		return 0, 0, nil
	}
	sequencer, ok := store.(team.TeamEventSequenceStore)
	if !ok || sequencer == nil {
		return 0, 0, nil
	}
	lastSeq, err := sequencer.LastTeamEventSeq(ctx, teamID)
	if err != nil {
		return 0, 0, err
	}
	return chatCollabRecentReadArgs(lastSeq, readLimit), readLimit, nil
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
	return chatCollabLinesWithReadWindow(session, limit, 0)
}

func chatCollabLinesWithReadWindow(session *ChatSession, limit int, readLimit int) []string {
	if session == nil || session.RuntimeSession == nil {
		return []string{"  <none>"}
	}
	return chatCollabLinesForSessionWithReadWindow(session, strings.TrimSpace(session.RuntimeSession.ID), limit, readLimit)
}

func chatCollabCommandLines(session *ChatSession, command string) []string {
	opts := parseChatCollabCommandConfig(command, 20)
	if opts.Follow {
		return chatCollabFollowUpdateLines(session, opts)
	}
	return chatCollabSnapshotLines(session, opts.Target, opts.Limit, opts.Filter)
}

func chatCollabSnapshotLines(session *ChatSession, target string, limit int, filter string) []string {
	return chatCollabSnapshotLinesWithReadWindow(session, target, limit, filter, 0)
}

func chatCollabSnapshotLinesWithReadWindow(session *ChatSession, target string, limit int, filter string, readLimit int) []string {
	if strings.TrimSpace(target) == "" {
		return filterChatCollabLines(chatCollabLinesWithReadWindow(session, limit, readLimit), filter)
	}
	if isChatCollabAllTarget(target) {
		return filterChatCollabLines(chatCollabAllLinesWithReadWindow(session, limit, readLimit), filter)
	}
	sessionID, err := resolveChatCollabTargetSession(session, target)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	return filterChatCollabLines(chatCollabLinesForSessionWithReadWindow(session, sessionID, limit, readLimit), filter)
}

func isChatCollabAllTarget(target string) bool {
	return strings.EqualFold(strings.TrimSpace(target), "all")
}

func parseChatCollabTargetAndLimit(command string, fallback int) (string, int) {
	target, limit, _ := parseChatCollabCommandOptions(command, fallback)
	return target, limit
}

func parseChatCollabCommandOptions(command string, fallback int) (string, int, string) {
	opts := parseChatCollabCommandConfig(command, fallback)
	return opts.Target, opts.Limit, opts.Filter
}

type chatCollabCommandConfig struct {
	Target  string
	Limit   int
	Filter  string
	Follow  bool
	Timeout time.Duration
}

func parseChatCollabCommandConfig(command string, fallback int) chatCollabCommandConfig {
	limit := fallback
	target := ""
	filterParts := []string{}
	follow := false
	timeout := 10 * time.Second
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		return chatCollabCommandConfig{Limit: limit, Timeout: timeout}
	}
	for _, field := range strings.Fields(arg) {
		if strings.EqualFold(field, "follow") || strings.EqualFold(field, "watch") {
			follow = true
			continue
		}
		if value, err := strconv.Atoi(field); err == nil && value > 0 {
			if value > 100 {
				value = 100
			}
			limit = value
			continue
		}
		if filter, ok := chatCollabFilterToken(field); ok {
			if filter != "" {
				filterParts = append(filterParts, filter)
			}
			continue
		}
		if duration, ok := chatCollabTimeoutToken(field); ok {
			if duration > 0 {
				timeout = duration
			}
			continue
		}
		if target == "" {
			target = strings.TrimSpace(field)
			continue
		}
		filterParts = append(filterParts, strings.TrimSpace(field))
	}
	return chatCollabCommandConfig{
		Target:  target,
		Limit:   limit,
		Filter:  strings.TrimSpace(strings.Join(filterParts, " ")),
		Follow:  follow,
		Timeout: timeout,
	}
}

func chatCollabFilterToken(field string) (string, bool) {
	for _, prefix := range []string{"filter=", "match="} {
		if value, ok := strings.CutPrefix(field, prefix); ok {
			return strings.TrimSpace(value), true
		}
	}
	return "", false
}

func chatCollabTimeoutToken(field string) (time.Duration, bool) {
	for _, prefix := range []string{"timeout=", "wait="} {
		if value, ok := strings.CutPrefix(field, prefix); ok {
			duration, err := time.ParseDuration(strings.TrimSpace(value))
			if err != nil {
				return 0, true
			}
			return duration, true
		}
	}
	return 0, false
}

func filterChatCollabLines(lines []string, filter string) []string {
	return filterChatEventLines(lines, filter)
}

func filterChatTimelineLines(lines []string, filter string) []string {
	return filterChatEventLines(lines, filter)
}

func filterChatEventLines(lines []string, filter string) []string {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" || len(lines) == 0 {
		return lines
	}
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- ") && !strings.Contains(strings.ToLower(trimmed), filter) {
			continue
		}
		filtered = append(filtered, line)
	}
	return filtered
}

func resolveChatCollabTargetSession(session *ChatSession, target string) (string, error) {
	target = strings.TrimSpace(target)
	if session == nil || session.RuntimeSession == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}
	if target == "" || strings.EqualFold(target, "parent") || strings.EqualFold(target, "current") {
		return strings.TrimSpace(session.RuntimeSession.ID), nil
	}
	if strings.EqualFold(target, "selected") || strings.EqualFold(target, "target") {
		target = strings.TrimSpace(session.SelectedAgentTarget)
		if target == "" {
			return "", fmt.Errorf("no selected agent target")
		}
	}
	if session.LocalRuntimeHost != nil && session.LocalRuntimeHost.ActorRegistry != nil {
		if resolved, err := resolveChatAgentTarget(session, target); err == nil && resolved != nil {
			if sessionID := strings.TrimSpace(resolved.SessionID); sessionID != "" {
				return sessionID, nil
			}
		}
	}
	if strings.HasPrefix(target, "/") {
		return "", fmt.Errorf("unknown agent target: %s", target)
	}
	return target, nil
}

type chatCollabMailboxTarget struct {
	Label     string
	SessionID string
	Status    string
}

func chatCollabAllLines(session *ChatSession, limit int) []string {
	return chatCollabAllLinesWithReadWindow(session, limit, 0)
}

func chatCollabAllLinesWithReadWindow(session *ChatSession, limit int, readLimit int) []string {
	targets, err := chatCollabMailboxTargets(session)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	if len(targets) == 0 {
		return []string{"  <none>"}
	}

	lines := []string{fmt.Sprintf("  targets=%d", len(targets))}
	for _, target := range targets {
		header := fmt.Sprintf("  target=%s session=%s", target.Label, target.SessionID)
		if target.Status != "" {
			header += " status=" + target.Status
		}
		lines = append(lines, header)
		for _, line := range chatCollabLinesForSessionWithReadWindow(session, target.SessionID, limit, readLimit) {
			if strings.TrimSpace(line) != "" {
				lines = append(lines, "  "+strings.TrimRight(line, " "))
			}
		}
	}
	return lines
}

func chatCollabMailboxTargets(session *ChatSession) ([]chatCollabMailboxTarget, error) {
	if session == nil || session.RuntimeSession == nil {
		return nil, nil
	}
	parentSessionID := strings.TrimSpace(session.RuntimeSession.ID)
	if parentSessionID == "" {
		return nil, nil
	}

	targets := []chatCollabMailboxTarget{{
		Label:     "parent",
		SessionID: parentSessionID,
		Status:    strings.TrimSpace(string(session.RuntimeSession.State)),
	}}
	seen := map[string]struct{}{strings.ToLower(parentSessionID): {}}
	agents, err := chatAgentPickerItems(session)
	if err != nil {
		return nil, err
	}
	for _, agent := range agents {
		sessionID := firstNonEmptyChatValue(agent.SessionID, agent.ID)
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		key := strings.ToLower(sessionID)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		targets = append(targets, chatCollabMailboxTarget{
			Label:     firstNonEmptyChatValue(agent.Path, agent.ID, sessionID),
			SessionID: sessionID,
			Status:    firstNonEmptyChatValue(agent.Status, agent.SessionState),
		})
	}
	return targets, nil
}

type chatCollabFollowUpdate struct {
	SessionID string
	Message   team.MailMessage
}

func chatCollabFollowUpdateLines(session *ChatSession, opts chatCollabCommandConfig) []string {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	sessionIDs, err := chatCollabFollowSessionIDs(session, opts.Target)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	if len(sessionIDs) == 0 {
		return []string{"  <none>"}
	}
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.EventStore == nil {
		return []string{"  follow=unavailable reason=mailbox_watcher_not_configured"}
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()
	updates := make(chan chatCollabFollowUpdate, max(1, len(sessionIDs)))
	unwatches := make([]func(), 0, len(sessionIDs))
	supported := false
	for _, sessionID := range sessionIDs {
		watch, unwatch, ok := runtimechat.WatchMailboxAgentControlFirst(ctx, session.LocalRuntimeHost.EventStore, sessionID)
		if !ok {
			continue
		}
		supported = true
		unwatches = append(unwatches, unwatch)
		go func(sessionID string, watch <-chan team.MailMessage) {
			select {
			case <-ctx.Done():
				return
			case message, ok := <-watch:
				if !ok {
					return
				}
				select {
				case updates <- chatCollabFollowUpdate{SessionID: sessionID, Message: message}:
				case <-ctx.Done():
				}
			}
		}(sessionID, watch)
	}
	defer func() {
		for _, unwatch := range unwatches {
			if unwatch != nil {
				unwatch()
			}
		}
	}()
	if !supported {
		return []string{"  follow=unavailable reason=mailbox_watcher_not_configured"}
	}

	lines := chatCollabSnapshotLines(session, opts.Target, opts.Limit, opts.Filter)
	lines = append(lines, fmt.Sprintf("  follow=waiting targets=%d timeout=%s", len(sessionIDs), opts.Timeout))
	select {
	case update := <-updates:
		lines = append(lines, chatCollabFollowUpdateLine(update))
		lines = append(lines, "  Follow Update:")
		lines = append(lines, chatCollabSnapshotLines(session, opts.Target, opts.Limit, opts.Filter)...)
	case <-ctx.Done():
		lines = append(lines, "  follow=timeout")
	}
	return lines
}

func chatCollabFollowSessionIDs(session *ChatSession, target string) ([]string, error) {
	target = strings.TrimSpace(target)
	if isChatCollabAllTarget(target) {
		targets, err := chatCollabMailboxTargets(session)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(targets))
		for _, item := range targets {
			if sessionID := strings.TrimSpace(item.SessionID); sessionID != "" {
				ids = append(ids, sessionID)
			}
		}
		return ids, nil
	}
	sessionID, err := resolveChatCollabTargetSession(session, target)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	return []string{strings.TrimSpace(sessionID)}, nil
}

func chatCollabFollowUpdateLine(update chatCollabFollowUpdate) string {
	parts := []string{"  follow=update", "session=" + strings.TrimSpace(update.SessionID)}
	if update.Message.Seq > 0 {
		parts = append(parts, fmt.Sprintf("seq=%d", update.Message.Seq))
	}
	if update.Message.SessionMailboxSeq > 0 {
		parts = append(parts, fmt.Sprintf("session_seq=%d", update.Message.SessionMailboxSeq))
	}
	if kind := strings.TrimSpace(update.Message.Kind); kind != "" {
		parts = append(parts, "kind="+kind)
	}
	if body := truncateChatRuntimeText(strings.TrimSpace(update.Message.Body), 80); body != "" {
		parts = append(parts, "body="+body)
	}
	return strings.Join(parts, " ")
}

func chatCollabLinesForSession(session *ChatSession, sessionID string, limit int) []string {
	return chatCollabLinesForSessionWithReadWindow(session, sessionID, limit, 0)
}

func chatCollabLinesForSessionWithReadWindow(session *ChatSession, sessionID string, limit int, readLimit int) []string {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.EventStore == nil || session.RuntimeSession == nil {
		return []string{"  <none>"}
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return []string{"  <none>"}
	}
	if limit <= 0 {
		limit = 20
	}
	if reader, ok := session.LocalRuntimeHost.EventStore.(runtimechat.AgentControlMailboxReaderStore); ok {
		return chatCollabAgentControlMailboxLinesWithReadWindow(context.Background(), reader, session.LocalRuntimeHost.EventStore, sessionID, limit, readLimit)
	}
	if reader, ok := session.LocalRuntimeHost.EventStore.(runtimechat.MailboxReaderStore); ok {
		return chatCollabMailboxSubstrateLinesWithReadWindow(context.Background(), reader, session.LocalRuntimeHost.EventStore, sessionID, limit, "mailbox", 0, readLimit)
	}
	afterSeq, readCount, err := chatCollabEventReadArgs(context.Background(), session.LocalRuntimeHost.EventStore, sessionID, readLimit)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	events, err := session.LocalRuntimeHost.EventStore.ListEvents(context.Background(), sessionID, afterSeq, readCount)
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
	lines := []string{fmt.Sprintf("  session=%s %s shown=%d", sessionID, chatCollabEventCountPart(total, readLimit), len(filtered))}
	for _, event := range filtered {
		line := chatCollabEventLine(event)
		if strings.TrimSpace(line) != "" {
			lines = append(lines, "  - "+line)
		}
	}
	return lines
}

func chatCollabAgentControlMailboxLines(ctx context.Context, reader runtimechat.AgentControlMailboxReaderStore, store interface {
	ListEvents(context.Context, string, int64, int) ([]runtimeevents.Event, error)
}, sessionID string, limit int) []string {
	return chatCollabAgentControlMailboxLinesWithReadWindow(ctx, reader, store, sessionID, limit, 0)
}

func chatCollabAgentControlMailboxLinesWithReadWindow(ctx context.Context, reader runtimechat.AgentControlMailboxReaderStore, store interface {
	ListEvents(context.Context, string, int64, int) ([]runtimeevents.Event, error)
}, sessionID string, limit int, readLimit int) []string {
	controlAfterSeq, controlReadCount, err := chatCollabAgentControlReadArgs(ctx, reader, sessionID, readLimit)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	controlMessages, err := reader.ListAgentControlMailbox(ctx, sessionID, controlAfterSeq, controlReadCount)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	messages := controlMessages
	if mailboxReader, ok := store.(runtimechat.MailboxReaderStore); ok && mailboxReader != nil {
		mailboxAfterSeq, mailboxReadCount, err := chatCollabMailboxReadArgs(ctx, mailboxReader, sessionID, readLimit)
		if err != nil {
			return []string{"  <error: " + err.Error() + ">"}
		}
		allMessages, err := mailboxReader.ListMailbox(ctx, sessionID, mailboxAfterSeq, mailboxReadCount)
		if err != nil {
			return []string{"  <error: " + err.Error() + ">"}
		}
		messages = allMessages
	}
	return chatCollabMailboxSubstrateLinesWithReadWindow(ctx, nil, store, sessionID, limit, "agent_control+mailbox", len(controlMessages), readLimit, messages...)
}

func chatCollabMailboxSubstrateLines(ctx context.Context, reader runtimechat.MailboxReaderStore, eventStore interface {
	ListEvents(context.Context, string, int64, int) ([]runtimeevents.Event, error)
}, sessionID string, limit int, source string, controlCount int, preloadedMessages ...team.MailMessage) []string {
	return chatCollabMailboxSubstrateLinesWithReadWindow(ctx, reader, eventStore, sessionID, limit, source, controlCount, 0, preloadedMessages...)
}

func chatCollabMailboxSubstrateLinesWithReadWindow(ctx context.Context, reader runtimechat.MailboxReaderStore, eventStore interface {
	ListEvents(context.Context, string, int64, int) ([]runtimeevents.Event, error)
}, sessionID string, limit int, source string, controlCount int, readLimit int, preloadedMessages ...team.MailMessage) []string {
	var messages []team.MailMessage
	if len(preloadedMessages) > 0 {
		messages = preloadedMessages
	} else if reader != nil {
		var err error
		afterSeq, readCount, err := chatCollabMailboxReadArgs(ctx, reader, sessionID, readLimit)
		if err != nil {
			return []string{"  <error: " + err.Error() + ">"}
		}
		messages, err = reader.ListMailbox(ctx, sessionID, afterSeq, readCount)
		if err != nil {
			return []string{"  <error: " + err.Error() + ">"}
		}
	}
	extras, err := listChatCollabNonMailboxEventsWithReadWindow(ctx, eventStore, sessionID, readLimit)
	if err != nil {
		return []string{"  <error: " + err.Error() + ">"}
	}
	total := len(messages) + len(extras)
	if total == 0 {
		return []string{fmt.Sprintf("  session=%s events=0", sessionID)}
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "mailbox"
	}
	header := fmt.Sprintf("  session=%s %s shown=%d source=%s", sessionID, chatCollabEventCountPart(total, readLimit), minChatTimelineLimit(total, limit), source)
	if controlCount > 0 {
		header += fmt.Sprintf(" control_events=%d", controlCount)
	}
	lines := []string{header}
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
	return listChatCollabNonMailboxEventsWithReadWindow(ctx, eventStore, sessionID, 0)
}

func listChatCollabNonMailboxEventsWithReadWindow(ctx context.Context, eventStore interface {
	ListEvents(context.Context, string, int64, int) ([]runtimeevents.Event, error)
}, sessionID string, readLimit int) ([]runtimeevents.Event, error) {
	if eventStore == nil {
		return nil, nil
	}
	afterSeq, readCount, err := chatCollabEventReadArgs(ctx, eventStore, sessionID, readLimit)
	if err != nil {
		return nil, err
	}
	events, err := eventStore.ListEvents(ctx, sessionID, afterSeq, readCount)
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

func chatCollabEventReadArgs(ctx context.Context, store interface{}, sessionID string, readLimit int) (int64, int, error) {
	if readLimit <= 0 {
		return 0, 0, nil
	}
	sequencer, ok := store.(runtimechat.EventSequenceStore)
	if !ok || sequencer == nil {
		return 0, 0, nil
	}
	lastSeq, err := sequencer.LastEventSeq(ctx, sessionID)
	if err != nil {
		return 0, 0, err
	}
	return chatCollabRecentReadArgs(lastSeq, readLimit), readLimit, nil
}

func chatCollabMailboxReadArgs(ctx context.Context, reader runtimechat.MailboxReaderStore, sessionID string, readLimit int) (int64, int, error) {
	if readLimit <= 0 {
		return 0, 0, nil
	}
	sequencer, ok := reader.(runtimechat.MailboxSequenceStore)
	if !ok || sequencer == nil {
		return 0, 0, nil
	}
	lastSeq, err := sequencer.LastMailboxSeq(ctx, sessionID)
	if err != nil {
		return 0, 0, err
	}
	return chatCollabRecentReadArgs(lastSeq, readLimit), readLimit, nil
}

func chatCollabAgentControlReadArgs(ctx context.Context, reader runtimechat.AgentControlMailboxReaderStore, sessionID string, readLimit int) (int64, int, error) {
	if readLimit <= 0 {
		return 0, 0, nil
	}
	sequencer, ok := reader.(runtimechat.AgentControlMailboxSequenceStore)
	if !ok || sequencer == nil {
		return 0, 0, nil
	}
	lastSeq, err := sequencer.LastAgentControlMailboxSeq(ctx, sessionID)
	if err != nil {
		return 0, 0, err
	}
	return chatCollabRecentReadArgs(lastSeq, readLimit), readLimit, nil
}

func chatCollabRecentReadArgs(lastSeq int64, readLimit int) int64 {
	if readLimit <= 0 {
		return 0
	}
	afterSeq := lastSeq - int64(readLimit)
	if afterSeq < 0 {
		return 0
	}
	return afterSeq
}

func chatCollabEventCountPart(total int, readLimit int) string {
	if readLimit > 0 && total >= readLimit {
		return fmt.Sprintf("events>=%d", total)
	}
	return fmt.Sprintf("events=%d", total)
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
	parts = appendAgentControlMetadataParts(parts, message.Metadata)
	if status := payloadStringValue(message.Metadata["status"]); status != "" {
		parts = append(parts, "status="+status)
	}
	return strings.Join(parts, " ")
}

func appendAgentControlMetadataParts(parts []string, metadata map[string]interface{}) []string {
	for _, field := range []struct {
		Key   string
		Label string
	}{
		{Key: "message_type", Label: "msg"},
		{Key: "control_action", Label: "action"},
		{Key: "workflow", Label: "workflow"},
		{Key: "mailbox_delivery", Label: "delivery"},
		{Key: "mailbox_kind", Label: "mailbox"},
		{Key: "event_type", Label: "event"},
		{Key: "target_session_id", Label: "target"},
	} {
		if value := payloadStringValue(metadata[field.Key]); value != "" {
			parts = append(parts, field.Label+"="+value)
		}
	}
	return parts
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
		parts = appendAgentControlMetadataParts(parts, metadata)
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
