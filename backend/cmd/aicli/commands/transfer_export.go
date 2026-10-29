package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport/registry"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type transferExportResult struct {
	Agent         string   `json:"agent"`
	SourceSession string   `json:"source_session_id"`
	TargetSession string   `json:"target_session_id"`
	BundleDir     string   `json:"bundle_dir"`
	SessionFile   string   `json:"session_file"`
	ManifestFile  string   `json:"manifest_file"`
	IndexFile     string   `json:"index_file,omitempty"`
	EventCount    int      `json:"event_count"`
	Compression   string   `json:"compression"`
	Warnings      []string `json:"warnings,omitempty"`
}

func newTransferExportCommand(getConfig func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "把 aicli 会话导出为其它 Agent 的原生 bundle",
		Long: `把 aicli 会话导出为 DeepSeek Harness v4 或 WorkBuddy JSONL bundle。

DSH bundle:
  sessions/--<cwd>--/<session-id>/session.v4.jsonl[.zstd]
  manifest.json

WorkBuddy bundle:
  projects/<project-slug>/<session-id>.jsonl
  session-index.json
  manifest.json

默认只生成 bundle，不写 DSH_HOME / workbuddy.db。WorkBuddy 没有可靠的
官方 import 入口，bundle 需要用户在目标版本确认后合并；直接 apply 是后续
受控实验能力。

这是跨 Agent 导出，不是 aicli 原生 --full 备份；需要原始 aicli 备份请用
aicli export --full。

--interactive 会依次选择目标、源会话、DSH 压缩方式和输出目录，并在写入前
显示摘要确认；--yes 可跳过最终确认。`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			transferExit(runTransferExport(cmd, getConfig))
		},
	}
	cmd.Flags().String("to", string(sessionport.AgentDeepSeekHarness), "目标 Agent（deepseek-harness|workbuddy）")
	cmd.Flags().String("session", "latest", "aicli 会话 ID（默认 latest）")
	cmd.Flags().String("session-dir", "", "源 aicli 会话持久化目录（默认: ~/.aicli/sessions）")
	cmd.Flags().String("user", "", "源 aicli 用户 ID")
	cmd.Flags().String("bundle-dir", "", "bundle 输出目录（默认: ~/.aicli/transfer/out/<timestamp>）")
	cmd.Flags().String("cwd", "", "目标 cwd（默认沿用源会话 workspace_path）")
	cmd.Flags().String("title", "", "目标标题（默认沿用源标题）")
	cmd.Flags().String("compression", "none", "DSH 物理编码（none|zstd）；WorkBuddy 只支持 none")
	cmd.Flags().String("target-user", "", "WorkBuddy 目标用户 ID（仅写入 index 提示，不迁移凭证）")
	cmd.Flags().Bool("interactive", false, "交互选择目标、源会话、压缩和输出目录")
	cmd.Flags().Bool("yes", false, "交互模式跳过最终确认")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 输出（等价于 --output json）")
	return cmd
}

func runTransferExport(cmd *cobra.Command, getConfig func() *config.Config) int {
	output, err := parseTransferOutputOptions(cmd)
	if err != nil {
		printTransferError("transfer export", structuredOutputOptions{Format: "text"}, err)
		return transferExitUsage
	}
	interactive := boolFlag(cmd, "interactive")
	if interactive && isJSONOutputFormat(output.Format) {
		printTransferError("transfer export", output, transferUsageError("--interactive 只支持 text 输出，不能和 --json 一起使用"))
		return transferExitUsage
	}
	if boolFlag(cmd, "yes") && !interactive {
		printTransferError("transfer export", output, transferUsageError("--yes 只能和 --interactive 一起使用"))
		return transferExitUsage
	}
	if interactive {
		return runTransferExportWizard(cmd, getConfig, boolFlag(cmd, "yes"))
	}
	target, err := registry.NormalizeAgent(stringFlag(cmd, "to"))
	if err != nil || target == "auto" {
		if err == nil {
			err = fmt.Errorf("--to 必须显式指定 deepseek-harness 或 workbuddy")
		}
		printTransferError("transfer export", output, err)
		return transferExitUsage
	}
	compression := strings.ToLower(strings.TrimSpace(stringFlag(cmd, "compression")))
	if compression != "" && compression != "none" && compression != "zstd" {
		printTransferError("transfer export", output, transferUsageError("--compression 只支持 none|zstd"))
		return transferExitUsage
	}
	if target == string(sessionport.AgentWorkBuddy) && compression == "zstd" {
		printTransferError("transfer export", output, transferUsageError("WorkBuddy bundle 只支持 --compression none"))
		return transferExitUsage
	}
	var cfg *config.Config
	if getConfig != nil {
		cfg = getConfig()
	}
	chatSession, err := newChatExportCommandSession(cfg, stringFlag(cmd, "session-dir"), stringFlag(cmd, "user"))
	if err != nil {
		printTransferError("transfer export", output, err)
		return transferExitFailure
	}
	defer chatSession.SessionManager.Stop()

	sourceID := strings.TrimSpace(stringFlag(cmd, "session"))
	if sourceID == "" {
		sourceID = "latest"
	}
	runtimeSession, _, err := resolveChatExportRuntimeSession(chatSession, chatExportOptions{
		Target: sourceID,
		Format: chatExportFormatFull,
	})
	if err != nil {
		printTransferError("transfer export", output, err)
		return transferExitFailure
	}
	history := make([]runtimetypes.Message, 0, runtimeSession.MessageCount())
	if err := streamChatExportMessages(chatSession, runtimeSession, func(_ int, message runtimetypes.Message) error {
		history = append(history, message)
		return nil
	}); err != nil {
		printTransferError("transfer export", output, err)
		return transferExitFailure
	}
	bundleDir := strings.TrimSpace(stringFlag(cmd, "bundle-dir"))
	if bundleDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			printTransferError("transfer export", output, err)
			return transferExitFailure
		}
		bundleDir = filepath.Join(home, ".aicli", "transfer", "out", time.Now().Format("20060102-150405"))
	}
	cwd := strings.TrimSpace(stringFlag(cmd, "cwd"))
	if cwd == "" {
		cwd = runtimeSessionWorkspacePath(runtimeSession)
	}
	title := strings.TrimSpace(stringFlag(cmd, "title"))
	if title == "" && runtimeSession.BuildPreview() != nil {
		title = runtimeSession.BuildPreview().Title
	}
	model := runtimeSession.Metadata.LastModel
	if model == "" {
		model = runtimeSessionContextString(runtimeSession, chatRuntimeContextModel)
	}
	exported, err := registry.Export(registry.ExportOptions{
		Agent:         target,
		SessionID:     "",
		CWD:           cwd,
		Title:         title,
		CreatedAt:     runtimeSession.CreatedAt,
		Model:         model,
		History:       history,
		BundleDir:     bundleDir,
		SourceSession: runtimeSession.ID,
		TargetUser:    strings.TrimSpace(stringFlag(cmd, "target-user")),
		Compression:   compression,
	})
	if err != nil {
		printTransferError("transfer export", output, err)
		return transferExitFailure
	}
	result := transferExportResult{
		Agent:         target,
		SourceSession: runtimeSession.ID,
		TargetSession: exported.SessionID,
		BundleDir:     exported.BundleDir,
		SessionFile:   exported.SessionFile,
		ManifestFile:  exported.ManifestFile,
		IndexFile:     exported.IndexFile,
		EventCount:    exported.EventCount,
		Compression:   exported.Compression,
		Warnings:      exported.Warnings,
	}
	if isJSONOutputFormat(output.Format) {
		printCommandJSONOutput("transfer export", output.Envelope, result)
		return transferExitOK
	}
	fmt.Println("跨 Agent bundle 已生成")
	fmt.Printf("Target Agent:  %s\n", result.Agent)
	fmt.Printf("Source Session: %s\n", result.SourceSession)
	fmt.Printf("Target Session: %s\n", result.TargetSession)
	fmt.Printf("Bundle:        %s\n", result.BundleDir)
	fmt.Printf("Session File:  %s\n", result.SessionFile)
	if result.IndexFile != "" {
		fmt.Printf("Index File:    %s\n", result.IndexFile)
	}
	fmt.Printf("Events:        %d\n", result.EventCount)
	fmt.Printf("Compression:   %s\n", result.Compression)
	for _, warning := range result.Warnings {
		fmt.Printf("提示: %s\n", warning)
	}
	if target == string(sessionport.AgentWorkBuddy) {
		fmt.Println("提示: 默认未写入 workbuddy.db；请先在目标 WorkBuddy 关闭时确认版本，再合并 projects 文件和 session-index.json。")
	} else {
		fmt.Println("提示: 默认未写入 DSH_HOME；确认目标版本/路径后，把 bundle 的 sessions 目录合并到目标数据根。")
	}
	return transferExitOK
}
