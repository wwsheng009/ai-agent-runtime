package dsh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ExportOptions describes an aicli → DSH v4 bundle.
type ExportOptions struct {
	SessionID     string
	CWD           string
	Title         string
	CreatedAt     time.Time
	Model         string
	History       []runtimetypes.Message
	Compression   string // none | zstd
	BundleDir     string
	SourceSession string
}

// ExportResult is one published target-native bundle.
type ExportResult struct {
	SessionID    string
	BundleDir    string
	SessionFile  string
	ManifestFile string
	EventCount   int
	Compression  string
	Warnings     []string
}

type outputEvent struct {
	Type      string      `json:"type"`
	Seq       int         `json:"seq"`
	Time      int64       `json:"time"`
	Data      interface{} `json:"data"`
	SurfaceOp interface{} `json:"surfaceOp,omitempty"`
}

type exportBuilder struct {
	opts     ExportOptions
	header   Header
	events   []outputEvent
	seq      int
	clock    int64
	warnings []string
	turn     int
	step     int
	turnOpen bool
	stepOpen bool
}

// ExportBundle writes a v4 JSONL bundle that can be copied into a DSH
// sessions root. It never writes DSH_HOME and never modifies the source
// session; --apply is intentionally a later phase.
func ExportBundle(opts ExportOptions) (*ExportResult, error) {
	opts.Compression = strings.ToLower(strings.TrimSpace(opts.Compression))
	if opts.Compression == "" {
		opts.Compression = "none"
	}
	if opts.Compression != "none" && opts.Compression != "zstd" {
		return nil, fmt.Errorf("不支持的 compression: %s（可选 none|zstd）", opts.Compression)
	}
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
		abs, err := filepath.Abs(cwd)
		if err != nil {
			return nil, fmt.Errorf("无法解析导出 cwd: %w", err)
		}
		cwd = abs
	}
	sessionID := strings.TrimSpace(opts.SessionID)
	if sessionID == "" {
		sessionID = "session-" + uuid.NewString()
	}
	createdAt := opts.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	header := Header{
		Type:            "session",
		Version:         currentFormat,
		ID:              sessionID,
		CreatedAt:       createdAt.UnixMilli(),
		CWD:             cwd,
		IsSeeded:        false,
		DelegationDepth: 0,
		AgentPreset:     "standard",
	}
	builder := &exportBuilder{
		opts:   opts,
		header: header,
		clock:  time.Now().UnixMilli(),
	}
	if err := builder.build(); err != nil {
		return nil, err
	}
	bundleDir, err := filepath.Abs(opts.BundleDir)
	if err != nil {
		return nil, fmt.Errorf("解析 bundle 目录失败: %w", err)
	}
	sessionDir := filepath.Join(bundleDir, "sessions", ProjectKey(cwd), EncodeSegment(sessionID))
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建 bundle 目录失败: %w", err)
	}
	extension := ".jsonl"
	if opts.Compression == "zstd" {
		extension = ".jsonl.zstd"
	}
	sessionFile := filepath.Join(sessionDir, "session.v4"+extension)
	if _, err := os.Stat(sessionFile); err == nil {
		return nil, fmt.Errorf("目标 bundle 已存在会话文件（拒绝覆盖）: %s", sessionFile)
	}
	if err := writeBundleLog(sessionFile, header, builder.events, opts.Compression); err != nil {
		return nil, err
	}
	relativeLog, _ := filepath.Rel(bundleDir, sessionFile)
	manifest := map[string]interface{}{
		"version":           1,
		"source_agent":      "aicli",
		"target_agent":      string(agentID),
		"source_session_id": opts.SourceSession,
		"target_session_id": sessionID,
		"cwd":               cwd,
		"format_version":    currentFormat,
		"compression":       opts.Compression,
		"event_count":       len(builder.events),
		"files":             []map[string]interface{}{{"path": filepath.ToSlash(relativeLog), "role": "session-log"}},
		"warnings":          uniqueStrings(builder.warnings),
	}
	manifestFile := filepath.Join(bundleDir, "manifest.json")
	if err := writeJSONAtomic(manifestFile, manifest); err != nil {
		return nil, err
	}
	return &ExportResult{
		SessionID:    sessionID,
		BundleDir:    bundleDir,
		SessionFile:  sessionFile,
		ManifestFile: manifestFile,
		EventCount:   len(builder.events),
		Compression:  opts.Compression,
		Warnings:     uniqueStrings(builder.warnings),
	}, nil
}

func (b *exportBuilder) build() error {
	if len(b.opts.History) == 0 {
		b.warnings = append(b.warnings, "源会话没有任何消息")
		return nil
	}
	index := 0
	seenUser := false
	for index < len(b.opts.History) {
		message := b.opts.History[index]
		role := strings.ToLower(strings.TrimSpace(message.Role))
		switch role {
		case "user":
			if b.turnOpen {
				b.closeStep()
				b.closeTurn()
			}
			seenUser = true
			b.turn++
			b.turnOpen = true
			b.emit("turn/start", map[string]interface{}{"turn": b.turn}, nil)
			b.emitUser(message)
			index++
		case "assistant":
			if !seenUser {
				b.warnings = append(b.warnings, "跳过首个用户消息之前的 assistant 消息")
				index++
				continue
			}
			if !b.stepOpen {
				b.step++
				b.stepOpen = true
				b.emit("step/start", map[string]interface{}{"turn": b.turn, "step": b.step}, nil)
			}
			b.emitAssistant(message)
			calls := toolCallsByID(message)
			index++
			consumed := map[string]bool{}
			for index < len(b.opts.History) && strings.EqualFold(strings.TrimSpace(b.opts.History[index].Role), "tool") {
				toolMessage := b.opts.History[index]
				if _, ok := calls[toolMessage.ToolCallID]; !ok {
					b.warnings = append(b.warnings, fmt.Sprintf("跳过未匹配 assistant tool call 的 tool result: %s", toolMessage.ToolCallID))
					index++
					continue
				}
				b.emitToolResult(toolMessage)
				consumed[toolMessage.ToolCallID] = true
				index++
			}
			for callID := range calls {
				if !consumed[callID] {
					b.warnings = append(b.warnings, fmt.Sprintf("tool call %s 没有结果；已合成错误结果以保持 DSH 生命周期完整", callID))
					b.emitSyntheticToolError(callID)
				}
			}
			b.closeStep()
		case "tool":
			b.warnings = append(b.warnings, "跳过没有前置 assistant 的 tool 消息")
			index++
		default:
			b.warnings = append(b.warnings, fmt.Sprintf("跳过不支持的消息角色: %s", message.Role))
			index++
		}
	}
	b.closeStep()
	b.closeTurn()
	return nil
}

func (b *exportBuilder) emitUser(message runtimetypes.Message) {
	blocks := contentBlocks(message, "user", b)
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": ""})
	}
	userID := uuid.NewString()
	seq := b.emit("user/message", map[string]interface{}{
		"id":      userID,
		"role":    "user",
		"content": blocks,
		"source":  map[string]interface{}{"kind": "user"},
	}, "append")
	if title := strings.TrimSpace(b.opts.Title); title != "" {
		b.emit("session/title", map[string]interface{}{
			"title":       title,
			"messageSeqs": []int{seq},
			"source":      map[string]interface{}{"kind": "fallback"},
		}, nil)
		b.opts.Title = ""
	}
}

func (b *exportBuilder) emitAssistant(message runtimetypes.Message) {
	blocks := contentBlocks(message, "assistant", b)
	calls := toolCallsByID(message)
	if len(blocks) == 0 && len(calls) == 0 {
		b.warnings = append(b.warnings, "跳过空 assistant 消息")
		return
	}
	if reasoning := runtimetypes.GetReasoningBlock(message.Metadata); reasoning != nil {
		if text := reasoning.DisplayText(); text != "" {
			blocks = append(blocks, map[string]interface{}{"type": "reasoning", "text": text})
		}
	}
	for _, call := range message.ToolCalls {
		if strings.TrimSpace(call.ID) == "" {
			continue
		}
		blocks = append(blocks, map[string]interface{}{
			"type":      "tool-call",
			"id":        call.ID,
			"name":      call.Name,
			"arguments": toolArguments(call),
		})
	}
	model := strings.TrimSpace(b.opts.Model)
	if model == "" {
		model = "unknown"
	}
	messageID := uuid.NewString()
	stream := assistantStream(blocks, b.clock, len(calls) > 0)
	b.emit("assistant/message", map[string]interface{}{
		"turn": b.turn,
		"step": b.step,
		"message": map[string]interface{}{
			"id":      messageID,
			"role":    "assistant",
			"content": blocks,
			"source":  map[string]interface{}{"kind": "model", "provider": "aicli", "model": model},
		},
		"stream": stream,
	}, "append")
	for _, call := range message.ToolCalls {
		callID := strings.TrimSpace(call.ID)
		if callID == "" {
			continue
		}
		args := toolArguments(call)
		b.emit("tool/call", map[string]interface{}{
			"turn":      b.turn,
			"step":      b.step,
			"callId":    callID,
			"name":      call.Name,
			"arguments": args,
		}, nil)
	}
}

func (b *exportBuilder) emitToolResult(message runtimetypes.Message) {
	callID := strings.TrimSpace(message.ToolCallID)
	if callID == "" {
		return
	}
	blocks := contentBlocks(message, "tool", b)
	isError := message.Metadata.GetBool("dsh_is_error", false)
	b.emit("tool/result", map[string]interface{}{
		"turn": b.turn,
		"step": b.step,
		"message": map[string]interface{}{
			"id":         uuid.NewString(),
			"role":       "tool",
			"source":     map[string]interface{}{"kind": "tool", "callId": callID},
			"toolCallId": callID,
			"content":    blocks,
			"isError":    isError,
		},
	}, "append")
}

func (b *exportBuilder) emitSyntheticToolError(callID string) {
	b.emit("tool/result", map[string]interface{}{
		"turn": b.turn,
		"step": b.step,
		"message": map[string]interface{}{
			"id":         uuid.NewString(),
			"role":       "tool",
			"source":     map[string]interface{}{"kind": "tool", "callId": callID},
			"toolCallId": callID,
			"isError":    true,
			"content": []map[string]interface{}{{
				"type": "text",
				"text": "Imported history ended without a recorded tool result; outcome is unknown.",
			}},
		},
	}, "append")
}

func (b *exportBuilder) closeStep() {
	if !b.stepOpen {
		return
	}
	b.emit("step/end", map[string]interface{}{"turn": b.turn, "step": b.step}, nil)
	b.stepOpen = false
}

func (b *exportBuilder) closeTurn() {
	if !b.turnOpen {
		return
	}
	b.emit("turn/end", map[string]interface{}{"turn": b.turn, "reason": map[string]interface{}{"kind": "completed"}}, nil)
	b.turnOpen = false
}

func (b *exportBuilder) emit(eventType string, data interface{}, surfaceOp interface{}) int {
	seq := b.seq
	b.seq++
	b.clock++
	b.events = append(b.events, outputEvent{
		Type:      eventType,
		Seq:       seq,
		Time:      b.clock,
		Data:      data,
		SurfaceOp: surfaceOp,
	})
	return seq
}

func toolCallsByID(message runtimetypes.Message) map[string]struct{} {
	result := make(map[string]struct{}, len(message.ToolCalls))
	for _, call := range message.ToolCalls {
		if id := strings.TrimSpace(call.ID); id != "" {
			result[id] = struct{}{}
		}
	}
	return result
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

func contentBlocks(message runtimetypes.Message, role string, builder *exportBuilder) []map[string]interface{} {
	blocks := make([]map[string]interface{}, 0, 2)
	if strings.TrimSpace(message.Content) != "" {
		blocks = append(blocks, map[string]interface{}{"type": "text", "text": message.Content})
	}
	for _, part := range message.ContentParts {
		switch part.Type {
		case runtimetypes.ContentPartText:
			if part.Text != "" {
				blocks = append(blocks, map[string]interface{}{"type": "text", "text": part.Text})
			}
		case runtimetypes.ContentPartImage:
			builder.warnings = append(builder.warnings, fmt.Sprintf("%s 消息包含图片；首版 bundle 以文本占位", role))
			blocks = append(blocks, map[string]interface{}{"type": "text", "text": "[image attachment from aicli omitted during DSH export]"})
		default:
			builder.warnings = append(builder.warnings, fmt.Sprintf("%s 消息包含未支持的 content part: %s", role, part.Type))
		}
	}
	return blocks
}

func assistantStream(blocks []map[string]interface{}, base int64, toolCalls bool) []interface{} {
	stream := make([]interface{}, 0, len(blocks)*3+1)
	time := base
	for index, block := range blocks {
		blockType, _ := block["type"].(string)
		time++
		stream = append(stream, map[string]interface{}{
			"type":  "chunk",
			"time":  time,
			"chunk": map[string]interface{}{"type": "block-start", "index": index, "blockType": blockType},
		})
		switch blockType {
		case "text":
			stream = append(stream, map[string]interface{}{
				"type": "text-chunks", "time0": time, "index": index, "dt": []int{},
				"texts": []string{stringValue(block["text"])},
			})
		case "reasoning":
			stream = append(stream, map[string]interface{}{
				"type": "reasoning-chunks", "time0": time, "index": index, "dt": []int{},
				"texts": []string{stringValue(block["text"])},
			})
		case "tool-call":
			stream = append(stream, map[string]interface{}{
				"type": "tool-call-chunks", "time0": time, "index": index, "dt": []int{},
				"id": stringValue(block["id"]), "name": stringValue(block["name"]),
				"args": []string{stringValue(block["arguments"])},
			})
		}
		stream = append(stream, map[string]interface{}{
			"type":  "chunk",
			"time":  time,
			"chunk": map[string]interface{}{"type": "block-end", "index": index, "block": block},
		})
	}
	stream = append(stream, map[string]interface{}{
		"type":  "chunk",
		"time":  time + 1,
		"chunk": map[string]interface{}{"type": "finish", "reason": map[string]interface{}{"kind": finishKind(toolCalls)}},
	})
	return stream
}

func finishKind(toolCalls bool) string {
	if toolCalls {
		return "tool-calls"
	}
	return "stop"
}

func stringValue(value interface{}) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func writeBundleLog(path string, header Header, events []outputEvent, compression string) error {
	headerPayload, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("序列化 DSH header 失败: %w", err)
	}
	eventLines := make([]string, 0, len(events))
	for _, event := range events {
		encoded, err := json.Marshal(event)
		if err != nil {
			return fmt.Errorf("序列化 DSH 事件失败: %w", err)
		}
		eventLines = append(eventLines, string(encoded))
	}
	if compression == "zstd" {
		encoder, err := zstd.NewWriter(nil, zstd.WithEncoderCRC(true))
		if err != nil {
			return fmt.Errorf("创建 zstd encoder 失败: %w", err)
		}
		defer encoder.Close()
		var output []byte
		output = append(output, encoder.EncodeAll(append(headerPayload, '\n'), nil)...)
		if len(eventLines) > 0 {
			output = append(output, encoder.EncodeAll([]byte(strings.Join(eventLines, "\n")+"\n"), nil)...)
		}
		return writeBytesAtomic(path, output)
	}
	content := string(headerPayload) + "\n"
	if len(eventLines) > 0 {
		content += strings.Join(eventLines, "\n") + "\n"
	}
	return writeBytesAtomic(path, []byte(content))
}

func writeBytesAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	tempPath := temp.Name()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("写入临时文件失败: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("发布文件失败: %w", err)
	}
	return nil
}

func writeJSONAtomic(path string, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 manifest 失败: %w", err)
	}
	data = append(data, '\n')
	return writeBytesAtomic(path, data)
}

// ProjectKey implements DSH's readable project-directory key.
func ProjectKey(cwd string) string {
	if cwd == "" {
		return "_no-cwd"
	}
	var builder strings.Builder
	separatorRun := false
	for _, runeValue := range cwd {
		switch {
		case runeValue == '/' || runeValue == '\\' || runeValue == ':':
			if !separatorRun {
				builder.WriteByte('-')
			}
			separatorRun = true
		case isSafePathRune(runeValue):
			builder.WriteRune(runeValue)
			separatorRun = false
		default:
			builder.WriteString(fmt.Sprintf("~%04X", runeValue))
			separatorRun = false
		}
	}
	slug := strings.TrimLeft(builder.String(), "-")
	if slug == "" {
		slug = "root"
	}
	if len(slug) > 251 {
		slug = slug[:251]
	}
	return "--" + slug + "--"
}

// EncodeSegment implements DSH's injective session-directory segment encoding.
func EncodeSegment(raw string) string {
	if raw == "" {
		return ""
	}
	if raw == "." {
		return "~002E"
	}
	if raw == ".." {
		return "~002E~002E"
	}
	var builder strings.Builder
	for _, runeValue := range raw {
		if isSafePathRune(runeValue) {
			builder.WriteRune(runeValue)
		} else {
			builder.WriteString(fmt.Sprintf("~%04X", runeValue))
		}
	}
	return builder.String()
}

func isSafePathRune(value rune) bool {
	return value == '.' || value == '_' || value == '-' ||
		value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}
