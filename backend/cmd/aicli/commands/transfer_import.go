package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport/registry"
)

func newTransferImportCommand(getConfig func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import",
		Short: "从其它 Agent 导入会话到 aicli",
		Long: `从 DeepSeek Harness 或 WorkBuddy 导入会话。

DSH:
  aicli transfer import --from deepseek-harness --zip dsh-session-xxx.zip
  aicli transfer import --from deepseek-harness --root ~/.dsh --latest
  aicli transfer import --from deepseek-harness --root ~/.dsh --session session-xxx --view transcript
  aicli transfer import --zip dsh-session-xxx.zip --interactive

WorkBuddy:
  aicli transfer import --from workbuddy --instance workbuddy:ai --latest
  aicli transfer import --from workbuddy --root D:\AgentData --session <uuid>
  aicli transfer import --from workbuddy --file .\bundle\projects\...\<uuid>.jsonl

不带选择参数时可用 --interactive 探测默认路径的实例和会话，再选择导入。

WorkBuddy 非标准布局必须让 --root 能解析到 workbuddy.db + projects，
或显式传 --db 与 --projects-dir。导入默认使用 surface 视图；transcript
保留 append-origin 历史并跳过 WorkBuddy resend fork 的旧分支。`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			transferExit(runTransferImport(cmd, getConfig))
		},
	}
	cmd.Flags().String("from", "auto", "源 Agent（auto|deepseek-harness|workbuddy）")
	cmd.Flags().String("zip", "", "DSH 官方 /export ZIP（仅 deepseek-harness）")
	cmd.Flags().String("root", "", "源数据根；DSH 可指向 home/sessions/session 目录，WorkBuddy 可指向 home/projects/session")
	cmd.Flags().String("instance", "", "detect 返回的实例 ID")
	cmd.Flags().String("file", "", "WorkBuddy 独立 JSONL/bundle 文件")
	cmd.Flags().String("db", "", "WorkBuddy workbuddy.db 路径（非标准布局）")
	cmd.Flags().String("projects-dir", "", "WorkBuddy projects 目录（非标准布局）")
	cmd.Flags().String("session", "", "源会话 ID")
	cmd.Flags().Bool("latest", false, "导入最近更新的源会话")
	cmd.Flags().String("view", string(sessionport.ViewSurface), "历史视图（surface|transcript）")
	cmd.Flags().String("session-dir", "", "目标 aicli 会话持久化目录（默认: ~/.aicli/sessions）")
	cmd.Flags().String("user", "", "覆盖导入后的 aicli 用户 ID")
	cmd.Flags().Bool("new-id", false, "目标已有同 ID 时生成新 ID")
	cmd.Flags().Bool("dry-run", false, "只校验和解析，不写入 aicli")
	cmd.Flags().Bool("interactive", false, "交互选择实例和/或会话，并在写入前确认")
	cmd.Flags().Bool("yes", false, "交互模式跳过最终确认")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 输出导入摘要（等价于 --output json）")
	return cmd
}

func runTransferImport(cmd *cobra.Command, getConfig func() *config.Config) int {
	output, err := parseTransferOutputOptions(cmd)
	if err != nil {
		printTransferError("transfer import", structuredOutputOptions{Format: "text"}, err)
		return transferExitUsage
	}
	interactive := boolFlag(cmd, "interactive")
	if interactive && isJSONOutputFormat(output.Format) {
		printTransferError("transfer import", output, transferUsageError("--interactive 只支持 text 输出，不能和 --json 一起使用"))
		return transferExitUsage
	}
	if boolFlag(cmd, "yes") && !interactive {
		printTransferError("transfer import", output, transferUsageError("--yes 只能和 --interactive 一起使用"))
		return transferExitUsage
	}
	agent, err := registry.NormalizeAgent(stringFlag(cmd, "from"))
	if err != nil {
		printTransferError("transfer import", output, transferUsageError(err.Error()))
		return transferExitUsage
	}
	zipPath := strings.TrimSpace(stringFlag(cmd, "zip"))
	root := strings.TrimSpace(stringFlag(cmd, "root"))
	instanceFlag := strings.TrimSpace(stringFlag(cmd, "instance"))
	filePath := strings.TrimSpace(stringFlag(cmd, "file"))
	dbPath := strings.TrimSpace(stringFlag(cmd, "db"))
	projectsDir := strings.TrimSpace(stringFlag(cmd, "projects-dir"))
	if zipPath != "" && (root != "" || instanceFlag != "" || filePath != "" || dbPath != "" || projectsDir != "") {
		printTransferError("transfer import", output, transferUsageError("--zip 只能与 DSH 选择参数一起使用"))
		return transferExitUsage
	}
	if agent == string(sessionport.AgentDeepSeekHarness) {
		if filePath != "" || dbPath != "" || projectsDir != "" {
			printTransferError("transfer import", output, transferUsageError("--file/--db/--projects-dir 只适用于 WorkBuddy；DSH 请用 --zip 或 --root"))
			return transferExitUsage
		}
	}
	if agent == string(sessionport.AgentWorkBuddy) && zipPath != "" {
		printTransferError("transfer import", output, transferUsageError("WorkBuddy 不支持 --zip；请用 --root、--instance 或 --file"))
		return transferExitUsage
	}
	if filePath != "" && (root != "" || instanceFlag != "" || dbPath != "" || projectsDir != "") {
		printTransferError("transfer import", output, transferUsageError("--file 不能和 --root/--instance/--db/--projects-dir 同时使用"))
		return transferExitUsage
	}
	sessionID := strings.TrimSpace(stringFlag(cmd, "session"))
	latest := boolFlag(cmd, "latest")
	if sessionID != "" && latest {
		printTransferError("transfer import", output, transferUsageError("--session 和 --latest 只能选一个"))
		return transferExitUsage
	}
	view, err := parseTransferView(stringFlag(cmd, "view"))
	if err != nil {
		printTransferError("transfer import", output, err)
		return transferExitUsage
	}
	if root == "" && zipPath == "" && filePath == "" && instanceFlag != "" {
		root, err = transferInstanceRoot(instanceFlag)
		if err != nil {
			printTransferError("transfer import", output, err)
			return transferExitUsage
		}
	}
	var prompt *transferPrompt
	if interactive {
		prompt = newTransferPrompt(cmd)
		if filePath == "" {
			roots := []string(nil)
			if root != "" {
				roots = []string{root}
			}
			selection, selectErr := selectTransferInteractive(prompt, agent, roots, instanceFlag, dbPath, projectsDir, zipPath, sessionID, latest)
			if selectErr != nil {
				printTransferError("transfer import", output, selectErr)
				return transferExitFailure
			}
			agent = selection.Agent
			if selection.Root != "" {
				root = selection.Root
			}
			if selection.InstanceID != "" {
				instanceFlag = selection.InstanceID
			}
			sessionID = selection.SessionID
			latest = false
		}
	}
	if !interactive && zipPath == "" && filePath == "" && sessionID == "" && !latest {
		latest = true
	}
	imported, err := registry.Read(registry.ReadOptions{
		Agent:       agent,
		Root:        root,
		Instance:    instanceFlag,
		SessionID:   sessionID,
		FilePath:    filePath,
		DBPath:      dbPath,
		ProjectsDir: projectsDir,
		ZipPath:     zipPath,
		Latest:      latest,
		View:        view,
	})
	if err != nil {
		printTransferError("transfer import", output, err)
		return transferExitFailure
	}
	if imported == nil || imported.Session == nil {
		printTransferError("transfer import", output, fmt.Errorf("reader 返回空会话"))
		return transferExitFailure
	}
	sourceAgent := imported.Ref.AgentID
	if sourceAgent == "" {
		sourceAgent = agent
	}
	if interactive && !boolFlag(cmd, "dry-run") && !boolFlag(cmd, "yes") {
		printTransferImportConfirmation(prompt.out, imported, sourceAgent, view)
		confirmed, confirmErr := prompt.confirm("确认导入到 aicli?", true)
		if confirmErr != nil {
			printTransferError("transfer import", output, confirmErr)
			return transferExitFailure
		}
		if !confirmed {
			fmt.Fprintln(prompt.out, "已取消，未写入 aicli。")
			return transferExitOK
		}
	}

	var cfg *config.Config
	if getConfig != nil {
		cfg = getConfig()
	}
	chatSession, err := newChatExportCommandSession(cfg, stringFlag(cmd, "session-dir"), stringFlag(cmd, "user"))
	if err != nil {
		printTransferError("transfer import", output, err)
		return transferExitFailure
	}
	defer chatSession.SessionManager.Stop()

	envelope := &chatSessionExportEnvelope{
		Version:    1,
		ExportedAt: time.Now(),
		Format:     sourceAgent,
		Source:     sourceAgent,
		Session:    imported.Session,
		Stats:      chatSessionExportStatsFor(imported.Session),
	}
	result, err := importChatSessionFromEnvelope(context.Background(), chatSession, envelope, importRequest{
		SourceFile: imported.SourceFile,
		NewID:      boolFlag(cmd, "new-id"),
		DryRun:     boolFlag(cmd, "dry-run"),
		UserID:     strings.TrimSpace(stringFlag(cmd, "user")),
	}, imported.Warnings)
	if err != nil {
		printTransferError("transfer import", output, err)
		if errors.Is(err, errImportUsage) {
			return transferExitUsage
		}
		return transferExitFailure
	}
	if result != nil {
		result.SourceAgent = sourceAgent
		result.SourceView = string(view)
	}
	printImportCommandResult(result, output)
	return transferExitOK
}

func printTransferImportConfirmation(out io.Writer, imported *registry.Imported, sourceAgent string, view sessionport.SessionView) {
	fmt.Fprintf(out, "\n准备从 %s 导入会话:\n", blankTransferValue(sourceAgent))
	if imported.Ref.SessionID != "" {
		fmt.Fprintf(out, "  Session: %s\n", imported.Ref.SessionID)
	}
	if imported.Ref.Title != "" {
		fmt.Fprintf(out, "  Title:   %s\n", imported.Ref.Title)
	}
	if imported.Ref.CWD != "" {
		fmt.Fprintf(out, "  CWD:     %s\n", imported.Ref.CWD)
	}
	fmt.Fprintf(out, "  Format:  v%d (%s), compression=%s\n",
		imported.Ref.FormatVersion, blankTransferValue(imported.Ref.FormatStatus), blankTransferValue(imported.Ref.Compression))
	fmt.Fprintf(out, "  View:    %s\n", view)
	fmt.Fprintf(out, "  Messages: %d\n", len(imported.Session.History))
	for _, warning := range imported.Warnings {
		fmt.Fprintf(out, "  Warning: %s\n", warning)
	}
}

func parseTransferView(value string) (sessionport.SessionView, error) {
	switch sessionport.SessionView(strings.ToLower(strings.TrimSpace(value))) {
	case "", sessionport.ViewSurface:
		return sessionport.ViewSurface, nil
	case sessionport.ViewTranscript:
		return sessionport.ViewTranscript, nil
	default:
		return "", fmt.Errorf("不支持的视图: %s（可选 surface|transcript）", value)
	}
}

// importChatSessionFromEnvelope bridges the existing adapter-neutral import
// core to in-memory foreign-agent envelopes. The temp file is an implementation
// detail of the current file-based import path; SourceFile keeps provenance
// pointing at the real ZIP, DSH artifact or WorkBuddy JSONL.
func importChatSessionFromEnvelope(ctx context.Context, chatSession *ChatSession, envelope *chatSessionExportEnvelope, req importRequest, extraWarnings []string) (*importSessionResult, error) {
	if envelope == nil {
		return nil, fmt.Errorf("导入 envelope 为空")
	}
	file, err := os.CreateTemp("", "aicli-transfer-import-*.json")
	if err != nil {
		return nil, fmt.Errorf("创建临时导入文件失败: %w", err)
	}
	tempPath := file.Name()
	defer os.Remove(tempPath)
	encoder := json.NewEncoder(file)
	if err := encoder.Encode(envelope); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("序列化导入 envelope 失败: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("关闭临时导入文件失败: %w", err)
	}
	req.Path = tempPath
	result, err := importChatSessionFromExportFile(ctx, chatSession, req)
	if err != nil {
		return nil, err
	}
	if result != nil && len(extraWarnings) > 0 {
		result.Warnings = append(result.Warnings, extraWarnings...)
	}
	return result, nil
}
