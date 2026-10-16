package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// profileSeedResult 是 `profile seed` 的结构化回执。
type profileSeedResult struct {
	// Root 本次补齐的层根（空 = 用户层根 ~/.aicli/profiles）。
	Root string `json:"root"`
	// Written 本次实际落盘的内置 profile（缺失名整套物化 / force 下缺文件补写）。
	Written []profileSeedEntry `json:"written"`
	// Skipped 已存在且无需动作的内置名（用户内容零碰）。
	Skipped []string `json:"skipped"`
	// Force 是否启用了缺文件补写。
	Force bool `json:"force"`
}

type profileSeedEntry struct {
	Name  string   `json:"name"`
	Root  string   `json:"root"`
	Files []string `json:"files"`
}

func newProfileSeedCommand(getConfig func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "seed [--root <dir>] [--force]",
		Short: "补齐缺失的内置 profile（不覆盖已有内容）",
		Long: `把内置模板清单里缺失的 profile 物化到用户层根（~/.aicli/profiles）。

与启动期自动播种的关系：自动播种只在用户层根**完全为空**时整套物化（一次
性初始化纪律：不补齐、不复活已删除项）。二进制新增内置模板（例如 full）
而用户层已有内容时，自动播种不会落盘新模板——本命令就是那个显式补齐入口。

语义边界：
  - 缺失的内置名 → 整套物化（与播种同一落盘纪律）；
  - 已存在的内置名 → 不动（用户改过的版本永远赢）；
  - --force → 仅对"已存在但缺文件"的内置名补写缺失文件，绝不改写任何
    已存在的文件（用于旧版本落盘缺新文件的修复场景）；
  - 用户自建名 → 与内置清单无交集，不受影响。

补齐后即可 /profile use <name>（或 aicli --profile <name>）使用。`,
		Example: `  aicli profile seed
  aicli profile seed --force
  aicli profile seed --root D:\my-profiles --output json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			root, _ := cmd.Flags().GetString("root")
			force, _ := cmd.Flags().GetBool("force")
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("profile seed", "json", err, nil)
			}
			executeStructuredCommand("profile seed", outputOptions, func() (profileSeedResult, map[string]interface{}, error) {
				result, err := runProfileSeedCommand(root, force)
				return result, nil, err
			}, func(result profileSeedResult) interface{} {
				return result
			}, renderProfileSeedText)
		},
	}
	cmd.Flags().String("root", "", "目标层根目录（默认用户层根 ~/.aicli/profiles）")
	cmd.Flags().Bool("force", false, "对已存在的内置名补写缺失文件（不改写已有文件）")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func runProfileSeedCommand(root string, force bool) (profileSeedResult, error) {
	result := profileSeedResult{Force: force}
	written, skipped, err := profilesys.EnsureBuiltinProfiles(root, force)
	if err != nil {
		return result, err
	}
	if strings.TrimSpace(root) == "" {
		if resolved, layerErr := profilesys.LayerRoot("user"); layerErr == nil {
			root = resolved
		}
	}
	result.Root = root
	for _, entry := range written {
		result.Written = append(result.Written, profileSeedEntry{
			Name:  entry.Name,
			Root:  entry.Root,
			Files: entry.Files,
		})
	}
	result.Skipped = skipped
	return result, nil
}

func renderProfileSeedText(result profileSeedResult) {
	var b strings.Builder
	fmt.Fprintf(&b, "profile seed: %s\n", result.Root)
	if len(result.Written) == 0 {
		b.WriteString("  无缺失内置 profile，未写入任何文件\n")
	} else {
		for _, entry := range result.Written {
			fmt.Fprintf(&b, "  + %s（%d 个文件）\n", entry.Name, len(entry.Files))
		}
	}
	if len(result.Skipped) > 0 {
		fmt.Fprintf(&b, "  已存在未动: %s\n", strings.Join(result.Skipped, ", "))
	}
	if result.Force {
		b.WriteString("  force: 已存在的内置名只补缺失文件，已有文件零改写\n")
	}
	b.WriteString("  使用: aicli --profile <name> 或会话内 /profile use <name>\n")
	fmt.Print(b.String())
}
