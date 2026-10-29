package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport/registry"
)

type transferListResult struct {
	Agent    string                   `json:"agent"`
	Instance sessionport.Instance     `json:"instance"`
	Sessions []sessionport.SessionRef `json:"sessions"`
	Count    int                      `json:"count"`
}

func newTransferListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出 Agent 实例中的会话",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			transferExit(runTransferList(cmd))
		},
	}
	cmd.Flags().String("agent", "auto", "Agent 适配器（auto|deepseek-harness|workbuddy）")
	cmd.Flags().String("instance", "", "detect 返回的实例 ID")
	cmd.Flags().String("root", "", "实例数据根；非标准 WorkBuddy 可配合 --db/--projects-dir")
	cmd.Flags().String("db", "", "WorkBuddy workbuddy.db 路径（非标准布局）")
	cmd.Flags().String("projects-dir", "", "WorkBuddy projects 目录（非标准布局）")
	cmd.Flags().Int("limit", 50, "最大条数（0 = 不限制）")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().Bool("json", false, "以 JSON 输出（等价于 --output json）")
	return cmd
}

func runTransferList(cmd *cobra.Command) int {
	output, err := parseTransferOutputOptions(cmd)
	if err != nil {
		printTransferError("transfer list", structuredOutputOptions{Format: "text"}, err)
		return transferExitUsage
	}
	agent, err := registry.NormalizeAgent(stringFlag(cmd, "agent"))
	if err != nil {
		printTransferError("transfer list", output, err)
		return transferExitUsage
	}
	instanceFlag := strings.TrimSpace(stringFlag(cmd, "instance"))
	root := strings.TrimSpace(stringFlag(cmd, "root"))
	dbPath := strings.TrimSpace(stringFlag(cmd, "db"))
	projectsDir := strings.TrimSpace(stringFlag(cmd, "projects-dir"))
	if (dbPath != "" || projectsDir != "") && agent == string(sessionport.AgentDeepSeekHarness) {
		printTransferError("transfer list", output, fmt.Errorf("--db/--projects-dir 只适用于 WorkBuddy"))
		return transferExitUsage
	}
	if root == "" && instanceFlag != "" {
		root, err = transferInstanceRoot(instanceFlag)
		if err != nil {
			printTransferError("transfer list", output, err)
			return transferExitUsage
		}
		if agent == "auto" {
			if strings.HasPrefix(strings.ToLower(instanceFlag), "workbuddy") {
				agent = string(sessionport.AgentWorkBuddy)
			} else if strings.HasPrefix(strings.ToLower(instanceFlag), "deepseek-harness") {
				agent = string(sessionport.AgentDeepSeekHarness)
			}
		}
	}
	if root == "" && dbPath == "" && projectsDir == "" && instanceFlag == "" {
		instances, detectErr := registry.Detect(nil, agent)
		if detectErr != nil {
			printTransferError("transfer list", output, detectErr)
			return transferExitFailure
		}
		if len(instances) == 0 {
			printTransferError("transfer list", output, fmt.Errorf("未发现可用实例；请用 --agent/--root 指定"))
			return transferExitFailure
		}
		if len(instances) > 1 {
			ids := make([]string, 0, len(instances))
			for _, item := range instances {
				ids = append(ids, item.InstanceID)
			}
			printTransferError("transfer list", output, fmt.Errorf("发现多个实例，请用 --instance 选择: %s", strings.Join(ids, ", ")))
			return transferExitUsage
		}
		instance := instances[0]
		refs, listErr := listTransferRefs(instance, cmd)
		if listErr != nil {
			printTransferError("transfer list", output, listErr)
			return transferExitFailure
		}
		printTransferListResult(output, instance, refs)
		return transferExitOK
	}
	instance, err := registry.DetectInstance(agent, root, true, dbPath, projectsDir)
	if err != nil {
		printTransferError("transfer list", output, err)
		return transferExitFailure
	}
	refs, err := listTransferRefs(instance, cmd)
	if err != nil {
		printTransferError("transfer list", output, err)
		return transferExitFailure
	}
	printTransferListResult(output, instance, refs)
	return transferExitOK
}

func listTransferRefs(instance sessionport.Instance, cmd *cobra.Command) ([]sessionport.SessionRef, error) {
	refs, err := registry.List(instance)
	if err != nil {
		return nil, err
	}
	limit, _ := cmd.Flags().GetInt("limit")
	if limit < 0 {
		return nil, fmt.Errorf("--limit 不能为负数")
	}
	if limit > 0 && len(refs) > limit {
		refs = refs[:limit]
	}
	return refs, nil
}

func printTransferListResult(output structuredOutputOptions, instance sessionport.Instance, refs []sessionport.SessionRef) {
	result := transferListResult{Agent: instance.AgentID, Instance: instance, Sessions: refs, Count: len(refs)}
	if isJSONOutputFormat(output.Format) {
		printCommandJSONOutput("transfer list", output.Envelope, result)
		return
	}
	if len(refs) == 0 {
		fmt.Printf("%s 实例没有可用会话: %s\n", blankTransferValue(instance.AgentID), instance.SessionsDir)
		return
	}
	fmt.Printf("%s 会话（%s）: %d 条\n", blankTransferValue(instance.AgentID), instance.InstanceID, len(refs))
	for _, ref := range refs {
		state := "readable"
		if !ref.Readable {
			state = blankTransferValue(ref.FormatStatus)
			if state == "-" {
				state = "unreadable"
			}
		}
		fmt.Printf("  %s · %s · v%d/%s · %s\n",
			ref.SessionID, blankTransferValue(ref.Title), ref.FormatVersion, blankTransferValue(ref.Compression), state)
		fmt.Printf("    cwd: %s\n", blankTransferValue(ref.CWD))
		for _, warning := range ref.Warnings {
			fmt.Printf("    warning: %s\n", warning)
		}
	}
}
