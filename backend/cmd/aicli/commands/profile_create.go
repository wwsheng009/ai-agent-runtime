package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

type profileCreateOptions struct {
	Name       string
	Template   string
	Root       string
	Agent      string
	Use        bool
	SetDefault bool
	Force      bool
	DryRun     bool
}

type profileCreateResult struct {
	Name              string   `json:"name"`
	Template          string   `json:"template"`
	Root              string   `json:"root"`
	Agent             string   `json:"agent"`
	Files             []string `json:"files"`
	Created           bool     `json:"created"`
	DryRun            bool     `json:"dry_run,omitempty"`
	ConfigPath        string   `json:"config_path,omitempty"`
	Registered        bool     `json:"registered,omitempty"`
	DefaultProfileSet bool     `json:"default_profile_set,omitempty"`
}

func newProfileCreateCommand(getConfig func() *config.Config) *cobra.Command {
	opts := profileCreateOptions{}
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "从内置模板生成 profile 目录",
		Long: `从内置模板（coding|review|minimal|docs）生成一个 profile 目录。

目标目录：
  --root 指定；否则 profiles.root/<name>；两者都没有时用 ./profiles/<name>。

写回边界（D15）：只在 profile 目录内写文件；--use / --set-default 额外更新
配置文件（与其它写点共用同一把写锁与分层写路由），失败时状态零改动。`,
		Example: `  aicli profile create coding --template coding --use
  aicli profile create review --template review --set-default
  aicli profile create my-profile --dry-run`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			opts.Name = args[0]
			opts.Template, _ = cmd.Flags().GetString("template")
			opts.Root, _ = cmd.Flags().GetString("root")
			opts.Agent, _ = cmd.Flags().GetString("agent")
			opts.Use, _ = cmd.Flags().GetBool("use")
			opts.SetDefault, _ = cmd.Flags().GetBool("set-default")
			opts.Force, _ = cmd.Flags().GetBool("force")
			opts.DryRun, _ = cmd.Flags().GetBool("dry-run")

			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("profile create", "json", err, nil)
			}
			executeStructuredCommand("profile create", outputOptions, func() (profileCreateResult, map[string]interface{}, error) {
				result, err := runProfileCreateCommand(getConfig(), opts)
				return result, nil, err
			}, func(result profileCreateResult) interface{} {
				return result
			}, renderProfileCreateText)
		},
	}
	cmd.Flags().String("template", "coding", "内置模板：coding|review|minimal|docs")
	cmd.Flags().String("root", "", "目标 profile 目录（默认 profiles.root/<name> 或 ./profiles/<name>）")
	cmd.Flags().String("agent", "", "默认 agent id（默认模板自带的 default）")
	cmd.Flags().Bool("use", false, "生成后注册到 config profiles.items[<name>].root")
	cmd.Flags().Bool("set-default", false, "生成后设置 config profiles.default_profile")
	cmd.Flags().Bool("force", false, "目标目录已存在且非空时覆盖模板文件")
	cmd.Flags().Bool("dry-run", false, "只展示将生成的文件，不写盘")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func runProfileCreateCommand(cfg *config.Config, opts profileCreateOptions) (profileCreateResult, error) {
	name := strings.TrimSpace(opts.Name)
	if err := validateProfileCreateName(name); err != nil {
		return profileCreateResult{}, err
	}
	template := strings.TrimSpace(opts.Template)
	if template == "" {
		template = "coding"
	}
	agentID := strings.TrimSpace(opts.Agent)
	files, err := profilesys.RenderTemplate(template, name, agentID)
	if err != nil {
		return profileCreateResult{}, err
	}
	if agentID == "" {
		agentID = profilesys.TemplateDefaultAgent
	}

	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = defaultProfileCreateRoot(cfg, name)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return profileCreateResult{}, fmt.Errorf("resolve profile root %s: %w", root, err)
	}
	if err := ensureProfileCreateTarget(absRoot, opts.Force); err != nil {
		return profileCreateResult{}, err
	}

	relPaths := sortedProfileTemplatePaths(files)
	result := profileCreateResult{
		Name:     name,
		Template: template,
		Root:     absRoot,
		Agent:    agentID,
		Files:    relPaths,
		DryRun:   opts.DryRun,
	}
	if opts.DryRun {
		return result, nil
	}

	if err := writeProfileTemplateFiles(absRoot, files, relPaths); err != nil {
		return profileCreateResult{}, err
	}
	result.Created = true

	if opts.Use || opts.SetDefault {
		configPath, err := ensureWritableAICLIConfigPath(cfg, "")
		if err != nil {
			return result, err
		}
		update := config.ProfilesConfigUpdate{}
		if opts.Use {
			update.ItemName = name
			update.ItemRoot = absRoot
		}
		if opts.SetDefault {
			update.DefaultProfile = name
		}
		if err := config.UpdateProfilesConfig(configPath, update); err != nil {
			return result, err
		}
		result.ConfigPath = configPath
		result.Registered = opts.Use
		result.DefaultProfileSet = opts.SetDefault
	}
	return result, nil
}

func defaultProfileCreateRoot(cfg *config.Config, name string) string {
	if cfg != nil && cfg.Profiles != nil {
		if root := strings.TrimSpace(cfg.Profiles.Root); root != "" {
			return filepath.Join(root, name)
		}
	}
	return filepath.Join("profiles", name)
}

// validateProfileCreateName 限制 profile 名称：它会成为目录名与 profile.name。
// 规则本体在 internal/profile.ValidateProfileName（与 profiles API 共用一份，
// 避免出现"API 能建、CLI 建不了"的第二套方言）。
func validateProfileCreateName(name string) error {
	return profilesys.ValidateProfileName(name)
}

func ensureProfileCreateTarget(root string, force bool) error {
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat profile root %s: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("目标已存在且不是目录：%s", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Errorf("read profile root %s: %w", root, err)
	}
	if len(entries) > 0 && !force {
		return fmt.Errorf("目标目录已存在且非空：%s（使用 --force 覆盖模板文件）", root)
	}
	return nil
}

func sortedProfileTemplatePaths(files map[string][]byte) []string {
	paths := make([]string, 0, len(files))
	for rel := range files {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	return paths
}

func writeProfileTemplateFiles(root string, files map[string][]byte, relPaths []string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create profile dir %s: %w", root, err)
	}
	for _, rel := range relPaths {
		target := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create dir for %s: %w", rel, err)
		}
		if err := os.WriteFile(target, files[rel], 0o644); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	return nil
}

func renderProfileCreateText(result profileCreateResult) {
	if result.DryRun {
		fmt.Fprintf(os.Stdout, "profile 将生成（--dry-run，未写盘）：%s（template: %s）\n", result.Name, result.Template)
	} else {
		fmt.Fprintf(os.Stdout, "profile 已生成：%s（template: %s）\n", result.Name, result.Template)
	}
	fmt.Fprintf(os.Stdout, "  root:  %s\n", result.Root)
	fmt.Fprintf(os.Stdout, "  agent: %s\n", result.Agent)
	fmt.Fprintf(os.Stdout, "  文件（%d）：\n", len(result.Files))
	for _, rel := range result.Files {
		fmt.Fprintf(os.Stdout, "    %s\n", rel)
	}
	if result.ConfigPath != "" {
		changes := make([]string, 0, 2)
		if result.Registered {
			changes = append(changes, "profiles.items."+result.Name+".root")
		}
		if result.DefaultProfileSet {
			changes = append(changes, "profiles.default_profile")
		}
		fmt.Fprintf(os.Stdout, "  config: %s（已更新 %s）\n", result.ConfigPath, strings.Join(changes, ", "))
	}
	if result.DryRun {
		fmt.Fprintln(os.Stdout, "  （--dry-run：未写盘、未更新 config）")
	}
}
