package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport/registry"
)

type transferDetectResult struct {
	Agent     string                 `json:"agent,omitempty"`
	Instances []sessionport.Instance `json:"instances"`
	Count     int                    `json:"count"`
}

func newTransferDetectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "detect",
		Short: "检测本机其它 Agent 的数据实例（终端中默认交互）",
		Long: `检测本机其它 Agent 的数据实例。

在交互终端中默认进入全屏选择界面：
  - 上下键/j/k 移动，PgUp/PgDn 翻页；
  - 直接输入关键词搜索，/ 显式进入搜索；
  - Enter 选择实例并查看会话，Esc/q 返回或取消。

--json 或 --no-interactive 会保持纯文本/JSON 输出，适合脚本和管道。`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			transferExit(runTransferDetect(cmd))
		},
	}
	cmd.Flags().String("agent", "auto", "只检测指定 Agent（auto|deepseek-harness|workbuddy）")
	cmd.Flags().StringArray("root", nil, "显式数据根，可重复（自动识别 DSH/WorkBuddy 签名）")
	cmd.Flags().String("db", "", "WorkBuddy workbuddy.db 路径（非标准布局）")
	cmd.Flags().String("projects-dir", "", "WorkBuddy projects 目录（非标准布局）")
	cmd.Flags().Bool("no-interactive", false, "不进入交互界面，直接输出检测结果")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 输出（等价于 --output json）")
	return cmd
}

func runTransferDetect(cmd *cobra.Command) int {
	output, err := parseTransferOutputOptions(cmd)
	if err != nil {
		printTransferError("transfer detect", structuredOutputOptions{Format: "text"}, err)
		return transferExitUsage
	}
	agent := stringFlag(cmd, "agent")
	dbPath := stringFlag(cmd, "db")
	projectsDir := stringFlag(cmd, "projects-dir")
	if dbPath != "" || projectsDir != "" {
		normalized, normErr := registry.NormalizeAgent(agent)
		if normErr != nil {
			printTransferError("transfer detect", output, normErr)
			return transferExitUsage
		}
		if normalized == string(sessionport.AgentDeepSeekHarness) {
			printTransferError("transfer detect", output, fmt.Errorf("--db/--projects-dir 只适用于 WorkBuddy"))
			return transferExitUsage
		}
	}
	var instances []sessionport.Instance
	if dbPath != "" || projectsDir != "" {
		roots := transferRoots(cmd)
		root := ""
		if len(roots) > 0 {
			root = roots[0]
		}
		instance, detectErr := registry.DetectInstance(agent, root, true, dbPath, projectsDir)
		if detectErr != nil {
			printTransferError("transfer detect", output, detectErr)
			return transferExitFailure
		}
		instances = []sessionport.Instance{instance}
	} else {
		instances, err = registry.Detect(transferRoots(cmd), agent)
	}
	if err != nil {
		printTransferError("transfer detect", output, err)
		return transferExitFailure
	}
	result := transferDetectResult{Agent: agent, Instances: instances, Count: len(instances)}
	if isJSONOutputFormat(output.Format) {
		printCommandJSONOutput("transfer detect", output.Envelope, result)
		return transferExitOK
	}
	if len(instances) == 0 {
		fmt.Println("未发现可用的 Agent 实例。")
		fmt.Println("提示: 设置 DSH_HOME / WORKBUDDY_HOME，或用 --root 指向数据根。")
		return transferExitOK
	}
	if !boolFlag(cmd, "no-interactive") {
		handled, uiErr := runTransferDetectInteractive(instances)
		if uiErr != nil {
			printTransferError("transfer detect", output, uiErr)
			return transferExitFailure
		}
		if handled {
			return transferExitOK
		}
	}
	printTransferDetectText(result)
	return transferExitOK
}

func printTransferDetectText(result transferDetectResult) {
	fmt.Printf("发现 %d 个 Agent 实例:\n", len(result.Instances))
	for _, instance := range result.Instances {
		printTransferDetectInstance(instance)
	}
}

func printTransferDetectInstance(instance sessionport.Instance) {
	fmt.Printf("  %s [%s]\n", instance.InstanceID, blankTransferValue(instance.AgentID))
	fmt.Printf("    root: %s\n", instance.Root)
	fmt.Printf("    sessions: %s\n", instance.SessionsDir)
	if instance.DBPath != "" {
		fmt.Printf("    db: %s\n", instance.DBPath)
	}
	if instance.ProjectsDir != "" {
		fmt.Printf("    projects: %s\n", instance.ProjectsDir)
	}
	fmt.Printf("    format: v%d (%s), backend=%s, compression=%s\n",
		instance.FormatVersion, blankTransferValue(instance.FormatStatus),
		blankTransferValue(instance.Backend), blankTransferValue(instance.Compression))
	if instance.AppVersion != "" {
		fmt.Printf("    app version: %s\n", instance.AppVersion)
	}
	if instance.SchemaVersion != "" {
		fmt.Printf("    schema: %s\n", instance.SchemaVersion)
	}
	fmt.Printf("    sessions/transcripts: %d/%d, confidence=%.2f\n",
		instance.SessionCount, instance.TranscriptCount, instance.Confidence)
	if !instance.Capabilities.Read {
		fmt.Println("    capability: read=no")
	}
	if instance.Capabilities.WriteBundle {
		fmt.Println("    capability: write-bundle=yes, apply=none")
	}
	for _, warning := range instance.Warnings {
		fmt.Printf("    warning: %s\n", warning)
	}
}

func runTransferDetectInteractive(instances []sessionport.Instance) (bool, error) {
	terminal := ui.NewTerminal()
	if !ui.CanUseFullScreenList(terminal) {
		return false, nil
	}
	ctx := context.Background()
	initialInstance := 0
	for {
		instanceOptions := transferDetectInstanceOptions(instances)
		instanceOptions.InitialIndex = initialInstance
		picked, err := ui.RunStartupFullScreenList(ctx, terminal, instanceOptions)
		if err != nil {
			if errors.Is(err, ui.ErrFullScreenUnavailable) {
				return false, nil
			}
			return true, err
		}
		if picked.Cancelled || picked.Index < 0 || picked.Index >= len(instances) {
			fmt.Println("已取消。")
			return true, nil
		}
		initialInstance = picked.Index
		instance := instances[picked.Index]
		refs, err := registry.List(instance)
		if err != nil {
			return true, fmt.Errorf("读取 %s 会话失败: %w", instance.InstanceID, err)
		}
		if len(refs) == 0 {
			fmt.Printf("%s 没有可用会话。\n", instance.InstanceID)
			if len(instances) == 1 {
				printTransferDetectInstance(instance)
				return true, nil
			}
			continue
		}
		sessionPicked, err := ui.RunStartupFullScreenList(ctx, terminal, transferDetectSessionOptions(instance, refs))
		if err != nil {
			if errors.Is(err, ui.ErrFullScreenUnavailable) {
				return false, nil
			}
			return true, err
		}
		if sessionPicked.Cancelled || sessionPicked.Index < 0 || sessionPicked.Index >= len(refs) {
			continue
		}
		printTransferDetectSelection(instance, refs[sessionPicked.Index])
		return true, nil
	}
}

func transferDetectInstanceOptions(instances []sessionport.Instance) ui.FullScreenListOptions {
	items := make([]ui.FullScreenListItem, 0, len(instances))
	for _, instance := range instances {
		items = append(items, ui.FullScreenListItem{
			Leading:    instance.AgentID,
			Title:      instance.InstanceID,
			Detail:     instance.Root,
			Preview:    transferDetectInstancePreview(instance),
			SearchText: strings.Join([]string{instance.AgentID, instance.InstanceID, instance.Root, instance.Format, instance.Backend}, " "),
		})
	}
	return ui.FullScreenListOptions{
		Title:        "Agent 会话检测",
		Subtitle:     fmt.Sprintf("检测到 %d 个 Agent 实例", len(instances)),
		EmptyMessage: "没有可用的 Agent 实例",
		ConfirmLabel: "查看会话",
		Items:        items,
	}
}

func transferDetectSessionOptions(instance sessionport.Instance, refs []sessionport.SessionRef) ui.FullScreenListOptions {
	items := make([]ui.FullScreenListItem, 0, len(refs))
	for _, ref := range refs {
		title := strings.TrimSpace(ref.Title)
		if title == "" {
			title = ref.SessionID
		}
		items = append(items, ui.FullScreenListItem{
			Leading:    fmt.Sprintf("v%d %s", ref.FormatVersion, blankTransferValue(ref.FormatStatus)),
			Title:      title,
			Detail:     transferDetectSessionDetail(ref),
			Preview:    transferDetectSessionPreview(ref),
			SearchText: strings.Join([]string{ref.SessionID, ref.Title, ref.CWD, ref.FormatStatus, ref.Compression}, " "),
			Disabled:   !ref.Readable,
		})
	}
	subtitle := strings.TrimSpace(instance.Root)
	if subtitle != "" {
		subtitle += " · "
	}
	subtitle += "Esc/q 返回实例列表"
	return ui.FullScreenListOptions{
		Title:        instance.InstanceID,
		Subtitle:     subtitle,
		EmptyMessage: "没有可用的会话",
		ConfirmLabel: "查看详情",
		Items:        items,
	}
}

func transferDetectInstancePreview(instance sessionport.Instance) string {
	lines := []string{
		fmt.Sprintf("Provider:   %s", instance.AgentID),
		fmt.Sprintf("Instance:   %s", instance.InstanceID),
		fmt.Sprintf("Root:       %s", instance.Root),
		fmt.Sprintf("Format:     v%d (%s)", instance.FormatVersion, blankTransferValue(instance.FormatStatus)),
		fmt.Sprintf("Backend:    %s / %s", blankTransferValue(instance.Backend), blankTransferValue(instance.Compression)),
		fmt.Sprintf("Sessions:   %d / %d", instance.SessionCount, instance.TranscriptCount),
		fmt.Sprintf("Confidence: %.2f", instance.Confidence),
		fmt.Sprintf("Capability: list=%t read=%t bundle=%t apply=%s",
			instance.Capabilities.List, instance.Capabilities.Read, instance.Capabilities.WriteBundle, blankTransferValue(instance.Capabilities.Apply)),
	}
	for _, warning := range instance.Warnings {
		lines = append(lines, "Warning:    "+warning)
	}
	return strings.Join(lines, "\n")
}

func transferDetectSessionDetail(ref sessionport.SessionRef) string {
	parts := []string{ref.SessionID}
	if ref.CWD != "" {
		parts = append(parts, ref.CWD)
	}
	if !ref.UpdatedAt.IsZero() {
		parts = append(parts, ref.UpdatedAt.Format("2006-01-02 15:04"))
	}
	return strings.Join(parts, " · ")
}

func transferDetectSessionPreview(ref sessionport.SessionRef) string {
	lines := []string{
		"Session:     " + ref.SessionID,
		fmt.Sprintf("Format:      v%d (%s)", ref.FormatVersion, blankTransferValue(ref.FormatStatus)),
		"Compression: " + blankTransferValue(ref.Compression),
		"CWD:         " + blankTransferValue(ref.CWD),
	}
	if !ref.UpdatedAt.IsZero() {
		lines = append(lines, "Updated:     "+ref.UpdatedAt.Format("2006-01-02 15:04:05"))
	}
	lines = append(lines, fmt.Sprintf("Events:      %d", ref.EventCount))
	for _, warning := range ref.Warnings {
		lines = append(lines, "Warning:     "+warning)
	}
	return strings.Join(lines, "\n")
}

func printTransferDetectSelection(instance sessionport.Instance, ref sessionport.SessionRef) {
	fmt.Printf("已选择会话:\n")
	fmt.Printf("  Agent:    %s\n", blankTransferValue(instance.AgentID))
	fmt.Printf("  Instance: %s\n", instance.InstanceID)
	fmt.Printf("  Session:  %s\n", ref.SessionID)
	fmt.Printf("  Title:    %s\n", blankTransferValue(ref.Title))
	fmt.Printf("  Format:   v%d (%s)\n", ref.FormatVersion, blankTransferValue(ref.FormatStatus))
	fmt.Printf("  CWD:      %s\n", blankTransferValue(ref.CWD))
	fmt.Printf("  Readable: %t\n", ref.Readable)
	fmt.Printf("  Import:   aicli transfer import --from %s --instance %s --session %s --dry-run\n",
		instance.AgentID, instance.InstanceID, ref.SessionID)
}

func blankTransferValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
