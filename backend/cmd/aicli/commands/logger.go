package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

const (
	chatLogRetainedMessages = 1024
	chatLogContentMaxBytes  = 16 * 1024
	chatLogRawMaxBytes      = 32 * 1024
)

type aicliLogScope struct {
	TurnID    string
	RequestID string
}

// ChatLogDetail 聊天日志详细信息
type ChatLogDetail struct {
	Timestamp      time.Time       `json:"timestamp"`
	MessageType    string          `json:"message_type"` // "request", "response", "tool_call", "tool_result", "tool_execution_summary"
	TurnID         string          `json:"turn_id,omitempty"`
	RequestID      string          `json:"request_id,omitempty"`
	ToolCallID     string          `json:"tool_call_id,omitempty"`
	Content        interface{}     `json:"content"`
	RawContent     string          `json:"raw_content,omitempty"`      // SSE/流式原始文本（data: {...} 格式）
	RawContentJSON json.RawMessage `json:"raw_content_json,omitempty"` // 非流式原始 JSON 对象
	Error          string          `json:"error,omitempty"`
	Duration       int64           `json:"duration_ms,omitempty"`
}

// ChatSessionLog 聊天会话日志
type ChatSessionLog struct {
	SessionID         string              `json:"session_id"`
	RuntimeSessionID  string              `json:"runtime_session_id,omitempty"`
	Title             string              `json:"title,omitempty"`
	WorkingDirectory  string              `json:"working_directory,omitempty"`
	ProjectPath       string              `json:"project_path,omitempty"`
	StartTime         time.Time           `json:"start_time"`
	EndTime           time.Time           `json:"end_time,omitempty"`
	LastObservedAt    time.Time           `json:"last_observed_at,omitempty"`
	Status            string              `json:"status,omitempty"`
	TerminationReason string              `json:"termination_reason,omitempty"`
	Provider          string              `json:"provider"`
	Protocol          string              `json:"protocol"`
	Model             string              `json:"model"`
	BaseURL           string              `json:"base_url,omitempty"`
	Stream            bool                `json:"stream"`
	InitialMessage    string              `json:"initial_message,omitempty"`
	Messages          []ChatLogDetail     `json:"messages"`
	DroppedMessages   int                 `json:"dropped_messages,omitempty"`
	SessionSummary    *ChatSessionSummary `json:"summary,omitempty"`
}

// ChatSessionSummary 会话摘要信息
type ChatSessionSummary struct {
	TotalRequests         int            `json:"total_requests"`
	TotalResponses        int            `json:"total_responses"`
	TotalToolCalls        int            `json:"total_tool_calls"`
	TotalTokens           int            `json:"total_tokens,omitempty"`
	AverageResponseTimeMs int64          `json:"average_response_time_ms"`
	TotalDurationMs       int64          `json:"total_duration_ms"`
	UsageInfo             map[string]int `json:"usage_info,omitempty"` // 存储各次调用的 usage
}

// ChatLogger 聊天日志记录器
type ChatLogger struct {
	sessionID       string
	logDir          string
	sessionLog      *ChatSessionLog
	currentReqIndex int // 当前请求日志索引（用于更新 duration）
	totalRequests   int
	totalResponses  int
	totalToolCalls  int
	responseTimeMS  int64
}

// NewChatLogger 创建新的聊天日志记录器
func NewChatLogger(provider, protocol, model string, stream bool, baseURL string) *ChatLogger {
	sessionID := newChatLogSessionID()
	now := time.Now()
	workingDirectory, projectPath := currentChatProjectContext()
	return &ChatLogger{
		sessionID: sessionID,
		logDir:    resolveDefaultChatLogDir(),
		sessionLog: &ChatSessionLog{
			SessionID:        sessionID,
			WorkingDirectory: workingDirectory,
			ProjectPath:      projectPath,
			StartTime:        now,
			LastObservedAt:   now,
			Status:           "active",
			Provider:         provider,
			Protocol:         protocol,
			Model:            model,
			BaseURL:          baseURL,
			Stream:           stream,
			Messages:         []ChatLogDetail{},
		},
		currentReqIndex: -1,
	}
}

func newChatLogSessionID() string {
	shortID := strings.ReplaceAll(uuid.NewString(), "-", "")
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	return time.Now().Format("20060102_150405.000") + "_" + shortID
}

// RotateSession ends the current chat-log session (best effort) and starts a
// fresh log/artifact session while keeping the same log root and provider
// metadata. Used by /new so diagnostic paths match the new runtime conversation.
func (cl *ChatLogger) RotateSession() error {
	if cl == nil {
		return nil
	}

	logDir := strings.TrimSpace(cl.logDir)
	provider, protocol, model, baseURL := "", "", "", ""
	stream := false
	if cl.sessionLog != nil {
		provider = cl.sessionLog.Provider
		protocol = cl.sessionLog.Protocol
		model = cl.sessionLog.Model
		baseURL = cl.sessionLog.BaseURL
		stream = cl.sessionLog.Stream
		if logDir != "" {
			// Best-effort close of the previous chat log so /new never loses
			// diagnostics when rotation itself succeeds. Skip empty shells to
			// avoid creating orphan chat_*.json files for unused sessions.
			if len(cl.sessionLog.Messages) > 0 || cl.totalRequests > 0 || cl.totalResponses > 0 || cl.totalToolCalls > 0 {
				_ = cl.SaveSession()
			}
		}
	}

	sessionID := newChatLogSessionID()
	now := time.Now()
	workingDirectory, projectPath := currentChatProjectContext()
	cl.sessionID = sessionID
	cl.currentReqIndex = -1
	cl.totalRequests = 0
	cl.totalResponses = 0
	cl.totalToolCalls = 0
	cl.responseTimeMS = 0
	cl.sessionLog = &ChatSessionLog{
		SessionID:        sessionID,
		WorkingDirectory: workingDirectory,
		ProjectPath:      projectPath,
		StartTime:        now,
		LastObservedAt:   now,
		Status:           "active",
		Provider:         provider,
		Protocol:         protocol,
		Model:            model,
		BaseURL:          baseURL,
		Stream:           stream,
		Messages:         []ChatLogDetail{},
	}
	if logDir == "" {
		return nil
	}
	return cl.ensureSessionArtifactLayout()
}

func currentChatProjectContext() (workingDirectory, projectPath string) {
	cwd, err := os.Getwd()
	if err != nil || strings.TrimSpace(cwd) == "" {
		return "", ""
	}
	workingDirectory = filepath.Clean(cwd)
	projectPath = findGitRoot(workingDirectory)
	if projectPath == "" {
		projectPath = workingDirectory
	}
	return workingDirectory, filepath.Clean(projectPath)
}

// SetLogDir 设置日志保存目录
func (cl *ChatLogger) SetLogDir(dir string) error {
	// 创建目录
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建日志目录失败: %w", err)
	}
	cl.logDir = dir
	if err := cl.ensureSessionArtifactLayout(); err != nil {
		return err
	}
	return nil
}

func (cl *ChatLogger) ensureSessionArtifactLayout() error {
	if cl == nil || strings.TrimSpace(cl.logDir) == "" || cl.sessionLog == nil {
		return nil
	}

	// 新布局：chat-logs/YYYY/MM/DD/<session-id>.{json,debug.log,http,shell,images,exports}
	for _, subDir := range []string{
		filepath.Dir(cl.SessionLogPath()),
		cl.RuntimeHTTPArtifactDir(),
		cl.LocalShellArtifactDir(),
		cl.GeneratedImagesDir(),
		cl.ExportsDir(),
	} {
		if strings.TrimSpace(subDir) == "" {
			continue
		}
		if err := os.MkdirAll(subDir, 0755); err != nil {
			return fmt.Errorf("创建会话 artifact 目录失败: %w", err)
		}
	}

	debugLogPath := cl.DebugLogPath()
	file, err := os.OpenFile(debugLogPath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("创建调试日志文件失败: %w", err)
	}
	if closeErr := file.Close(); closeErr != nil {
		return fmt.Errorf("关闭调试日志文件失败: %w", closeErr)
	}
	return nil
}

// LogRequest 记录请求
func (cl *ChatLogger) LogRequest(scope aicliLogScope, content interface{}) {
	detail := ChatLogDetail{
		Timestamp:   time.Now(),
		MessageType: "request",
		TurnID:      scope.TurnID,
		RequestID:   scope.RequestID,
		Content:     boundChatLogContent(content),
	}
	cl.appendDetail(detail)
	cl.currentReqIndex = len(cl.sessionLog.Messages) - 1
}

// LogResponse 记录响应
func (cl *ChatLogger) LogResponse(scope aicliLogScope, content interface{}, raw []byte, isStream bool, err error, durationMs int64) {
	detail := ChatLogDetail{
		Timestamp:   time.Now(),
		MessageType: "response",
		TurnID:      scope.TurnID,
		RequestID:   scope.RequestID,
		Content:     boundChatLogContent(content),
		Duration:    durationMs,
	}
	if err != nil {
		detail.Error = err.Error()
	}
	if raw != nil {
		raw = boundedTailBytes(raw, chatLogRawMaxBytes)
		if isStream {
			// SSE/流式格式：保存为字符串
			detail.RawContent = string(raw)
		} else {
			// 非 SSE 格式：优先保存 JSON；若上游返回纯文本/HTML，则退回字符串，避免日志序列化失败。
			if json.Valid(raw) {
				detail.RawContentJSON = json.RawMessage(raw)
			} else {
				detail.RawContent = string(raw)
			}
		}
	}
	cl.appendDetail(detail)

	// 更新当前请求的 duration（如果需要）
	if cl.currentReqIndex >= 0 && cl.currentReqIndex < len(cl.sessionLog.Messages) {
		cl.sessionLog.Messages[cl.currentReqIndex].Duration = durationMs
		cl.currentReqIndex = -1
	}

	// 更新摘要
	cl.updateSummary(content, durationMs)
}

// LogToolCall 记录 Function Call
func (cl *ChatLogger) LogToolCall(scope aicliLogScope, toolCallID, function string, args interface{}) {
	detail := ChatLogDetail{
		Timestamp:   time.Now(),
		MessageType: "tool_call",
		TurnID:      scope.TurnID,
		RequestID:   scope.RequestID,
		ToolCallID:  toolCallID,
		Content: boundChatLogContent(map[string]interface{}{
			"function": function,
			"args":     args,
		}),
	}
	cl.appendDetail(detail)
}

// LogToolResult 记录工具执行结果
func (cl *ChatLogger) LogToolResult(scope aicliLogScope, toolCallID, function string, result interface{}, err error) {
	detail := ChatLogDetail{
		Timestamp:   time.Now(),
		MessageType: "tool_result",
		TurnID:      scope.TurnID,
		RequestID:   scope.RequestID,
		ToolCallID:  toolCallID,
		Content: boundChatLogContent(map[string]interface{}{
			"function": function,
			"result":   result,
		}),
	}
	if err != nil {
		detail.Error = err.Error()
	}
	cl.appendDetail(detail)
}

// LogToolExecutionSummary 记录一次工具执行批次的聚合摘要
func (cl *ChatLogger) LogToolExecutionSummary(scope aicliLogScope, summary interface{}) {
	detail := ChatLogDetail{
		Timestamp:   time.Now(),
		MessageType: "tool_execution_summary",
		TurnID:      scope.TurnID,
		RequestID:   scope.RequestID,
		Content:     boundChatLogContent(summary),
	}
	cl.appendDetail(detail)
}

func (cl *ChatLogger) appendDetail(detail ChatLogDetail) {
	if cl == nil || cl.sessionLog == nil {
		return
	}
	switch detail.MessageType {
	case "request":
		cl.totalRequests++
	case "response":
		cl.totalResponses++
		cl.responseTimeMS += detail.Duration
	case "tool_call":
		cl.totalToolCalls++
	}
	cl.sessionLog.Messages = append(cl.sessionLog.Messages, detail)
	if len(cl.sessionLog.Messages) <= chatLogRetainedMessages {
		return
	}
	drop := len(cl.sessionLog.Messages) - chatLogRetainedMessages
	cl.sessionLog.DroppedMessages += drop
	copy(cl.sessionLog.Messages, cl.sessionLog.Messages[drop:])
	clear(cl.sessionLog.Messages[len(cl.sessionLog.Messages)-drop:])
	cl.sessionLog.Messages = cl.sessionLog.Messages[:chatLogRetainedMessages]
	if cl.currentReqIndex >= 0 {
		cl.currentReqIndex -= drop
		if cl.currentReqIndex < 0 {
			cl.currentReqIndex = -1
		}
	}
}

func boundChatLogContent(content interface{}) interface{} {
	if content == nil {
		return nil
	}
	if text, ok := content.(string); ok {
		if len(text) <= chatLogContentMaxBytes {
			return text
		}
		return map[string]interface{}{
			"truncated":  true,
			"byte_count": len(text),
			"preview":    truncateUTF8Bytes(text, chatLogContentMaxBytes),
		}
	}
	payload, err := json.Marshal(content)
	if err != nil || len(payload) <= chatLogContentMaxBytes {
		return content
	}
	bounded := map[string]interface{}{
		"truncated":  true,
		"byte_count": len(payload),
		"preview":    truncateUTF8ByteSlice(payload, chatLogContentMaxBytes-4096),
	}
	for key, value := range chatLogDiagnosticEnvelope(payload) {
		bounded[key] = value
	}
	return bounded
}

func chatLogDiagnosticEnvelope(payload []byte) map[string]interface{} {
	var source map[string]interface{}
	if json.Unmarshal(payload, &source) != nil {
		return nil
	}
	preserved := make(map[string]interface{})
	for key, value := range source {
		if !isChatLogDiagnosticKey(key) || !isChatLogDiagnosticScalar(value) {
			continue
		}
		preserved[key] = value
	}
	if _, hasError := source["error"]; hasError {
		preserved["error_present"] = true
	}
	if len(preserved) > 0 {
		preserved["diagnostic_envelope_preserved"] = true
	}
	return preserved
}

func isChatLogDiagnosticKey(key string) bool {
	key = strings.TrimSpace(key)
	if strings.HasPrefix(key, "usage_") {
		return true
	}
	switch key {
	case "event_type", "source", "success", "provider", "model", "step",
		"trace_id", "logical_turn_id", "llm_request_id", "stream_id",
		"tool_call_count", "context_prompt_tokens", "context_window_tokens",
		"prompt_budget", "executor_path":
		return true
	default:
		return false
	}
}

func isChatLogDiagnosticScalar(value interface{}) bool {
	switch value.(type) {
	case string, bool, float64, json.Number:
		return true
	default:
		return false
	}
}

func boundedTailBytes(raw []byte, limit int) []byte {
	if limit <= 0 || len(raw) <= limit {
		return append([]byte(nil), raw...)
	}
	return append([]byte(nil), raw[len(raw)-limit:]...)
}

// WriteDebugInfo 写入调试信息到单独的日志文件
func (cl *ChatLogger) WriteDebugInfo(logDir, debugInfo string) error {
	// 如果传入的 logDir 为空，使用 logger 自身的 logDir
	if logDir == "" {
		logDir = cl.logDir
	}
	if logDir == "" {
		return nil // 没有设置日志目录，跳过
	}

	// 确定调试日志路径（新布局 <sid>.debug.log）
	debugLogPath := cl.debugLogPathFor(logDir)
	if debugLogPath == "" {
		return fmt.Errorf("调试日志路径为空")
	}
	// 创建会话分区目录
	if err := os.MkdirAll(filepath.Dir(debugLogPath), 0755); err != nil {
		return fmt.Errorf("创建会话目录失败: %w", err)
	}

	// 追加写入调试信息
	file, err := os.OpenFile(debugLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("打开调试日志文件失败: %w", err)
	}
	defer file.Close()

	// 写入带时间戳的调试信息
	timestamp := time.Now().Format("2006-01-02 15:04:05.000")
	if _, err := file.WriteString(fmt.Sprintf("[%s] %s\n", timestamp, debugInfo)); err != nil {
		return fmt.Errorf("写入调试日志失败: %w", err)
	}

	return nil
}

// SetInitialMessage 设置初始消息
func (cl *ChatLogger) SetInitialMessage(msg string) {
	if cl == nil || cl.sessionLog == nil {
		return
	}
	cl.sessionLog.InitialMessage = truncateUTF8Bytes(msg, chatLogContentMaxBytes)
}

// SetRuntimeSessionMetadata links diagnostics to the durable conversation.
func (cl *ChatLogger) SetRuntimeSessionMetadata(sessionID, title string) {
	if cl == nil || cl.sessionLog == nil {
		return
	}
	cl.sessionLog.RuntimeSessionID = strings.TrimSpace(sessionID)
	cl.sessionLog.Title = truncateUTF8Bytes(strings.Join(strings.Fields(title), " "), 512)
}

// EndSession 结束会话
func (cl *ChatLogger) EndSession() {
	if cl == nil || cl.sessionLog == nil {
		return
	}
	now := time.Now()
	cl.sessionLog.EndTime = now
	cl.sessionLog.LastObservedAt = now
	cl.sessionLog.Status, cl.sessionLog.TerminationReason = cl.inferTerminalStatus()
}

// FailSession records a terminal failure and persists the final snapshot.
func (cl *ChatLogger) FailSession(terminalErr error) error {
	if cl == nil || cl.sessionLog == nil {
		return nil
	}
	now := time.Now()
	cl.sessionLog.EndTime = now
	cl.sessionLog.LastObservedAt = now
	cl.sessionLog.Status = "failed"
	if terminalErr != nil {
		cl.sessionLog.TerminationReason = truncateUTF8Bytes(strings.TrimSpace(terminalErr.Error()), chatLogContentMaxBytes)
	} else {
		cl.sessionLog.TerminationReason = ""
	}
	return cl.FlushSession()
}

func (cl *ChatLogger) inferTerminalStatus() (string, string) {
	if cl == nil || cl.sessionLog == nil {
		return "completed", ""
	}
	for index := len(cl.sessionLog.Messages) - 1; index >= 0; index-- {
		message := cl.sessionLog.Messages[index]
		if message.MessageType != "response" {
			continue
		}
		if terminalErr := strings.TrimSpace(message.Error); terminalErr != "" {
			return "failed", truncateUTF8Bytes(terminalErr, chatLogContentMaxBytes)
		}
		return "completed", ""
	}
	return "completed", ""
}

// FlushSession 刷新保存会话（不结束会话，即不设置 EndTime）
// 用于在每次对话后实时保存日志，防止数据丢失
func (cl *ChatLogger) FlushSession() error {
	if cl == nil || cl.sessionLog == nil {
		return fmt.Errorf("聊天日志未初始化")
	}
	if cl.logDir == "" {
		return fmt.Errorf("日志目录未设置，调用 SetLogDir 方法设置")
	}

	now := time.Now()
	if cl.sessionLog.LastObservedAt.IsZero() || now.After(cl.sessionLog.LastObservedAt) {
		cl.sessionLog.LastObservedAt = now
	}
	if cl.sessionLog.EndTime.IsZero() && strings.TrimSpace(cl.sessionLog.Status) == "" {
		cl.sessionLog.Status = "active"
	}

	cl.sanitizeRawJSONMessages()

	// 更新摘要
	cl.sessionLog.SessionSummary = cl.calculateSummary()

	logPath := cl.SessionLogPath()
	if logPath == "" {
		return fmt.Errorf("会话日志路径为空")
	}
	// 创建会话分区目录
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return fmt.Errorf("创建会话目录失败: %w", err)
	}

	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("打开会话日志失败: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(cl.sessionLog); err != nil {
		_ = file.Close()
		return fmt.Errorf("序列化会话日志失败: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭会话日志失败: %w", err)
	}

	return nil
}

func (cl *ChatLogger) sanitizeRawJSONMessages() {
	if cl == nil || cl.sessionLog == nil {
		return
	}
	for i := range cl.sessionLog.Messages {
		msg := &cl.sessionLog.Messages[i]
		if len(msg.RawContentJSON) == 0 {
			continue
		}
		if json.Valid(msg.RawContentJSON) {
			continue
		}
		msg.RawContent = string(msg.RawContentJSON)
		msg.RawContentJSON = nil
	}
}

// SaveSession 保存会话（结束会话并保存）
func (cl *ChatLogger) SaveSession() error {
	// 结束会话（设置 EndTime）
	cl.EndSession()
	// 调用 FlushSession 保存数据
	return cl.FlushSession()
}

// CurrentSummary 返回当前摘要快照。
func (cl *ChatLogger) CurrentSummary() *ChatSessionSummary {
	if cl == nil || cl.sessionLog == nil {
		return nil
	}
	summary := cl.calculateSummary()
	if summary == nil {
		return nil
	}
	cloned := *summary
	if summary.UsageInfo != nil {
		cloned.UsageInfo = make(map[string]int, len(summary.UsageInfo))
		for key, value := range summary.UsageInfo {
			cloned.UsageInfo[key] = value
		}
	}
	return &cloned
}

// SessionLogPath 返回当前会话日志路径（chat-logs/YYYY/MM/DD/<session-id>.json）。
func (cl *ChatLogger) SessionLogPath() string {
	return cl.sessionArtifactPath(cl.sessionPathBase() + ".json")
}

// DebugLogPath 返回当前会话调试日志路径（<session-id>.debug.log）。
func (cl *ChatLogger) DebugLogPath() string {
	return cl.sessionArtifactPath(cl.sessionPathBase() + ".debug.log")
}

// RuntimeHTTPArtifactDir 返回 runtime HTTP artifact 目录（<session-id>.http）。
func (cl *ChatLogger) RuntimeHTTPArtifactDir() string {
	return cl.sessionArtifactPath(cl.sessionPathBase() + ".http")
}

// LocalShellArtifactDir 返回本地 shell 原始输出 artifact 目录（<session-id>.shell）。
func (cl *ChatLogger) LocalShellArtifactDir() string {
	return cl.sessionArtifactPath(cl.sessionPathBase() + ".shell")
}

// GeneratedImagesDir 返回生成图片 artifact 目录（<session-id>.images）。
func (cl *ChatLogger) GeneratedImagesDir() string {
	return cl.sessionArtifactPath(cl.sessionPathBase() + ".images")
}

// ExportsDir 返回导出文件目录（<session-id>.exports）。
func (cl *ChatLogger) ExportsDir() string {
	return cl.sessionArtifactPath(cl.sessionPathBase() + ".exports")
}

// RuntimeEventsDir 返回 runtime 事件文件目录（<session-id>.events）。
func (cl *ChatLogger) RuntimeEventsDir() string {
	return cl.sessionArtifactPath(cl.sessionPathBase() + ".events")
}

// updateSummary 更新会话摘要
func (cl *ChatLogger) updateSummary(content interface{}, durationMs int64) {
	// 解析响应中的 usage 信息
	totalTokens := usageTotalTokensFromLogContent(content)
	if totalTokens <= 0 {
		return
	}
	if cl.sessionLog.SessionSummary == nil {
		cl.sessionLog.SessionSummary = &ChatSessionSummary{
			UsageInfo: make(map[string]int),
		}
	} else if cl.sessionLog.SessionSummary.UsageInfo == nil {
		cl.sessionLog.SessionSummary.UsageInfo = make(map[string]int)
	}
	cl.sessionLog.SessionSummary.TotalTokens += totalTokens
}

// calculateSummary 计算会话摘要
func (cl *ChatLogger) calculateSummary() *ChatSessionSummary {
	summary := &ChatSessionSummary{
		TotalRequests:  cl.totalRequests,
		TotalResponses: cl.totalResponses,
		TotalToolCalls: cl.totalToolCalls,
	}

	totalResponseTime := cl.responseTimeMS
	messageCount := len(cl.sessionLog.Messages)

	if summary.TotalRequests == 0 && summary.TotalResponses == 0 && summary.TotalToolCalls == 0 {
		for i := 0; i < messageCount; i++ {
			msg := cl.sessionLog.Messages[i]
			switch msg.MessageType {
			case "request":
				summary.TotalRequests++
			case "response":
				summary.TotalResponses++
				totalResponseTime += msg.Duration
			case "tool_call":
				summary.TotalToolCalls++
			}
		}
	}

	// 计算平均响应时间
	if summary.TotalResponses > 0 {
		summary.AverageResponseTimeMs = totalResponseTime / int64(summary.TotalResponses)
	}

	// EndTime is intentionally empty for active sessions. Use the latest flush
	// observation so incremental logs still report an accurate elapsed duration.
	durationEnd := cl.sessionLog.EndTime
	if durationEnd.IsZero() {
		durationEnd = cl.sessionLog.LastObservedAt
	}
	if !durationEnd.IsZero() && !cl.sessionLog.StartTime.IsZero() && durationEnd.After(cl.sessionLog.StartTime) {
		summary.TotalDurationMs = durationEnd.Sub(cl.sessionLog.StartTime).Milliseconds()
	}

	// 从已有数据中获取 tokens 信息
	if cl.sessionLog.SessionSummary != nil {
		summary.TotalTokens = cl.sessionLog.SessionSummary.TotalTokens
		summary.UsageInfo = cl.sessionLog.SessionSummary.UsageInfo
	}
	if summary.TotalTokens == 0 {
		summary.TotalTokens = cl.extractTotalTokensFromMessages()
	}

	return summary
}

// sessionPathBase 返回会话文件与 artifact 命名的基名：
// 仅将 sessionID 中的点号/空格替换为下划线，避免 Windows 路径歧义，
// 保留日期分区可解析前缀（YYYYMMDD_HHMMSS）。
func (cl *ChatLogger) sessionPathBase() string {
	if cl == nil {
		return ""
	}
	return strings.NewReplacer(".", "_", " ", "_").Replace(strings.TrimSpace(cl.sessionID))
}

// partitionAt 返回会话的日期分区基准时间。
func (cl *ChatLogger) partitionAt() time.Time {
	if cl != nil && cl.sessionLog != nil && !cl.sessionLog.StartTime.IsZero() {
		return cl.sessionLog.StartTime
	}
	if cl != nil {
		if parsed, ok := aiclipaths.ParseTimestampedSessionIDTime(cl.sessionID); ok {
			return parsed
		}
	}
	return time.Now()
}

// sessionArtifactPath 在 logDir 的 YYYY/MM/DD 日期分区下拼接 leaf（如 "<sid>.json"）。
func (cl *ChatLogger) sessionArtifactPath(leaf string) string {
	if cl == nil || strings.TrimSpace(cl.logDir) == "" || strings.TrimSpace(leaf) == "" {
		return ""
	}
	if cl.sessionPathBase() == "" {
		return ""
	}
	return aiclipaths.JoinDatePartition(cl.logDir, cl.partitionAt(), leaf)
}

// debugLogPathFor 返回 debug 日志路径，支持外部传入的 logDir（兼容 WriteDebugInfo 用法）。
func (cl *ChatLogger) debugLogPathFor(logDir string) string {
	if cl == nil || strings.TrimSpace(cl.sessionID) == "" || strings.TrimSpace(logDir) == "" {
		return ""
	}
	base := cl.sessionPathBase()
	if logDir == cl.logDir {
		return cl.DebugLogPath()
	}
	return aiclipaths.JoinDatePartition(logDir, cl.partitionAt(), base+".debug.log")
}

func (cl *ChatLogger) extractTotalTokensFromMessages() int {
	if cl == nil || cl.sessionLog == nil {
		return 0
	}

	total := 0
	for _, msg := range cl.sessionLog.Messages {
		if msg.MessageType != "response" {
			continue
		}
		if usageTotal := usageTotalTokensFromLogContent(msg.Content); usageTotal > 0 {
			total += usageTotal
			continue
		}
		if usageTotal := usageTotalTokensFromRawJSON(msg.RawContentJSON); usageTotal > 0 {
			total += usageTotal
		}
	}
	return total
}

func extractUsageFromLogContent(content interface{}) map[string]interface{} {
	payload, ok := content.(map[string]interface{})
	if !ok || payload == nil {
		return nil
	}
	if usage, ok := payload["usage"].(map[string]interface{}); ok && len(usage) > 0 {
		return usage
	}
	if total, ok := payloadIntValue(payload["usage_total_tokens"]); ok && total > 0 {
		return map[string]interface{}{
			"total_tokens": total,
		}
	}
	return nil
}

func extractUsageFromRawJSON(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	return extractUsageFromLogContent(payload)
}

func usageTotalTokens(usage map[string]interface{}) int {
	if len(usage) == 0 {
		return 0
	}
	if total, ok := usage["total_tokens"].(float64); ok {
		return int(total)
	}
	if total, ok := usage["total_tokens"].(int); ok {
		return total
	}
	return 0
}

func usageTotalTokensFromLogContent(content interface{}) int {
	return usageTotalTokens(extractUsageFromLogContent(content))
}

func usageTotalTokensFromRawJSON(raw json.RawMessage) int {
	return usageTotalTokens(extractUsageFromRawJSON(raw))
}

func payloadIntValue(value interface{}) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int8:
		return int(typed), true
	case int16:
		return int(typed), true
	case int32:
		return int(typed), true
	case int64:
		return int(typed), true
	case uint:
		return int(typed), true
	case uint8:
		return int(typed), true
	case uint16:
		return int(typed), true
	case uint32:
		return int(typed), true
	case uint64:
		return int(typed), true
	case float32:
		return int(typed), true
	case float64:
		return int(typed), true
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return int(parsed), true
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
