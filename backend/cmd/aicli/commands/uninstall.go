package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

type uninstallCommandResult struct {
	DryRun       bool                    `json:"dry_run"`
	DeletedCount int                     `json:"deleted_count"`
	MissingCount int                     `json:"missing_count"`
	Targets      []uninstallTargetResult `json:"targets"`
	Message      string                  `json:"message"`
}

type uninstallTargetResult struct {
	Scope   string `json:"scope"`
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Deleted bool   `json:"deleted"`
	Skipped bool   `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
}

type uninstallRequest struct {
	DryRun    bool
	Yes       bool
	UserOnly  bool
	LocalOnly bool
}

type uninstallTarget struct {
	Scope string
	Path  string
}

// NewUninstallCommand creates the command that removes aicli-owned config data.
func NewUninstallCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "删除 aicli 用户级和当前目录配置文件",
		Long: "删除 aicli 在用户目录和当前工作目录树下的 .aicli 目录。\n\n" +
			"默认目标为 $HOME/.aicli，以及当前工作目录和子目录中的所有 .aicli 目录，包含配置、凭证、日志、会话、skills 等文件。\n\n" +
			"该命令只清理配置与数据目录，不删除 aicli 可执行文件本身。\n" +
			"更多说明见 docs/aicli/install.md。",
		Example: `  aicli uninstall --dry-run
  aicli uninstall --yes
  aicli uninstall --user-only --yes
  aicli uninstall --local-only --yes
  aicli uninstall --output json --dry-run`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			HandleUninstall(cmd)
		},
	}
	cmd.Flags().BoolP("yes", "y", false, "确认递归删除目标 .aicli 目录")
	cmd.Flags().Bool("dry-run", false, "只预览将删除的目录，不修改文件系统")
	cmd.Flags().Bool("user-only", false, "只删除用户目录下的 ~/.aicli")
	cmd.Flags().Bool("local-only", false, "只删除当前工作目录树下的 .aicli 目录")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func HandleUninstall(cmd *cobra.Command) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("uninstall", "json", err, nil)
	}
	req := uninstallRequest{
		DryRun:    boolFlag(cmd, "dry-run"),
		Yes:       boolFlag(cmd, "yes"),
		UserOnly:  boolFlag(cmd, "user-only"),
		LocalOnly: boolFlag(cmd, "local-only"),
	}
	executeCommand("uninstall", outputOptions, func() (uninstallCommandResult, map[string]interface{}, error) {
		result, err := runUninstallCommand(req)
		if err != nil {
			return result, uninstallResultDetails(result), err
		}
		return result, nil, nil
	}, renderUninstallResult)
}

func runUninstallCommand(req uninstallRequest) (uninstallCommandResult, error) {
	result := uninstallCommandResult{DryRun: req.DryRun}
	if req.UserOnly && req.LocalOnly {
		return result, fmt.Errorf("--user-only 和 --local-only 不能同时使用")
	}
	if !req.DryRun && !req.Yes {
		return result, fmt.Errorf("卸载会递归删除 .aicli 目录；请添加 --yes 确认，或使用 --dry-run 预览")
	}

	targets, err := uninstallTargets(req)
	if err != nil {
		return result, err
	}
	for _, target := range targets {
		item := uninstallTargetResult{Scope: target.Scope, Path: target.Path}
		if err := validateUninstallTarget(target); err != nil {
			item.Error = err.Error()
			result.Targets = append(result.Targets, item)
			return result, err
		}

		info, statErr := os.Stat(target.Path)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				item.Skipped = true
				result.MissingCount++
				result.Targets = append(result.Targets, item)
				continue
			}
			item.Error = statErr.Error()
			result.Targets = append(result.Targets, item)
			return result, fmt.Errorf("检查卸载目标 %s: %w", target.Path, statErr)
		}
		if !info.IsDir() {
			item.Error = "target exists but is not a directory"
			result.Targets = append(result.Targets, item)
			return result, fmt.Errorf("卸载目标不是目录: %s", target.Path)
		}

		item.Exists = true
		if req.DryRun {
			item.Skipped = true
			result.Targets = append(result.Targets, item)
			continue
		}
		if err := os.RemoveAll(target.Path); err != nil {
			item.Error = err.Error()
			result.Targets = append(result.Targets, item)
			return result, fmt.Errorf("删除卸载目标 %s: %w", target.Path, err)
		}
		item.Deleted = true
		result.DeletedCount++
		result.Targets = append(result.Targets, item)
	}

	if req.DryRun {
		result.Message = "dry run completed"
	} else {
		result.Message = "uninstall completed"
	}
	return result, nil
}

func uninstallTargets(req uninstallRequest) ([]uninstallTarget, error) {
	targets := make([]uninstallTarget, 0, 2)
	if !req.LocalOnly {
		globalConfigPath, err := config.ResolveGlobalConfigPath()
		if err != nil {
			return nil, err
		}
		targets = append(targets, uninstallTarget{
			Scope: "user",
			Path:  filepath.Dir(globalConfigPath),
		})
	}
	if !req.UserOnly {
		localTargets, err := discoverLocalUninstallTargets()
		if err != nil {
			return nil, err
		}
		targets = append(targets, localTargets...)
	}
	return dedupeUninstallTargets(targets), nil
}

func discoverLocalUninstallTargets() ([]uninstallTarget, error) {
	root, err := filepath.Abs(".")
	if err != nil {
		return nil, fmt.Errorf("resolve current directory: %w", err)
	}
	targets := make([]uninstallTarget, 0, 1)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		switch name {
		case ".git", "node_modules", "vendor":
			return filepath.SkipDir
		case ".aicli":
			targets = append(targets, uninstallTarget{Scope: "local", Path: path})
			return filepath.SkipDir
		default:
			return nil
		}
	})
	if err != nil {
		return nil, fmt.Errorf("扫描当前目录树中的 .aicli 目录: %w", err)
	}
	if len(targets) == 0 {
		targets = append(targets, uninstallTarget{Scope: "local", Path: filepath.Join(root, ".aicli")})
	}
	return targets, nil
}

func dedupeUninstallTargets(targets []uninstallTarget) []uninstallTarget {
	out := make([]uninstallTarget, 0, len(targets))
	seen := make(map[string]int, len(targets))
	for _, target := range targets {
		resolved, err := filepath.Abs(target.Path)
		if err == nil {
			target.Path = filepath.Clean(resolved)
		} else {
			target.Path = filepath.Clean(target.Path)
		}
		key := uninstallPathKey(target.Path)
		if idx, ok := seen[key]; ok {
			if !strings.Contains(out[idx].Scope, target.Scope) {
				out[idx].Scope += "+" + target.Scope
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, target)
	}
	return out
}

func validateUninstallTarget(target uninstallTarget) error {
	path := filepath.Clean(strings.TrimSpace(target.Path))
	if path == "" || path == "." || filepath.VolumeName(path) == path {
		return fmt.Errorf("拒绝删除不安全的卸载目标: %s", target.Path)
	}
	if filepath.Base(path) != ".aicli" {
		return fmt.Errorf("拒绝删除非 .aicli 目录: %s", target.Path)
	}
	parent := filepath.Dir(path)
	if parent == path || strings.TrimSpace(parent) == "" || parent == "." {
		return fmt.Errorf("拒绝删除父目录不明确的卸载目标: %s", target.Path)
	}
	return nil
}

func uninstallPathKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

func uninstallResultDetails(result uninstallCommandResult) map[string]interface{} {
	if len(result.Targets) == 0 {
		return nil
	}
	return map[string]interface{}{"targets": result.Targets}
}

func renderUninstallResult(result uninstallCommandResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("uninstall", outputOptions.Envelope, result)
		return
	}
	if result.DryRun {
		fmt.Println("卸载预览：不会修改文件系统")
	} else {
		fmt.Println("卸载完成")
	}
	for _, target := range result.Targets {
		switch {
		case target.Deleted:
			fmt.Printf("已删除 [%s]: %s\n", target.Scope, target.Path)
		case target.Exists && result.DryRun:
			fmt.Printf("将删除 [%s]: %s\n", target.Scope, target.Path)
		case target.Skipped:
			fmt.Printf("已跳过 [%s]: %s（不存在）\n", target.Scope, target.Path)
		case target.Error != "":
			fmt.Printf("失败 [%s]: %s（%s）\n", target.Scope, target.Path, target.Error)
		default:
			fmt.Printf("目标 [%s]: %s\n", target.Scope, target.Path)
		}
	}
	if !result.DryRun && result.DeletedCount == 0 {
		fmt.Println("没有找到需要删除的 .aicli 目录。")
	}
}
