package commands

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	osuser "os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimeprompt "github.com/wwsheng009/ai-agent-runtime/internal/prompt"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	chatRuntimeContextProviderName    = "aicli_provider_name"
	chatRuntimeContextProtocol        = "aicli_protocol"
	chatRuntimeContextModel           = "aicli_model"
	chatRuntimeContextReasoningEffort = "aicli_reasoning_effort"
	chatRuntimeContextApprovalReuse   = "aicli_approval_reuse"
	chatRuntimeContextStream          = "aicli_stream"
	chatRuntimeContextDisableTools    = "aicli_disable_tools"
	chatRuntimeContextMessageCount    = "aicli_message_count"
	chatRuntimeContextProfileName     = "aicli_profile_name"
	chatRuntimeContextProfileAgent    = "aicli_profile_agent"
	chatRuntimeContextProfileRoot     = "aicli_profile_root"
)

type ChatSessionListFilter struct {
	State    runtimechat.SessionState
	Protocol string
	Provider string
	Model    string
	Query    string
	Limit    int
}

func newChatSessionManager(dir string) (*runtimechat.SessionManager, string, string, error) {
	resolvedDir := strings.TrimSpace(dir)
	if resolvedDir == "" {
		resolvedDir = resolveDefaultChatSessionDir()
	}

	storage, err := runtimechat.NewFileStorage(resolvedDir)
	if err != nil {
		return nil, "", "", err
	}

	cfg := runtimechat.DefaultSessionManagerConfig()
	cfg.MaxHistory = 200
	cfg.CleanupInterval = 6 * time.Hour
	cfg.IdleTimeout = 72 * time.Hour

	return runtimechat.NewSessionManager(storage, cfg), resolveChatSessionUserID(), resolvedDir, nil
}

func resolveDefaultChatSessionDir() string {
	return aiclipaths.DefaultSessionsDir()
}

func resolveDefaultChatLogDir() string {
	return aiclipaths.DefaultChatLogsDir()
}

// ResolveDefaultChatLogDir exposes the default chat log directory for command flags and callers
// outside the commands package.
func ResolveDefaultChatLogDir() string {
	return resolveDefaultChatLogDir()
}

func resolveChatSessionUserID() string {
	if current, err := osuser.Current(); err == nil {
		if username := strings.TrimSpace(current.Username); username != "" {
			return username
		}
	}
	for _, key := range []string{"AICLI_SESSION_USER", "USERNAME", "USER"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return "default"
}

func loadRequestedRuntimeSession(ctx context.Context, manager *runtimechat.SessionManager, userID, sessionID string, resume bool) (*runtimechat.Session, error) {
	if manager == nil {
		return nil, nil
	}

	if trimmedID := strings.TrimSpace(sessionID); trimmedID != "" {
		session, err := manager.Get(ctx, trimmedID)
		if err != nil {
			return nil, err
		}
		if session.UserID != userID {
			return nil, fmt.Errorf("session %s does not belong to user %s", trimmedID, userID)
		}
		return session, nil
	}

	if !resume {
		return nil, nil
	}

	session, err := manager.GetLatest(ctx, userID)
	if err != nil {
		if errors.Is(err, runtimechat.ErrSessionNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return session, nil
}

func restoreChatStateFromRuntimeSession(session *ChatSession, runtimeSession *runtimechat.Session) error {
	if session == nil || runtimeSession == nil {
		return nil
	}

	if err := replaceRuntimeMessages(session, runtimeSession.History); err != nil {
		return err
	}
	session.RuntimeSession = runtimeSession.Clone()
	session.MsgCount = countRuntimeUserMessages(session.Messages)
	session.TurnRequestCount = 0
	resetChatTurnTokenUsage(session)
	restoreChatRuntimeContext(session, session.RuntimeSession)
	restoreChatTokenCount(session, session.RuntimeSession)
	return nil
}

func createNewRuntimeConversation(session *ChatSession, title string) error {
	if session == nil || session.SessionManager == nil {
		return fmt.Errorf("会话管理未启用")
	}

	runtimeSession, err := session.SessionManager.Create(context.Background(), session.SessionUserID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(title) != "" {
		runtimeSession.UpdateTitle(title)
	}

	if err := replaceRuntimeMessages(session, nil); err != nil {
		return err
	}
	session.MsgCount = 0
	session.TurnRequestCount = 0
	resetChatConversationTokenUsage(session)
	session.RuntimeSession = runtimeSession
	ensureChatSystemPromptMessage(session)
	if err := syncRuntimeSessionFromChat(session); err != nil {
		return err
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	return nil
}

func loadRuntimeConversation(session *ChatSession, sessionID string) error {
	if session == nil || session.SessionManager == nil {
		return fmt.Errorf("会话管理未启用")
	}

	runtimeSession, err := session.SessionManager.Get(context.Background(), sessionID)
	if err != nil {
		return err
	}
	if runtimeSession.UserID != session.SessionUserID {
		return fmt.Errorf("会话 %s 不属于当前用户", sessionID)
	}
	if err := ensureRuntimeSessionCompatible(session, runtimeSession); err != nil {
		return err
	}
	if err := restoreChatStateFromRuntimeSession(session, runtimeSession); err != nil {
		return err
	}
	ensureChatSystemPromptMessage(session)
	return syncRuntimeSessionFromChat(session)
}

func resumeLatestRuntimeConversation(session *ChatSession) error {
	if session == nil || session.SessionManager == nil {
		return fmt.Errorf("会话管理未启用")
	}

	runtimeSession, err := session.SessionManager.GetLatest(context.Background(), session.SessionUserID)
	if err != nil {
		return err
	}
	if err := ensureRuntimeSessionCompatible(session, runtimeSession); err != nil {
		return err
	}
	if err := restoreChatStateFromRuntimeSession(session, runtimeSession); err != nil {
		return err
	}
	ensureChatSystemPromptMessage(session)
	return syncRuntimeSessionFromChat(session)
}

func syncRuntimeSessionFromChat(session *ChatSession) error {
	if session == nil || session.SessionManager == nil || session.RuntimeSession == nil {
		return nil
	}

	history := cloneRuntimeMessages(session.Messages)

	runtimeSession := session.RuntimeSession.Clone()
	if runtimeSession == nil {
		return runtimechat.ErrInvalidSession
	}
	runtimeSession.ReplaceHistory(history)
	runtimeSession.MarkActive()
	runtimeSession.Metadata.LastModel = session.Model
	if runtimeSession.Metadata.Context == nil {
		runtimeSession.Metadata.Context = make(map[string]interface{})
	}
	runtimeSession.Metadata.Context[chatRuntimeContextProviderName] = session.ProviderName
	runtimeSession.Metadata.Context[chatRuntimeContextProtocol] = session.Provider.GetProtocol()
	runtimeSession.Metadata.Context[chatRuntimeContextModel] = session.Model
	runtimeSession.Metadata.Context[chatRuntimeContextReasoningEffort] = runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	runtimeSession.Metadata.Context[chatRuntimeContextApprovalReuse] = string(session.ApprovalReuseMode)
	runtimeSession.Metadata.Context[chatRuntimeContextStream] = session.Stream
	runtimeSession.Metadata.Context[chatRuntimeContextDisableTools] = session.DisableTools
	runtimeSession.Metadata.Context[chatRuntimeContextMessageCount] = len(session.Messages)
	if session.TokenCount > 0 {
		runtimeSession.Metadata.Context[chatRuntimeContextTokenCount] = session.TokenCount
	} else {
		delete(runtimeSession.Metadata.Context, chatRuntimeContextTokenCount)
	}
	if strings.TrimSpace(session.ProfileName) != "" {
		runtimeSession.Metadata.Context[chatRuntimeContextProfileName] = session.ProfileName
	}
	if strings.TrimSpace(session.ProfileAgent) != "" {
		runtimeSession.Metadata.Context[chatRuntimeContextProfileAgent] = session.ProfileAgent
	}
	if strings.TrimSpace(session.ProfileRoot) != "" {
		runtimeSession.Metadata.Context[chatRuntimeContextProfileRoot] = session.ProfileRoot
	}
	syncChatRuntimeContext(session, runtimeSession)

	if err := session.SessionManager.Update(context.Background(), runtimeSession); err != nil {
		if errors.Is(err, runtimechat.ErrSessionNotFound) {
			if saveErr := session.SessionManager.GetStorage().Save(context.Background(), runtimeSession); saveErr != nil {
				return saveErr
			}
		} else {
			return err
		}
	}
	session.RuntimeSession = runtimeSession
	return nil
}

func countRuntimeUserMessages(messages []runtimetypes.Message) int {
	count := 0
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "user") {
			count++
		}
	}
	return count
}

func warnIfChatSessionSyncFails(session *ChatSession, operation string, err error) {
	if session == nil || err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[会话保存失败] %s: %v\n", operation, err)
}

func printCurrentRuntimeSession(session *ChatSession) {
	if session == nil || session.RuntimeSession == nil {
		return
	}

	preview := session.RuntimeSession.BuildPreview()
	if preview == nil {
		return
	}

	printChatSessionMetaRow("Session:", fmt.Sprintf("%s [%s]", preview.ID, preview.State))
	if sessionPath := currentRuntimeSessionPath(session); sessionPath != "" {
		printChatSessionMetaRow("Session File:", sessionPath)
	}
	if store := currentRuntimeSessionStoreSummary(session); store != "" {
		printChatSessionMetaRow("Session Store:", store)
	}
	if logPath := currentChatLogFile(session); logPath != "" {
		printChatSessionMetaRow("Chat Log File:", logPath)
	}
	if debugPath := currentDebugLogFile(session); debugPath != "" {
		printChatSessionMetaRow("Debug Log File:", debugPath)
	}
	if artifactDir := currentRuntimeHTTPArtifactDir(session); artifactDir != "" {
		printChatSessionMetaRow("HTTP Artifact Dir:", artifactDir)
	}
	if artifactDir := currentLocalShellArtifactDir(session); artifactDir != "" {
		printChatSessionMetaRow("Shell Artifact Dir:", artifactDir)
	}
	if session.runtimeHTTPCapture != nil {
		snapshot := session.runtimeHTTPCapture.Snapshot()
		if snapshot.RequestArtifactPath != "" {
			printChatSessionMetaRow("Last HTTP Req:", resolveAbsoluteChatPath(snapshot.RequestArtifactPath))
		}
		if snapshot.ResponseArtifactPath != "" {
			printChatSessionMetaRow("Last HTTP Resp:", resolveAbsoluteChatPath(snapshot.ResponseArtifactPath))
		}
	}
	if path := currentLastLocalShellArtifactPath(session); path != "" {
		printChatSessionMetaRow("Last Shell Out:", path)
	}
	if preview.Title != "" {
		printChatSessionMetaRow("Title:", preview.Title)
	}
	if preview.MessageCount > 0 {
		printChatSessionMetaRow("History:", fmt.Sprintf("%d messages", preview.MessageCount))
	}
}

func printChatSessionSummaries(manager *runtimechat.SessionManager, userID, currentID string, filter ChatSessionListFilter) error {
	if manager == nil {
		return fmt.Errorf("会话管理未启用")
	}

	sessions, err := listFilteredChatSessions(manager, userID, filter)
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		fmt.Println("暂无可恢复会话")
		return nil
	}

	now := time.Now()
	fmt.Println("可用会话:")
	for _, item := range sessions {
		if item == nil {
			continue
		}

		preview := item.BuildPreview()
		title := preview.Title
		if title == "" {
			title = "(untitled)"
		}

		lastUsed := formatSessionLastUsed(item.UpdatedAt, now)
		line := fmt.Sprintf("  %s [%s] 最后使用: %s", item.ID, item.State, lastUsed)
		if currentID != "" && item.ID == currentID {
			line += " current"
		}

		provider := runtimeSessionContextString(item, chatRuntimeContextProviderName)
		model := runtimeSessionContextString(item, chatRuntimeContextModel)
		if provider != "" || model != "" {
			line += fmt.Sprintf(" provider=%s model=%s", blankToDash(provider), blankToDash(model))
		}

		fmt.Println(line)
		fmt.Printf("    %s\n", title)
		if preview.Summary != "" && preview.Summary != title {
			fmt.Printf("    %s\n", preview.Summary)
		}
	}
	return nil
}

func listFilteredChatSessions(manager *runtimechat.SessionManager, userID string, filter ChatSessionListFilter) ([]*runtimechat.Session, error) {
	if manager == nil {
		return nil, fmt.Errorf("会话管理未启用")
	}

	sessions, err := manager.List(context.Background(), userID)
	if err != nil {
		return nil, err
	}

	filtered := make([]*runtimechat.Session, 0, len(sessions))
	for _, session := range sessions {
		if session == nil {
			continue
		}
		if !matchesChatSessionFilter(session, filter) {
			continue
		}
		filtered = append(filtered, session)
		if filter.Limit > 0 && len(filtered) >= filter.Limit {
			break
		}
	}
	return filtered, nil
}

func matchesChatSessionFilter(session *runtimechat.Session, filter ChatSessionListFilter) bool {
	if session == nil {
		return false
	}

	if filter.State != "" && session.State != filter.State {
		return false
	}

	if protocol := strings.TrimSpace(filter.Protocol); protocol != "" {
		storedProtocol := runtimeSessionContextString(session, chatRuntimeContextProtocol)
		if storedProtocol == "" || !strings.EqualFold(storedProtocol, protocol) {
			return false
		}
	}

	if provider := strings.TrimSpace(filter.Provider); provider != "" {
		if !strings.EqualFold(runtimeSessionContextString(session, chatRuntimeContextProviderName), provider) {
			return false
		}
	}

	if model := strings.TrimSpace(filter.Model); model != "" {
		if !strings.EqualFold(runtimeSessionContextString(session, chatRuntimeContextModel), model) {
			return false
		}
	}

	query := strings.ToLower(strings.TrimSpace(filter.Query))
	if query == "" {
		return true
	}

	preview := session.BuildPreview()
	candidates := []string{
		session.ID,
		preview.Title,
		preview.Summary,
		runtimeSessionContextString(session, chatRuntimeContextProviderName),
		runtimeSessionContextString(session, chatRuntimeContextModel),
	}
	for _, candidate := range candidates {
		if strings.Contains(strings.ToLower(candidate), query) {
			return true
		}
	}
	return false
}

func promptStartupSessionSelection(manager *runtimechat.SessionManager, userID string, filter ChatSessionListFilter) (*runtimechat.Session, bool, error) {
	return promptStartupSessionSelectionWithReader(manager, userID, filter, bufio.NewReader(os.Stdin))
}

func promptStartupSessionSelectionWithReader(manager *runtimechat.SessionManager, userID string, filter ChatSessionListFilter, reader *bufio.Reader) (*runtimechat.Session, bool, error) {
	sessions, err := listFilteredChatSessions(manager, userID, filter)
	if err != nil {
		return nil, false, err
	}
	if len(sessions) == 0 {
		return nil, true, nil
	}

	uiPrintSessionSelectionSummary(len(sessions), filter)
	optionWidth := startupSessionOptionLabelWidth()

	for {
		fmt.Printf("  %-*s %s\n", optionWidth, "[1]", "恢复最近会话")
		fmt.Printf("  %-*s %s\n", optionWidth, "[2]", "选择历史会话")
		fmt.Printf("  %-*s %s\n", optionWidth, "[3]", "新建会话")
		fmt.Print("请输入选项 (默认: 1): ")

		input, _ := reader.ReadString('\n')
		choice := strings.TrimSpace(input)
		switch choice {
		case "", "1":
			return sessions[0], false, nil
		case "2":
			return promptSelectSessionFromList(reader, sessions)
		case "3":
			return nil, true, nil
		default:
			ui.PrintWarning("无效的选择，请重新输入")
		}
	}
}

func promptSelectSessionFromList(reader *bufio.Reader, sessions []*runtimechat.Session) (*runtimechat.Session, bool, error) {
	if len(sessions) == 0 {
		return nil, true, nil
	}

	fmt.Println("历史会话:")
	now := time.Now()
	idWidth := startupSessionListIDWidth(sessions)
	stateWidth := startupSessionStateWidth(sessions)
	for index, session := range sessions {
		if session == nil {
			continue
		}
		preview := session.BuildPreview()
		title := preview.Title
		if title == "" {
			title = "(untitled)"
		}
		fmt.Printf("  [%-2d] %-*s [%-*s] 最后使用: %s\n",
			index+1,
			idWidth, session.ID,
			stateWidth, session.State,
			formatSessionLastUsed(session.UpdatedAt, now))
		fmt.Printf("       %s\n", title)
	}

	for {
		fmt.Print("请输入编号或会话 ID (默认: 1): ")

		input, _ := reader.ReadString('\n')
		choice := strings.TrimSpace(input)
		if choice == "" || choice == "1" {
			return sessions[0], false, nil
		}

		var index int
		if _, err := fmt.Sscanf(choice, "%d", &index); err == nil {
			if index >= 1 && index <= len(sessions) {
				return sessions[index-1], false, nil
			}
			ui.PrintWarning("无效的选择，请重新输入")
			continue
		}

		for _, session := range sessions {
			if session != nil && session.ID == choice {
				return session, false, nil
			}
		}

		ui.PrintWarning("未找到会话，请重新输入")
	}
}

func uiPrintSessionSelectionSummary(count int, filter ChatSessionListFilter) {
	fmt.Println()
	fmt.Println("检测到历史会话:")
	fmt.Printf("  %-12s %d\n", "匹配会话:", count)
	if filter.State != "" {
		fmt.Printf("  %-12s %s\n", "state:", filter.State)
	}
	if filter.Protocol != "" {
		fmt.Printf("  %-12s %s\n", "protocol:", filter.Protocol)
	}
	if filter.Provider != "" {
		fmt.Printf("  %-12s %s\n", "provider:", filter.Provider)
	}
	if filter.Model != "" {
		fmt.Printf("  %-12s %s\n", "model:", filter.Model)
	}
	if filter.Query != "" {
		fmt.Printf("  %-12s %s\n", "query:", filter.Query)
	}
}

func startupSessionOptionLabelWidth() int {
	return 4
}

func startupSessionListIDWidth(sessions []*runtimechat.Session) int {
	width := len("(untitled)")
	for _, session := range sessions {
		if session == nil {
			continue
		}
		if length := len(session.ID); length > width {
			width = length
		}
	}
	return width
}

func startupSessionStateWidth(sessions []*runtimechat.Session) int {
	width := len("active")
	for _, session := range sessions {
		if session == nil {
			continue
		}
		if length := len(session.State); length > width {
			width = length
		}
	}
	return width
}

func currentRuntimeSessionID(session *ChatSession) string {
	if session == nil || session.RuntimeSession == nil {
		return ""
	}
	return session.RuntimeSession.ID
}

func currentRuntimeSessionPath(session *ChatSession) string {
	if session == nil {
		return ""
	}
	sessionDir := resolveAbsoluteChatPath(session.SessionDir)
	sessionID := currentRuntimeSessionID(session)
	if sessionDir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(sessionDir, filepath.Base(strings.TrimSpace(sessionID))+".json")
}

func currentRuntimeSessionStoreSummary(session *ChatSession) string {
	sessionDir := ""
	if session != nil {
		sessionDir = resolveAbsoluteChatPath(session.SessionDir)
	}
	if sessionDir == "" {
		return ""
	}
	defaultDir := resolveAbsoluteChatPath(resolveDefaultChatSessionDir())
	if defaultDir == "" {
		return sessionDir
	}
	if pathWithinBaseDir(defaultDir, currentRuntimeSessionPath(session)) {
		return fmt.Sprintf("%s (default)", sessionDir)
	}
	return fmt.Sprintf("%s (custom; default %s)", sessionDir, defaultDir)
}

func resolveAbsoluteChatPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(resolved)
}

func pathWithinBaseDir(baseDir, targetPath string) bool {
	baseDir = resolveAbsoluteChatPath(baseDir)
	targetPath = resolveAbsoluteChatPath(targetPath)
	if baseDir == "" || targetPath == "" {
		return false
	}
	relative, err := filepath.Rel(baseDir, targetPath)
	if err != nil {
		return false
	}
	relative = filepath.Clean(relative)
	if relative == "." {
		return true
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func ensureRuntimeSessionCompatible(session *ChatSession, runtimeSession *runtimechat.Session) error {
	if session == nil || runtimeSession == nil {
		return nil
	}

	storedProtocol := runtimeSessionContextString(runtimeSession, chatRuntimeContextProtocol)
	currentProtocol := session.Provider.GetProtocol()
	if storedProtocol != "" && currentProtocol != "" && !strings.EqualFold(storedProtocol, currentProtocol) {
		return fmt.Errorf("会话协议为 %s，当前 provider 协议为 %s，无法在当前 chat 中恢复", storedProtocol, currentProtocol)
	}
	return nil
}

func runtimeSessionContextString(session *runtimechat.Session, key string) string {
	if session == nil {
		return ""
	}
	value, ok := session.GetContext(key)
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}

func runtimeSessionContextBool(session *runtimechat.Session, key string) (bool, bool) {
	if session == nil {
		return false, false
	}
	value, ok := session.GetContext(key)
	if !ok || value == nil {
		return false, false
	}
	boolean, ok := value.(bool)
	return boolean, ok
}

func ensureChatSystemPromptMessage(session *ChatSession) {
	syncChatSystemPromptMessage(session)
}

func composeChatSystemPromptWithGuidance(session *ChatSession) string {
	if session == nil {
		return ""
	}
	lines := make([]string, 0, 3)
	if base := strings.TrimSpace(session.SystemPromptText); base != "" {
		lines = append(lines, base)
	}
	if guidance := strings.TrimSpace(runtimeprompt.RenderShellExecutionGuidance()); guidance != "" {
		lines = append(lines, guidance)
	}
	if guidance := strings.TrimSpace(runtimeprompt.RenderFileEditingGuidance()); guidance != "" {
		lines = append(lines, guidance)
	}
	return strings.Join(lines, "\n\n")
}

func runtimeMessageFromAICLIMessage(raw map[string]interface{}) (runtimetypes.Message, error) {
	normalized := normalizeAICLIMessageMap(raw)
	recoverAssistantToolCallsFromReasoning(normalized)
	role, _ := normalized["role"].(string)
	role = strings.TrimSpace(role)
	if role == "" {
		return runtimetypes.Message{}, fmt.Errorf("message role cannot be empty")
	}

	message := runtimetypes.Message{
		Role:      role,
		Metadata:  runtimetypes.NewMetadata(),
		ToolCalls: decodeRuntimeToolCalls(normalized["tool_calls"]),
	}
	if content, ok := normalized["content"].(string); ok {
		message.Content = content
	}
	if toolCallID, ok := normalized["tool_call_id"].(string); ok {
		message.ToolCallID = toolCallID
	}
	if metadata, ok := normalized["metadata"].(map[string]interface{}); ok {
		for key, value := range metadata {
			if strings.TrimSpace(key) == "" {
				continue
			}
			message.Metadata[key] = value
		}
	}
	if reasoning, ok := normalized["reasoning_content"].(string); ok {
		message.Metadata.Set("reasoning_content", reasoning)
	}
	if reasoningBlock := runtimellm.ReasoningBlockFromAssistantMessage(normalized); reasoningBlock != nil {
		runtimetypes.SetReasoningBlock(message.Metadata, reasoningBlock)
		if text := strings.TrimSpace(reasoningBlock.DisplayText()); text != "" {
			message.Metadata.Set(chatcoreReasoningMetadataKey, text)
		}
	} else if reasoning, ok := normalized["reasoning_content"].(string); ok && strings.TrimSpace(reasoning) != "" {
		message.Metadata.Set(chatcoreReasoningMetadataKey, strings.TrimSpace(reasoning))
		runtimetypes.SetReasoningBlock(message.Metadata, &runtimetypes.ReasoningBlock{
			Summary:    strings.TrimSpace(reasoning),
			Visibility: runtimetypes.ReasoningVisibilitySummary,
		})
	}
	return message, nil
}

func recoverAssistantToolCallsFromReasoning(normalized map[string]interface{}) {
	if len(normalized) == 0 {
		return
	}
	role, _ := normalized["role"].(string)
	if !strings.EqualFold(strings.TrimSpace(role), "assistant") {
		return
	}

	existing := decodeRuntimeToolCalls(normalized["tool_calls"])
	recovered := decodeRuntimeToolCallsFromCodexOutputItems(normalized)
	if len(recovered) <= len(existing) {
		return
	}
	normalized["tool_calls"] = encodeRuntimeToolCalls(recovered)
}

func decodeRuntimeToolCallsFromCodexOutputItems(normalized map[string]interface{}) []runtimetypes.ToolCall {
	if len(normalized) == 0 {
		return nil
	}
	block := runtimellm.ReasoningBlockFromAssistantMessage(normalized)
	if block == nil || len(block.Metadata) == 0 {
		return nil
	}
	items := normalizeMapSlice(block.Metadata["response_output_items"])
	if len(items) == 0 {
		return nil
	}

	result := make([]runtimetypes.ToolCall, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		itemType, _ := item["type"].(string)
		if !strings.EqualFold(strings.TrimSpace(itemType), "function_call") {
			continue
		}

		call := runtimetypes.ToolCall{}
		if id, ok := item["call_id"].(string); ok {
			call.ID = strings.TrimSpace(id)
		} else if id, ok := item["id"].(string); ok {
			call.ID = strings.TrimSpace(id)
		}
		if name, ok := item["name"].(string); ok {
			call.Name = strings.TrimSpace(name)
		}
		switch args := item["arguments"].(type) {
		case map[string]interface{}:
			call.Args = args
		case string:
			call.Args = decodeToolArguments(args)
		}
		if fn, ok := item["function"].(map[string]interface{}); ok {
			if call.Name == "" {
				if name, ok := fn["name"].(string); ok {
					call.Name = strings.TrimSpace(name)
				}
			}
			switch args := fn["arguments"].(type) {
			case map[string]interface{}:
				call.Args = args
			case string:
				call.Args = decodeToolArguments(args)
			}
		}
		if call.Name != "" {
			result = append(result, call)
		}
	}
	return result
}

func aicliMessageFromRuntimeMessage(message runtimetypes.Message) (map[string]interface{}, error) {
	if strings.TrimSpace(message.Role) == "" {
		return nil, fmt.Errorf("message role cannot be empty")
	}

	raw := map[string]interface{}{
		"role":    strings.TrimSpace(message.Role),
		"content": message.Content,
	}
	if message.ToolCallID != "" {
		raw["tool_call_id"] = message.ToolCallID
	}
	if len(message.ToolCalls) > 0 {
		raw["tool_calls"] = encodeRuntimeToolCalls(message.ToolCalls)
	}
	if block := runtimetypes.GetReasoningBlock(message.Metadata); block != nil {
		if encoded := block.ToMap(); len(encoded) > 0 {
			raw["reasoning_details"] = encoded
		}
		if text := strings.TrimSpace(block.DisplayText()); text != "" {
			raw["reasoning_content"] = text
		}
	}
	if value, exists := message.Metadata["reasoning_content"]; exists {
		raw["reasoning_content"] = value
	}
	if value, exists := message.Metadata["finish_reason"]; exists {
		raw["finish_reason"] = value
	}
	mergeAICLIMessageMetadata(raw, message.Metadata)
	return raw, nil
}

func normalizeAICLIMessageMap(raw map[string]interface{}) map[string]interface{} {
	if len(raw) == 0 {
		return map[string]interface{}{}
	}

	data, err := json.Marshal(raw)
	if err != nil {
		cloned := make(map[string]interface{}, len(raw))
		for key, value := range raw {
			cloned[key] = value
		}
		return cloned
	}

	var cloned map[string]interface{}
	if err := json.Unmarshal(data, &cloned); err != nil || cloned == nil {
		cloned = make(map[string]interface{}, len(raw))
		for key, value := range raw {
			cloned[key] = value
		}
	}

	if normalizedCalls := normalizeMapSlice(cloned["tool_calls"]); len(normalizedCalls) > 0 {
		cloned["tool_calls"] = normalizedCalls
	}
	return cloned
}

func normalizeMapSlice(raw interface{}) []map[string]interface{} {
	switch typed := raw.(type) {
	case []map[string]interface{}:
		return typed
	case []interface{}:
		result := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			if value, ok := item.(map[string]interface{}); ok {
				result = append(result, value)
			}
		}
		return result
	default:
		return nil
	}
}

func mergeAICLIMessageMetadata(raw map[string]interface{}, metadata runtimetypes.Metadata) {
	exported := exportRuntimeMessageMetadata(metadata)
	if len(exported) == 0 {
		return
	}
	existing, _ := raw["metadata"].(map[string]interface{})
	if existing == nil {
		raw["metadata"] = exported
		return
	}
	for key, value := range exported {
		if _, ok := existing[key]; ok {
			continue
		}
		existing[key] = value
	}
}

func exportRuntimeMessageMetadata(metadata runtimetypes.Metadata) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	exported := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		exported[key] = value
	}
	if len(exported) == 0 {
		return nil
	}
	return exported
}

func decodeRuntimeToolCalls(raw interface{}) []runtimetypes.ToolCall {
	items := normalizeMapSlice(raw)
	if len(items) == 0 {
		return nil
	}

	result := make([]runtimetypes.ToolCall, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}

		call := runtimetypes.ToolCall{}
		if id, ok := item["id"].(string); ok {
			call.ID = id
		}
		if name, ok := item["name"].(string); ok {
			call.Name = name
		}

		switch args := item["arguments"].(type) {
		case map[string]interface{}:
			call.Args = args
		case string:
			call.Args = decodeToolArguments(args)
		}

		if fn, ok := item["function"].(map[string]interface{}); ok {
			if call.Name == "" {
				call.Name, _ = fn["name"].(string)
			}
			switch args := fn["arguments"].(type) {
			case map[string]interface{}:
				call.Args = args
			case string:
				call.Args = decodeToolArguments(args)
			}
		}

		if call.Name != "" {
			result = append(result, call)
		}
	}
	return result
}

func encodeRuntimeToolCalls(calls []runtimetypes.ToolCall) []map[string]interface{} {
	if len(calls) == 0 {
		return nil
	}

	result := make([]map[string]interface{}, 0, len(calls))
	for _, call := range calls {
		argsJSON := "{}"
		if len(call.Args) > 0 {
			if data, err := json.Marshal(call.Args); err == nil {
				argsJSON = string(data)
			}
		}
		result = append(result, map[string]interface{}{
			"id":   call.ID,
			"type": "function",
			"function": map[string]interface{}{
				"name":      call.Name,
				"arguments": argsJSON,
			},
		})
	}
	return result
}

func decodeToolArguments(raw string) map[string]interface{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]interface{}{}
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return map[string]interface{}{
			"_raw":         raw,
			"_parse_error": err.Error(),
		}
	}
	return args
}

func blankToDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func formatSessionLastUsed(updatedAt time.Time, now time.Time) string {
	if updatedAt.IsZero() {
		return "-"
	}
	return fmt.Sprintf("%s (%s)", updatedAt.Format("2006-01-02 15:04"), formatSessionRelativeTime(updatedAt, now))
}

func formatSessionRelativeTime(updatedAt time.Time, now time.Time) string {
	if updatedAt.IsZero() {
		return "-"
	}
	delta := now.Sub(updatedAt)
	suffix := "前"
	if delta < 0 {
		delta = -delta
		suffix = "后"
	}

	if delta < time.Minute {
		if suffix == "前" {
			return "刚刚"
		}
		return "即将"
	}
	if delta < time.Hour {
		return fmt.Sprintf("%d分钟%s", int(delta.Minutes()), suffix)
	}
	if delta < 24*time.Hour {
		return fmt.Sprintf("%d小时%s", int(delta.Hours()), suffix)
	}
	return fmt.Sprintf("%d天%s", int(delta.Hours()/24), suffix)
}
