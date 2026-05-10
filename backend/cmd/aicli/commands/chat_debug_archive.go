package commands

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type chatDebugArchiveOptions struct {
	OutputPath string
	OutputDir  string
}

type chatDebugArchiveItem struct {
	Label string `json:"label"`
	Path  string `json:"path"`
	Kind  string `json:"kind"`
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
	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" {
		printChatDebugInfo(session)
		return false
	}
	action, opts, err := parseChatDebugArchiveCommand(arg)
	if err != nil {
		fmt.Printf("错误: %v\n", err)
		fmt.Println("用法: /debug 或 /debug export [--output <zip>|--dir <dir>]")
		return false
	}
	switch action {
	case "export":
		result, err := exportChatDebugArchive(session, opts)
		if err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		printChatDebugArchiveResult(result)
	default:
		printChatDebugInfo(session)
	}
	return false
}

func parseChatDebugArchiveCommand(argument string) (string, chatDebugArchiveOptions, error) {
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
		case lower == "export" || lower == "zip" || lower == "pack" || lower == "archive" || lower == "--zip" || lower == "--export":
			action = "export"
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
		default:
			return action, opts, fmt.Errorf("未知 /debug 参数: %s", token)
		}
	}
	if action == "" {
		action = "show"
	}
	return action, opts, nil
}

func exportChatDebugArchive(session *ChatSession, opts chatDebugArchiveOptions) (*chatDebugArchiveResult, error) {
	if session == nil {
		return nil, fmt.Errorf("当前没有活动会话")
	}
	outputPath, err := resolveChatDebugArchiveOutputPath(session, opts)
	if err != nil {
		return nil, err
	}
	items := collectChatDebugArchiveItems(session)
	if len(items) == 0 {
		return nil, fmt.Errorf("当前 /debug 没有可打包的会话文件")
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return nil, fmt.Errorf("创建 debug 打包目录失败: %w", err)
	}
	result, err := writeChatDebugArchive(outputPath, session, items)
	if err != nil {
		return nil, err
	}
	result.Path = resolveAbsoluteChatPath(outputPath)
	return result, nil
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
	return resolveAbsoluteChatPath(filepath.Join(outputDir, name)), nil
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
		info, statErr := os.Stat(item.Path)
		if statErr != nil {
			result.Missing = append(result.Missing, item)
			continue
		}
		if item.Kind == "dir" || info.IsDir() {
			if err := addDebugArchiveDir(zipWriter, item, usedNames, result); err != nil {
				_ = zipWriter.Close()
				return nil, err
			}
			result.Items = append(result.Items, item)
			continue
		}
		if err := addDebugArchiveFile(zipWriter, item.Path, debugArchiveEntryName(item.Label, filepath.Base(item.Path)), usedNames); err != nil {
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

func addDebugArchiveDir(zipWriter *zip.Writer, item chatDebugArchiveItem, usedNames map[string]struct{}, result *chatDebugArchiveResult) error {
	root := resolveAbsoluteChatPath(item.Path)
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
		name := debugArchiveEntryName(item.Label, relative)
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
