package dsh

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const (
	contextSourceAgent   = "source_agent"
	contextSourcePath    = "source_path"
	contextSourceSession = "source_session_id"
	contextSourceFormat  = "source_format"
	contextSourceView    = "source_view"
	contextSourceCWD     = "source_cwd"
	contextSourceVersion = "source_format_version"
)

// ImportOptions selects one DSH session or official export ZIP.
type ImportOptions struct {
	Root      string
	Instance  string
	SessionID string
	ZipPath   string
	Latest    bool
	View      sessionport.SessionView
}

// Read resolves and normalizes one DSH session.
func Read(opts ImportOptions) (*Imported, error) {
	view, err := NormalizeView(string(opts.View))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.ZipPath) != "" {
		loaded, sourceFile, err := readZipSession(opts.ZipPath, opts.SessionID, opts.Latest)
		if err != nil {
			return nil, err
		}
		return convertLoaded(loaded, view, sourceFile)
	}

	instance, err := DetectInstance(firstNonEmpty(opts.Root, instanceRoot(opts.Instance)), false)
	if err != nil {
		return nil, err
	}
	refs, err := List(instance)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("DSH 实例没有可用会话: %s", instance.SessionsDir)
	}
	var ref *sessionport.SessionRef
	switch {
	case strings.TrimSpace(opts.SessionID) != "":
		for index := range refs {
			if refs[index].SessionID == strings.TrimSpace(opts.SessionID) {
				ref = &refs[index]
				break
			}
		}
		if ref == nil {
			return nil, fmt.Errorf("找不到 DSH 会话: %s", opts.SessionID)
		}
	case opts.Latest || strings.TrimSpace(opts.SessionID) == "":
		ref = &refs[0]
	default:
		return nil, fmt.Errorf("必须指定 --session 或 --latest")
	}
	if !ref.Readable {
		if ref.FormatStatus == "migration-required" {
			return nil, fmt.Errorf("DSH 会话使用历史格式 v%d；请先在 DSH Web 中 /export 当前格式，或使用 DSH 自身迁移后再导入", ref.FormatVersion)
		}
		return nil, fmt.Errorf("DSH 会话不可读（%s）", firstNonEmpty(ref.FormatStatus, "unknown"))
	}
	loaded, err := loadFile(ref.File, *ref)
	if err != nil {
		return nil, err
	}
	return convertLoaded(loaded, view, ref.File)
}

func instanceRoot(instance string) string {
	value := strings.TrimSpace(instance)
	if value == "" {
		return ""
	}
	if value == agentID+":default" {
		return DefaultHome()
	}
	return ""
}

func convertLoaded(loaded *Loaded, view sessionport.SessionView, sourceFile string) (*Imported, error) {
	if loaded == nil {
		return nil, fmt.Errorf("DSH 会话为空")
	}
	if loaded.Header.Version != currentFormat {
		if loaded.Header.Version > 0 && loaded.Header.Version < currentFormat {
			return nil, fmt.Errorf("DSH 会话使用历史格式 v%d；请使用 DSH 官方 /export 生成的当前格式 ZIP", loaded.Header.Version)
		}
		return nil, fmt.Errorf("不支持的 DSH Session 格式 v%d（当前支持 v%d）", loaded.Header.Version, currentFormat)
	}

	var nodes []int
	var surfaceWarnings []string
	switch view {
	case sessionport.ViewTranscript:
		nodes, surfaceWarnings = transcriptNodes(loaded.Events)
	default:
		nodes, surfaceWarnings = foldSurface(loaded.Events)
	}
	warnings := append([]string(nil), loaded.Warnings...)
	warnings = append(warnings, surfaceWarnings...)

	history := make([]runtimetypes.Message, 0, len(nodes))
	skippedUserContext := 0
	skippedSystem := 0
	skippedDeveloper := 0
	lastModel := ""
	for _, index := range nodes {
		if index < 0 || index >= len(loaded.Events) {
			continue
		}
		event := loaded.Events[index]
		switch event.Type {
		case "system/message":
			skippedSystem++
		case "developer/message":
			skippedDeveloper++
		case "user/message":
			message, included, warning := convertUserMessage(event)
			if warning != "" {
				warnings = append(warnings, warning)
			}
			if !included {
				skippedUserContext++
				continue
			}
			history = append(history, message)
		case "assistant/message":
			message, warning := convertAssistantMessage(event)
			if warning != "" {
				warnings = append(warnings, warning)
			}
			if strings.TrimSpace(message.Content) == "" && len(message.ToolCalls) == 0 && len(message.ContentParts) == 0 {
				continue
			}
			history = append(history, message)
			if source := strings.TrimSpace(message.Metadata.GetString("dsh_model", "")); source != "" {
				lastModel = source
			}
		case "tool/result":
			message, warning := convertToolResult(event)
			if warning != "" {
				warnings = append(warnings, warning)
			}
			history = append(history, message)
		}
	}
	if skippedSystem > 0 {
		warnings = append(warnings, fmt.Sprintf("已跳过 %d 条 DSH system message（不把源系统提示注入 aicli）", skippedSystem))
	}
	if skippedDeveloper > 0 {
		warnings = append(warnings, fmt.Sprintf("已跳过 %d 条 DSH developer message（tool-addition/removal 不迁移）", skippedDeveloper))
	}
	if skippedUserContext > 0 {
		warnings = append(warnings, fmt.Sprintf("已跳过 %d 条 DSH 注入型 user message（runtime-context/agent-instructions/goal 等）", skippedUserContext))
	}
	warnings = append(warnings, validateToolPairs(history)...)

	title := sessionTitle(loaded.Events)
	if title == "" {
		title = firstHumanUserText(history)
	}
	updatedAt := lastEventTime(loaded.Events)
	if updatedAt.IsZero() {
		updatedAt = loaded.Ref.UpdatedAt
	}
	createdAt := time.Time{}
	if loaded.Header.CreatedAt > 0 {
		createdAt = time.UnixMilli(loaded.Header.CreatedAt)
	}
	session := runtimechat.NewSession("")
	session.ID = loaded.Header.ID
	session.History = history
	session.HistoryLoaded = true
	session.CanonicalMessageCount = len(history)
	session.State = runtimechat.StateActive
	session.CreatedAt = createdAt
	session.UpdatedAt = updatedAt
	session.Metadata.Title = title
	session.Metadata.Tags = []string{}
	session.Metadata.LastModel = lastModel
	session.Metadata.Context = map[string]interface{}{
		contextSourceAgent:   agentID,
		contextSourcePath:    sourceFile,
		contextSourceSession: loaded.Header.ID,
		contextSourceFormat:  formatName,
		contextSourceView:    string(view),
		contextSourceCWD:     loaded.Header.CWD,
		contextSourceVersion: loaded.Header.Version,
	}
	if loaded.Header.AgentPreset != "" {
		session.Metadata.Context["source_agent_preset"] = loaded.Header.AgentPreset
	}
	if loaded.Header.CWD != "" {
		// Keep the source workspace addressable by aicli's sessions/resume
		// filters and by the target bundle writer.
		session.Metadata.Context["workspace_path"] = loaded.Header.CWD
	}
	if loaded.Header.ParentSession != "" {
		session.Metadata.Context["source_parent_session_id"] = loaded.Header.ParentSession
	}
	if loaded.Header.IsSeeded {
		session.Metadata.Context["source_is_seeded"] = true
	}
	if loaded.Header.DelegationDepth > 0 {
		session.Metadata.Context["source_delegation_depth"] = loaded.Header.DelegationDepth
	}
	session.Metadata.Context["source_event_count"] = len(loaded.Events)
	session.Metadata.Context["source_view_message_count"] = len(history)

	return &Imported{
		Session:    session,
		SourceFile: sourceFile,
		Ref:        loaded.Ref,
		Warnings:   uniqueStrings(warnings),
	}, nil
}

func convertUserMessage(event Event) (runtimetypes.Message, bool, string) {
	var data userMessageData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return runtimetypes.Message{}, false, fmt.Sprintf("解析 DSH user/message seq=%d 失败: %v", event.Seq, err)
	}
	kind := strings.TrimSpace(data.Source.Kind)
	if kind != "" && kind != "user" {
		return runtimetypes.Message{}, false, ""
	}
	text, toolCalls, reasoning, warning := convertBlocks(data.Content, "user")
	_ = toolCalls
	_ = reasoning
	message := *runtimetypes.NewUserMessage(text)
	message.Metadata.Set("dsh_source_event_seq", event.Seq)
	message.Metadata.Set("dsh_message_id", data.ID)
	if kind != "" {
		message.Metadata.Set("dsh_source_kind", kind)
	}
	return message, true, warning
}

func convertAssistantMessage(event Event) (runtimetypes.Message, string) {
	var data assistantMessageData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return runtimetypes.Message{}, fmt.Sprintf("解析 DSH assistant/message seq=%d 失败: %v", event.Seq, err)
	}
	text, toolCalls, reasoning, warning := convertBlocks(data.Message.Content, "assistant")
	message := *runtimetypes.NewAssistantMessage(text)
	message.ToolCalls = toolCalls
	message.Metadata.Set("dsh_source_event_seq", event.Seq)
	message.Metadata.Set("dsh_message_id", data.Message.ID)
	message.Metadata.Set("dsh_turn", eventTurn(event))
	message.Metadata.Set("dsh_step", eventStep(event))
	if data.Message.Source.Provider != "" {
		message.Metadata.Set("dsh_provider", data.Message.Source.Provider)
	}
	if data.Message.Source.Model != "" {
		message.Metadata.Set("dsh_model", data.Message.Source.Model)
	}
	if data.Message.Source.Kind != "" {
		message.Metadata.Set("dsh_source_kind", data.Message.Source.Kind)
	}
	if len(reasoning) > 0 {
		runtimetypes.SetReasoningBlock(message.Metadata, &runtimetypes.ReasoningBlock{
			Provider:   "deepseek-harness",
			Format:     "dsh-reasoning",
			Content:    strings.Join(reasoning, "\n\n"),
			Visibility: runtimetypes.ReasoningVisibilityFull,
		})
	}
	return message, warning
}

func convertToolResult(event Event) (runtimetypes.Message, string) {
	var data toolResultData
	if err := json.Unmarshal(event.Data, &data); err != nil {
		return runtimetypes.Message{}, fmt.Sprintf("解析 DSH tool/result seq=%d 失败: %v", event.Seq, err)
	}
	text, _, _, warning := convertBlocks(data.Message.Content, "tool")
	toolCallID := strings.TrimSpace(data.Message.ToolCallID)
	if toolCallID == "" {
		toolCallID = strings.TrimSpace(data.Message.Source.CallID)
	}
	if toolCallID == "" {
		warning = joinWarning(warning, fmt.Sprintf("DSH tool/result seq=%d 缺少 toolCallId", event.Seq))
	}
	message := *runtimetypes.NewToolMessage(toolCallID, text)
	message.Metadata.Set("dsh_source_event_seq", event.Seq)
	message.Metadata.Set("dsh_message_id", data.Message.ID)
	message.Metadata.Set("dsh_turn", eventTurn(event))
	message.Metadata.Set("dsh_step", eventStep(event))
	if data.Message.IsError {
		message.Metadata.Set("dsh_is_error", true)
	}
	if data.Message.Source.Kind != "" {
		message.Metadata.Set("dsh_source_kind", data.Message.Source.Kind)
	}
	return message, warning
}

func convertBlocks(blocks []ContentBlock, role string) (string, []runtimetypes.ToolCall, []string, string) {
	texts := make([]string, 0, len(blocks))
	reasoning := make([]string, 0)
	toolCalls := make([]runtimetypes.ToolCall, 0)
	warnings := make([]string, 0)
	for _, block := range blocks {
		switch block.Type {
		case "text":
			if block.Text != "" {
				texts = append(texts, block.Text)
			}
		case "reasoning":
			if block.Text != "" {
				reasoning = append(reasoning, block.Text)
			}
		case "tool-call":
			call := runtimetypes.ToolCall{
				ID:       block.ID,
				Type:     "function",
				Name:     block.Name,
				RawInput: block.Arguments,
			}
			if strings.TrimSpace(block.Arguments) != "" {
				var args map[string]interface{}
				if err := json.Unmarshal([]byte(block.Arguments), &args); err != nil {
					args = map[string]interface{}{"_raw": block.Arguments}
					warnings = append(warnings, fmt.Sprintf("tool call %s 的 arguments 不是 JSON 对象，已保留原始输入", block.ID))
				}
				call.Args = args
			}
			if call.Args == nil {
				call.Args = map[string]interface{}{}
			}
			toolCalls = append(toolCalls, call)
		case "image":
			texts = append(texts, "[DeepSeek Harness image attachment omitted during import]")
			warnings = append(warnings, fmt.Sprintf("%s 消息包含 image attachment；首版未迁移图片字节", role))
		case "file":
			texts = append(texts, "[DeepSeek Harness file attachment omitted during import]")
			warnings = append(warnings, fmt.Sprintf("%s 消息包含 file attachment；首版未迁移文件字节", role))
		default:
			warnings = append(warnings, fmt.Sprintf("%s 消息包含未支持的 content block: %s", role, block.Type))
		}
	}
	return strings.Join(texts, "\n\n"), toolCalls, reasoning, strings.Join(warnings, "；")
}

func sessionTitle(events []Event) string {
	title := ""
	for _, event := range events {
		if event.Type != "session/title" {
			continue
		}
		var data titleData
		if err := json.Unmarshal(event.Data, &data); err != nil {
			continue
		}
		if strings.TrimSpace(data.Title) != "" {
			title = strings.TrimSpace(data.Title)
		}
	}
	return title
}

func firstHumanUserText(history []runtimetypes.Message) string {
	for index := range history {
		if !strings.EqualFold(history[index].Role, "user") {
			continue
		}
		text := strings.TrimSpace(history[index].Content)
		if text == "" {
			continue
		}
		if len(text) > 120 {
			text = text[:120] + "…"
		}
		return text
	}
	return "DeepSeek Harness session"
}

func lastEventTime(events []Event) time.Time {
	var latest int64
	for _, event := range events {
		if event.Time > latest {
			latest = event.Time
		}
	}
	if latest <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(latest)
}

func validateToolPairs(history []runtimetypes.Message) []string {
	callIDs := map[string]struct{}{}
	resultIDs := map[string]struct{}{}
	warnings := make([]string, 0)
	for index := range history {
		for _, call := range history[index].ToolCalls {
			callIDs[strings.TrimSpace(call.ID)] = struct{}{}
		}
		if strings.EqualFold(history[index].Role, "tool") {
			resultIDs[strings.TrimSpace(history[index].ToolCallID)] = struct{}{}
		}
	}
	missing := make([]string, 0)
	orphan := make([]string, 0)
	for id := range callIDs {
		if id == "" {
			continue
		}
		if _, ok := resultIDs[id]; !ok {
			missing = append(missing, id)
		}
	}
	for id := range resultIDs {
		if id == "" {
			continue
		}
		if _, ok := callIDs[id]; !ok {
			orphan = append(orphan, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(orphan)
	if len(missing) > 0 {
		warnings = append(warnings, "DSH 会话中有未配对的 tool call: "+strings.Join(missing, ", "))
	}
	if len(orphan) > 0 {
		warnings = append(warnings, "DSH 会话中有未配对的 tool result: "+strings.Join(orphan, ", "))
	}
	return warnings
}

func eventTurn(event Event) int {
	switch event.Type {
	case "assistant/message":
		var data assistantMessageData
		if json.Unmarshal(event.Data, &data) == nil {
			return data.Turn
		}
	case "tool/result":
		var data toolResultData
		if json.Unmarshal(event.Data, &data) == nil {
			return data.Turn
		}
	}
	return 0
}

func eventStep(event Event) int {
	switch event.Type {
	case "assistant/message":
		var data assistantMessageData
		if json.Unmarshal(event.Data, &data) == nil {
			return data.Step
		}
	case "tool/result":
		var data toolResultData
		if json.Unmarshal(event.Data, &data) == nil {
			return data.Step
		}
	}
	return 0
}

func joinWarning(existing, next string) string {
	if strings.TrimSpace(existing) == "" {
		return next
	}
	if strings.TrimSpace(next) == "" {
		return existing
	}
	return existing + "；" + next
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
