package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
)

// NewAgentsCommand 创建 `aicli agents` 命令组：便携 agent 定义（agentdef）
// 的只读浏览/校验与模板化创建。
//
// 命名区分：`aicli agent stdio` 是 ACP 协议宿主；`aicli agents` 管理的是
// `.agents/agents/*.md` / `~/.aicli/agents/*` / profile agents 这类"角色定义"；
// chat 内 `/agents` 管理的是运行中子代理（协作面板）。三者语义不同。
func NewAgentsCommand(getConfig func() *config.Config) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "管理便携 agent 定义（list/show/lint/new）",
		Long: `管理便携 agent 定义（CommandCode 风格的角色文件）。

子命令：
  list   按来源（builtin/user/project/profile）分组列出定义、覆盖关系与告警
  show   展示单个定义的原始声明、运行时绑定与 lint 结果
  lint   全量校验定义（工具语义、覆盖、未知推理档等），warning 存在时退出码非 0
  new    从内置模板生成定义文件（默认项目 .agents/agents/<name>.md）

发现顺序（后者覆盖前者）：builtin → ~/.aicli/agents → 项目 .agents/agents 与
.aicli/agents → profile agents/<id>/agent.yaml → --dir 追加目录。`,
	}
	cmd.AddCommand(newAgentsListCommand(getConfig))
	cmd.AddCommand(newAgentsShowCommand(getConfig))
	cmd.AddCommand(newAgentsLintCommand(getConfig))
	cmd.AddCommand(newAgentsNewCommand())
	return cmd
}

type agentsCommonFlags struct {
	projectRoot string
	profileRoot string
	dirs        []string
	jsonOut     bool
}

func addAgentsCommonFlags(cmd *cobra.Command, flags *agentsCommonFlags) {
	cmd.Flags().StringVar(&flags.projectRoot, "project-root", "", "项目根（默认当前目录）；其 .agents/agents 与 .aicli/agents 会被扫描")
	cmd.Flags().StringVar(&flags.profileRoot, "profile-root", "", "额外扫描 profile agents/<id>/agent.yaml")
	cmd.Flags().StringArrayVar(&flags.dirs, "dir", nil, "追加定义目录（可重复；优先级最高）")
	cmd.Flags().BoolVar(&flags.jsonOut, "json", false, "以 JSON 输出")
}

func (f *agentsCommonFlags) discoverOptions(getConfig func() *config.Config) agentdef.DiscoverOptions {
	opts := agentdef.DiscoverOptions{
		ProjectRoot: strings.TrimSpace(f.projectRoot),
		ExtraDirs:   append([]string(nil), f.dirs...),
	}
	profileRoot := strings.TrimSpace(f.profileRoot)
	if profileRoot == "" {
		if cfg := configOrNil(getConfig); cfg != nil && cfg.Profiles != nil {
			profileRoot = strings.TrimSpace(cfg.Profiles.Root)
		}
	}
	opts.ProfileRoot = profileRoot
	return opts
}

func configOrNil(getConfig func() *config.Config) *config.Config {
	if getConfig == nil {
		return nil
	}
	return getConfig()
}

type agentsListEntry struct {
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	Source          string   `json:"source"`
	Path            string   `json:"path,omitempty"`
	ReadOnly        bool     `json:"read_only,omitempty"`
	PermissionMode  string   `json:"permission_mode,omitempty"`
	Tools           []string `json:"tools,omitempty"`
	ToolsWildcard   bool     `json:"tools_wildcard,omitempty"`
	MaxTurns        int      `json:"max_turns,omitempty"`
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
	Background      bool     `json:"background,omitempty"`
	ShowOutput      bool     `json:"show_output,omitempty"`
	Overrides       string   `json:"overrides,omitempty"`
	Warnings        []string `json:"warnings,omitempty"`
}

var agentsSourceOrder = []agentdef.Source{
	agentdef.SourceBuiltin,
	agentdef.SourceUser,
	agentdef.SourceProject,
	agentdef.SourceProfile,
}

func buildAgentsListEntries(catalog *agentdef.Catalog) []agentsListEntry {
	if catalog == nil {
		return nil
	}
	entries := make([]agentsListEntry, 0, len(catalog.Order))
	for _, def := range catalog.List() {
		if def == nil {
			continue
		}
		entry := agentsListEntry{
			Name:            def.Name,
			Description:     def.Description,
			Source:          string(def.Source),
			Path:            def.SourcePath,
			PermissionMode:  def.PermissionMode,
			Tools:           append([]string(nil), def.Tools...),
			ToolsWildcard:   def.ToolsWildcard(),
			MaxTurns:        def.MaxTurns,
			ReasoningEffort: def.ReasoningEffort,
			Background:      def.Background,
			ShowOutput:      def.ShowOutput,
			Warnings:        append([]string(nil), def.Warnings...),
		}
		if binding, err := agentdef.BuildBinding(def); err == nil && binding != nil {
			if binding.ReadOnly != nil {
				entry.ReadOnly = *binding.ReadOnly
			}
			if binding.PermissionMode != "" {
				entry.PermissionMode = string(binding.PermissionMode)
			}
		} else {
			entry.ReadOnly = def.Sandbox == "read-only" || def.Sandbox == "readonly"
		}
		if overridden, ok := catalog.Overridden[def.Name]; ok {
			entry.Overrides = string(overridden)
		}
		entries = append(entries, entry)
	}
	return entries
}

func newAgentsListCommand(getConfig func() *config.Config) *cobra.Command {
	flags := &agentsCommonFlags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "按来源分组列出 agent 定义",
		RunE: func(cmd *cobra.Command, args []string) error {
			catalog, err := agentdef.Discover(flags.discoverOptions(getConfig))
			if err != nil {
				return err
			}
			entries := buildAgentsListEntries(catalog)
			if flags.jsonOut {
				encoded, err := json.MarshalIndent(map[string]interface{}{
					"agents":     entries,
					"overridden": catalog.Overridden,
					"total":      len(entries),
				}, "", "  ")
				if err != nil {
					return err
				}
				cmd.Println(string(encoded))
				return nil
			}
			printAgentsList(cmd, entries)
			return nil
		},
	}
	addAgentsCommonFlags(cmd, flags)
	return cmd
}

func printAgentsList(cmd *cobra.Command, entries []agentsListEntry) {
	if len(entries) == 0 {
		cmd.Println("未发现 agent 定义。")
		return
	}
	bySource := make(map[string][]agentsListEntry, len(agentsSourceOrder))
	for _, entry := range entries {
		bySource[entry.Source] = append(bySource[entry.Source], entry)
	}
	total := 0
	for _, source := range agentsSourceOrder {
		group := bySource[string(source)]
		if len(group) == 0 {
			continue
		}
		sort.Slice(group, func(i, j int) bool { return group[i].Name < group[j].Name })
		cmd.Printf("%s (%d)\n", strings.ToUpper(string(source)), len(group))
		for _, entry := range group {
			suffix := ""
			if entry.ReadOnly {
				suffix += " [read-only]"
			}
			if entry.PermissionMode != "" && entry.PermissionMode != "default" {
				suffix += " [permission=" + entry.PermissionMode + "]"
			}
			line := fmt.Sprintf("  %-20s %s%s", entry.Name, entry.Description, suffix)
			cmd.Println(strings.TrimRight(line, " "))
			if entry.Path != "" {
				cmd.Printf("  %-20s %s\n", "", entry.Path)
			}
			if entry.Overrides != "" {
				cmd.Printf("  %-20s overrides %s\n", "", entry.Overrides)
			}
			for _, warning := range entry.Warnings {
				cmd.Printf("  %-20s warning: %s\n", "", warning)
			}
			total++
		}
		cmd.Println()
	}
	// Sources outside the known order (future-proofing).
	for source, group := range bySource {
		known := false
		for _, ordered := range agentsSourceOrder {
			if source == string(ordered) {
				known = true
				break
			}
		}
		if known {
			continue
		}
		for _, entry := range group {
			cmd.Printf("%s  %s\n", entry.Source, entry.Name)
			total++
		}
	}
	cmd.Printf("合计 %d 个定义。\n", total)
}

func newAgentsShowCommand(getConfig func() *config.Config) *cobra.Command {
	flags := &agentsCommonFlags{}
	cmd := &cobra.Command{
		Use:   "show <name>",
		Short: "展示单个定义的声明、绑定与 lint 结果",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := flags.discoverOptions(getConfig)
			def, err := agentdef.Resolve(args[0], opts)
			if err != nil {
				catalog, discoverErr := agentdef.Discover(opts)
				if discoverErr == nil && catalog != nil {
					return fmt.Errorf("%w（可用：%s）", err, strings.Join(catalog.Order, ", "))
				}
				return err
			}
			binding, bindingErr := agentdef.BuildBinding(def)
			issues := agentdef.LintDefinition(def)
			if flags.jsonOut {
				payload := map[string]interface{}{
					"definition": def,
					"binding":    binding,
					"lint":       issues,
				}
				if bindingErr != nil {
					payload["binding_error"] = bindingErr.Error()
				}
				encoded, err := json.MarshalIndent(payload, "", "  ")
				if err != nil {
					return err
				}
				cmd.Println(string(encoded))
				return nil
			}
			cmd.Printf("name:                  %s\n", def.Name)
			if def.Description != "" {
				cmd.Printf("description:           %s\n", def.Description)
			}
			cmd.Printf("source:                %s\n", def.Source)
			if def.SourcePath != "" {
				cmd.Printf("path:                  %s\n", def.SourcePath)
			}
			cmd.Printf("permissionMode:        %s\n", def.PermissionMode)
			cmd.Printf("sandbox:               %s\n", def.Sandbox)
			cmd.Printf("promptMode:            %s\n", def.PromptMode)
			cmd.Printf("completionRequirement: %s\n", def.CompletionRequirement)
			cmd.Printf("tools:                 %s\n", describeAgentsTools(def))
			cmd.Printf("disallowedTools:       %s\n", strings.Join(def.DisallowedTools, ", "))
			cmd.Printf("model:                 %s\n", def.Model)
			cmd.Printf("provider:              %s\n", def.Provider)
			cmd.Printf("maxTurns:              %d\n", def.MaxTurns)
			cmd.Printf("reasoningEffort:       %s\n", def.ReasoningEffort)
			cmd.Printf("background:            %t\n", def.Background)
			cmd.Printf("showOutput:            %t\n", def.ShowOutput)
			if def.Body != "" {
				cmd.Printf("body:                  %d bytes\n", len(def.Body))
			}
			if bindingErr != nil {
				cmd.Printf("binding error:         %v\n", bindingErr)
			} else if binding != nil {
				readOnly := false
				if binding.ReadOnly != nil {
					readOnly = *binding.ReadOnly
				}
				cmd.Printf("effective:             read_only=%t permission=%s tools=%s skills=%s maxTurns=%d\n",
					readOnly, binding.PermissionMode, describeAgentsTools(def), strings.Join(binding.SkillAllowlist, ", "), binding.MaxTurns)
			}
			if len(issues) > 0 {
				cmd.Println("lint:")
				for _, issue := range issues {
					cmd.Printf("  [%s] %s\n", issue.Severity, issue.Message)
				}
			}
			return nil
		},
	}
	addAgentsCommonFlags(cmd, flags)
	return cmd
}

func describeAgentsTools(def *agentdef.Definition) string {
	if def == nil || !def.HasExplicitTools() {
		return "(omitted: inherit full toolkit)"
	}
	if def.ToolsWildcard() {
		return "\"*\" (all tools, MCP included)"
	}
	return strings.Join(def.Tools, ", ")
}

func newAgentsLintCommand(getConfig func() *config.Config) *cobra.Command {
	flags := &agentsCommonFlags{}
	cmd := &cobra.Command{
		Use:   "lint",
		Short: "校验全部 agent 定义（warning 存在时退出码非 0）",
		RunE: func(cmd *cobra.Command, args []string) error {
			catalog, err := agentdef.Discover(flags.discoverOptions(getConfig))
			if err != nil {
				return err
			}
			issues := agentdef.LintCatalog(catalog)
			if flags.jsonOut {
				encoded, err := json.MarshalIndent(map[string]interface{}{
					"issues": issues,
					"total":  len(issues),
				}, "", "  ")
				if err != nil {
					return err
				}
				cmd.Println(string(encoded))
			} else if len(issues) == 0 {
				cmd.Println("OK: 未发现问题。")
			} else {
				for _, issue := range issues {
					location := issue.Name
					if issue.Path != "" {
						location = issue.Path + " (" + issue.Name + ")"
					}
					cmd.Printf("[%s] %s: %s\n", issue.Severity, location, issue.Message)
				}
			}
			warnings := 0
			for _, issue := range issues {
				if issue.Severity == "warning" {
					warnings++
				}
			}
			if warnings > 0 {
				return fmt.Errorf("agents lint: %d warning(s) found", warnings)
			}
			return nil
		},
	}
	addAgentsCommonFlags(cmd, flags)
	return cmd
}

func newAgentsNewCommand() *cobra.Command {
	var (
		scope       string
		template    string
		description string
		dir         string
		force       bool
	)
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "从模板创建 agent 定义文件",
		Long: `从内置模板创建 agent 定义文件。

scope=project（默认）写入 .agents/agents/<name>.md；scope=user 写入 ~/.aicli/agents/<name>.md；
--dir 覆盖两者，直接写入指定目录。默认不覆盖已存在的文件（--force 允许）。

模板：read-only（只读探索）、writer（可写实现）、blank（最小骨架，tools: ["*"]）。`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := normalizeAgentsCreateName(args[0])
			if name == "" {
				return fmt.Errorf("agents new: name is required")
			}
			targetDir, err := resolveAgentsCreateDir(scope, dir)
			if err != nil {
				return err
			}
			path := filepath.Join(targetDir, name+".md")
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("agents new: %s already exists (use --force to overwrite)", path)
			}
			content, err := agentsTemplateContent(template, name, description)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(targetDir, 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				return err
			}
			cmd.Printf("created %s\n", path)
			if def, parseErr := agentdef.ParseFile(path); parseErr != nil {
				return fmt.Errorf("agents new: created file failed to parse: %w", parseErr)
			} else if issues := agentdef.LintDefinition(def); len(issues) > 0 {
				for _, issue := range issues {
					cmd.Printf("[%s] %s\n", issue.Severity, issue.Message)
				}
			} else {
				cmd.Println("lint: OK")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "project", "写入层：project|user")
	cmd.Flags().StringVar(&template, "template", "read-only", "模板：read-only|writer|blank")
	cmd.Flags().StringVar(&description, "description", "", "description（父模型据此判断何时委派）")
	cmd.Flags().StringVar(&dir, "dir", "", "显式写入目录（覆盖 scope）")
	cmd.Flags().BoolVar(&force, "force", false, "覆盖已存在的文件")
	return cmd
}

func normalizeAgentsCreateName(raw string) string {
	name := strings.ToLower(strings.TrimSpace(raw))
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "_", "-")
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	return strings.Trim(name, "-")
}

func resolveAgentsCreateDir(scope, dir string) (string, error) {
	if explicit := strings.TrimSpace(dir); explicit != "" {
		return filepath.Clean(explicit), nil
	}
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", "project":
		return filepath.Join(".agents", "agents"), nil
	case "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("agents new: resolve user home: %w", err)
		}
		return filepath.Join(home, ".aicli", "agents"), nil
	default:
		return "", fmt.Errorf("agents new: invalid scope %q (want project|user)", scope)
	}
}

func agentsTemplateContent(template, name, description string) (string, error) {
	if strings.TrimSpace(description) == "" {
		description = "Describe when this agent should be delegated to."
	}
	switch strings.ToLower(strings.TrimSpace(template)) {
	case "", "read-only", "readonly", "explore":
		return fmt.Sprintf(`---
name: %s
description: %s
tools: ["view", "grep", "glob", "ls", "shell"]
disallowedTools: ["write", "edit", "apply_patch", "append_write", "multiedit"]
permissionMode: plan
sandbox: read-only
---

You are a read-only investigator. Prefer view/grep/glob/ls; use shell only for
read-only commands. Report findings with file:line evidence and never mutate
the workspace.
`, name, description), nil
	case "writer", "implementer":
		return fmt.Sprintf(`---
name: %s
description: %s
tools: ["view", "grep", "glob", "ls", "shell", "write", "edit", "multiedit", "apply_patch", "append_write"]
permissionMode: default
sandbox: workspace
---

You are an implementation agent. Make focused, verified changes and follow the
repository conventions. Prefer apply_patch for multi-line edits.
`, name, description), nil
	case "blank", "minimal":
		return fmt.Sprintf(`---
name: %s
description: %s
tools: ["*"]
---

Describe this agent's role and constraints here.
`, name, description), nil
	default:
		return "", fmt.Errorf("agents new: unknown template %q (want read-only|writer|blank)", template)
	}
}
