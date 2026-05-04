package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

type initCommandResult struct {
	ConfigPath     string `json:"config_path"`
	Created        bool   `json:"created"`
	AlreadyExists  bool   `json:"already_exists"`
	Message        string `json:"message"`
}

// NewInitCommand creates the explicit initialization command for aicli.
func NewInitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "初始化 aicli starter 配置",
		Long: `初始化本地 aicli 配置文件。

默认会在当前工作目录的 .aicli/config.yaml 创建最小 starter 配置。
如需写入用户目录，可使用 --global 或 --config ~/.aicli/config.yaml。
如果配置文件已经存在，则保持原样，不会覆盖。`,
		Example: `  aicli init
  aicli init --global
  aicli init --config .aicli/config.yaml
  aicli init --config ~/.aicli/config.yaml
  aicli init --config /path/to/custom/config.yaml`,
		Run: func(cmd *cobra.Command, args []string) {
			handleInitCommand(cmd)
		},
	}
	cmd.Flags().StringP("config", "c", "", "初始化目标配置文件路径（留空时使用默认本地 starter 路径）")
	cmd.Flags().Bool("global", false, "初始化用户目录下的 ~/.aicli/config.yaml（等价于 --config ~/.aicli/config.yaml）")
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
	if cmd != nil {
		explicitPath, _ = cmd.Flags().GetString("config")
		useGlobal, _ = cmd.Flags().GetBool("global")
	}
	targetPath := strings.TrimSpace(explicitPath)
	if targetPath == "" && useGlobal {
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
