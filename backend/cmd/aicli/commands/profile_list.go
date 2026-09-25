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

// profileListEntry 描述 `profile list` 的一行。
// 四来源（设计文档 D5 + G4 层）：config 注册项 / default root 下的目录 /
// 标准层（user|project）根下的目录 / 显式路径。
type profileListEntry struct {
	Name        string `json:"name"`
	Source      string `json:"source"`
	Root        string `json:"root"`
	Exists      bool   `json:"exists"`
	IsDefault   bool   `json:"is_default,omitempty"`
	FromEnv     bool   `json:"from_env,omitempty"`
	Description string `json:"description,omitempty"`
	Error       string `json:"error,omitempty"`
}

type profileListResult struct {
	Profiles       []profileListEntry `json:"profiles"`
	DefaultProfile string             `json:"default_profile,omitempty"`
	DefaultRoot    string             `json:"default_root,omitempty"`
	EnvVars        []string           `json:"env_vars,omitempty"`
}

func newProfileListCommand(getConfig func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [path]",
		Short: "列出可用 profile",
		Long: `列出可用 profile，标注四来源与当前默认生效项：

  1. config profiles.items 注册项
  2. profiles.root（default root）下含 profile.yaml 的子目录
  3. 标准层根下含 profile.yaml 的子目录（user=<home>/.aicli/profiles，project=<cwd>/.aicli/profiles）
  4. 命令行显式给出的路径

同名去重顺序即优先级：config 注册项 > profiles.root > project 层 > user 层。

DEFAULT_PROFILE / PROFILES_ROOT 环境变量生效时会在输出中标注。`,
		Example: `  aicli profile list
  aicli profile list .\my-profiles\review
  aicli profile list --output json`,
		Args: cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			explicitPath := ""
			if len(args) > 0 {
				explicitPath = args[0]
			}
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("profile list", "json", err, nil)
			}
			executeStructuredCommand("profile list", outputOptions, func() (profileListResult, map[string]interface{}, error) {
				result, err := runProfileListCommand(getConfig(), explicitPath)
				return result, nil, err
			}, func(result profileListResult) interface{} {
				return result
			}, renderProfileListText)
		},
	}
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func runProfileListCommand(cfg *config.Config, explicitPath string) (profileListResult, error) {
	result := profileListResult{}
	result.EnvVars = profileListEnvVars()

	var profiles *config.ProfilesConfig
	if cfg != nil {
		profiles = cfg.Profiles
	}
	defaultName := ""
	defaultRoot := ""
	if profiles != nil {
		defaultName = strings.TrimSpace(profiles.DefaultProfile)
		defaultRoot = strings.TrimSpace(profiles.Root)
	}
	result.DefaultProfile = defaultName
	result.DefaultRoot = defaultRoot

	defaultFromEnv := strings.TrimSpace(os.Getenv("DEFAULT_PROFILE")) != ""
	rootFromEnv := strings.TrimSpace(os.Getenv("PROFILES_ROOT")) != ""

	entries := make([]profileListEntry, 0, 8)
	seen := make(map[string]struct{})

	addEntry := func(entry profileListEntry, dedupe bool) {
		if dedupe {
			if _, exists := seen[entry.Name]; exists {
				return
			}
			seen[entry.Name] = struct{}{}
		}
		entries = append(entries, entry)
	}

	for _, name := range sortedProfileItemNames(profiles) {
		root := strings.TrimSpace(profiles.Items[name].Root)
		entry := profileListEntry{
			Name:      name,
			Source:    "config",
			Root:      root,
			Exists:    profileRootHasProfileYAML(root),
			IsDefault: name == defaultName,
			FromEnv:   defaultFromEnv && name == defaultName,
		}
		entry.Description, entry.Error = describeProfileRoot(root, entry.Exists)
		addEntry(entry, true)
	}

	if defaultRoot != "" {
		subdirs, err := os.ReadDir(defaultRoot)
		if err != nil {
			if !os.IsNotExist(err) {
				return result, fmt.Errorf("read profiles root %s: %w", defaultRoot, err)
			}
		} else {
			names := make([]string, 0, len(subdirs))
			for _, dir := range subdirs {
				if dir.IsDir() {
					names = append(names, dir.Name())
				}
			}
			sort.Strings(names)
			for _, name := range names {
				root := filepath.Join(defaultRoot, name)
				if !profileRootHasProfileYAML(root) {
					continue
				}
				entry := profileListEntry{
					Name:      name,
					Source:    "root",
					Root:      root,
					Exists:    true,
					IsDefault: name == defaultName,
					FromEnv:   defaultFromEnv && name == defaultName,
				}
				entry.Description, entry.Error = describeProfileRoot(root, true)
				addEntry(entry, true)
			}
		}
	}

	// 层来源（G4 写点的读侧）：create/duplicate/import/move 的落盘目标是标准层根，
	// 层目录必须与 config/root 同列可见——否则"刚创建的 profile 查不到、切不了"。
	// 去重顺序即优先级：config 注册项 > profiles.root > project 层 > user 层。
	for _, layerProfile := range profilesys.LayerProfiles() {
		root := layerProfile.Root
		entry := profileListEntry{
			Name:      layerProfile.Name,
			Source:    layerProfile.Layer,
			Root:      root,
			Exists:    true,
			IsDefault: layerProfile.Name == defaultName,
			FromEnv:   defaultFromEnv && layerProfile.Name == defaultName,
		}
		entry.Description, entry.Error = describeProfileRoot(root, true)
		addEntry(entry, true)
	}

	// 默认 profile 尚未出现在任何来源时补一行，避免"默认值指向不存在的 profile"被静默吞掉。
	if defaultName != "" {
		if _, exists := seen[defaultName]; !exists {
			root := ""
			if profiles != nil {
				root = strings.TrimSpace(profiles.ResolveRoot(defaultName))
			}
			entry := profileListEntry{
				Name:      defaultName,
				Source:    "default",
				Root:      root,
				Exists:    profileRootHasProfileYAML(root),
				IsDefault: true,
				FromEnv:   defaultFromEnv,
			}
			entry.Description, entry.Error = describeProfileRoot(root, entry.Exists)
			addEntry(entry, true)
		}
	}

	if path := strings.TrimSpace(explicitPath); path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			return result, fmt.Errorf("resolve profile path %s: %w", path, err)
		}
		name := filepath.Base(filepath.Clean(abs))
		entry := profileListEntry{
			Name:   name,
			Source: "path",
			Root:   abs,
			Exists: profileRootHasProfileYAML(abs),
		}
		entry.Description, entry.Error = describeProfileRoot(abs, entry.Exists)
		addEntry(entry, false)
	}

	_ = rootFromEnv // 环境变量来源已在 EnvVars 中标注；此处保留对称性说明
	result.Profiles = entries
	return result, nil
}

func describeProfileRoot(root string, exists bool) (string, string) {
	if strings.TrimSpace(root) == "" {
		return "", "root 未配置"
	}
	if !exists {
		return "", "profile.yaml 不存在"
	}
	spec, err := loadProfileSpecForCLI(root)
	if err != nil {
		return "", err.Error()
	}
	return strings.TrimSpace(spec.Profile.Description), ""
}

func profileListEnvVars() []string {
	vars := make([]string, 0, 2)
	for _, key := range []string{"DEFAULT_PROFILE", "PROFILES_ROOT"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			vars = append(vars, key+"="+value)
		}
	}
	return vars
}

func renderProfileListText(result profileListResult) {
	if result.DefaultProfile != "" {
		line := fmt.Sprintf("默认 profile: %s", result.DefaultProfile)
		if len(result.EnvVars) > 0 {
			line += fmt.Sprintf("（env: %s）", strings.Join(result.EnvVars, ", "))
		}
		fmt.Fprintln(os.Stdout, line)
	}
	if result.DefaultRoot != "" {
		fmt.Fprintf(os.Stdout, "profiles root: %s\n", result.DefaultRoot)
	}
	if len(result.Profiles) == 0 {
		fmt.Fprintln(os.Stdout, "未发现任何 profile。可用 `aicli profile create <name> --template coding` 生成一个。")
		return
	}

	fmt.Fprintf(os.Stdout, "\n%-20s %-8s %-8s %-7s %s\n", "NAME", "SOURCE", "DEFAULT", "EXISTS", "ROOT")
	for _, entry := range result.Profiles {
		defaultMark := ""
		if entry.IsDefault {
			defaultMark = "yes"
		}
		exists := "no"
		if entry.Exists {
			exists = "yes"
		}
		fmt.Fprintf(os.Stdout, "%-20s %-8s %-8s %-7s %s\n", entry.Name, entry.Source, defaultMark, exists, entry.Root)
		if entry.Description != "" {
			fmt.Fprintf(os.Stdout, "%-20s %s\n", "", entry.Description)
		}
		if entry.Error != "" {
			fmt.Fprintf(os.Stdout, "%-20s ! %s\n", "", entry.Error)
		}
	}
}
