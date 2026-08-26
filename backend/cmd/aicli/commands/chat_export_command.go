package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type chatExportFormat string

const (
	chatExportFormatFull chatExportFormat = "full"
	chatExportFormatBody chatExportFormat = "body"
)

type chatExportOptions struct {
	Target         string
	Format         chatExportFormat
	OutputPath     string
	OutputDir      string
	ExplicitTarget bool
	ExplicitFormat bool
}

type chatSessionExportStats struct {
	MessageCount     int `json:"message_count"`
	ToolCallCount    int `json:"tool_call_count"`
	ToolResultCount  int `json:"tool_result_count"`
	ContentPartCount int `json:"content_part_count"`
}

type chatSessionExportEnvelope struct {
	Version      int                         `json:"version"`
	ExportedAt   time.Time                   `json:"exported_at"`
	Format       string                      `json:"format"`
	Source       string                      `json:"source,omitempty"`
	SessionPath  string                      `json:"session_path,omitempty"`
	SessionStore string                      `json:"session_store,omitempty"`
	Preview      *runtimechat.SessionPreview `json:"preview,omitempty"`
	Stats        chatSessionExportStats      `json:"stats"`
	Session      *runtimechat.Session        `json:"session"`
}

type chatExportResult struct {
	Path      string
	Format    chatExportFormat
	SessionID string
	Stats     chatSessionExportStats
}

func handleExportCommand(session *ChatSession, command string) bool {
	if unifiedDirectInteractiveOutput(session) {
		if result, handled := executeStructuredExportCommand(session, command); handled {
			renderErr := renderChatCommandResult(session, result, false)
			if renderErr == nil && result.OpenExportPicker != nil {
				openChatExportPicker(session, *result.OpenExportPicker)
			}
			return false
		}
		// executeStructuredExportCommand handles every /export variant today, so
		// this branch is defensive. The deny-list fence no longer contains
		// /export (it is fully migrated), so rejectUnifiedInteractiveLegacyCommand
		// would fail open into the legacy stdout handler; fail closed instead.
		_ = renderChatCommandResult(session, commandTextResult("错误: /export 变体无法通过统一渲染命令通道处理"), false)
		return false
	}
	if rejectUnifiedInteractiveLegacyCommand(session, "/export") {
		return false
	}
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	opts, err := parseChatExportOptions(extractCommandArgument(command))
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		fmt.Println("用法: /export [current|latest|<session-id>] [--full|--body] [--output <path>|--dir <dir>]")
		return false
	}
	if !opts.ExplicitTarget && !session.NoInteractive && !session.JSONOutput {
		return exportInteractiveSelect(session, opts)
	}
	if !opts.ExplicitTarget {
		opts.Target = "current"
		opts.ExplicitTarget = true
	}
	result, err := exportChatSession(session, opts)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	printChatExportResult(result)
	return false
}

func parseChatExportOptions(argument string) (chatExportOptions, error) {
	opts := chatExportOptions{Format: chatExportFormatFull}
	fields := splitChatCommandFields(argument)
	for i := 0; i < len(fields); i++ {
		token := strings.TrimSpace(fields[i])
		if token == "" {
			continue
		}
		lower := strings.ToLower(token)
		switch {
		case lower == "--full" || lower == "full" || lower == "json":
			opts.Format = chatExportFormatFull
			opts.ExplicitFormat = true
		case lower == "--body" || lower == "body" || lower == "text" || lower == "markdown" || lower == "md":
			opts.Format = chatExportFormatBody
			opts.ExplicitFormat = true
		case lower == "--format" || lower == "--mode":
			if i+1 >= len(fields) {
				return opts, fmt.Errorf("%s 需要指定 full 或 body", token)
			}
			i++
			if err := applyChatExportFormat(&opts, fields[i]); err != nil {
				return opts, err
			}
		case strings.HasPrefix(lower, "--format="):
			if err := applyChatExportFormat(&opts, token[len("--format="):]); err != nil {
				return opts, err
			}
		case strings.HasPrefix(lower, "--mode="):
			if err := applyChatExportFormat(&opts, token[len("--mode="):]); err != nil {
				return opts, err
			}
		case lower == "--output" || lower == "-o":
			if i+1 >= len(fields) {
				return opts, fmt.Errorf("%s 需要指定输出文件路径", token)
			}
			i++
			opts.OutputPath = strings.TrimSpace(fields[i])
		case strings.HasPrefix(lower, "--output="):
			opts.OutputPath = strings.TrimSpace(token[len("--output="):])
		case lower == "--dir":
			if i+1 >= len(fields) {
				return opts, fmt.Errorf("%s 需要指定输出目录", token)
			}
			i++
			opts.OutputDir = strings.TrimSpace(fields[i])
		case strings.HasPrefix(lower, "--dir="):
			opts.OutputDir = strings.TrimSpace(token[len("--dir="):])
		case strings.HasPrefix(lower, "-"):
			return opts, fmt.Errorf("未知 /export 选项: %s", token)
		default:
			if opts.ExplicitTarget {
				return opts, fmt.Errorf("只能指定一个导出会话目标")
			}
			opts.Target = token
			opts.ExplicitTarget = true
		}
	}
	return opts, nil
}

func applyChatExportFormat(opts *chatExportOptions, value string) error {
	if opts == nil {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "full", "json":
		opts.Format = chatExportFormatFull
	case "body", "text", "markdown", "md":
		opts.Format = chatExportFormatBody
	default:
		return fmt.Errorf("未知导出格式: %s", strings.TrimSpace(value))
	}
	opts.ExplicitFormat = true
	return nil
}

type exportMenuChoice int

const (
	exportChoiceDefault exportMenuChoice = iota
	exportChoicePick
	exportChoiceCancel
)

func exportInteractiveSelect(session *ChatSession, opts chatExportOptions) bool {
	if session.SessionManager == nil && session.RuntimeSession == nil {
		fmt.Println("当前没有可导出的会话")
		return false
	}
	if session.SessionManager == nil {
		opts.Target = "current"
		opts.ExplicitTarget = true
		return exportSelectedSession(session, opts)
	}

	candidates, err := listResumeCandidateChatSessions(session.SessionManager, session.SessionUserID, session.SessionFilter, currentRuntimeSessionID(session))
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	if session.RuntimeSession == nil && len(candidates) == 0 {
		fmt.Println("当前没有可导出的会话")
		return false
	}
	if session.RuntimeSession != nil && len(candidates) == 0 {
		beginDirectInteractiveOutput(session)
		opts.Target = "current"
		opts.ExplicitTarget = true
		if !opts.ExplicitFormat {
			format, ok, err := readExportFormatChoice(session, startupSessionOptionLabelWidth())
			if err != nil {
				fmt.Printf("错误: %v\n", err)
				return false
			}
			if !ok {
				fmt.Println("已取消导出")
				return false
			}
			opts.Format = format
			opts.ExplicitFormat = true
		}
		return exportSelectedSession(session, opts)
	}

	beginDirectInteractiveOutput(session)
	uiPrintSessionSelectionSummary(len(candidates), session.SessionFilter)
	choice, err := readExportMenuChoice(session, startupSessionOptionLabelWidth(), session.RuntimeSession != nil)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	switch choice {
	case exportChoiceCancel:
		fmt.Println("已取消导出")
		return false
	case exportChoicePick:
		picked, err := readHistoricalSessionPick(
			session,
			candidates,
			"选择要导出的历史会话（最近更新优先）:",
			"选择会话 (回车选择 1，q 取消): ",
		)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		if picked == nil {
			fmt.Println("已取消导出")
			return false
		}
		opts.Target = picked.ID
	default:
		if session.RuntimeSession != nil {
			opts.Target = "current"
		} else {
			opts.Target = "latest"
		}
	}
	opts.ExplicitTarget = true
	if !opts.ExplicitFormat {
		format, ok, err := readExportFormatChoice(session, startupSessionOptionLabelWidth())
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		if !ok {
			fmt.Println("已取消导出")
			return false
		}
		opts.Format = format
		opts.ExplicitFormat = true
	}
	return exportSelectedSession(session, opts)
}

func exportSelectedSession(session *ChatSession, opts chatExportOptions) bool {
	result, err := exportChatSession(session, opts)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	printChatExportResult(result)
	return false
}

func readExportMenuChoice(session *ChatSession, optionWidth int, hasCurrent bool) (exportMenuChoice, error) {
	prompt := "选项 (回车=1): "
	usePopup := useRuntimeSelectionPopup(session)
	if usePopup {
		defer clearRuntimeSelectionPopup(session)
	}
	defaultLabel := "导出当前会话"
	if !hasCurrent {
		defaultLabel = "导出最近可导出历史会话"
	}
	warning := ""
	for {
		lines := []string{
			fmt.Sprintf("  %-*s %s", optionWidth, "[1]", defaultLabel),
			fmt.Sprintf("  %-*s %s", optionWidth, "[2]", "选择历史会话"),
			fmt.Sprintf("  %-*s %s", optionWidth, "[3]", "取消（返回当前会话）"),
		}
		if usePopup {
			popupLines := append([]string(nil), lines...)
			if warning != "" {
				popupLines = append(popupLines, warning)
			}
			showRuntimeSelectionPopup(session, popupLines, prompt)
		} else {
			for _, line := range lines {
				fmt.Println(line)
			}
			fmt.Print(prompt)
		}

		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if !usePopup {
			fmt.Println()
		}
		if err != nil {
			return exportChoiceCancel, err
		}
		choice := strings.TrimSpace(normalizeQueuedInputLine(text))
		warning = ""
		switch choice {
		case "", "1":
			return exportChoiceDefault, nil
		case "2":
			return exportChoicePick, nil
		case "3", "q", "quit", "cancel", "exit":
			return exportChoiceCancel, nil
		default:
			if usePopup {
				warning = "  无效的选择，请重新输入"
			} else {
				ui.PrintWarning("无效的选择，请重新输入")
			}
		}
	}
}

func readExportFormatChoice(session *ChatSession, optionWidth int) (chatExportFormat, bool, error) {
	prompt := "格式 (回车=1): "
	usePopup := useRuntimeSelectionPopup(session)
	if usePopup {
		defer clearRuntimeSelectionPopup(session)
	}
	warning := ""
	for {
		lines := []string{
			fmt.Sprintf("  %-*s %s", optionWidth, "[1]", "完整 JSON（包含 metadata、tool_calls、tool 结果等）"),
			fmt.Sprintf("  %-*s %s", optionWidth, "[2]", "正文 Markdown（仅用户/助手正文）"),
			fmt.Sprintf("  %-*s %s", optionWidth, "[3]", "取消"),
		}
		if usePopup {
			popupLines := append([]string(nil), lines...)
			if warning != "" {
				popupLines = append(popupLines, warning)
			}
			showRuntimeSelectionPopup(session, popupLines, prompt)
		} else {
			for _, line := range lines {
				fmt.Println(line)
			}
			fmt.Print(prompt)
		}
		text, err := chatInteractiveReadPriorityLineWithPrompt(session, context.Background(), prompt)
		if !usePopup {
			fmt.Println()
		}
		if err != nil {
			return chatExportFormatFull, false, err
		}
		choice := strings.TrimSpace(normalizeQueuedInputLine(text))
		warning = ""
		switch choice {
		case "", "1":
			return chatExportFormatFull, true, nil
		case "2":
			return chatExportFormatBody, true, nil
		case "3", "q", "quit", "cancel", "exit":
			return chatExportFormatFull, false, nil
		default:
			if usePopup {
				warning = "  无效的选择，请重新输入"
			} else {
				ui.PrintWarning("无效的选择，请重新输入")
			}
		}
	}
}

func exportChatSession(session *ChatSession, opts chatExportOptions) (*chatExportResult, error) {
	runtimeSession, source, err := resolveChatExportRuntimeSession(session, opts)
	if err != nil {
		return nil, err
	}
	if runtimeSession == nil {
		return nil, fmt.Errorf("未找到可导出的会话")
	}
	outputPath, err := resolveChatExportOutputPath(session, runtimeSession, opts)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建导出目录失败: %w", err)
	}
	temporaryFile, err := os.CreateTemp(filepath.Dir(outputPath), filepath.Base(outputPath)+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("创建导出临时文件失败: %w", err)
	}
	temporaryPath := temporaryFile.Name()
	if err := temporaryFile.Chmod(0o644); err != nil {
		_ = temporaryFile.Close()
		_ = os.Remove(temporaryPath)
		return nil, fmt.Errorf("设置导出临时文件权限失败: %w", err)
	}
	if err := temporaryFile.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, fmt.Errorf("关闭导出临时文件失败: %w", err)
	}
	defer os.Remove(temporaryPath)
	var stats chatSessionExportStats
	switch opts.Format {
	case chatExportFormatBody:
		stats, err = writeChatSessionBodyExport(temporaryPath, session, runtimeSession)
	default:
		opts.Format = chatExportFormatFull
		stats, err = writeChatSessionFullExport(temporaryPath, session, runtimeSession, source)
	}
	if err != nil {
		return nil, err
	}
	if err := publishChatExportFile(temporaryPath, outputPath); err != nil {
		return nil, err
	}
	return &chatExportResult{
		Path:      resolveAbsoluteChatPath(outputPath),
		Format:    opts.Format,
		SessionID: strings.TrimSpace(runtimeSession.ID),
		Stats:     stats,
	}, nil
}

func publishChatExportFile(temporaryPath, outputPath string) error {
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		if err := os.Rename(temporaryPath, outputPath); err != nil {
			return fmt.Errorf("发布输出文件失败: %w", err)
		}
		return nil
	} else if err != nil {
		return fmt.Errorf("检查现有输出文件失败: %w", err)
	}
	backupPath := temporaryPath + ".previous"
	if err := os.Rename(outputPath, backupPath); err != nil {
		return fmt.Errorf("准备替换输出文件失败: %w", err)
	}
	if err := os.Rename(temporaryPath, outputPath); err != nil {
		restoreErr := os.Rename(backupPath, outputPath)
		if restoreErr != nil {
			return fmt.Errorf("发布输出文件失败: %w；恢复原文件失败: %v；原文件保留在 %s", err, restoreErr, backupPath)
		}
		return fmt.Errorf("发布输出文件失败: %w（原文件已恢复）", err)
	}
	_ = os.Remove(backupPath)
	return nil
}

func streamChatExportMessages(session *ChatSession, runtimeSession *runtimechat.Session, visit func(int, runtimetypes.Message) error) error {
	if session != nil && session.SessionManager != nil && runtimeSession != nil && strings.TrimSpace(runtimeSession.ID) != "" {
		return session.SessionManager.StreamHistory(context.Background(), runtimeSession.ID, visit)
	}
	if runtimeSession == nil {
		return nil
	}
	for index, message := range runtimeSession.GetMessages() {
		if err := visit(index+1, message); err != nil {
			return err
		}
	}
	return nil
}

func streamChatExportMessageJSON(session *ChatSession, runtimeSession *runtimechat.Session, visit func(int, runtimechat.CanonicalMessageInfo, io.Reader) error) error {
	if session != nil && session.SessionManager != nil && runtimeSession != nil && strings.TrimSpace(runtimeSession.ID) != "" {
		if streamer, ok := session.SessionManager.GetStorage().(runtimechat.SessionStorageCanonicalJSONStreamer); ok {
			return streamer.StreamMessageJSON(context.Background(), runtimeSession.ID, visit)
		}
	}
	return streamChatExportMessages(session, runtimeSession, func(sequence int, message runtimetypes.Message) error {
		payload, err := json.Marshal(message)
		if err != nil {
			return err
		}
		return visit(sequence, canonicalMessageInfo(message), bytes.NewReader(payload))
	})
}

func canonicalMessageInfo(message runtimetypes.Message) runtimechat.CanonicalMessageInfo {
	return runtimechat.CanonicalMessageInfo{
		Role:             message.Role,
		RoleKnown:        true,
		ToolCallCount:    len(message.ToolCalls),
		ToolResult:       strings.EqualFold(strings.TrimSpace(message.Role), "tool"),
		ContentPartCount: len(message.ContentParts),
		StatsKnown:       true,
	}
}

func updateChatExportStats(stats *chatSessionExportStats, message runtimetypes.Message) {
	stats.MessageCount++
	stats.ToolCallCount += len(message.ToolCalls)
	stats.ContentPartCount += len(message.ContentParts)
	if strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
		stats.ToolResultCount++
	}
}

func updateChatExportStatsFromInfo(stats *chatSessionExportStats, info runtimechat.CanonicalMessageInfo) {
	stats.MessageCount++
	stats.ToolCallCount += info.ToolCallCount
	stats.ContentPartCount += info.ContentPartCount
	if info.ToolResult {
		stats.ToolResultCount++
	}
}

func writeChatSessionFullExport(path string, session *ChatSession, runtimeSession *runtimechat.Session, source string) (chatSessionExportStats, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return chatSessionExportStats{}, fmt.Errorf("创建会话导出文件失败: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = file.Close()
		}
	}()
	prefix := struct {
		Version      int                         `json:"version"`
		ExportedAt   time.Time                   `json:"exported_at"`
		Format       string                      `json:"format"`
		Source       string                      `json:"source,omitempty"`
		SessionPath  string                      `json:"session_path,omitempty"`
		SessionStore string                      `json:"session_store,omitempty"`
		Preview      *runtimechat.SessionPreview `json:"preview,omitempty"`
	}{Version: 1, ExportedAt: time.Now(), Format: string(chatExportFormatFull), Source: source, Preview: runtimeSession.BuildPreview()}
	if session != nil {
		prefix.SessionStore = currentRuntimeSessionStoreSummary(session)
		if strings.EqualFold(strings.TrimSpace(runtimeSession.ID), currentRuntimeSessionID(session)) {
			prefix.SessionPath = currentRuntimeSessionPath(session)
		}
	}
	prefixJSON, err := json.Marshal(prefix)
	if err != nil {
		return chatSessionExportStats{}, fmt.Errorf("序列化会话导出元数据失败: %w", err)
	}
	if len(prefixJSON) == 0 || prefixJSON[len(prefixJSON)-1] != '}' {
		return chatSessionExportStats{}, fmt.Errorf("无效的会话导出元数据")
	}
	if _, err := file.Write(prefixJSON[:len(prefixJSON)-1]); err != nil {
		return chatSessionExportStats{}, err
	}
	if _, err := io.WriteString(file, `,"session":`); err != nil {
		return chatSessionExportStats{}, err
	}
	stats, err := writeStreamedRuntimeSessionJSON(file, session, runtimeSession)
	if err != nil {
		return chatSessionExportStats{}, err
	}
	statsJSON, err := json.Marshal(stats)
	if err != nil {
		return chatSessionExportStats{}, err
	}
	if _, err := io.WriteString(file, `,"stats":`); err != nil {
		return chatSessionExportStats{}, err
	}
	if _, err := file.Write(statsJSON); err != nil {
		return chatSessionExportStats{}, err
	}
	if _, err := io.WriteString(file, "}\n"); err != nil {
		return chatSessionExportStats{}, err
	}
	if err := file.Close(); err != nil {
		return chatSessionExportStats{}, err
	}
	closed = true
	return stats, nil
}

func writeStreamedRuntimeSessionJSON(writer io.Writer, session *ChatSession, runtimeSession *runtimechat.Session) (chatSessionExportStats, error) {
	metadataOnly := runtimeSession.Clone()
	metadataOnly.History = nil
	payload, err := json.Marshal(metadataOnly)
	if err != nil {
		return chatSessionExportStats{}, fmt.Errorf("序列化会话 metadata 失败: %w", err)
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(payload, &fields); err != nil {
		return chatSessionExportStats{}, err
	}
	delete(fields, "history")
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if _, err := io.WriteString(writer, "{"); err != nil {
		return chatSessionExportStats{}, err
	}
	wroteField := false
	for _, key := range keys {
		keyJSON, _ := json.Marshal(key)
		if wroteField {
			_, err = io.WriteString(writer, ",")
		}
		if err == nil {
			_, err = writer.Write(keyJSON)
		}
		if err == nil {
			_, err = io.WriteString(writer, ":")
		}
		if err == nil {
			_, err = writer.Write(fields[key])
		}
		if err != nil {
			return chatSessionExportStats{}, err
		}
		wroteField = true
	}
	if wroteField {
		if _, err := io.WriteString(writer, ","); err != nil {
			return chatSessionExportStats{}, err
		}
	}
	if _, err := io.WriteString(writer, `"history":[`); err != nil {
		return chatSessionExportStats{}, err
	}
	stats := chatSessionExportStats{}
	firstMessage := true
	err = streamChatExportMessageJSON(session, runtimeSession, func(_ int, info runtimechat.CanonicalMessageInfo, payload io.Reader) error {
		if !firstMessage {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		if info.StatsKnown {
			if _, err := io.Copy(writer, payload); err != nil {
				return err
			}
			updateChatExportStatsFromInfo(&stats, info)
		} else {
			var message runtimetypes.Message
			if err := json.NewDecoder(payload).Decode(&message); err != nil {
				return err
			}
			if err := json.NewEncoder(writer).Encode(message); err != nil {
				return err
			}
			updateChatExportStats(&stats, message)
		}
		firstMessage = false
		return nil
	})
	if err != nil {
		return chatSessionExportStats{}, fmt.Errorf("流式读取 canonical 会话历史失败: %w", err)
	}
	if _, err := io.WriteString(writer, "]}"); err != nil {
		return chatSessionExportStats{}, err
	}
	return stats, nil
}

func writeChatSessionBodyExport(path string, session *ChatSession, runtimeSession *runtimechat.Session) (chatSessionExportStats, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return chatSessionExportStats{}, fmt.Errorf("创建会话正文导出文件失败: %w", err)
	}
	defer file.Close()
	title := "(untitled)"
	if preview := runtimeSession.BuildPreview(); preview != nil && strings.TrimSpace(preview.Title) != "" {
		title = strings.TrimSpace(preview.Title)
	}
	fmt.Fprintf(file, "# %s\n\n- Session: %s\n- State: %s\n- Created: %s\n- Updated: %s\n\n## Conversation\n",
		markdownPlainLine(title), strings.TrimSpace(runtimeSession.ID), runtimeSession.State,
		formatChatExportTime(runtimeSession.CreatedAt), formatChatExportTime(runtimeSession.UpdatedAt))
	stats := chatSessionExportStats{}
	wrote := false
	err = streamChatExportMessageJSON(session, runtimeSession, func(_ int, info runtimechat.CanonicalMessageInfo, payload io.Reader) error {
		role := strings.ToLower(strings.TrimSpace(info.Role))
		if info.RoleKnown && role != "user" && role != "assistant" {
			if info.StatsKnown {
				updateChatExportStatsFromInfo(&stats, info)
			} else {
				stats.MessageCount++
				if role == "tool" {
					stats.ToolResultCount++
				}
			}
			return nil
		}
		if info.StatsKnown {
			updateChatExportStatsFromInfo(&stats, info)
		}
		var message runtimetypes.Message
		if err := json.NewDecoder(payload).Decode(&message); err != nil {
			return err
		}
		if !info.StatsKnown {
			updateChatExportStats(&stats, message)
			role = strings.ToLower(strings.TrimSpace(message.Role))
		}
		if role != "user" && role != "assistant" {
			return nil
		}
		content := strings.TrimSpace(chatExportMessageBodyText(message))
		if content == "" {
			return nil
		}
		label := "User"
		if role == "assistant" {
			label = "Assistant"
		}
		_, err := fmt.Fprintf(file, "\n### %s\n\n%s\n", label, content)
		wrote = true
		return err
	})
	if err != nil {
		return chatSessionExportStats{}, fmt.Errorf("流式读取 canonical 会话历史失败: %w", err)
	}
	if !wrote {
		_, err = io.WriteString(file, "\n<empty>\n")
	}
	if err != nil {
		return chatSessionExportStats{}, err
	}
	if err := file.Close(); err != nil {
		return chatSessionExportStats{}, err
	}
	return stats, nil
}

func resolveChatExportRuntimeSession(session *ChatSession, opts chatExportOptions) (*runtimechat.Session, string, error) {
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		target = "current"
	}
	switch strings.ToLower(target) {
	case "current", "now", ".":
		if session == nil || session.RuntimeSession == nil {
			return nil, "", fmt.Errorf("当前没有可导出的持久化会话")
		}
		warnIfChatSessionSyncFails(session, "export current session", syncRuntimeSessionFromChat(session))
		return session.RuntimeSession.Clone(), "current", nil
	case "latest", "last":
		if session == nil || session.SessionManager == nil {
			return nil, "", fmt.Errorf("会话管理未启用")
		}
		runtimeSession, err := loadLatestResumableRuntimeSessionExcluding(context.Background(), session.SessionManager, session.SessionUserID, currentRuntimeSessionID(session))
		if err != nil {
			return nil, "", err
		}
		return runtimeSession.Clone(), "latest", nil
	default:
		if session == nil || session.SessionManager == nil {
			return nil, "", fmt.Errorf("会话管理未启用")
		}
		runtimeSession, err := session.SessionManager.Get(context.Background(), target)
		if err != nil {
			return nil, "", err
		}
		if runtimeSession.UserID != session.SessionUserID {
			return nil, "", fmt.Errorf("会话 %s 不属于当前用户", target)
		}
		return runtimeSession.Clone(), "session", nil
	}
}

func buildChatSessionExportEnvelope(session *ChatSession, runtimeSession *runtimechat.Session, source string, stats chatSessionExportStats) chatSessionExportEnvelope {
	envelope := chatSessionExportEnvelope{
		Version:    1,
		ExportedAt: time.Now(),
		Format:     string(chatExportFormatFull),
		Source:     source,
		Preview:    runtimeSession.BuildPreview(),
		Stats:      stats,
		Session:    runtimeSession.Clone(),
	}
	if session != nil {
		envelope.SessionStore = currentRuntimeSessionStoreSummary(session)
		if strings.EqualFold(strings.TrimSpace(runtimeSession.ID), currentRuntimeSessionID(session)) {
			envelope.SessionPath = currentRuntimeSessionPath(session)
		} else if session.SessionDir != "" && runtimeSession.ID != "" {
			envelope.SessionPath = resolveAbsoluteChatPath(fileSessionJSONPath(session.SessionDir, runtimeSession.ID, runtimeSession.CreatedAt))
		}
	}
	return envelope
}

func resolveChatExportOutputPath(session *ChatSession, runtimeSession *runtimechat.Session, opts chatExportOptions) (string, error) {
	if strings.TrimSpace(opts.OutputPath) != "" {
		path := resolveAbsoluteChatPath(opts.OutputPath)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			opts.OutputDir = path
		} else {
			return path, nil
		}
	}
	outputDir := strings.TrimSpace(opts.OutputDir)
	if outputDir == "" {
		outputDir = defaultChatExportDir(session)
	}
	if outputDir == "" {
		return "", fmt.Errorf("无法确定导出目录")
	}
	extension := ".json"
	if opts.Format == chatExportFormatBody {
		extension = ".md"
	}
	sessionID := "session"
	if runtimeSession != nil && strings.TrimSpace(runtimeSession.ID) != "" {
		sessionID = sanitizeChatExportFileComponent(runtimeSession.ID)
	}
	filename := fmt.Sprintf("%s_%s_%s%s", sessionID, time.Now().Format("20060102_150405"), opts.Format, extension)
	return resolveAbsoluteChatPath(filepath.Join(outputDir, filename)), nil
}

func defaultChatExportDir(session *ChatSession) string {
	if session != nil && session.Logger != nil {
		if dir := session.Logger.ExportsDir(); strings.TrimSpace(dir) != "" {
			return resolveAbsoluteChatPath(dir)
		}
	}
	return resolveAbsoluteChatPath(filepath.Join(resolveDefaultChatLogDir(), "exports"))
}

func chatSessionExportStatsFor(session *runtimechat.Session) chatSessionExportStats {
	stats := chatSessionExportStats{}
	if session == nil {
		return stats
	}
	for _, message := range session.GetMessages() {
		stats.MessageCount++
		stats.ToolCallCount += len(message.ToolCalls)
		stats.ContentPartCount += len(message.ContentParts)
		if strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			stats.ToolResultCount++
		}
	}
	return stats
}

func renderChatSessionBodyMarkdown(session *runtimechat.Session) string {
	var builder strings.Builder
	title := "(untitled)"
	if session != nil {
		if preview := session.BuildPreview(); preview != nil && strings.TrimSpace(preview.Title) != "" {
			title = strings.TrimSpace(preview.Title)
		}
	}
	builder.WriteString("# ")
	builder.WriteString(markdownPlainLine(title))
	builder.WriteString("\n\n")
	if session != nil {
		builder.WriteString("- Session: ")
		builder.WriteString(strings.TrimSpace(session.ID))
		builder.WriteString("\n")
		builder.WriteString("- State: ")
		builder.WriteString(string(session.State))
		builder.WriteString("\n")
		builder.WriteString("- Created: ")
		builder.WriteString(formatChatExportTime(session.CreatedAt))
		builder.WriteString("\n")
		builder.WriteString("- Updated: ")
		builder.WriteString(formatChatExportTime(session.UpdatedAt))
		builder.WriteString("\n\n")
	}
	builder.WriteString("## Conversation\n")
	wrote := false
	if session != nil {
		for _, message := range session.GetMessages() {
			role := strings.ToLower(strings.TrimSpace(message.Role))
			if role != "user" && role != "assistant" {
				continue
			}
			content := strings.TrimSpace(chatExportMessageBodyText(message))
			if content == "" {
				continue
			}
			builder.WriteString("\n### ")
			if role == "assistant" {
				builder.WriteString("Assistant")
			} else {
				builder.WriteString("User")
			}
			builder.WriteString("\n\n")
			builder.WriteString(content)
			builder.WriteString("\n")
			wrote = true
		}
	}
	if !wrote {
		builder.WriteString("\n<empty>\n")
	}
	return builder.String()
}

func chatExportMessageBodyText(message runtimetypes.Message) string {
	if strings.TrimSpace(message.Content) != "" {
		return normalizeChatExportBodyText(message.Content)
	}
	if len(message.ContentParts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(message.ContentParts))
	for _, part := range message.ContentParts {
		if part.Type != runtimetypes.ContentPartText {
			continue
		}
		if text := normalizeChatExportBodyText(part.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func normalizeChatExportBodyText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

func formatChatExportTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.Format(time.RFC3339)
}

func markdownPlainLine(text string) string {
	text = strings.TrimSpace(strings.ReplaceAll(text, "\n", " "))
	if text == "" {
		return "(untitled)"
	}
	return text
}

func sanitizeChatExportFileComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "session"
	}
	replacer := strings.NewReplacer(
		"<", "_", ">", "_", ":", "_", "\"", "_",
		"/", "_", "\\", "_", "|", "_", "?", "_", "*", "_",
		" ", "_",
	)
	value = replacer.Replace(value)
	value = strings.Trim(value, "._-")
	if value == "" {
		return "session"
	}
	if len(value) > 96 {
		value = value[:96]
	}
	return value
}

func printChatExportResult(result *chatExportResult) {
	if result == nil {
		return
	}
	fmt.Println("会话已导出")
	printChatSessionMetaRow("Session:", chatDebugValueOrNone(result.SessionID))
	printChatSessionMetaRow("Format:", string(result.Format))
	printChatSessionMetaRow("Output File:", chatDebugValueOrNone(result.Path))
	printChatSessionMetaRow("Messages:", fmt.Sprintf("%d", result.Stats.MessageCount))
	if result.Format == chatExportFormatFull {
		printChatSessionMetaRow("Tool Calls:", fmt.Sprintf("%d", result.Stats.ToolCallCount))
		printChatSessionMetaRow("Tool Results:", fmt.Sprintf("%d", result.Stats.ToolResultCount))
	}
}
