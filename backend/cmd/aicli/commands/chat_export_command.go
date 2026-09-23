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
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type chatExportFormat string

const (
	chatExportFormatFull chatExportFormat = "full"
	chatExportFormatBody chatExportFormat = "body"
	// chatExportFormatMarkdownTools 在正文 Markdown 之外附带每个工具调用的
	// 名称与输入参数（不含输出）。
	chatExportFormatMarkdownTools chatExportFormat = "md-tools"
	// chatExportFormatMarkdownTrace 在正文 Markdown 之外附带每个工具调用的
	// 输入参数与输出结果（按 tool_call_id 配对）。
	chatExportFormatMarkdownTrace chatExportFormat = "md-trace"
)

// chatExportUsage 是 /export 的规范用法行：legacy stdout 路径与统一命令通道
// 共用同一份文案，避免两处用法漂移。
const chatExportUsage = "/export [current|latest|<session-id>] [--full|--body|--tools|--trace] [--output <path>|--dir <dir>]"

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
		fmt.Println("用法: " + chatExportUsage)
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
	return parseChatExportOptionFields(splitChatCommandFields(argument))
}

// parseChatExportOptionFields 是 /export 与顶层 `aicli export` 共用的选项解析：
// 前者按空白/引号把命令参数切成 token，后者把 cobra flag 与位置参数拼成同一组
// token。两条入口共用同一份格式映射、目标判定与报错文案，避免语义漂移。
func parseChatExportOptionFields(fields []string) (chatExportOptions, error) {
	opts := chatExportOptions{Format: chatExportFormatFull}
	for i := 0; i < len(fields); i++ {
		token := strings.TrimSpace(fields[i])
		if token == "" {
			continue
		}
		lower := strings.ToLower(token)
		switch {
		case lower == "--format" || lower == "--mode":
			if i+1 >= len(fields) {
				return opts, fmt.Errorf("%s 需要指定 full、body、tools 或 trace", token)
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
		default:
			// 裸格式词（full/body/md-tools/...）与 --full/--body/--tools/--trace
			// 共用同一张映射表；其余裸词才是会话目标。
			if format, ok := matchChatExportFormatToken(lower); ok {
				opts.Format = format
				opts.ExplicitFormat = true
				continue
			}
			if strings.HasPrefix(lower, "-") {
				return opts, fmt.Errorf("未知 /export 选项: %s", token)
			}
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
	format, ok := matchChatExportFormatToken(value)
	if !ok {
		return fmt.Errorf("未知导出格式: %s", strings.TrimSpace(value))
	}
	opts.Format = format
	opts.ExplicitFormat = true
	return nil
}

// matchChatExportFormatToken 归一化 /export 的格式词：--format/--mode 的值、
// 裸格式词与 --full/--body/--tools/--trace 共用同一张映射表。full 是完整 JSON；
// body 只导出用户/助手正文；md-tools 额外导出工具调用名称与输入参数；
// md-trace 再额外按 tool_call_id 配对导出输出结果。
func matchChatExportFormatToken(token string) (chatExportFormat, bool) {
	switch strings.ToLower(strings.TrimSpace(token)) {
	case "full", "json", "--full":
		return chatExportFormatFull, true
	case "body", "text", "markdown", "md", "--body":
		return chatExportFormatBody, true
	case "tools", "tool", "tool-calls", "toolcalls", "md-tools", "--tools":
		return chatExportFormatMarkdownTools, true
	case "trace", "tool-trace", "tool-results", "md-trace", "full-md", "--trace":
		return chatExportFormatMarkdownTrace, true
	default:
		return "", false
	}
}

// chatExportFormatOption 描述一种导出格式在交互菜单里的呈现。编号菜单与
// fullscreen 选择器共用同一顺序（1-based 编号 == 切片索引+1），避免两处漂移。
type chatExportFormatOption struct {
	Format    chatExportFormat
	MenuLabel string
	// PickerDetail 是选择器列表行右侧的一行简述。列表行的 detail 列宽是
	// min(32, width/3)（见 ui.renderFullScreenListItem），超出即被 "…" 截断，
	// 因此这里必须控制在 chatExportPickerDetailMaxWidth 以内。
	PickerDetail string
	// PickerPreview 是选中行下方预览区的完整说明：预览区按终端宽度折行渲染，
	// 承载 PickerDetail 放不下的信息，保证选择界面不会丢掉格式语义。
	PickerPreview string
	SearchText    string
}

// chatExportPickerDetailMaxWidth 是 PickerDetail 的显示宽度上限。80 列终端下
// detail 列只有 width/3 = 26 格，超过就会在行内出现 "…" 截断。
const chatExportPickerDetailMaxWidth = 26

func chatExportFormatOptions() []chatExportFormatOption {
	return []chatExportFormatOption{
		{
			Format:        chatExportFormatFull,
			MenuLabel:     "完整 JSON（包含 metadata、tool_calls、tool 结果等）",
			PickerDetail:  "完整 JSON（全字段）",
			PickerPreview: "完整 JSON：messages + metadata + tool_calls + tool 结果全量导出，适合归档或程序处理",
			SearchText:    "full json 完整",
		},
		{
			Format:        chatExportFormatBody,
			MenuLabel:     "正文 Markdown（仅用户/助手正文）",
			PickerDetail:  "纯正文（无工具链）",
			PickerPreview: "纯正文 Markdown：仅导出用户/助手正文，不含工具调用与结果",
			SearchText:    "body text markdown 正文",
		},
		{
			Format:        chatExportFormatMarkdownTools,
			MenuLabel:     "Markdown + 工具调用（工具名与输入参数）",
			PickerDetail:  "正文 + 工具调用",
			PickerPreview: "Markdown 正文 + 工具调用：正文后追加 #### Tool Calls，含工具名、call id 与输入参数 JSON",
			SearchText:    "tools md-tools markdown 工具 调用 参数 输入",
		},
		{
			Format:        chatExportFormatMarkdownTrace,
			MenuLabel:     "Markdown + 工具调用与结果（输入/输出）",
			PickerDetail:  "正文 + 工具调用与结果",
			PickerPreview: "Markdown 正文 + 工具调用与结果：在 md-tools 基础上按 tool_call_id 内联输出，单条输出超过 32KB 自动截断",
			SearchText:    "trace md-trace markdown 工具 调用 结果 输入 输出",
		},
	}
}

// chatExportFormatByMenuChoice 把 1-based 菜单编号映射回格式；越界返回 false。
func chatExportFormatByMenuChoice(choice string, options []chatExportFormatOption) (chatExportFormat, bool) {
	index, err := strconv.Atoi(strings.TrimSpace(choice))
	if err != nil || index < 1 || index > len(options) {
		return "", false
	}
	return options[index-1].Format, true
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
	options := chatExportFormatOptions()
	warning := ""
	for {
		lines := make([]string, 0, len(options)+1)
		for index, option := range options {
			lines = append(lines, fmt.Sprintf("  %-*s %s", optionWidth, fmt.Sprintf("[%d]", index+1), option.MenuLabel))
		}
		lines = append(lines, fmt.Sprintf("  %-*s %s", optionWidth, fmt.Sprintf("[%d]", len(options)+1), "取消"))
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
		if choice == "" {
			return options[0].Format, true, nil
		}
		if format, ok := chatExportFormatByMenuChoice(choice, options); ok {
			return format, true, nil
		}
		switch choice {
		case "q", "quit", "cancel", "exit":
			return options[0].Format, false, nil
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
	if mode, ok := chatExportMarkdownModeForFormat(opts.Format); ok {
		stats, err = writeChatSessionMarkdownExport(temporaryPath, session, runtimeSession, mode)
	} else {
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

// chatExportMarkdownMode 区分三种 Markdown 变体：body 只输出正文；tools 附带
// 工具调用名称与输入参数；trace 再附带按 tool_call_id 配对的输出结果。
type chatExportMarkdownMode int

const (
	chatExportMarkdownBody chatExportMarkdownMode = iota
	chatExportMarkdownTools
	chatExportMarkdownTrace
)

// chatExportMarkdownModeForFormat 把导出格式映射为 Markdown 渲染模式；第二个
// 返回值表示该格式是否输出 Markdown（false 走完整 JSON 路径）。
func chatExportMarkdownModeForFormat(format chatExportFormat) (chatExportMarkdownMode, bool) {
	switch format {
	case chatExportFormatBody:
		return chatExportMarkdownBody, true
	case chatExportFormatMarkdownTools:
		return chatExportMarkdownTools, true
	case chatExportFormatMarkdownTrace:
		return chatExportMarkdownTrace, true
	default:
		return chatExportMarkdownBody, false
	}
}

func (m chatExportMarkdownMode) includesToolCalls() bool {
	return m == chatExportMarkdownTools || m == chatExportMarkdownTrace
}

func (m chatExportMarkdownMode) includesToolResults() bool {
	return m == chatExportMarkdownTrace
}

// chatExportFormatReportsToolStats 工具调用统计只对包含工具信息的格式有意义
// （full 与两种 Markdown 变体）；纯正文导出保持原有精简输出。
func chatExportFormatReportsToolStats(format chatExportFormat) bool {
	return format != chatExportFormatBody
}

// chatExportMarkdownMaxToolOutput 限制单个工具结果进入 Markdown 的体积：trace
// 模式面向阅读，数百 KB 的 artifact 会把文件撑到难以打开；截断处保留显式标记，
// 完整内容仍可用 --full JSON 导出获取。
const chatExportMarkdownMaxToolOutput = 32 * 1024

// chatExportToolOutputIndex 按 tool_call_id 索引工具结果输出，供 md-trace 在
// 助手消息的工具调用块里内联渲染（结果消息位于调用消息之后）。同一 call id
// 的多次结果按出现顺序全部保留。
type chatExportToolOutputIndex map[string][]string

func writeChatSessionMarkdownExport(path string, session *ChatSession, runtimeSession *runtimechat.Session, mode chatExportMarkdownMode) (chatSessionExportStats, error) {
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
	// trace 模式需要先按 tool_call_id 索引工具结果，才能在助手消息的工具调用块
	// 里内联输出（结果消息在调用消息之后到达，单遍流式无法回填）。
	toolOutputs := chatExportToolOutputIndex{}
	if mode.includesToolResults() {
		toolOutputs, err = collectChatExportToolOutputs(session, runtimeSession)
		if err != nil {
			return chatSessionExportStats{}, err
		}
	}
	consumedToolOutputs := map[string]bool{}
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
		if role == "assistant" {
			toolBlocks := mode.includesToolCalls() && len(message.ToolCalls) > 0
			if content == "" && !toolBlocks {
				return nil
			}
			if _, err := io.WriteString(file, "\n### Assistant\n"); err != nil {
				return err
			}
			if content != "" {
				if _, err := fmt.Fprintf(file, "\n%s\n", content); err != nil {
					return err
				}
			}
			if toolBlocks {
				if err := writeChatExportToolCallBlocks(file, message.ToolCalls, mode, toolOutputs, consumedToolOutputs); err != nil {
					return err
				}
			}
			wrote = true
			return nil
		}
		if content == "" {
			return nil
		}
		if _, err := fmt.Fprintf(file, "\n### User\n\n%s\n", content); err != nil {
			return err
		}
		wrote = true
		return nil
	})
	if err != nil {
		return chatSessionExportStats{}, fmt.Errorf("流式读取 canonical 会话历史失败: %w", err)
	}
	if mode.includesToolResults() {
		extra, err := writeChatExportUnmatchedToolResults(file, toolOutputs, consumedToolOutputs)
		if err != nil {
			return chatSessionExportStats{}, err
		}
		wrote = wrote || extra
	}
	if !wrote {
		if _, err := io.WriteString(file, "\n<empty>\n"); err != nil {
			return chatSessionExportStats{}, err
		}
	}
	if err := file.Close(); err != nil {
		return chatSessionExportStats{}, err
	}
	return stats, nil
}

// collectChatExportToolOutputs 单遍扫描 canonical 历史，按 tool_call_id 收集
// 工具结果正文（md-trace 的第二遍渲染据此内联输出）。
func collectChatExportToolOutputs(session *ChatSession, runtimeSession *runtimechat.Session) (chatExportToolOutputIndex, error) {
	index := chatExportToolOutputIndex{}
	err := streamChatExportMessageJSON(session, runtimeSession, func(_ int, info runtimechat.CanonicalMessageInfo, payload io.Reader) error {
		role := strings.ToLower(strings.TrimSpace(info.Role))
		if info.RoleKnown && role != "tool" && !info.ToolResult {
			return nil
		}
		var message runtimetypes.Message
		if err := json.NewDecoder(payload).Decode(&message); err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			return nil
		}
		callID := strings.TrimSpace(message.ToolCallID)
		if callID == "" {
			return nil
		}
		text := chatExportToolResultText(message)
		if text == "" {
			return nil
		}
		index[callID] = append(index[callID], text)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("流式读取 canonical 会话历史失败: %w", err)
	}
	return index, nil
}

// chatExportToolResultText 提取工具结果正文：与正文导出不同，工具输出里的
// 前导空格常承载语义（缩进、`git status --short` 的状态列），因此只规整换行
// 并去掉首尾空行，不做 TrimSpace。
func chatExportToolResultText(message runtimetypes.Message) string {
	if strings.TrimSpace(message.Content) != "" {
		return normalizeChatExportToolOutputText(message.Content)
	}
	parts := make([]string, 0, len(message.ContentParts))
	for _, part := range message.ContentParts {
		if part.Type != runtimetypes.ContentPartText {
			continue
		}
		text := normalizeChatExportToolOutputText(part.Text)
		if strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func normalizeChatExportToolOutputText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Trim(text, "\n")
}

// writeChatExportToolCallBlocks 在助手段落里渲染工具调用块：名称 + call id、
// 输入参数（Input），trace 模式再内联每个调用的输出（Output）。
func writeChatExportToolCallBlocks(writer io.Writer, calls []runtimetypes.ToolCall, mode chatExportMarkdownMode, outputs chatExportToolOutputIndex, consumed map[string]bool) error {
	if _, err := io.WriteString(writer, "\n#### Tool Calls\n"); err != nil {
		return err
	}
	for _, call := range calls {
		name := strings.TrimSpace(call.Name)
		if name == "" {
			name = "(unnamed)"
		}
		header := "\n**" + name + "**"
		callID := strings.TrimSpace(call.ID)
		if callID != "" {
			header += " (`" + callID + "`)"
		}
		if _, err := io.WriteString(writer, header+"\n"); err != nil {
			return err
		}
		if input, ok := chatExportToolCallInput(call); ok {
			if err := writeChatExportFencedBlock(writer, "Input", input); err != nil {
				return err
			}
		} else if _, err := io.WriteString(writer, "\nInput: (none)\n"); err != nil {
			return err
		}
		if !mode.includesToolResults() || callID == "" {
			continue
		}
		results := outputs[callID]
		if len(results) == 0 {
			continue
		}
		consumed[callID] = true
		for _, result := range results {
			if err := writeChatExportFencedBlock(writer, "Output", truncateChatExportToolOutput(result)); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeChatExportUnmatchedToolResults 兜底渲染没有对应工具调用消息的结果
// （历史被裁剪、call id 缺失等），避免 trace 导出静默丢内容。
func writeChatExportUnmatchedToolResults(writer io.Writer, outputs chatExportToolOutputIndex, consumed map[string]bool) (bool, error) {
	var ids []string
	for id, results := range outputs {
		if consumed[id] || len(results) == 0 {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return false, nil
	}
	sort.Strings(ids)
	if _, err := io.WriteString(writer, "\n## Unmatched Tool Results\n"); err != nil {
		return false, err
	}
	for _, id := range ids {
		if _, err := fmt.Fprintf(writer, "\n### Tool Result (`%s`)\n", id); err != nil {
			return false, err
		}
		for _, result := range outputs[id] {
			if err := writeChatExportFencedBlock(writer, "Output", truncateChatExportToolOutput(result)); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// chatExportToolCallInput 返回工具调用的输入展示文本：Args（结构化 map）优先，
// 否则回退 RawInput（freeform/custom tool 的原始输入）。第二个返回值表示是否
// 存在可渲染的输入。
func chatExportToolCallInput(call runtimetypes.ToolCall) (string, bool) {
	if len(call.Args) > 0 {
		if payload, err := json.MarshalIndent(call.Args, "", "  "); err == nil {
			return string(payload), true
		}
	}
	raw := strings.TrimSpace(call.RawInput)
	if raw == "" {
		return "", false
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, []byte(raw), "", "  "); err == nil {
		return pretty.String(), true
	}
	return raw, true
}

// writeChatExportFencedBlock 写 "Label:" + 围栏代码块；围栏长度随内容自适应，
// 工具输出里的 ``` 不会提前闭合代码块。
func writeChatExportFencedBlock(writer io.Writer, label, content string) error {
	fence := chatExportCodeFence(content)
	_, err := fmt.Fprintf(writer, "\n%s:\n\n%s\n%s\n%s\n", label, fence, content, fence)
	return err
}

// chatExportCodeFence 生成不短于 3、且比内容中最长反引号串更长的围栏。
func chatExportCodeFence(content string) string {
	longest, current := 0, 0
	for _, r := range content {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	size := longest + 1
	if size < 3 {
		size = 3
	}
	return strings.Repeat("`", size)
}

// truncateChatExportToolOutput 按 chatExportMarkdownMaxToolOutput 截断工具输出，
// 回退到最近的 UTF-8 边界并留下显式标记。
func truncateChatExportToolOutput(text string) string {
	if len(text) <= chatExportMarkdownMaxToolOutput {
		return text
	}
	head := text[:chatExportMarkdownMaxToolOutput]
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
	return head + fmt.Sprintf("\n... <truncated: %d bytes omitted>", len(text)-len(head))
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
		// 与 CLI 会话加载一致：按显式 ID 导出不校验用户归属，跨身份平面
		// （web/server 的 "anonymous" 与本地 OS 用户）创建的会话同样可导出。
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
	if _, isMarkdown := chatExportMarkdownModeForFormat(opts.Format); isMarkdown {
		extension = ".md"
	}
	sessionID := "session"
	if runtimeSession != nil && strings.TrimSpace(runtimeSession.ID) != "" {
		sessionID = sanitizeChatExportFileComponent(runtimeSession.ID)
	}
	filename := fmt.Sprintf("%s_%s_%s%s", sessionID, time.Now().Format("20060102_150405"), opts.Format, extension)
	return uniqueChatArtifactPath(resolveAbsoluteChatPath(filepath.Join(outputDir, filename))), nil
}

// uniqueChatArtifactPath 避免同秒内重复导出静默覆盖上一份产物：文件名的时间戳
// 只到秒，两次导出会算出同一路径，而写盘路径是 O_TRUNC/rename（后者在 Windows
// 上同样替换目标）。这里保留首个路径不变，仅在已存在时追加 -2/-3… 序号。
// 仅用于默认命名；用户在 OutputPath 里显式指定的路径不做改名。
func uniqueChatArtifactPath(path string) string {
	if path == "" {
		return path
	}
	if _, err := os.Stat(path); err != nil {
		return path
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for attempt := 2; attempt < 1000; attempt++ {
		candidate := fmt.Sprintf("%s-%d%s", base, attempt, ext)
		if _, err := os.Stat(candidate); err != nil {
			return candidate
		}
	}
	return path
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
	if chatExportFormatReportsToolStats(result.Format) {
		printChatSessionMetaRow("Tool Calls:", fmt.Sprintf("%d", result.Stats.ToolCallCount))
		printChatSessionMetaRow("Tool Results:", fmt.Sprintf("%d", result.Stats.ToolResultCount))
	}
}
