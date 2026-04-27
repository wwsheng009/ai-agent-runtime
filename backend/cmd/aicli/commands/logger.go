package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	SessionID      string              `json:"session_id"`
	StartTime      time.Time           `json:"start_time"`
	EndTime        time.Time           `json:"end_time,omitempty"`
	Provider       string              `json:"provider"`
	Protocol       string              `json:"protocol"`
	Model          string              `json:"model"`
	BaseURL        string              `json:"base_url,omitempty"`
	Stream         bool                `json:"stream"`
	InitialMessage string              `json:"initial_message,omitempty"`
	Messages       []ChatLogDetail     `json:"messages"`
	SessionSummary *ChatSessionSummary `json:"summary,omitempty"`
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
}

// NewChatLogger 创建新的聊天日志记录器
func NewChatLogger(provider, protocol, model string, stream bool, baseURL string) *ChatLogger {
	sessionID := time.Now().Format("20060102_150405")
	return &ChatLogger{
		sessionID: sessionID,
		logDir:    "", // 默认不保存，需要调用 SetLogDir 来设置
		sessionLog: &ChatSessionLog{
			SessionID: sessionID,
			StartTime: time.Now(),
			Provider:  provider,
			Protocol:  protocol,
			Model:     model,
			BaseURL:   baseURL,
			Stream:    stream,
			Messages:  []ChatLogDetail{},
		},
		currentReqIndex: -1,
	}
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

	sessionDir := filepath.Join(cl.logDir, cl.sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("创建会话目录失败: %w", err)
	}

	for _, subDir := range []string{
		filepath.Join(sessionDir, "runtime-http"),
		filepath.Join(sessionDir, "local-shell"),
	} {
		if err := os.MkdirAll(subDir, 0755); err != nil {
			return fmt.Errorf("创建会话 artifact 目录失败: %w", err)
		}
	}

	debugLogPath := filepath.Join(sessionDir, "debug.log")
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
		Content:     content,
	}
	cl.sessionLog.Messages = append(cl.sessionLog.Messages, detail)
	cl.currentReqIndex = len(cl.sessionLog.Messages) - 1
}

// LogResponse 记录响应
func (cl *ChatLogger) LogResponse(scope aicliLogScope, content interface{}, raw []byte, isStream bool, err error, durationMs int64) {
	detail := ChatLogDetail{
		Timestamp:   time.Now(),
		MessageType: "response",
		TurnID:      scope.TurnID,
		RequestID:   scope.RequestID,
		Content:     content,
		Duration:    durationMs,
	}
	if err != nil {
		detail.Error = err.Error()
	}
	if raw != nil {
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
	cl.sessionLog.Messages = append(cl.sessionLog.Messages, detail)

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
		Content: map[string]interface{}{
			"function": function,
			"args":     args,
		},
	}
	cl.sessionLog.Messages = append(cl.sessionLog.Messages, detail)
}

// LogToolResult 记录工具执行结果
func (cl *ChatLogger) LogToolResult(scope aicliLogScope, toolCallID, function string, result interface{}, err error) {
	detail := ChatLogDetail{
		Timestamp:   time.Now(),
		MessageType: "tool_result",
		TurnID:      scope.TurnID,
		RequestID:   scope.RequestID,
		ToolCallID:  toolCallID,
		Content: map[string]interface{}{
			"function": function,
			"result":   result,
		},
	}
	if err != nil {
		detail.Error = err.Error()
	}
	cl.sessionLog.Messages = append(cl.sessionLog.Messages, detail)
}

// LogToolExecutionSummary 记录一次工具执行批次的聚合摘要
func (cl *ChatLogger) LogToolExecutionSummary(scope aicliLogScope, summary interface{}) {
	detail := ChatLogDetail{
		Timestamp:   time.Now(),
		MessageType: "tool_execution_summary",
		TurnID:      scope.TurnID,
		RequestID:   scope.RequestID,
		Content:     summary,
	}
	cl.sessionLog.Messages = append(cl.sessionLog.Messages, detail)
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

	// 创建会话子目录
	sessionDir := filepath.Join(logDir, cl.sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("创建会话目录失败: %w", err)
	}

	// 调试日志文件名
	debugLogPath := filepath.Join(sessionDir, "debug.log")

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
	cl.sessionLog.InitialMessage = msg
}

// EndSession 结束会话
func (cl *ChatLogger) EndSession() {
	cl.sessionLog.EndTime = time.Now()
}

// FlushSession 刷新保存会话（不结束会话，即不设置 EndTime）
// 用于在每次对话后实时保存日志，防止数据丢失
func (cl *ChatLogger) FlushSession() error {
	if cl.logDir == "" {
		return fmt.Errorf("日志目录未设置，调用 SetLogDir 方法设置")
	}

	cl.sanitizeRawJSONMessages()

	// 更新摘要
	cl.sessionLog.SessionSummary = cl.calculateSummary()

	// 创建会话子目录
	sessionDir := filepath.Join(cl.logDir, cl.sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return fmt.Errorf("创建会话目录失败: %w", err)
	}

	// 序列化
	data, err := json.MarshalIndent(cl.sessionLog, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化会话日志失败: %w", err)
	}

	logPath := cl.buildLogPath()

	// 写入文件
	if err := os.WriteFile(logPath, data, 0644); err != nil {
		return fmt.Errorf("写入会话日志失败: %w", err)
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

// SessionDirPath 返回当前聊天日志会话目录。
func (cl *ChatLogger) SessionDirPath() string {
	if cl == nil || cl.logDir == "" || cl.sessionLog == nil {
		return ""
	}
	return filepath.Join(cl.logDir, cl.sessionID)
}

// SessionLogPath 返回当前会话日志路径。
func (cl *ChatLogger) SessionLogPath() string {
	if cl == nil || cl.logDir == "" || cl.sessionLog == nil {
		return ""
	}
	return cl.buildLogPath()
}

// DebugLogPath 返回当前会话调试日志路径。
func (cl *ChatLogger) DebugLogPath() string {
	sessionDir := cl.SessionDirPath()
	if sessionDir == "" {
		return ""
	}
	return filepath.Join(sessionDir, "debug.log")
}

// RuntimeHTTPArtifactDir 返回 runtime HTTP artifact 目录。
func (cl *ChatLogger) RuntimeHTTPArtifactDir() string {
	sessionDir := cl.SessionDirPath()
	if sessionDir == "" {
		return ""
	}
	return filepath.Join(sessionDir, "runtime-http")
}

// LocalShellArtifactDir 返回本地 shell 原始输出 artifact 目录。
func (cl *ChatLogger) LocalShellArtifactDir() string {
	sessionDir := cl.SessionDirPath()
	if sessionDir == "" {
		return ""
	}
	return filepath.Join(sessionDir, "local-shell")
}

// updateSummary 更新会话摘要
func (cl *ChatLogger) updateSummary(content interface{}, durationMs int64) {
	// 解析响应中的 usage 信息
	if resp, ok := content.(map[string]interface{}); ok {
		if usage, ok := resp["usage"].(map[string]interface{}); ok {
			if cl.sessionLog.SessionSummary == nil {
				cl.sessionLog.SessionSummary = &ChatSessionSummary{
					UsageInfo: make(map[string]int),
				}
			} else if cl.sessionLog.SessionSummary.UsageInfo == nil {
				cl.sessionLog.SessionSummary.UsageInfo = make(map[string]int)
			}

			// 累加 tokens
			if totalTokens, ok := usage["total_tokens"].(float64); ok {
				cl.sessionLog.SessionSummary.TotalTokens += int(totalTokens)
			}
			if totalTokens, ok := usage["total_tokens"].(int); ok {
				cl.sessionLog.SessionSummary.TotalTokens += totalTokens
			}
		}
	}
}

// calculateSummary 计算会话摘要
func (cl *ChatLogger) calculateSummary() *ChatSessionSummary {
	summary := &ChatSessionSummary{
		TotalRequests:  0,
		TotalResponses: 0,
		TotalToolCalls: 0,
	}

	var totalResponseTime int64
	messageCount := len(cl.sessionLog.Messages)

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

	// 计算平均响应时间
	if summary.TotalResponses > 0 {
		summary.AverageResponseTimeMs = totalResponseTime / int64(summary.TotalResponses)
	}

	// 总时长
	if !cl.sessionLog.EndTime.IsZero() {
		summary.TotalDurationMs = cl.sessionLog.EndTime.Sub(cl.sessionLog.StartTime).Milliseconds()
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

func (cl *ChatLogger) buildLogPath() string {
	if cl == nil || cl.sessionLog == nil {
		return ""
	}

	sanitize := func(s string) string {
		invalid := []rune{'<', '>', ':', '"', '/', '\\', '|', '?', '*'}
		for _, ch := range invalid {
			s = strings.ReplaceAll(s, string(ch), "_")
		}
		return s
	}

	filename := fmt.Sprintf("chat_%s_%s_%s_%s.json",
		sanitize(cl.sessionLog.Provider),
		sanitize(cl.sessionLog.Protocol),
		sanitize(cl.sessionLog.Model),
		cl.sessionID)
	return filepath.Join(cl.logDir, cl.sessionID, filename)
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
		if usage := extractUsageFromLogContent(msg.Content); len(usage) > 0 {
			total += usageTotalTokens(usage)
			continue
		}
		if usage := extractUsageFromRawJSON(msg.RawContentJSON); len(usage) > 0 {
			total += usageTotalTokens(usage)
		}
	}
	return total
}

func extractUsageFromLogContent(content interface{}) map[string]interface{} {
	payload, ok := content.(map[string]interface{})
	if !ok || payload == nil {
		return nil
	}
	usage, _ := payload["usage"].(map[string]interface{})
	return usage
}

func extractUsageFromRawJSON(raw json.RawMessage) map[string]interface{} {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	usage, _ := payload["usage"].(map[string]interface{})
	return usage
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
