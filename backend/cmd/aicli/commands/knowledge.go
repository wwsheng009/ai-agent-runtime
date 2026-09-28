package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// ============================================================================
// `aicli knowledge`：知识层状态面（06 §4 Phase 1 交付 5 的 CLI 入口）。
//
// 与 HTTP 面（GET /api/runtime/knowledge/status）共用 knowledge.StatusReport：
// CLI 与运行时看到的是同一份口径与同一组字段，避免两边漂移。
//
// 只读契约（与 ADR-0003 §4.6 的状态面约束一致）：
//   - 不触发索引：Activate 传 SkipInitialIndex，查询状态不得改变被查对象；
//   - 不写库：workspace 解析走 FindWorkspace（lookupWorkspace），reader 也安全；
//   - 若其它进程持有 owner 锁，Activate 自动降级为 reader，照样能读出行数 /
//     job / 锁等待，并以 degraded_reason 标注只读与锁持有者 pid。
// ============================================================================

// NewKnowledgeCommand 创建 `aicli knowledge` 命令树。
func NewKnowledgeCommand(getConfig func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "knowledge",
		Short: "知识层状态（索引状态 / 文件数 / 符号数 / DB 大小 / 最近 job / 锁等待 p95）",
		Long: `查看知识层（knowledge layer）的运行状态快照。

子命令：
  knowledge status   打印当前工作区的知识层状态（只读，不触发索引）

说明：知识层默认关闭（knowledge.mode=off）。在 runtime.yaml 中启用 shadow / on
后，本命令可查看：索引行数（files/symbols/refs）、DB 大小、最近一次索引 job、
本进程写路径的锁等待 p95。与 runtime-server 的 GET /api/runtime/knowledge/status
共用同一载荷定义。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(newKnowledgeStatusCommand(getConfig))
	return cmd
}

// newKnowledgeStatusCommand 构造 `aicli knowledge status`。
func newKnowledgeStatusCommand(getConfig func() *config.Config) *cobra.Command {
	var (
		workspaceFlag string
		jsonOutput    bool
		timeout       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "status",
		Short: "打印知识层状态快照（只读；不触发索引）",
		Long: `打印知识层状态快照（mode / role / 行数 / DB 大小 / 最近 job / 锁等待 p95）。

工作区锚点与三个入口（runtime-server / TUI / ACP）同源：
  --workspace 显式指定 → runtime.yaml 的 workspace.root → 进程 cwd。

退出码：0=成功（含 mode=off）；1=查询失败（配置非法 / store 不可读）。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKnowledgeStatus(getConfig, workspaceFlag, jsonOutput, timeout, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&workspaceFlag, "workspace", "", "工作区根目录（缺省取 runtime.yaml 的 workspace.root，再退化为进程 cwd）")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "以 JSON 输出（与 HTTP 状态面同形）")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "查询超时")
	return cmd
}

// runKnowledgeStatus 组装并打印状态快照。
func runKnowledgeStatus(getConfig func() *config.Config, workspace string, jsonOutput bool, timeout time.Duration, out io.Writer) error {
	var cfg *config.Config
	if getConfig != nil {
		cfg = getConfig()
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	// workspace 锚点与三入口同源（loadRuntimeToolConfig 写 Workspace.Root 的同一解析）。
	runtimeConfig := loadRuntimeToolConfig(cfg, nil)
	ws := strings.TrimSpace(workspace)
	if ws == "" {
		ws = strings.TrimSpace(runtimeConfig.Workspace.Root)
	}
	if ws == "" {
		if cwd, err := os.Getwd(); err == nil {
			ws = cwd
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// SkipInitialIndex：状态查询不触发索引；mode=off 时 Activate 返回 (nil, nil)，
	// 后续调用全部 nil-safe，行为与"无知识层"一致。
	act, err := knowledge.Activate(ctx, runtimeConfig.Knowledge, ws, knowledge.ActivationOptions{
		SkipInitialIndex: true,
	})
	if err != nil {
		return fmt.Errorf("打开知识层失败（workspace=%s）：%w", ws, err)
	}
	defer func() {
		if cerr := act.Close(); cerr != nil {
			fmt.Fprintf(os.Stderr, "Warning: 关闭知识层失败: %v\n", cerr)
		}
	}()

	report, err := act.Status(ctx)
	if err != nil {
		return fmt.Errorf("读取知识层状态失败：%w", err)
	}

	if jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	printKnowledgeStatusReport(out, report)
	return nil
}

// printKnowledgeStatusReport 输出人类可读的状态快照。
func printKnowledgeStatusReport(out io.Writer, report knowledge.StatusReport) {
	role := string(report.Role)
	if report.OwnerPID > 0 {
		role = fmt.Sprintf("%s (pid %d)", role, report.OwnerPID)
	}
	fmt.Fprintf(out, "Knowledge Layer Status\n")
	fmt.Fprintf(out, "  %-14s %s\n", "mode", report.Mode)
	fmt.Fprintf(out, "  %-14s %s\n", "role", role)
	if report.Workspace != "" {
		fmt.Fprintf(out, "  %-14s %s\n", "workspace", report.Workspace)
	}
	if report.DBPath != "" {
		fmt.Fprintf(out, "  %-14s %s (%s, schema v%d)\n",
			"database", report.DBPath, knowledgeHumanBytes(report.DBSizeBytes), report.SchemaVersion)
	}
	fmt.Fprintf(out, "  %-14s files=%d symbols=%d refs=%d\n",
		"index", report.Files, report.Symbols, report.Refs)
	if report.IndexedAt > 0 {
		fmt.Fprintf(out, "  %-14s %s (stale %s)\n",
			"indexed_at", knowledgeFormatUnixMillis(report.IndexedAt), knowledgeFormatDurationMS(report.StalenessMS))
	} else {
		fmt.Fprintf(out, "  %-14s -\n", "indexed_at")
	}
	fmt.Fprintf(out, "  %-14s %v\n", "index_running", report.IndexRunning)

	if job := report.LastJob; job != nil {
		detail := fmt.Sprintf("%s %s %d/%d files", job.Kind, job.Status, job.FilesDone, job.FilesTotal)
		if job.DurationMS > 0 {
			detail += ", " + knowledgeFormatDurationMS(job.DurationMS)
		}
		if job.StartedAt > 0 {
			detail += " (started " + knowledgeFormatUnixMillis(job.StartedAt) + ")"
		}
		if job.Error != "" {
			detail += " error=" + job.Error
		}
		fmt.Fprintf(out, "  %-14s %s\n", "last_job", detail)
	} else {
		fmt.Fprintf(out, "  %-14s -\n", "last_job")
	}

	wait := report.LockWait
	fmt.Fprintf(out, "  %-14s samples=%d p50=%.1fms p95=%.1fms max=%.1fms retry_failures=%d\n",
		"lock_wait", wait.Samples, wait.P50MS, wait.P95MS, wait.MaxMS, wait.RetryFailures)

	if report.DegradedReason != "" {
		fmt.Fprintf(out, "  %-14s %s\n", "degraded", report.DegradedReason)
	}
	if report.Mode == knowledge.ModeOff {
		fmt.Fprintf(out, "\n提示：知识层未启用。在 runtime.yaml 中设置 knowledge.mode=shadow（或 on）后重试。\n")
	}
}

// knowledgeHumanBytes 以 IEC 单位格式化字节数（与状态栏口径一致）。
func knowledgeHumanBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	value := float64(bytes)
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	for _, name := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.1f %s", value, name)
		}
	}
	return fmt.Sprintf("%.1f PiB", value/unit)
}

// knowledgeFormatUnixMillis 把 unix 毫秒格式化为本地时间。
func knowledgeFormatUnixMillis(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return time.UnixMilli(ms).Local().Format("2006-01-02 15:04:05")
}

// knowledgeFormatDurationMS 把毫秒格式化为人类可读时长。
func knowledgeFormatDurationMS(ms int64) string {
	if ms <= 0 {
		return "0s"
	}
	return time.Duration(ms * int64(time.Millisecond)).Round(time.Second).String()
}
