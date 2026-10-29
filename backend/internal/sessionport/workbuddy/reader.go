package workbuddy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

var (
	userQueryRE       = regexp.MustCompile(`(?s)<user_query>(.*?)</user_query>`)
	userInputRE       = regexp.MustCompile(`(?s)<(?:user_message|user_input|user_intent)>(.*?)</(?:user_message|user_input|user_intent)>`)
	systemBlockRE     = regexp.MustCompile(`(?s)<system-reminder\b[^>]*>.*?</system-reminder>`)
	projectContextRE  = regexp.MustCompile(`(?s)<project_context\b[^>]*>.*?</project_context>`)
	identityContextRE = regexp.MustCompile(`(?s)<identity_context\b[^>]*>.*?</identity_context>`)
)

// Read resolves and normalizes one WorkBuddy session.
func Read(opts ImportOptions) (*Imported, error) {
	view, err := normalizeView(opts.View)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.FilePath) == "" && strings.HasSuffix(strings.ToLower(strings.TrimSpace(opts.Root)), ".jsonl") {
		opts.FilePath = opts.Root
		opts.Root = ""
	}
	if strings.TrimSpace(opts.FilePath) != "" {
		absolute, err := filepath.Abs(opts.FilePath)
		if err != nil {
			return nil, err
		}
		if !fileExists(absolute) {
			return nil, fmt.Errorf("WorkBuddy JSONL 不存在: %s", absolute)
		}
		ref := sessionport.SessionRef{
			AgentID:      agentID,
			SessionID:    strings.TrimSuffix(filepath.Base(absolute), filepath.Ext(absolute)),
			File:         absolute,
			FormatStatus: "current",
		}
		loaded, err := loadFile(absolute, ref)
		if err != nil {
			return nil, err
		}
		return convertLoaded(loaded, view)
	}

	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root, err = InstanceRoot(opts.Instance)
		if err != nil {
			return nil, err
		}
	}
	instance, err := DetectInstance(root, false, opts.DBPath, opts.ProjectsDir)
	if err != nil {
		return nil, err
	}
	refs, err := List(instance)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("WorkBuddy 实例没有可用会话: %s", instance.ProjectsDir)
	}
	var ref *sessionport.SessionRef
	switch {
	case strings.TrimSpace(opts.SessionID) != "":
		for i := range refs {
			if refs[i].SessionID == strings.TrimSpace(opts.SessionID) {
				ref = &refs[i]
				break
			}
		}
		if ref == nil {
			return nil, fmt.Errorf("找不到 WorkBuddy 会话: %s", opts.SessionID)
		}
	case opts.Latest || strings.TrimSpace(opts.SessionID) == "":
		for i := range refs {
			if refs[i].Readable {
				ref = &refs[i]
				break
			}
		}
		if ref == nil {
			ref = &refs[0]
		}
	default:
		return nil, fmt.Errorf("必须指定 --session 或 --latest")
	}
	if !ref.Readable {
		return nil, fmt.Errorf("WorkBuddy 会话不可读: %s", strings.Join(ref.Warnings, "；"))
	}
	loaded, err := loadFile(ref.File, *ref)
	if err != nil {
		return nil, err
	}
	return convertLoaded(loaded, view)
}

func normalizeView(value sessionport.SessionView) (sessionport.SessionView, error) {
	switch value {
	case "", sessionport.ViewSurface:
		return sessionport.ViewSurface, nil
	case sessionport.ViewTranscript:
		return sessionport.ViewTranscript, nil
	default:
		return "", fmt.Errorf("不支持的视图: %s（可选 surface|transcript）", value)
	}
}

// loadRecords parses the append-only JSONL stream. Malformed lines are retained
// as warnings instead of discarding the whole session: WorkBuddy is a desktop
// app and a crash can leave a partial final line.
func loadRecords(path string, ref sessionport.SessionRef) (*Loaded, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 WorkBuddy JSONL 失败: %w", err)
	}
	defer file.Close()
	loaded := &Loaded{Ref: ref, Warnings: make([]string, 0)}
	loaded.Ref.File = path
	if loaded.Ref.SessionID == "" {
		loaded.Ref.SessionID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	lineNo := 0
	hasHistory := false
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimPrefix(line, "\ufeff")
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			loaded.Warnings = append(loaded.Warnings, fmt.Sprintf("跳过 WorkBuddy JSONL 第 %d 行（解析失败: %v）", lineNo, err))
			continue
		}
		loaded.Records = append(loaded.Records, record)
		switch record.Type {
		case "message", "function_call", "function_call_result":
			hasHistory = true
		}
		if record.SessionID != "" && loaded.Ref.SessionID == "" {
			loaded.Ref.SessionID = record.SessionID
		}
		if record.CWD != "" && loaded.CWD == "" {
			loaded.CWD = record.CWD
		}
		if record.Timestamp > 0 {
			if loaded.CreatedAt == 0 || record.Timestamp < loaded.CreatedAt {
				loaded.CreatedAt = record.Timestamp
			}
			if record.Timestamp > loaded.Ref.UpdatedAt.UnixMilli() || loaded.Ref.UpdatedAt.IsZero() {
				loaded.Ref.UpdatedAt = time.UnixMilli(record.Timestamp)
			}
		}
		if record.Type == "ai-title" && strings.TrimSpace(record.AITitle) != "" {
			loaded.Title = strings.TrimSpace(record.AITitle)
		}
		if record.ProviderData.Model != "" {
			loaded.Model = record.ProviderData.Model
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取 WorkBuddy JSONL 失败: %w", err)
	}
	if len(loaded.Records) == 0 {
		return nil, fmt.Errorf("WorkBuddy JSONL 没有可解析记录: %s", path)
	}
	if !hasHistory {
		return nil, fmt.Errorf("WorkBuddy JSONL 没有消息/工具历史: %s", path)
	}
	loaded.Ref.EventCount = len(loaded.Records)
	loaded.Ref.FormatVersion = currentFormat
	loaded.Ref.FormatStatus = "current"
	loaded.Ref.Compression = "none"
	loaded.Ref.Readable = true
	if loaded.Ref.CWD == "" {
		loaded.Ref.CWD = loaded.CWD
	}
	if loaded.Ref.Title == "" {
		loaded.Ref.Title = loaded.Title
	}
	if loaded.Ref.UpdatedAt.IsZero() {
		if info, statErr := os.Stat(path); statErr == nil {
			loaded.Ref.UpdatedAt = info.ModTime()
		}
	}
	loaded.Warnings = append(loaded.Warnings, loaded.Ref.Warnings...)
	return loaded, nil
}

func loadFile(path string, ref sessionport.SessionRef) (*Loaded, error) {
	return loadRecords(path, ref)
}

func convertLoaded(loaded *Loaded, view sessionport.SessionView) (*Imported, error) {
	if loaded == nil {
		return nil, fmt.Errorf("WorkBuddy 会话为空")
	}
	active, forkWarnings := activeRecords(loaded.Records, view)
	warnings := append([]string(nil), loaded.Warnings...)
	warnings = append(warnings, forkWarnings...)

	callsByParent := map[string][]Record{}
	callsByMessage := map[string][]Record{}
	for _, record := range active {
		switch record.Type {
		case "function_call":
			if record.ParentID != "" {
				callsByParent[record.ParentID] = append(callsByParent[record.ParentID], record)
			}
			if messageID := strings.TrimSpace(record.ProviderData.MessageID); messageID != "" {
				callsByMessage[messageID] = append(callsByMessage[messageID], record)
			}
		}
	}

	history := make([]runtimetypes.Message, 0, len(active))
	consumedCalls := map[string]struct{}{}
	pendingReasoning := make([]string, 0, 2)
	skippedContext := 0
	skippedEvents := map[string]int{}
	processedCallGroups := map[string]struct{}{}
	lastModel := ""
	for index, record := range active {
		switch record.Type {
		case "message":
			switch strings.ToLower(strings.TrimSpace(record.Role)) {
			case "user":
				text, included, blockWarnings := extractUserText(record.Content)
				for _, warning := range blockWarnings {
					warnings = append(warnings, warning)
				}
				if !included {
					skippedContext++
					continue
				}
				message := *runtimetypes.NewUserMessage(text)
				message.Metadata.Set("workbuddy_record_index", index)
				message.Metadata.Set("workbuddy_message_id", record.ID)
				history = append(history, message)
			case "assistant":
				text, blockWarnings := assistantText(record.Content)
				for _, warning := range blockWarnings {
					warnings = append(warnings, warning)
				}
				callRecords := append([]Record(nil), callsByParent[record.ID]...)
				if messageID := strings.TrimSpace(record.ProviderData.MessageID); messageID != "" {
					callRecords = appendUniqueRecords(callRecords, callsByMessage[messageID]...)
				}
				calls := convertCalls(callRecords, &warnings)
				for _, call := range calls {
					consumedCalls[call.ID] = struct{}{}
				}
				for _, callRecord := range callRecords {
					consumedCalls[callRecord.ID] = struct{}{}
					if callRecord.CallID != "" {
						consumedCalls[callRecord.CallID] = struct{}{}
					}
				}
				reasoning := strings.Join(pendingReasoning, "\n\n")
				pendingReasoning = pendingReasoning[:0]
				if strings.TrimSpace(text) == "" && len(calls) == 0 && reasoning == "" {
					continue
				}
				message := *runtimetypes.NewAssistantMessage(text)
				message.ToolCalls = calls
				message.Metadata.Set("workbuddy_record_index", index)
				message.Metadata.Set("workbuddy_message_id", record.ID)
				message.Metadata.Set("workbuddy_model", record.ProviderData.Model)
				message.Metadata.Set("workbuddy_step", record.ProviderData.StepSeq)
				if record.ProviderData.ConversationRequestID != "" {
					message.Metadata.Set("workbuddy_request_id", record.ProviderData.ConversationRequestID)
				}
				if reasoning != "" {
					runtimetypes.SetReasoningBlock(message.Metadata, &runtimetypes.ReasoningBlock{
						Provider:   "workbuddy",
						Format:     "workbuddy-reasoning",
						Content:    reasoning,
						Visibility: runtimetypes.ReasoningVisibilityFull,
					})
				}
				if record.ProviderData.Model != "" {
					lastModel = record.ProviderData.Model
				}
				history = append(history, message)
			case "tool":
				message, warning := convertToolRecord(record, index)
				if warning != "" {
					warnings = append(warnings, warning)
				}
				history = append(history, message)
			default:
				skippedEvents["message/"+record.Role]++
			}
		case "reasoning":
			text := reasoningText(record)
			if text != "" {
				pendingReasoning = append(pendingReasoning, text)
			}
		case "function_call":
			if _, ok := consumedCalls[record.ID]; ok {
				continue
			}
			if record.CallID != "" {
				if _, ok := consumedCalls[record.CallID]; ok {
					continue
				}
			}
			groupKey := strings.TrimSpace(record.ProviderData.MessageID)
			if groupKey == "" {
				groupKey = strings.TrimSpace(record.ParentID)
			}
			if groupKey == "" {
				groupKey = record.ID
			}
			if _, ok := processedCallGroups[groupKey]; ok {
				continue
			}
			group := collectCallGroup(active, groupKey, consumedCalls)
			if len(group) == 0 {
				continue
			}
			processedCallGroups[groupKey] = struct{}{}
			calls := convertCalls(group, &warnings)
			if len(calls) == 0 {
				continue
			}
			message := *runtimetypes.NewAssistantMessage("")
			message.ToolCalls = calls
			message.Metadata.Set("workbuddy_record_index", index)
			message.Metadata.Set("workbuddy_message_id", group[0].ProviderData.MessageID)
			for _, groupRecord := range group {
				consumedCalls[groupRecord.ID] = struct{}{}
				if groupRecord.CallID != "" {
					consumedCalls[groupRecord.CallID] = struct{}{}
				}
			}
			if group[0].ProviderData.Model != "" {
				message.Metadata.Set("workbuddy_model", group[0].ProviderData.Model)
				lastModel = group[0].ProviderData.Model
			}
			if len(pendingReasoning) > 0 {
				runtimetypes.SetReasoningBlock(message.Metadata, &runtimetypes.ReasoningBlock{
					Provider:   "workbuddy",
					Format:     "workbuddy-reasoning",
					Content:    strings.Join(pendingReasoning, "\n\n"),
					Visibility: runtimetypes.ReasoningVisibilityFull,
				})
				pendingReasoning = pendingReasoning[:0]
			}
			history = append(history, message)
		case "function_call_result":
			message, warning := convertFunctionResult(record, index)
			if warning != "" {
				warnings = append(warnings, warning)
			}
			history = append(history, message)
		case "session-meta", "ai-title", "resend-fork-notice":
			// Metadata/control records.
		case "file-history-snapshot":
			skippedEvents[record.Type]++
		default:
			skippedEvents[record.Type]++
		}
	}
	if len(pendingReasoning) > 0 {
		warnings = append(warnings, "会话末尾存在未归属的 reasoning 记录，已忽略")
	}
	if skippedContext > 0 {
		warnings = append(warnings, fmt.Sprintf("已跳过 %d 条 WorkBuddy 注入型 user message（system-reminder/project-context 等）", skippedContext))
	}
	for eventType, count := range skippedEvents {
		warnings = append(warnings, fmt.Sprintf("已跳过 %d 条 WorkBuddy 事件: %s", count, eventType))
	}
	warnings = append(warnings, validateToolPairs(history)...)

	title := firstNonEmpty(loaded.Ref.Title, loaded.Title)
	if title == "" {
		title = firstHumanUserText(history)
	}
	createdAt := time.Time{}
	if loaded.CreatedAt > 0 {
		createdAt = time.UnixMilli(loaded.CreatedAt)
	}
	session := runtimechat.NewSession("")
	session.ID = loaded.Ref.SessionID
	session.History = history
	session.HistoryLoaded = true
	session.CanonicalMessageCount = len(history)
	session.State = runtimechat.StateActive
	session.CreatedAt = createdAt
	session.UpdatedAt = loaded.Ref.UpdatedAt
	session.Metadata.Title = title
	session.Metadata.Tags = []string{}
	session.Metadata.LastModel = lastModel
	cwd := firstNonEmpty(loaded.Ref.CWD, loaded.CWD)
	session.Metadata.Context = map[string]interface{}{
		contextSourceAgent:          agentID,
		contextSourcePath:           loaded.Ref.File,
		contextSourceSession:        loaded.Ref.SessionID,
		contextSourceFormat:         formatName,
		contextSourceView:           string(view),
		contextSourceCWD:            cwd,
		contextSourceVersion:        currentFormat,
		"source_event_count":        len(loaded.Records),
		"source_view_message_count": len(history),
	}
	if cwd != "" {
		session.Metadata.Context["workspace_path"] = cwd
	}
	if loaded.Model != "" {
		session.Metadata.Context["source_model"] = loaded.Model
	}
	return &Imported{
		Session:    session,
		SourceFile: loaded.Ref.File,
		Ref:        loaded.Ref,
		Warnings:   uniqueStrings(warnings),
	}, nil
}

// activeRecords applies WorkBuddy's resend-fork-notice semantics. A notice
// points at the edited user item; the old user message and everything below it
// belong to the replaced branch. surface keeps the new branch only; transcript
// intentionally keeps the raw append-origin stream for archival reading.
func activeRecords(records []Record, view sessionport.SessionView) ([]Record, []string) {
	warnings := make([]string, 0)
	if view == sessionport.ViewTranscript {
		for _, record := range records {
			if record.Type == "resend-fork-notice" && record.EditedUserItemID != "" {
				warnings = append(warnings, "transcript 视图保留 WorkBuddy resend fork 的旧分支；阅读归档时请以 surface 为准")
				break
			}
		}
		return records, warnings
	}
	children := map[string][]string{}
	byID := map[string]Record{}
	for _, record := range records {
		if record.ID == "" {
			continue
		}
		byID[record.ID] = record
		if record.ParentID != "" {
			children[record.ParentID] = append(children[record.ParentID], record.ID)
		}
	}
	removed := map[string]struct{}{}
	for _, record := range records {
		if record.Type != "resend-fork-notice" || strings.TrimSpace(record.EditedUserItemID) == "" {
			continue
		}
		if _, ok := byID[record.EditedUserItemID]; !ok {
			warnings = append(warnings, "resend-fork-notice 指向不存在的 user item: "+record.EditedUserItemID)
			continue
		}
		queue := []string{record.EditedUserItemID}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			if _, ok := removed[id]; ok {
				continue
			}
			removed[id] = struct{}{}
			queue = append(queue, children[id]...)
		}
	}
	if len(removed) == 0 {
		return records, warnings
	}
	active := make([]Record, 0, len(records)-len(removed))
	for _, record := range records {
		if _, ok := removed[record.ID]; ok {
			continue
		}
		active = append(active, record)
	}
	warnings = append(warnings, fmt.Sprintf("surface 视图已按 resend-fork-notice 移除 %d 条旧分支记录", len(removed)))
	return active, warnings
}

func extractUserText(blocks []ContentBlock) (string, bool, []string) {
	warnings := make([]string, 0)
	texts := make([]string, 0)
	queries := make([]string, 0)
	for _, block := range blocks {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "input_text", "text":
			value := block.Text
			matches := userQueryRE.FindAllStringSubmatch(value, -1)
			if len(matches) > 0 {
				for _, match := range matches {
					if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
						queries = append(queries, match[1])
					}
				}
				continue
			}
			matches = userInputRE.FindAllStringSubmatch(value, -1)
			if len(matches) > 0 {
				for _, match := range matches {
					if len(match) > 1 && strings.TrimSpace(match[1]) != "" {
						queries = append(queries, match[1])
					}
				}
				continue
			}
			clean := stripInjectedContext(value)
			if strings.TrimSpace(clean) != "" {
				texts = append(texts, clean)
			}
		case "input_image", "image":
			warnings = append(warnings, "WorkBuddy user message 包含图片；首版未迁移图片字节")
			texts = append(texts, "[WorkBuddy image attachment omitted during import]")
		case "input_file", "file":
			warnings = append(warnings, "WorkBuddy user message 包含文件；首版未迁移文件字节")
			texts = append(texts, "[WorkBuddy file attachment omitted during import]")
		default:
			if strings.TrimSpace(block.Text) != "" {
				texts = append(texts, block.Text)
			}
		}
	}
	if len(queries) > 0 {
		return strings.Join(queries, "\n\n"), true, warnings
	}
	text := strings.TrimSpace(strings.Join(texts, "\n\n"))
	if text == "" {
		return "", false, warnings
	}
	return text, true, warnings
}

func assistantText(blocks []ContentBlock) (string, []string) {
	texts := make([]string, 0)
	warnings := make([]string, 0)
	for _, block := range blocks {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "output_text", "text":
			if block.Text != "" {
				texts = append(texts, block.Text)
			}
		case "reasoning_text":
			// Older streams may put reasoning in content; normalize it the same
			// way as a standalone reasoning record.
			if block.Text != "" {
				texts = append(texts, block.Text)
			}
		case "image", "output_image":
			warnings = append(warnings, "WorkBuddy assistant message 包含图片；首版未迁移图片字节")
			texts = append(texts, "[WorkBuddy image attachment omitted during import]")
		default:
			if block.Text != "" {
				texts = append(texts, block.Text)
			}
		}
	}
	return strings.Join(texts, "\n\n"), warnings
}

func reasoningText(record Record) string {
	texts := make([]string, 0)
	for _, block := range append(append([]ContentBlock(nil), record.RawContent...), record.Content...) {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "reasoning_text", "text":
			if strings.TrimSpace(block.Text) != "" {
				texts = append(texts, block.Text)
			}
		}
	}
	return strings.Join(texts, "\n\n")
}

func stripInjectedContext(value string) string {
	value = systemBlockRE.ReplaceAllString(value, "")
	value = projectContextRE.ReplaceAllString(value, "")
	value = identityContextRE.ReplaceAllString(value, "")
	return strings.TrimSpace(value)
}

func convertCalls(records []Record, warnings *[]string) []runtimetypes.ToolCall {
	calls := make([]runtimetypes.ToolCall, 0, len(records))
	for _, record := range records {
		call := convertCall(record, warnings)
		if call.ID != "" {
			calls = append(calls, call)
		}
	}
	return calls
}

func appendUniqueRecords(base []Record, extra ...Record) []Record {
	seen := make(map[string]struct{}, len(base)+len(extra))
	for _, record := range base {
		seen[record.ID] = struct{}{}
	}
	for _, record := range extra {
		if _, ok := seen[record.ID]; ok {
			continue
		}
		seen[record.ID] = struct{}{}
		base = append(base, record)
	}
	return base
}

func collectCallGroup(active []Record, groupKey string, consumed map[string]struct{}) []Record {
	group := make([]Record, 0, 2)
	for _, record := range active {
		if record.Type != "function_call" {
			continue
		}
		if _, ok := consumed[record.ID]; ok {
			continue
		}
		if record.CallID != "" {
			if _, ok := consumed[record.CallID]; ok {
				continue
			}
		}
		key := strings.TrimSpace(record.ProviderData.MessageID)
		if key == "" {
			key = strings.TrimSpace(record.ParentID)
		}
		if key == "" {
			key = record.ID
		}
		if key == groupKey {
			group = append(group, record)
		}
	}
	return group
}

func convertCall(record Record, warnings *[]string) runtimetypes.ToolCall {
	callID := strings.TrimSpace(record.CallID)
	if callID == "" {
		callID = strings.TrimSpace(record.ID)
	}
	call := runtimetypes.ToolCall{
		ID:       callID,
		Type:     "function",
		Name:     record.Name,
		RawInput: record.Arguments,
	}
	if strings.TrimSpace(record.Arguments) != "" {
		var args map[string]interface{}
		if err := json.Unmarshal([]byte(record.Arguments), &args); err != nil {
			args = map[string]interface{}{"_raw": record.Arguments}
			*warnings = append(*warnings, fmt.Sprintf("WorkBuddy tool call %s 的 arguments 不是 JSON 对象，已保留原始输入", callID))
		}
		call.Args = args
	}
	if call.Args == nil {
		call.Args = map[string]interface{}{}
	}
	return call
}

func convertFunctionResult(record Record, index int) (runtimetypes.Message, string) {
	callID := strings.TrimSpace(record.CallID)
	if callID == "" {
		callID = strings.TrimSpace(record.ID)
	}
	warning := ""
	if callID == "" {
		warning = "WorkBuddy function_call_result 缺少 callId"
	}
	text := decodeOutput(record.Output)
	message := *runtimetypes.NewToolMessage(callID, text)
	message.Metadata.Set("workbuddy_record_index", index)
	message.Metadata.Set("workbuddy_message_id", record.ID)
	message.Metadata.Set("workbuddy_tool_name", record.Name)
	message.Metadata.Set("workbuddy_status", record.Status)
	if record.Status != "" && !strings.EqualFold(record.Status, "completed") && !strings.EqualFold(record.Status, "success") {
		message.Metadata.Set("workbuddy_is_error", true)
	}
	return message, warning
}

func convertToolRecord(record Record, index int) (runtimetypes.Message, string) {
	callID := strings.TrimSpace(record.CallID)
	if callID == "" {
		callID = strings.TrimSpace(record.ID)
	}
	text, _ := assistantText(record.Content)
	message := *runtimetypes.NewToolMessage(callID, text)
	message.Metadata.Set("workbuddy_record_index", index)
	message.Metadata.Set("workbuddy_message_id", record.ID)
	return message, ""
}

func decodeOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var block ContentBlock
	if err := json.Unmarshal(raw, &block); err == nil && block.Text != "" {
		return block.Text
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var value map[string]interface{}
	if err := json.Unmarshal(raw, &value); err == nil {
		if text, ok := value["text"].(string); ok {
			return text
		}
		if content, ok := value["content"].(string); ok {
			return content
		}
	}
	return string(raw)
}

func validateToolPairs(history []runtimetypes.Message) []string {
	callIDs := map[string]struct{}{}
	resultIDs := map[string]struct{}{}
	warnings := make([]string, 0)
	for i := range history {
		for _, call := range history[i].ToolCalls {
			if id := strings.TrimSpace(call.ID); id != "" {
				callIDs[id] = struct{}{}
			}
		}
		if strings.EqualFold(history[i].Role, "tool") {
			if id := strings.TrimSpace(history[i].ToolCallID); id != "" {
				resultIDs[id] = struct{}{}
			}
		}
	}
	for id := range callIDs {
		if _, ok := resultIDs[id]; !ok {
			warnings = append(warnings, "WorkBuddy tool call 没有对应结果: "+id)
		}
	}
	for id := range resultIDs {
		if _, ok := callIDs[id]; !ok {
			warnings = append(warnings, "WorkBuddy tool result 没有对应调用: "+id)
		}
	}
	return warnings
}

func firstHumanUserText(history []runtimetypes.Message) string {
	for i := range history {
		if !strings.EqualFold(history[i].Role, "user") {
			continue
		}
		text := strings.TrimSpace(history[i].Content)
		if text == "" {
			continue
		}
		if len([]rune(text)) > 120 {
			runes := []rune(text)
			text = string(runes[:120]) + "…"
		}
		return text
	}
	return "WorkBuddy session"
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
