package workbuddy

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ExportBundle writes a WorkBuddy-readable JSONL bundle. It intentionally does
// not write workbuddy.db: the desktop app treats that SQLite index as private
// state. A caller can merge the generated file and index metadata after the
// target application has been closed, or a future --apply adapter can do it.
func ExportBundle(opts ExportOptions) (*ExportResult, error) {
	if strings.TrimSpace(opts.BundleDir) == "" {
		return nil, fmt.Errorf("缺少 --bundle-dir")
	}
	cwd := strings.TrimSpace(opts.CWD)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("无法确定导出 cwd: %w", err)
		}
	}
	if !filepath.IsAbs(cwd) {
		absolute, err := filepath.Abs(cwd)
		if err != nil {
			return nil, fmt.Errorf("无法解析导出 cwd: %w", err)
		}
		cwd = absolute
	}
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	if _, err := safeSessionID(sessionID); err != nil {
		return nil, err
	}
	createdAt := opts.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	bundleDir, err := filepath.Abs(opts.BundleDir)
	if err != nil {
		return nil, fmt.Errorf("解析 bundle 目录失败: %w", err)
	}
	sessionDir := filepath.Join(bundleDir, "projects", projectSlug(cwd))
	sessionFile := filepath.Join(sessionDir, sessionID+".jsonl")
	if fileExists(sessionFile) {
		return nil, fmt.Errorf("目标 bundle 已存在会话文件（拒绝覆盖）: %s", sessionFile)
	}
	builder := &workbuddyExportBuilder{
		sessionID: sessionID,
		cwd:       cwd,
		title:     strings.TrimSpace(opts.Title),
		model:     strings.TrimSpace(opts.Model),
		createdAt: createdAt,
		clock:     createdAt.UnixMilli(),
		userID:    strings.TrimSpace(opts.TargetUser),
	}
	builder.build(opts.History)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 WorkBuddy bundle 目录失败: %w", err)
	}
	if err := writeWorkBuddyJSONL(sessionFile, builder.records); err != nil {
		return nil, err
	}
	manifest := map[string]interface{}{
		"version":             1,
		"source_agent":        "aicli",
		"target_agent":        agentID,
		"source_session_id":   opts.SourceSession,
		"target_session_id":   sessionID,
		"cwd":                 cwd,
		"format":              formatName,
		"format_version":      currentFormat,
		"backend":             "sqlite+jsonl",
		"requires_index":      true,
		"apply_supported":     false,
		"target_session_file": filepath.ToSlash(filepath.Join("projects", projectSlug(cwd), sessionID+".jsonl")),
		"record_count":        len(builder.records),
		"files": []map[string]interface{}{
			{"path": filepath.ToSlash(filepath.Join("projects", projectSlug(cwd), sessionID+".jsonl")), "role": "session-jsonl"},
			{"path": "session-index.json", "role": "index-plan"},
		},
		"warnings": uniqueStrings(builder.warnings),
	}
	manifestFile := filepath.Join(bundleDir, "manifest.json")
	if err := writeWorkBuddyJSONAtomic(manifestFile, manifest); err != nil {
		return nil, err
	}
	index := map[string]interface{}{
		"version": 1,
		"note":    "This index is a merge aid. WorkBuddy itself owns workbuddy.db; no schema is assumed or written.",
		"sessions": []map[string]interface{}{{
			"id":               sessionID,
			"cwd":              cwd,
			"user_id":          builder.userID,
			"title":            builder.title,
			"custom_title":     builder.title,
			"status":           "completed",
			"created_at":       createdAt.UnixMilli(),
			"updated_at":       builder.clock,
			"last_activity_at": builder.clock,
			"is_playground":    0,
			"model":            builder.model,
			"transport":        "local",
			"permission_mode":  "",
		}},
	}
	indexFile := filepath.Join(bundleDir, "session-index.json")
	if err := writeWorkBuddyJSONAtomic(indexFile, index); err != nil {
		return nil, err
	}
	return &ExportResult{
		SessionID:    sessionID,
		BundleDir:    bundleDir,
		SessionFile:  sessionFile,
		ManifestFile: manifestFile,
		IndexFile:    indexFile,
		EventCount:   len(builder.records),
		Warnings:     uniqueStrings(builder.warnings),
	}, nil
}

type workbuddyExportBuilder struct {
	sessionID string
	cwd       string
	title     string
	model     string
	userID    string
	createdAt time.Time
	clock     int64
	records   []map[string]interface{}
	warnings  []string
}

func (b *workbuddyExportBuilder) build(history []runtimetypes.Message) {
	b.records = append(b.records, map[string]interface{}{
		"type":      "session-meta",
		"id":        uuid.NewString(),
		"sessionId": b.sessionID,
		"timestamp": b.nextTime(),
		"meta":      map[string]interface{}{"codebuddy.ai/hostKind": "unopted", "aicli.import": true},
		"cwd":       b.cwd,
	})
	var lastAssistantID string
	var lastUserID string
	pendingCalls := map[string]string{}
	consumedResults := map[string]struct{}{}
	for _, message := range history {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "user":
			id := uuid.NewString()
			parent := lastAssistantID
			b.records = append(b.records, map[string]interface{}{
				"id":        id,
				"type":      "message",
				"role":      "user",
				"timestamp": b.nextTime(),
				"sessionId": b.sessionID,
				"cwd":       b.cwd,
				"parentId":  emptyStringNil(parent),
				"content": []map[string]interface{}{{
					"type": "input_text",
					"text": messageText(message, "user", &b.warnings),
				}},
				"providerData": map[string]interface{}{"startsNewUserRequest": true, "agent": "cli"},
			})
			lastUserID = id
		case "assistant":
			assistantID := uuid.NewString()
			reasoning := reasoningTextFromMessage(message)
			if reasoning != "" {
				b.records = append(b.records, map[string]interface{}{
					"id":        uuid.NewString(),
					"type":      "reasoning",
					"parentId":  assistantID,
					"timestamp": b.nextTime(),
					"sessionId": b.sessionID,
					"cwd":       b.cwd,
					"content":   []interface{}{},
					"rawContent": []map[string]interface{}{{
						"type": "reasoning_text",
						"text": reasoning,
					}},
					"providerData": map[string]interface{}{
						"model": b.model,
					},
				})
			}
			b.records = append(b.records, map[string]interface{}{
				"id":        assistantID,
				"type":      "message",
				"role":      "assistant",
				"parentId":  emptyStringNil(lastUserID),
				"timestamp": b.nextTime(),
				"sessionId": b.sessionID,
				"cwd":       b.cwd,
				"status":    "completed",
				"content": []map[string]interface{}{{
					"type": "output_text",
					"text": messageText(message, "assistant", &b.warnings),
				}},
				"providerData": map[string]interface{}{
					"agent": "cli",
					"model": b.model,
				},
			})
			for _, call := range message.ToolCalls {
				callID := strings.TrimSpace(call.ID)
				if callID == "" {
					continue
				}
				callRecordID := uuid.NewString()
				pendingCalls[callID] = callRecordID
				b.records = append(b.records, map[string]interface{}{
					"id":        callRecordID,
					"type":      "function_call",
					"parentId":  assistantID,
					"timestamp": b.nextTime(),
					"sessionId": b.sessionID,
					"cwd":       b.cwd,
					"callId":    callID,
					"name":      call.Name,
					"arguments": toolArguments(call),
					"providerData": map[string]interface{}{
						"agent": "cli",
						"model": b.model,
					},
				})
			}
			lastAssistantID = assistantID
			lastUserID = ""
		case "tool":
			callID := strings.TrimSpace(message.ToolCallID)
			if callID == "" {
				b.warnings = append(b.warnings, "跳过缺少 tool_call_id 的 tool 消息")
				continue
			}
			parentID, ok := pendingCalls[callID]
			if !ok {
				b.warnings = append(b.warnings, "跳过未匹配 assistant tool call 的 tool result: "+callID)
				continue
			}
			consumedResults[callID] = struct{}{}
			b.records = append(b.records, map[string]interface{}{
				"id":        uuid.NewString(),
				"type":      "function_call_result",
				"parentId":  parentID,
				"timestamp": b.nextTime(),
				"sessionId": b.sessionID,
				"cwd":       b.cwd,
				"callId":    callID,
				"name":      toolNameForResult(history, callID),
				"status":    "completed",
				"output": map[string]interface{}{
					"type": "text",
					"text": messageText(message, "tool", &b.warnings),
				},
			})
		default:
			b.warnings = append(b.warnings, "跳过不支持的消息角色: "+message.Role)
		}
	}
	for callID, parentID := range pendingCalls {
		if _, ok := consumedResults[callID]; ok {
			continue
		}
		b.warnings = append(b.warnings, "tool call 没有结果；已合成错误结果以保持 WorkBuddy 生命周期完整: "+callID)
		b.records = append(b.records, map[string]interface{}{
			"id":        uuid.NewString(),
			"type":      "function_call_result",
			"parentId":  parentID,
			"timestamp": b.nextTime(),
			"sessionId": b.sessionID,
			"cwd":       b.cwd,
			"callId":    callID,
			"status":    "error",
			"output": map[string]interface{}{
				"type": "text",
				"text": "Imported history ended without a recorded tool result; outcome is unknown.",
			},
		})
	}
	if b.title != "" {
		b.records = append(b.records, map[string]interface{}{
			"id":        uuid.NewString(),
			"type":      "ai-title",
			"timestamp": b.nextTime(),
			"sessionId": b.sessionID,
			"cwd":       b.cwd,
			"aiTitle":   b.title,
		})
	}
	if len(b.records) == 1 {
		b.warnings = append(b.warnings, "源会话没有任何可导出的消息")
	}
}

func (b *workbuddyExportBuilder) nextTime() int64 {
	b.clock++
	if b.clock < b.createdAt.UnixMilli() {
		b.clock = b.createdAt.UnixMilli() + 1
	}
	return b.clock
}

func messageText(message runtimetypes.Message, role string, warnings *[]string) string {
	parts := make([]string, 0, 2)
	if strings.TrimSpace(message.Content) != "" {
		parts = append(parts, message.Content)
	}
	for _, part := range message.ContentParts {
		switch part.Type {
		case runtimetypes.ContentPartText:
			if part.Text != "" {
				parts = append(parts, part.Text)
			}
		case runtimetypes.ContentPartImage:
			*warnings = append(*warnings, fmt.Sprintf("%s 消息包含图片；WorkBuddy bundle 以文本占位", role))
			parts = append(parts, "[image attachment from aicli omitted during WorkBuddy export]")
		default:
			*warnings = append(*warnings, fmt.Sprintf("%s 消息包含未支持的 content part: %s", role, part.Type))
		}
	}
	return strings.Join(parts, "\n\n")
}

func reasoningTextFromMessage(message runtimetypes.Message) string {
	reasoning := runtimetypes.GetReasoningBlock(message.Metadata)
	if reasoning == nil {
		return ""
	}
	return strings.TrimSpace(reasoning.DisplayText())
}

func toolArguments(call runtimetypes.ToolCall) string {
	if strings.TrimSpace(call.RawInput) != "" {
		return call.RawInput
	}
	if len(call.Args) == 0 {
		return "{}"
	}
	encoded, err := json.Marshal(call.Args)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func toolNameForResult(history []runtimetypes.Message, callID string) string {
	for i := range history {
		for _, call := range history[i].ToolCalls {
			if call.ID == callID {
				return call.Name
			}
		}
	}
	return ""
}

func emptyStringNil(value string) interface{} {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func writeWorkBuddyJSONL(path string, records []map[string]interface{}) error {
	var builder strings.Builder
	for _, record := range records {
		encoded, err := json.Marshal(record)
		if err != nil {
			return fmt.Errorf("序列化 WorkBuddy 记录失败: %w", err)
		}
		builder.Write(encoded)
		builder.WriteByte('\n')
	}
	return writeWorkBuddyBytesAtomic(path, []byte(builder.String()))
}

func writeWorkBuddyJSONAtomic(path string, value interface{}) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 JSON 失败: %w", err)
	}
	encoded = append(encoded, '\n')
	return writeWorkBuddyBytesAtomic(path, encoded)
}

func writeWorkBuddyBytesAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".workbuddy-transfer-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}
