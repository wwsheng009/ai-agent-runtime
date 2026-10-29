package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport/registry"
)

type transferInspectGroup struct {
	Instance sessionport.Instance     `json:"instance"`
	Sessions []sessionport.SessionRef `json:"sessions"`
	Warnings []string                 `json:"warnings,omitempty"`
}

type transferInspectResult struct {
	Zip           string                   `json:"zip,omitempty"`
	Agent         string                   `json:"agent,omitempty"`
	Instances     []transferInspectGroup   `json:"instances,omitempty"`
	Sessions      []sessionport.SessionRef `json:"sessions,omitempty"`
	Warnings      []string                 `json:"warnings,omitempty"`
	Count         int                      `json:"count"`
	InstanceCount int                      `json:"instance_count,omitempty"`
}

func newTransferInspectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect",
		Short: "检查 Agent 实例或 DSH ZIP 中的可导入会话",
		Long: `只读检查可导入的会话，不写 aicli，也不修改源 Agent。

不带参数时按默认路径探测 DeepSeek Harness 和 WorkBuddy 实例，并列出
每个实例的会话。也可以直接检查 DSH 官方 /export ZIP 内的根会话：

  aicli transfer inspect --agent auto --json
  aicli transfer inspect --zip .\dsh-export.zip --json
  aicli transfer inspect --zip .\dsh-export.zip --session session-xxx

输出包含 provider/agent、实例根、格式版本、可读性和 warning，适合在
真正执行 transfer import 前做预检，也适合脚本消费。`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			transferExit(runTransferInspect(cmd))
		},
	}
	cmd.Flags().String("agent", "auto", "Agent 适配器（auto|deepseek-harness|workbuddy）")
	cmd.Flags().StringArray("root", nil, "实例数据根，可重复（自动识别 DSH/WorkBuddy 签名）")
	cmd.Flags().String("instance", "", "detect 返回的实例 ID")
	cmd.Flags().String("db", "", "WorkBuddy workbuddy.db 路径（非标准布局）")
	cmd.Flags().String("projects-dir", "", "WorkBuddy projects 目录（非标准布局）")
	cmd.Flags().String("zip", "", "DSH 官方 /export ZIP")
	cmd.Flags().String("session", "", "只显示指定源会话 ID")
	cmd.Flags().Int("limit", 50, "每个实例最多列出的会话数（0 = 不限制）")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 输出（等价于 --output json）")
	return cmd
}

func runTransferInspect(cmd *cobra.Command) int {
	output, err := parseTransferOutputOptions(cmd)
	if err != nil {
		printTransferError("transfer inspect", structuredOutputOptions{Format: "text"}, err)
		return transferExitUsage
	}
	agent, err := registry.NormalizeAgent(stringFlag(cmd, "agent"))
	if err != nil {
		printTransferError("transfer inspect", output, err)
		return transferExitUsage
	}
	zipPath := strings.TrimSpace(stringFlag(cmd, "zip"))
	instanceFlag := strings.TrimSpace(stringFlag(cmd, "instance"))
	dbPath := strings.TrimSpace(stringFlag(cmd, "db"))
	projectsDir := strings.TrimSpace(stringFlag(cmd, "projects-dir"))
	sessionID := strings.TrimSpace(stringFlag(cmd, "session"))
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 0 {
		printTransferError("transfer inspect", output, transferUsageError("--limit 不能为负数"))
		return transferExitUsage
	}
	if zipPath != "" {
		if instanceFlag != "" || dbPath != "" || projectsDir != "" || len(transferRoots(cmd)) > 0 {
			printTransferError("transfer inspect", output, transferUsageError("--zip 不能和 --root/--instance/--db/--projects-dir 同时使用"))
			return transferExitUsage
		}
		if agent != "auto" && agent != string(sessionport.AgentDeepSeekHarness) {
			printTransferError("transfer inspect", output, transferUsageError("ZIP 只支持 DeepSeek Harness"))
			return transferExitUsage
		}
		inspected, inspectErr := registry.InspectZip(zipPath)
		if inspectErr != nil {
			printTransferError("transfer inspect", output, inspectErr)
			return transferExitFailure
		}
		refs, found := filterTransferRefs(inspected.Sessions, sessionID, limit)
		if sessionID != "" && !found {
			printTransferError("transfer inspect", output, fmt.Errorf("ZIP 中找不到会话: %s", sessionID))
			return transferExitFailure
		}
		result := transferInspectResult{
			Zip:      zipPath,
			Agent:    inspected.AgentID,
			Sessions: refs,
			Warnings: inspected.Warnings,
			Count:    len(refs),
		}
		printTransferInspectResult(output, result)
		return transferExitOK
	}

	instances, err := transferInspectInstances(agent, transferRoots(cmd), instanceFlag, dbPath, projectsDir)
	if err != nil {
		printTransferError("transfer inspect", output, err)
		return transferExitFailure
	}
	result := transferInspectResult{Agent: agent, InstanceCount: len(instances)}
	for _, instance := range instances {
		group := transferInspectGroup{Instance: instance}
		refs, listErr := registry.List(instance)
		if listErr != nil {
			group.Warnings = append(group.Warnings, "列会话失败: "+listErr.Error())
		} else {
			var found bool
			refs, found = filterTransferRefs(refs, sessionID, limit)
			if sessionID != "" && !found {
				group.Warnings = append(group.Warnings, "找不到指定会话: "+sessionID)
			}
		}
		group.Sessions = refs
		result.Count += len(refs)
		result.Instances = append(result.Instances, group)
	}
	if sessionID != "" && result.Count == 0 {
		printTransferError("transfer inspect", output, fmt.Errorf("没有找到会话: %s", sessionID))
		return transferExitFailure
	}
	printTransferInspectResult(output, result)
	return transferExitOK
}

func transferInspectInstances(agent string, roots []string, instanceFlag, dbPath, projectsDir string) ([]sessionport.Instance, error) {
	if instanceFlag != "" {
		root := ""
		if len(roots) > 0 {
			root = roots[0]
		} else {
			var err error
			root, err = transferInstanceRoot(instanceFlag)
			if err != nil {
				return nil, err
			}
		}
		instance, err := registry.DetectInstance(agent, root, true, dbPath, projectsDir)
		if err != nil {
			return nil, err
		}
		return []sessionport.Instance{instance}, nil
	}
	if dbPath != "" || projectsDir != "" {
		root := ""
		if len(roots) > 0 {
			root = roots[0]
		}
		instance, err := registry.DetectInstance(agent, root, true, dbPath, projectsDir)
		if err != nil {
			return nil, err
		}
		return []sessionport.Instance{instance}, nil
	}
	return registry.Detect(roots, agent)
}

func filterTransferRefs(refs []sessionport.SessionRef, sessionID string, limit int) ([]sessionport.SessionRef, bool) {
	if sessionID != "" {
		for _, ref := range refs {
			if ref.SessionID == sessionID {
				return []sessionport.SessionRef{ref}, true
			}
		}
		return nil, false
	}
	if limit > 0 && len(refs) > limit {
		refs = refs[:limit]
	}
	return refs, true
}

func printTransferInspectResult(output structuredOutputOptions, result transferInspectResult) {
	if isJSONOutputFormat(output.Format) {
		printCommandJSONOutput("transfer inspect", output.Envelope, result)
		return
	}
	if result.Zip != "" {
		fmt.Printf("DSH ZIP: %s\n", result.Zip)
		fmt.Printf("Agent: %s\n", blankTransferValue(result.Agent))
		printTransferInspectRefs(result.Sessions)
		for _, warning := range result.Warnings {
			fmt.Printf("警告: %s\n", warning)
		}
		return
	}
	if len(result.Instances) == 0 {
		fmt.Println("未发现可用的 Agent 实例。")
		fmt.Println("提示: 设置 DSH_HOME / WORKBUDDY_HOME，或用 --root 指向数据根。")
		return
	}
	fmt.Printf("发现 %d 个 Agent 实例，%d 个可列会话:\n", len(result.Instances), result.Count)
	for index, group := range result.Instances {
		fmt.Printf("\n[%d] %s [%s]\n", index+1, group.Instance.InstanceID, blankTransferValue(group.Instance.AgentID))
		fmt.Printf("    root: %s\n", group.Instance.Root)
		fmt.Printf("    format: v%d (%s), backend=%s, compression=%s\n",
			group.Instance.FormatVersion, blankTransferValue(group.Instance.FormatStatus),
			blankTransferValue(group.Instance.Backend), blankTransferValue(group.Instance.Compression))
		fmt.Printf("    sessions: %d/%d\n", group.Instance.SessionCount, group.Instance.TranscriptCount)
		printTransferInspectRefs(group.Sessions)
		for _, warning := range group.Warnings {
			fmt.Printf("    警告: %s\n", warning)
		}
		for _, warning := range group.Instance.Warnings {
			fmt.Printf("    警告: %s\n", warning)
		}
	}
}

func printTransferInspectRefs(refs []sessionport.SessionRef) {
	if len(refs) == 0 {
		fmt.Println("    (没有会话)")
		return
	}
	for index, ref := range refs {
		title := strings.TrimSpace(ref.Title)
		if title == "" {
			title = "(untitled)"
		}
		fmt.Printf("    [%d] %s  %s  v%d/%s  %s\n",
			index+1, ref.SessionID, blankTransferValue(ref.FormatStatus), ref.FormatVersion,
			blankTransferValue(ref.Compression), title)
		fmt.Printf("        cwd: %s\n", blankTransferValue(ref.CWD))
	}
}
