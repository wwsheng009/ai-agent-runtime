package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
)

type initCommandResult struct {
	ConfigPath    string `json:"config_path"`
	Created       bool   `json:"created"`
	AlreadyExists bool   `json:"already_exists"`
	Message       string `json:"message"`
}

// NewInitCommand creates the explicit initialization command for aicli.
func NewInitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "初始化 aicli starter 配置",
		Long: fmt.Sprintf(`初始化本地 aicli 配置文件。

默认会在用户目录创建 ~/.aicli/%[1]s（最小 starter 配置），避免在任意目录就地生成配置文件。
如需在当前工作目录创建项目级配置，使用 --project。
--global 与 --config ~/.aicli/%[1]s 仍然可用（与默认行为等价，保留以兼容旧脚本）。
如果配置文件已经存在，则保持原样，不会覆盖。

首次使用推荐：
  aicli init
  aicli login --provider openai --protocol openai --base-url https://api.openai.com --api-key sk-... --set-default
  aicli

更多说明见 docs/aicli/quickstart.md。`, aiclipaths.DefaultConfigFileName),
		Example: fmt.Sprintf(`  aicli init
  aicli init --global
  aicli init --project
  aicli init --config .aicli/%[1]s
  aicli init --config ~/.aicli/%[1]s
  aicli init --config /path/to/custom/config.yaml
  aicli init --project --json`, aiclipaths.DefaultConfigFileName),
		Run: func(cmd *cobra.Command, args []string) {
			handleInitCommand(cmd)
		},
	}
	cmd.Flags().StringP("config", "c", "", "初始化目标配置文件路径（留空时使用默认本地 starter 路径）")
	cmd.Flags().Bool("project", false, fmt.Sprintf(
		"在当前工作目录创建项目级配置 .aicli/%[1]s（默认写入用户目录）",
		aiclipaths.DefaultConfigFileName,
	))
	cmd.Flags().Bool("global", false, fmt.Sprintf(
		"初始化用户目录下的 ~/.aicli/%[1]s（已是默认行为，保留以兼容旧脚本）",
		aiclipaths.DefaultConfigFileName,
	))
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func handleInitCommand(cmd *cobra.Command) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("init", "json", err, nil)
	}
	executeCommand("init", outputOptions, func() (initCommandResult, map[string]interface{}, error) {
		return runInitCommand(cmd)
	}, func(result initCommandResult, outputOptions structuredOutputOptions) {
		renderInitCommandResult(result, outputOptions)
	})
}

func runInitCommand(cmd *cobra.Command) (initCommandResult, map[string]interface{}, error) {
	result := initCommandResult{}
	var details map[string]interface{}

	explicitPath := ""
	useGlobal := false
	useProject := false
	if cmd != nil {
		explicitPath, _ = cmd.Flags().GetString("config")
		useGlobal, _ = cmd.Flags().GetBool("global")
		useProject, _ = cmd.Flags().GetBool("project")
	}
	targetPath := strings.TrimSpace(explicitPath)
	if targetPath != "" && (useProject || useGlobal) {
		return result, details, fmt.Errorf("--config 与 --project/--global 不能同时使用")
	}
	if useProject && useGlobal {
		return result, details, fmt.Errorf("--project 与 --global 不能同时使用")
	}
	switch {
	case targetPath != "":
	case useProject, targetPath == "" && !useGlobal:
		targetPath = config.ResolveProjectConfigPath()
	case useGlobal:
		globalPath, err := config.ResolveGlobalConfigPath()
		if err != nil {
			return result, details, err
		}
		targetPath = globalPath
	}
	targetPath = config.ResolveWritableConfigPath(targetPath)
	if strings.TrimSpace(targetPath) == "" {
		return result, details, fmt.Errorf("config path is required")
	}

	result.ConfigPath = targetPath
	if _, created, err := config.EnsureStarterConfigAtPath(targetPath); err != nil {
		return result, details, err
	} else {
		result.Created = created
		result.AlreadyExists = !created
	}
	if result.Created {
		result.Message = "starter config created"
	} else {
		result.Message = "config already exists"
	}

	details = map[string]interface{}{
		"config_path":    result.ConfigPath,
		"created":        result.Created,
		"already_exists": result.AlreadyExists,
	}
	return result, details, nil
}

func renderInitCommandResult(result initCommandResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("init", outputOptions.Envelope, result)
		return
	}
	if result.Created {
		fmt.Printf("已创建 starter 配置: %s\n", result.ConfigPath)
		fmt.Println("下一步: 编辑 providers.items，然后运行 `aicli login` 或直接使用 chat/test/context。")
		return
	}
	fmt.Printf("配置已存在: %s\n", result.ConfigPath)
	if strings.TrimSpace(result.Message) != "" {
		fmt.Printf("%s\n", result.Message)
	}
}
