package commands

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

type chatDebugArchiveOptions struct {
	OutputPath string
	OutputDir  string
	// RequireSnapshot 保留严格模式：快照失败即中止归档；
	// 默认 false（degrade）——快照失败只标记 unavailable，归档继续产出（P2.13/D3）。
	RequireSnapshot bool
}

type chatDebugArchiveItem struct {
	Label             string `json:"label"`
	Path              string `json:"path"`
	Kind              string `json:"kind"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
	SourcePath        string `json:"-"`
	ArchivePrefix     string `json:"-"`
}

type chatDebugArchiveResult struct {
	Path      string
	FileCount int
	Items     []chatDebugArchiveItem
	Missing   []chatDebugArchiveItem
	Skipped   []chatDebugArchiveItem
}

type chatDebugArchiveManifest struct {
	CreatedAt time.Time              `json:"created_at"`
	SessionID string                 `json:"session_id,omitempty"`
	Items     []chatDebugArchiveItem `json:"items"`
	Missing   []chatDebugArchiveItem `json:"missing,omitempty"`
	Skipped   []chatDebugArchiveItem `json:"skipped,omitempty"`
}

func handleDebugCommand(session *ChatSession, command string) bool {
	if unifiedDirectInteractiveOutput(session) {
		result, handled := tryExecuteStructuredDebugCommand(session, command)
		if !handled {
			result = commandTextResult("错误: /debug " + strings.TrimSpace(extractCommandArgument(command)) + " 尚未迁移到统一渲染命令通道。")
		}
		_ = renderChatCommandResult(session, result, false)
		// /debug display is an alternate-screen viewer, not a Scene cell. It is
		// opened after the (empty) command result crosses the dispatch boundary
		// so the primary presenter stays suspended for the whole modal.
		if result.OpenDebugOverlay {
			openChatDebugOverlay(session)
		}
		return false
	}
	// §5.5 用户交互例外：/debug 输出捕获触发时刻模型尾部锚点（不进入编码器因果链）。
	if session != nil && session.RuntimeEventBridge != nil {
		session.RuntimeEventBridge.recordInteractionAnchor("debug")
		// legacy 路径输出直接打印（不产生命令 cell），无锚定注入消费方；
		// 返回前清除 pending 标记，防止残留污染后续普通命令注入。
		defer session.RuntimeEventBridge.clearPendingInteraction()
	}
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		printChatDebugModeStatus(session)
		return false
	}
	// P2-12: /debug supervision is the local acknowledge/defer/resolve entry
	// for the durable supervision inbox. It is dispatched before the generic
	// parser, which rejects unknown top-level tokens.
	if isChatDebugSupervisionArgument(arg) {
		text, err := handleChatDebugSupervisionCommand(session, arg)
		if err != nil {
			printfChatCommandOutput(session, "错误: %v\n", err)
			return false
		}
		printChatCommandOutput(session, text)
		return false
	}
	action, opts, err := parseChatDebugCommand(arg)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		printChatDebugUsage()
		return false
	}
	switch action {
	case "on":
		setChatDebugMode(session, true)
	case "off":
		setChatDebugMode(session, false)
	case "status":
		printChatDebugModeStatus(session)
	case "display":
		printChatDebugInfo(session)
	case "routing":
		printChatDebugRoutingSummary(session)
	case "export":
		result, err := exportChatDebugArchive(session, opts)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		printChatDebugArchiveResult(result)
	default:
		printChatDebugModeStatus(session)
	}
	return false
}

func parseChatDebugCommand(argument string) (string, chatDebugArchiveOptions, error) {
	opts := chatDebugArchiveOptions{}
	fields := splitChatCommandFields(argument)
	action := ""
	for i := 0; i < len(fields); i++ {
		token := strings.TrimSpace(fields[i])
		if token == "" {
			continue
		}
		lower := strings.ToLower(token)
		switch {
		case lower == "on" || lower == "enable" || lower == "enabled":
			next, err := chooseChatDebugAction(action, "on", token)
			if err != nil {
				return action, opts, err
			}
			action = next
		case lower == "off" || lower == "disable" || lower == "disabled":
			next, err := chooseChatDebugAction(action, "off", token)
			if err != nil {
				return action, opts, err
			}
			action = next
		case lower == "status" || lower == "state":
			next, err := chooseChatDebugAction(action, "status", token)
			if err != nil {
				return action, opts, err
			}
			action = next
		case lower == "display" || lower == "show" || lower == "info":
			next, err := chooseChatDebugAction(action, "display", token)
			if err != nil {
				return action, opts, err
			}
			action = next
		case lower == "routing" || lower == "route" || lower == "routes":
			next, err := chooseChatDebugAction(action, "routing", token)
			if err != nil {
				return action, opts, err
			}
			action = next
		case lower == "export" || lower == "zip" || lower == "pack" || lower == "archive" || lower == "--zip" || lower == "--export":
			next, err := chooseChatDebugAction(action, "export", token)
			if err != nil {
				return action, opts, err
			}
			action = next
		case lower == "--output" || lower == "-o":
			if i+1 >= len(fields) {
				return action, opts, fmt.Errorf("%s 需要指定 zip 文件路径", token)
			}
			i++
			opts.OutputPath = strings.TrimSpace(fields[i])
		case strings.HasPrefix(lower, "--output="):
			opts.OutputPath = strings.TrimSpace(token[len("--output="):])
		case lower == "--dir":
			if i+1 >= len(fields) {
				return action, opts, fmt.Errorf("%s 需要指定输出目录", token)
			}
			i++
			opts.OutputDir = strings.TrimSpace(fields[i])
		case strings.HasPrefix(lower, "--dir="):
			opts.OutputDir = strings.TrimSpace(token[len("--dir="):])
		case lower == "--require-snapshot" || lower == "--strict-snapshot":
			opts.RequireSnapshot = true
		default:
			return action, opts, fmt.Errorf("未知 /debug 参数: %s", token)
		}
	}
	if action == "" {
		action = "status"
	}
	if action != "export" && (strings.TrimSpace(opts.OutputPath) != "" || strings.TrimSpace(opts.OutputDir) != "" || opts.RequireSnapshot) {
		return action, opts, fmt.Errorf("--output/--dir/--require-snapshot 只能与 /debug export 一起使用")
	}
	return action, opts, nil
}

func chooseChatDebugAction(current, next, token string) (string, error) {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	if current == "" || current == next {
		return next, nil
	}
	return current, fmt.Errorf("参数 %s 不能与 /debug %s 同时使用", token, current)
}

func setChatDebugMode(session *ChatSession, enabled bool) {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return
	}
	setChatDebugModeState(session, enabled)
	if session.Surface != nil {
		// /debug on|off also drives the render paint reconciliation probe so
		// rendering anomalies (white repaints / missing coverage) are
		// captured while the symptom is reproduced.
		session.Surface.SetPaintTraceEnabled(enabled)
	}
	warnIfChatSessionSyncFails(session, "toggle debug mode", syncRuntimeSessionFromChat(session))
	printChatSessionMetaRow("Debug Mode:", chatDebugBool(enabled))
}

func printChatDebugModeStatus(session *ChatSession) {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return
	}
	printChatSessionMetaRow("Debug Mode:", chatDebugBool(chatSessionDebugMode(session)))
	printChatSessionMetaRow("HTTP Debug:", chatDebugBool(session.HTTPDebug))
	printChatSessionMetaRow("Skills Debug:", chatDebugBool(session.SkillsDebug))
	printChatDebugUsage()
}

func printChatDebugUsage() {
	fmt.Println(chatDebugUsageText())
}

func chatDebugUsageText() string {
	return "用法: /debug on | /debug off | /debug status | /debug display | /debug routing | /debug export [--output <zip>|--dir <dir>] [--require-snapshot] | /debug supervision list|ack|defer|resolve"
}

func exportChatDebugArchive(session *ChatSession, opts chatDebugArchiveOptions) (*chatDebugArchiveResult, error) {
	if session == nil {
		return nil, fmt.Errorf("当前没有活动会话")
	}
	outputPath, err := resolveChatDebugArchiveOutputPath(session, opts)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建 debug 打包目录失败: %w", err)
	}
	items := collectChatDebugArchiveItems(session)
	if len(items) == 0 {
		return nil, fmt.Errorf("当前 /debug 没有可打包的会话文件")
	}
	cleanupSnapshot, err := attachChatDebugSessionSnapshot(session, outputPath, items, opts.RequireSnapshot)
	if err != nil {
		return nil, err
	}
	defer cleanupSnapshot()
	temporaryFile, err := os.CreateTemp(filepath.Dir(outputPath), filepath.Base(outputPath)+".*.tmp")
	if err != nil {
		return nil, fmt.Errorf("创建 debug zip 临时文件失败: %w", err)
	}
	temporaryPath := temporaryFile.Name()
	if err := temporaryFile.Chmod(0o644); err != nil {
		_ = temporaryFile.Close()
		_ = os.Remove(temporaryPath)
		return nil, fmt.Errorf("设置 debug zip 临时文件权限失败: %w", err)
	}
	if err := temporaryFile.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return nil, fmt.Errorf("关闭 debug zip 临时文件失败: %w", err)
	}
	defer os.Remove(temporaryPath)
	result, err := writeChatDebugArchive(temporaryPath, session, items)
	if err != nil {
		return nil, err
	}
	if err := publishChatExportFile(temporaryPath, outputPath); err != nil {
		return nil, err
	}
	result.Path = resolveAbsoluteChatPath(outputPath)
	return result, nil
}

func attachChatDebugSessionSnapshot(session *ChatSession, outputPath string, items []chatDebugArchiveItem, requireSnapshot bool) (func(), error) {
	cleanup := func() {}
	if session == nil || session.SessionManager == nil {
		return cleanup, nil
	}
	storage := session.SessionManager.GetStorage()
	sessionSnapshotter, hasSessionSnapshot := storage.(runtimechat.SessionStorageSessionSnapshotter)
	snapshotter, hasFullSnapshot := storage.(runtimechat.SessionStorageSnapshotter)
	if !hasSessionSnapshot && !hasFullSnapshot {
		return cleanup, nil
	}
	// P2.13/D7（审查 R6）：进程被杀时 defer 清理不会执行，先清扫超龄的
	// 快照临时目录；新鲜目录可能属于进行中的并发归档，保持不动。
	if removed := sweepStaleChatDebugSnapshotDirs(filepath.Dir(outputPath), chatDebugSnapshotDirStaleAge); removed > 0 {
		if logger := logpkg.S(); logger != nil {
			logger.Infof("[debug-archive] swept %d stale session snapshot dir(s) under %s", removed, filepath.Dir(outputPath))
		}
	}
	temporaryDir, err := os.MkdirTemp(filepath.Dir(outputPath), ".aicli-session-snapshot-*")
	if err != nil {
		return cleanup, fmt.Errorf("创建 SQLite 会话快照目录失败: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(temporaryDir) }
	sourcePath := currentRuntimeSessionPath(session)
	snapshotName := filepath.Base(sourcePath)
	if snapshotName == "" || snapshotName == "." {
		snapshotName = aiclipaths.DefaultSessionHistoryFileName
	}
	snapshotPath := filepath.Join(temporaryDir, snapshotName)
	var snapshotErr error
	if hasSessionSnapshot {
		snapshotErr = sessionSnapshotter.SnapshotSession(context.Background(), currentRuntimeSessionID(session), snapshotPath)
	} else {
		snapshotErr = snapshotter.Snapshot(context.Background(), snapshotPath)
	}
	if snapshotErr != nil {
		if requireSnapshot {
			cleanup()
			return func() {}, fmt.Errorf("创建 SQLite 会话一致性快照失败: %w", snapshotErr)
		}
		// 默认降级（D3）：标记 session_file 不可用，归档继续产出其余诊断材料。
		for index := range items {
			if items[index].Label == "session_file" {
				items[index].UnavailableReason = snapshotErr.Error()
				break
			}
		}
		if logger := logpkg.S(); logger != nil {
			logger.Warnf("[debug-archive] session snapshot unavailable, archiving without it: %v", snapshotErr)
		}
		return cleanup, nil
	}
	for index := range items {
		if items[index].Label == "session_file" {
			items[index].SourcePath = snapshotPath
			break
		}
	}
	return cleanup, nil
}

const chatDebugSnapshotDirStaleAge = 24 * time.Hour

// sweepStaleChatDebugSnapshotDirs 清理异常退出遗留的快照临时目录：
// 仅处理目标目录下前缀匹配且 mtime 超过 staleAge 的目录。
func sweepStaleChatDebugSnapshotDirs(parentDir string, staleAge time.Duration) int {
	parentDir = strings.TrimSpace(parentDir)
	if parentDir == "" {
		return 0
	}
	if staleAge <= 0 {
		staleAge = chatDebugSnapshotDirStaleAge
	}
	entries, err := os.ReadDir(parentDir)
	if err != nil {
		return 0
	}
	cutoff := time.Now().Add(-staleAge)
	removed := 0
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".aicli-session-snapshot-") {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(parentDir, entry.Name())); err == nil {
			removed++
		}
	}
	return removed
}

func resolveChatDebugArchiveOutputPath(session *ChatSession, opts chatDebugArchiveOptions) (string, error) {
	if strings.TrimSpace(opts.OutputPath) != "" {
		path := resolveAbsoluteChatPath(opts.OutputPath)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			opts.OutputDir = path
		} else {
			if !strings.EqualFold(filepath.Ext(path), ".zip") {
				path += ".zip"
			}
			return path, nil
		}
	}
	outputDir := strings.TrimSpace(opts.OutputDir)
	if outputDir == "" {
		outputDir = defaultChatExportDir(session)
	}
	if outputDir == "" {
		return "", fmt.Errorf("无法确定 debug 打包目录")
	}
	sessionID := sanitizeChatExportFileComponent(currentRuntimeSessionID(session))
	if sessionID == "" {
		sessionID = "session"
	}
	name := fmt.Sprintf("%s_%s_debug.zip", sessionID, time.Now().Format("20060102_150405"))
	return uniqueChatArtifactPath(resolveAbsoluteChatPath(filepath.Join(outputDir, name))), nil
}

func collectChatDebugArchiveItems(session *ChatSession) []chatDebugArchiveItem {
	added := make(map[string]struct{})
	dirs := make([]string, 0, 3)
	items := make([]chatDebugArchiveItem, 0, 8)
	add := func(label, path, kind string) {
		path = resolveAbsoluteChatPath(path)
		if path == "" {
			return
		}
		key := strings.ToLower(filepath.Clean(path))
		if _, ok := added[key]; ok {
			return
		}
		if kind == "file" {
			for _, dir := range dirs {
				if pathWithinBaseDir(dir, path) {
					return
				}
			}
		}
		added[key] = struct{}{}
		items = append(items, chatDebugArchiveItem{Label: label, Path: path, Kind: kind})
		if kind == "dir" {
			dirs = append(dirs, path)
		}
	}

	add("session_file", currentRuntimeSessionPath(session), "file")
	if root := currentCanonicalSessionArtifactDir(session); root != "" {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			add("canonical_session_artifact_dir", root, "dir")
			items[len(items)-1].ArchivePrefix = filepath.ToSlash(filepath.Join(
				"session_file", "session-artifacts", filepath.Base(root)))
		}
	}
	add("chat_log_file", currentChatLogFile(session), "file")
	add("debug_log_file", currentDebugLogFile(session), "file")
	add("runtime_http_artifact_dir", currentRuntimeHTTPArtifactDir(session), "dir")
	add("local_shell_artifact_dir", currentLocalShellArtifactDir(session), "dir")
	add("generated_image_artifact_dir", currentGeneratedImageArtifactDir(session), "dir")
	add("last_http_request", chatDebugLastHTTPArtifactPath(session, true), "file")
	add("last_http_response", chatDebugLastHTTPArtifactPath(session, false), "file")
	add("last_shell_output", currentLastLocalShellArtifactPath(session), "file")
	return items
}

func writeChatDebugArchive(path string, session *ChatSession, items []chatDebugArchiveItem) (*chatDebugArchiveResult, error) {
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("创建 debug zip 失败: %w", err)
	}
	defer file.Close()
	zipWriter := zip.NewWriter(file)
	result := &chatDebugArchiveResult{}
	usedNames := make(map[string]struct{})

	for _, item := range items {
		if reason := strings.TrimSpace(item.UnavailableReason); reason != "" {
			// 明确不可用的项（如降级跳过的会话快照）进入 manifest.skipped，
			// 避免读者把「文件缺失」误解为「会话为空」（P2.13/G5）。
			result.Skipped = append(result.Skipped, item)
			continue
		}
		sourcePath := chatDebugArchiveSourcePath(item)
		info, statErr := os.Stat(sourcePath)
		if statErr != nil {
			result.Missing = append(result.Missing, item)
			continue
		}
		if item.Kind == "dir" || info.IsDir() {
			item.SourcePath = sourcePath
			if err := addDebugArchiveDir(zipWriter, item, usedNames, result); err != nil {
				_ = zipWriter.Close()
				return nil, err
			}
			result.Items = append(result.Items, item)
			continue
		}
		if err := addDebugArchiveFile(zipWriter, sourcePath, debugArchiveItemEntryName(item, filepath.Base(item.Path)), usedNames); err != nil {
			_ = zipWriter.Close()
			return nil, err
		}
		result.FileCount++
		result.Items = append(result.Items, item)
	}

	manifest := chatDebugArchiveManifest{
		CreatedAt: time.Now(),
		SessionID: currentRuntimeSessionID(session),
		Items:     result.Items,
		Missing:   result.Missing,
		Skipped:   result.Skipped,
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		_ = zipWriter.Close()
		return nil, fmt.Errorf("序列化 debug manifest 失败: %w", err)
	}
	if err := addDebugArchiveBytes(zipWriter, "manifest.json", data, usedNames); err != nil {
		_ = zipWriter.Close()
		return nil, err
	}
	result.FileCount++
	if err := zipWriter.Close(); err != nil {
		return nil, fmt.Errorf("写入 debug zip 失败: %w", err)
	}
	return result, nil
}

func chatDebugArchiveSourcePath(item chatDebugArchiveItem) string {
	if path := strings.TrimSpace(item.SourcePath); path != "" {
		return resolveAbsoluteChatPath(path)
	}
	return resolveAbsoluteChatPath(item.Path)
}

func debugArchiveItemEntryName(item chatDebugArchiveItem, relative string) string {
	if prefix := strings.Trim(filepath.ToSlash(strings.TrimSpace(item.ArchivePrefix)), "/"); prefix != "" {
		return filepath.ToSlash(filepath.Join(prefix, relative))
	}
	return debugArchiveEntryName(item.Label, relative)
}

func addDebugArchiveDir(zipWriter *zip.Writer, item chatDebugArchiveItem, usedNames map[string]struct{}, result *chatDebugArchiveResult) error {
	root := chatDebugArchiveSourcePath(item)
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry == nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			result.Skipped = append(result.Skipped, chatDebugArchiveItem{
				Label: item.Label,
				Path:  resolveAbsoluteChatPath(path),
				Kind:  "symlink",
			})
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.Clean(relative)
		if relative == "." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || relative == ".." {
			return nil
		}
		name := debugArchiveItemEntryName(item, relative)
		if err := addDebugArchiveFile(zipWriter, path, name, usedNames); err != nil {
			return err
		}
		result.FileCount++
		return nil
	})
}

func addDebugArchiveFile(zipWriter *zip.Writer, sourcePath, archiveName string, usedNames map[string]struct{}) error {
	sourcePath = resolveAbsoluteChatPath(sourcePath)
	file, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("打开 debug 文件失败 %s: %w", sourcePath, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("读取 debug 文件信息失败 %s: %w", sourcePath, err)
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return fmt.Errorf("创建 debug zip header 失败 %s: %w", sourcePath, err)
	}
	header.Name = uniqueDebugArchiveName(filepath.ToSlash(archiveName), usedNames)
	header.Method = zip.Deflate
	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("创建 debug zip entry 失败 %s: %w", sourcePath, err)
	}
	if _, err := io.Copy(writer, file); err != nil {
		return fmt.Errorf("写入 debug zip entry 失败 %s: %w", sourcePath, err)
	}
	return nil
}

func addDebugArchiveBytes(zipWriter *zip.Writer, archiveName string, data []byte, usedNames map[string]struct{}) error {
	header := &zip.FileHeader{
		Name:   uniqueDebugArchiveName(filepath.ToSlash(archiveName), usedNames),
		Method: zip.Deflate,
	}
	header.SetModTime(time.Now())
	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("创建 debug manifest entry 失败: %w", err)
	}
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("写入 debug manifest 失败: %w", err)
	}
	return nil
}

func debugArchiveEntryName(label, relative string) string {
	label = sanitizeChatExportFileComponent(label)
	relative = filepath.Clean(strings.TrimSpace(relative))
	if relative == "." || relative == "" {
		relative = "artifact"
	}
	return filepath.ToSlash(filepath.Join(label, relative))
}

func uniqueDebugArchiveName(name string, used map[string]struct{}) string {
	name = strings.TrimLeft(filepath.ToSlash(strings.TrimSpace(name)), "/")
	if name == "" {
		name = "artifact"
	}
	if used == nil {
		return name
	}
	if _, ok := used[name]; !ok {
		used[name] = struct{}{}
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for index := 2; ; index++ {
		candidate := fmt.Sprintf("%s_%d%s", base, index, ext)
		if _, ok := used[candidate]; !ok {
			used[candidate] = struct{}{}
			return candidate
		}
	}
}

func printChatDebugArchiveResult(result *chatDebugArchiveResult) {
	if result == nil {
		return
	}
	fmt.Println("Debug 文件已打包")
	printChatSessionMetaRow("Archive:", chatDebugValueOrNone(result.Path))
	printChatSessionMetaRow("Files:", fmt.Sprintf("%d", result.FileCount))
	if len(result.Missing) > 0 {
		printChatSessionMetaRow("Missing:", fmt.Sprintf("%d", len(result.Missing)))
	}
	if len(result.Skipped) > 0 {
		printChatSessionMetaRow("Skipped:", fmt.Sprintf("%d", len(result.Skipped)))
	}
}
