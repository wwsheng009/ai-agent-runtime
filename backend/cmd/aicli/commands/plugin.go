package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/internal/plugins"
)

type pluginInstallOptions struct {
	Source     string
	Name       string
	TargetRoot string
	DryRun     bool
	Force      bool
	Trust      bool
}

type pluginListResult struct {
	Plugins []pluginListEntry `json:"plugins"`
	Count   int               `json:"count"`
	Home    string            `json:"plugins_home"`
	Message string            `json:"message,omitempty"`
}

type pluginListEntry struct {
	Name        string `json:"name"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
	Trust       string `json:"trust"`
	Enabled     bool   `json:"enabled"`
	Active      bool   `json:"active"`
	Skills      int    `json:"skill_dirs"`
	Agents      int    `json:"agent_dirs"`
	Hooks       int    `json:"hooks"`
	HasMCP      bool   `json:"has_mcp"`
}

type pluginTrustResult struct {
	Name    string `json:"name"`
	Trust   string `json:"trust"`
	Enabled bool   `json:"enabled"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

type pluginEnableResult struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Trust   string `json:"trust"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// NewPluginCommand creates local plugin install/trust/list commands (no marketplace).
func NewPluginCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "plugin",
		Aliases: []string{"plugins"},
		Short:   "管理本地 plugin 包（安装/信任/启用）",
		Long: `本地 plugin 打包与信任管理。

plugin 是带 plugin.yaml 的目录，可贡献 skills/、agents/、hooks、mcp。
默认安装到 ~/.aicli/plugins（或 $AICLI_HOME/plugins）。
新安装默认 untrusted：不会向 runtime 贡献任何内容，直到 aicli plugin trust <name>。
不做 marketplace。

安装与信任流程见 docs/aicli/install.md；
skills 暴露与路由见 docs/skill_runtime/aicli_skills_usage.md；
agent 定义层见 docs/aicli/agents.md。`,
	}
	cmd.AddCommand(newPluginInstallCommand())
	cmd.AddCommand(newPluginListCommand())
	cmd.AddCommand(newPluginTrustCommand())
	cmd.AddCommand(newPluginUntrustCommand())
	cmd.AddCommand(newPluginEnableCommand())
	cmd.AddCommand(newPluginDisableCommand())
	return cmd
}

func newPluginInstallCommand() *cobra.Command {
	opts := pluginInstallOptions{}
	cmd := &cobra.Command{
		Use:   "install <source-dir>",
		Short: "安装本地 plugin 目录到 plugins home",
		Long: `将包含 plugin.yaml 的目录复制到本地 plugins home。

默认不信任（untrusted）。使用 --trust 可在安装时直接标记 trusted。
已安装同名 plugin 需 --force 覆盖。`,
		Example: `  aicli plugin install ./my-plugin
  aicli plugin install ./my-plugin --trust
  aicli plugin install ./my-plugin --name demo --force
  aicli plugin install ./my-plugin --target-dir %USERPROFILE%\.aicli\plugins --dry-run --output json`,
		Args: cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			opts.Source = args[0]
			handlePluginInstallCommand(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.Name, "name", "", "安装目录名（默认使用 manifest name）")
	cmd.Flags().StringVar(&opts.TargetRoot, "target-dir", "", "plugins home 根目录（默认 ~/.aicli/plugins）")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只显示安装计划，不写入")
	cmd.Flags().BoolVarP(&opts.Force, "force", "f", false, "目标已存在时覆盖")
	cmd.Flags().BoolVar(&opts.Trust, "trust", false, "安装后标记为 trusted（默认可贡献）")
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func newPluginListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出已发现的本地 plugin",
		Example: `  aicli plugin list
  aicli plugin list --output json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			handlePluginListCommand(cmd)
		},
	}
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func newPluginTrustCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "trust <name>",
		Short: "将 plugin 标记为 trusted（允许 runtime 贡献）",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			handlePluginTrustCommand(cmd, args[0], true)
		},
	}
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func newPluginUntrustCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "untrust <name>",
		Short: "撤销 plugin 信任（停止 runtime 贡献）",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			handlePluginTrustCommand(cmd, args[0], false)
		},
	}
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func newPluginEnableCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "enable <name>",
		Short: "启用已信任的 plugin",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			handlePluginEnableCommand(cmd, args[0], true)
		},
	}
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func newPluginDisableCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disable <name>",
		Short: "禁用 plugin（即使 trusted 也不贡献）",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			handlePluginEnableCommand(cmd, args[0], false)
		},
	}
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
	return cmd
}

func handlePluginInstallCommand(cmd *cobra.Command, opts pluginInstallOptions) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("plugin install", "json", err, nil)
	}
	executeCommand("plugin install", outputOptions, func() (plugins.InstallResult, map[string]interface{}, error) {
		return runPluginInstallCommand(opts)
	}, renderPluginInstallResult)
}

func handlePluginListCommand(cmd *cobra.Command) {
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError("plugin list", "json", err, nil)
	}
	executeCommand("plugin list", outputOptions, func() (pluginListResult, map[string]interface{}, error) {
		return runPluginListCommand()
	}, renderPluginListResult)
}

func handlePluginTrustCommand(cmd *cobra.Command, name string, trusted bool) {
	commandName := "plugin trust"
	if !trusted {
		commandName = "plugin untrust"
	}
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError(commandName, "json", err, nil)
	}
	executeCommand(commandName, outputOptions, func() (pluginTrustResult, map[string]interface{}, error) {
		return runPluginTrustCommand(name, trusted)
	}, renderPluginTrustResult)
}

func handlePluginEnableCommand(cmd *cobra.Command, name string, enabled bool) {
	commandName := "plugin enable"
	if !enabled {
		commandName = "plugin disable"
	}
	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		exitCommandError(commandName, "json", err, nil)
	}
	executeCommand(commandName, outputOptions, func() (pluginEnableResult, map[string]interface{}, error) {
		return runPluginEnableCommand(name, enabled)
	}, renderPluginEnableResult)
}

func runPluginInstallCommand(opts pluginInstallOptions) (plugins.InstallResult, map[string]interface{}, error) {
	source := strings.TrimSpace(opts.Source)
	if source == "" {
		return plugins.InstallResult{}, nil, fmt.Errorf("plugin source directory is required")
	}
	if abs, err := filepath.Abs(source); err == nil {
		source = abs
	}
	info, err := os.Stat(source)
	if err != nil {
		return plugins.InstallResult{}, map[string]interface{}{"source": source}, fmt.Errorf("source not found: %w", err)
	}
	if !info.IsDir() {
		return plugins.InstallResult{}, map[string]interface{}{"source": source}, fmt.Errorf("source is not a directory: %s", source)
	}

	targetRoot := strings.TrimSpace(opts.TargetRoot)
	if targetRoot == "" {
		targetRoot = defaultPluginCLIHome()
	}
	store := defaultPluginStateStore(targetRoot)
	trust := plugins.TrustUntrusted
	if opts.Trust {
		trust = plugins.TrustTrusted
	}

	result, err := plugins.Install(plugins.InstallOptions{
		Source:     source,
		TargetRoot: targetRoot,
		Name:       strings.TrimSpace(opts.Name),
		Force:      opts.Force,
		DryRun:     opts.DryRun,
		Trust:      trust,
		State:      store,
	})
	details := map[string]interface{}{
		"source":      result.Source,
		"target_dir":  result.TargetDir,
		"target_root": result.TargetRoot,
		"name":        result.Name,
	}
	if err != nil {
		return result, details, err
	}
	ClearPluginCatalogCache()
	return result, details, nil
}

func runPluginListCommand() (pluginListResult, map[string]interface{}, error) {
	home := defaultPluginCLIHome()
	result := pluginListResult{Home: home}
	store := defaultPluginStateStore(home)
	catalog, err := plugins.Discover(plugins.DiscoverOptions{
		State: store,
	})
	if err != nil {
		return result, map[string]interface{}{"plugins_home": home}, err
	}
	for _, pkg := range catalog.List() {
		if pkg == nil {
			continue
		}
		entry := pluginListEntry{
			Name:        pkg.Manifest.Name,
			Version:     pkg.Manifest.Version,
			Description: pkg.Manifest.Description,
			Path:        pkg.Root,
			Trust:       string(pkg.Trust),
			Enabled:     pkg.Enabled,
			Active:      pkg.IsActive(),
			Skills:      len(pkg.SkillDirs),
			Agents:      len(pkg.AgentDirs),
			Hooks:       len(pkg.Hooks),
			HasMCP:      pkg.MCP != nil && len(pkg.MCP.MCPServers) > 0,
		}
		result.Plugins = append(result.Plugins, entry)
	}
	result.Count = len(result.Plugins)
	if result.Count == 0 {
		result.Message = "no plugins discovered"
	}
	return result, map[string]interface{}{"plugins_home": home}, nil
}

func runPluginTrustCommand(name string, trusted bool) (pluginTrustResult, map[string]interface{}, error) {
	name = strings.TrimSpace(name)
	result := pluginTrustResult{Name: name}
	if name == "" {
		return result, nil, fmt.Errorf("plugin name is required")
	}
	store := defaultPluginStateStore("")
	path := resolvePluginPathForState(name, store)
	trust := plugins.TrustUntrusted
	if trusted {
		trust = plugins.TrustTrusted
	}
	st, err := store.SetTrust(name, trust, path)
	if err != nil {
		return result, map[string]interface{}{"name": name}, err
	}
	result.Name = st.ID
	result.Trust = string(st.Trust)
	result.Enabled = st.Enabled
	result.Path = st.Path
	if trusted {
		result.Message = "plugin trusted"
	} else {
		result.Message = "plugin untrusted"
	}
	ClearPluginCatalogCache()
	return result, map[string]interface{}{"name": result.Name}, nil
}

func runPluginEnableCommand(name string, enabled bool) (pluginEnableResult, map[string]interface{}, error) {
	name = strings.TrimSpace(name)
	result := pluginEnableResult{Name: name, Enabled: enabled}
	if name == "" {
		return result, nil, fmt.Errorf("plugin name is required")
	}
	store := defaultPluginStateStore("")
	path := resolvePluginPathForState(name, store)
	st, err := store.SetEnabled(name, enabled, path)
	if err != nil {
		return result, map[string]interface{}{"name": name}, err
	}
	result.Name = st.ID
	result.Enabled = st.Enabled
	result.Trust = string(st.Trust)
	result.Path = st.Path
	if enabled {
		result.Message = "plugin enabled"
	} else {
		result.Message = "plugin disabled"
	}
	ClearPluginCatalogCache()
	return result, map[string]interface{}{"name": result.Name}, nil
}

func resolvePluginPathForState(name string, store *plugins.StateStore) string {
	if st, ok, err := store.Get(name); err == nil && ok && strings.TrimSpace(st.Path) != "" {
		return st.Path
	}
	catalog, err := plugins.Discover(plugins.DiscoverOptions{State: store})
	if err != nil || catalog == nil {
		return ""
	}
	if pkg, ok := catalog.Get(name); ok && pkg != nil {
		return pkg.Root
	}
	// Fall back to plugins home/<name> if present.
	candidate := filepath.Join(defaultPluginCLIHome(), name)
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		return candidate
	}
	return ""
}

func renderPluginInstallResult(result plugins.InstallResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("plugin install", outputOptions.Envelope, result)
		return
	}
	switch {
	case result.DryRun:
		fmt.Printf("计划安装 plugin: %s\n", result.Name)
	case result.Overwritten:
		fmt.Printf("已覆盖安装 plugin: %s\n", result.Name)
	default:
		fmt.Printf("已安装 plugin: %s\n", result.Name)
	}
	fmt.Printf("Source:      %s\n", result.Source)
	fmt.Printf("Target Root: %s\n", result.TargetRoot)
	fmt.Printf("Target Dir:  %s\n", result.TargetDir)
	fmt.Printf("Trust:       %s\n", result.Trust)
	fmt.Printf("Files:       %d\n", result.FileCount)
	fmt.Printf("Dirs:        %d\n", result.DirCount)
	if result.Message != "" {
		fmt.Printf("Message:     %s\n", result.Message)
	}
	if result.Trust == plugins.TrustUntrusted && result.Installed {
		fmt.Println("提示: plugin 默认 untrusted，运行 `aicli plugin trust " + result.Name + "` 后才会贡献 skills/hooks/agents。")
	}
}

func renderPluginListResult(result pluginListResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("plugin list", outputOptions.Envelope, result)
		return
	}
	fmt.Printf("Plugins home: %s\n", result.Home)
	if result.Count == 0 {
		fmt.Println("No plugins discovered.")
		return
	}
	fmt.Printf("Count: %d\n\n", result.Count)
	for _, entry := range result.Plugins {
		active := "inactive"
		if entry.Active {
			active = "active"
		}
		fmt.Printf("- %s  trust=%s enabled=%v %s\n", entry.Name, entry.Trust, entry.Enabled, active)
		if entry.Version != "" {
			fmt.Printf("    version: %s\n", entry.Version)
		}
		if entry.Description != "" {
			fmt.Printf("    desc: %s\n", entry.Description)
		}
		fmt.Printf("    path: %s\n", entry.Path)
		fmt.Printf("    contributes: skills=%d agents=%d hooks=%d mcp=%v\n", entry.Skills, entry.Agents, entry.Hooks, entry.HasMCP)
	}
}

func renderPluginTrustResult(result pluginTrustResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("plugin trust", outputOptions.Envelope, result)
		return
	}
	fmt.Printf("%s\n", result.Message)
	fmt.Printf("Name:    %s\n", result.Name)
	fmt.Printf("Trust:   %s\n", result.Trust)
	fmt.Printf("Enabled: %v\n", result.Enabled)
	if result.Path != "" {
		fmt.Printf("Path:    %s\n", result.Path)
	}
}

func renderPluginEnableResult(result pluginEnableResult, outputOptions structuredOutputOptions) {
	if isJSONOutputFormat(outputOptions.Format) {
		printCommandJSONOutput("plugin enable", outputOptions.Envelope, result)
		return
	}
	fmt.Printf("%s\n", result.Message)
	fmt.Printf("Name:    %s\n", result.Name)
	fmt.Printf("Enabled: %v\n", result.Enabled)
	fmt.Printf("Trust:   %s\n", result.Trust)
	if result.Path != "" {
		fmt.Printf("Path:    %s\n", result.Path)
	}
}
