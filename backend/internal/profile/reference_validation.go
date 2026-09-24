package profile

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// 共享校验实现（实施方案 Batch 8 任务 1）：`aicli profile validate`（CLI）与
// `POST /api/runtime/profiles/{ref}/validate`（API）调用同一份逻辑，避免"CLI 说
// 合法、UI 说非法"的第二套校验口径。
//
// 检查项与顺序（与既有 CLI 行为逐条对齐）：
//  1. profile.yaml 声明（复用 ValidateProfileSpec）+ profile.name 必填 + 有 mode 无 prompt 文件
//  2. 工具名是否在工具清单中登记（未登记按 warning：可能是 MCP/动态工具）
//  3. skills 引用是否存在（allowlist 缺失=error，denylist 缺失=warning 冗余）
//  4. mcp use/exclude 引用是否存在（use 缺失=error，exclude 缺失=warning）
//  5. prompt 文件可读性与空文件检测
//
// 返回 error 的语义与 CLI 现状一致：**引用不存在**（profile.yaml/agent 缺失）或
// **声明无法解析**（ErrInvalidProfileSpec）时返回错误，由调用方决定如何呈现；
// 其余情况一律返回报告（含 issues），不做隐式降级。

// ReferenceValidation 是一次 profile 引用校验的完整报告。
type ReferenceValidation struct {
	Reference    string             `json:"reference"`
	ProfileName  string             `json:"profile_name,omitempty"`
	ProfileRoot  string             `json:"profile_root"`
	AgentID      string             `json:"agent_id,omitempty"`
	Valid        bool               `json:"valid"`
	ErrorCount   int                `json:"error_count"`
	WarningCount int                `json:"warning_count"`
	Issues       []ProfileSpecIssue `json:"issues"`
	// Spec / Resolved 供调用方复用（API 写回与影响面预览需要），不参与 JSON。
	Spec     *ProfileSpec   `json:"-"`
	Resolved *ResolvedAgent `json:"-"`
}

// ValidateProfileReference 解析 root 指向的 profile 并执行全部引用校验。
// options.Root 会被 root 覆盖（调用方无需自行拼装）。
func ValidateProfileReference(root, agent string, options ResolveOptions) (*ReferenceValidation, error) {
	options.Root = root
	options.Agent = strings.TrimSpace(agent)
	resolved, err := Resolve(options)
	if err != nil {
		return nil, err
	}

	spec, specErr := LoadProfile(resolved.ProfileRoot)
	if specErr != nil {
		// Resolve 成功但 profile.yaml 读不到：只可能是竞态（文件被删除/权限变化）。
		// 按声明级问题报告，绝不伪装成"合法"。
		return &ReferenceValidation{
			Reference:   resolved.ProfileRoot,
			ProfileRoot: resolved.ProfileRoot,
			AgentID:     resolved.AgentID,
			Valid:       false,
			ErrorCount:  1,
			Issues: []ProfileSpecIssue{{
				Severity: ProfileSpecIssueError,
				Path:     "profile.yaml",
				Message:  fmt.Sprintf("读取失败：%v", specErr),
			}},
			Resolved: resolved,
		}, nil
	}

	result := &ReferenceValidation{
		Reference:   resolved.ProfileRoot,
		ProfileName: resolved.ProfileName,
		ProfileRoot: resolved.ProfileRoot,
		AgentID:     resolved.AgentID,
		Issues:      make([]ProfileSpecIssue, 0, 4),
		Spec:        spec,
		Resolved:    resolved,
	}
	addIssue := func(severity ProfileSpecIssueSeverity, path, format string, args ...interface{}) {
		result.Issues = append(result.Issues, ProfileSpecIssue{
			Severity: severity,
			Path:     path,
			Message:  fmt.Sprintf(format, args...),
		})
	}

	// 1. 声明校验（与解析期同一校验器：双执行）。
	for _, issue := range ValidateProfileSpec(spec) {
		addIssue(issue.Severity, issue.Path, "%s", issue.Message)
	}
	if strings.TrimSpace(spec.Profile.Name) == "" {
		addIssue(ProfileSpecIssueError, "profile.name", "未声明 profile.name")
	}
	hasPromptFile := strings.TrimSpace(resolved.Prompts.System) != "" ||
		strings.TrimSpace(resolved.Prompts.Role) != "" ||
		strings.TrimSpace(resolved.Prompts.Tools) != ""
	if strings.TrimSpace(spec.Prompts.Mode) != "" && !hasPromptFile {
		addIssue(ProfileSpecIssueWarning, "prompts.mode",
			"声明了 prompts.mode=%q，但没有发现任何 prompt 文件（不会产生注入）", spec.Prompts.Mode)
	}

	// 2. 工具名登记。
	validateToolNames := func(path string, names []string) {
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" || name == "*" {
				continue
			}
			if _, ok := runtimepolicy.LookupToolTaxonomy(name); !ok {
				addIssue(ProfileSpecIssueWarning, path,
					"未在工具清单中登记：%q（若为 MCP/动态工具可忽略）", name)
			}
		}
	}
	validateToolNames("tools.allowlist", resolved.ToolPolicy.Allowlist)
	validateToolNames("tools.denylist", resolved.ToolPolicy.Denylist)

	// 3. skills 引用。
	discovered := DiscoverSkillNames(resolved.SkillDirs)
	discoveredSet := make(map[string]struct{}, len(discovered))
	for _, name := range discovered {
		discoveredSet[strings.ToLower(name)] = struct{}{}
	}
	if !resolved.Skills.Empty() && len(resolved.SkillDirs) == 0 {
		switch {
		case HasEffectiveNames(resolved.Skills.Allowlist):
			addIssue(ProfileSpecIssueWarning, "skills",
				"声明了 skills allowlist，但没有发现任何技能目录（选择不会生效）")
		case HasEffectiveNames(resolved.Skills.Denylist):
			addIssue(ProfileSpecIssueWarning, "skills.denylist",
				"声明了 skills denylist，但没有发现任何技能目录（deny 冗余）")
		}
	}
	for _, name := range resolved.Skills.Allowlist {
		name = strings.TrimSpace(name)
		if name == "" || name == "*" {
			continue
		}
		if _, ok := discoveredSet[strings.ToLower(name)]; !ok {
			addIssue(ProfileSpecIssueError, "skills.allowlist",
				"技能不存在：%q（已发现：%s）", name, FormatNameList(discovered))
		}
	}
	for _, name := range resolved.Skills.Denylist {
		name = strings.TrimSpace(name)
		if name == "" || name == "*" {
			continue
		}
		if _, ok := discoveredSet[strings.ToLower(name)]; !ok {
			addIssue(ProfileSpecIssueWarning, "skills.denylist",
				"技能不存在，deny 冗余：%q", name)
		}
	}

	// 4. mcp 引用。
	if !resolved.MCPSelection.Empty() {
		if strings.TrimSpace(resolved.MCPConfig) == "" {
			switch {
			case HasEffectiveNames(resolved.MCPSelection.UseServers):
				addIssue(ProfileSpecIssueError, "mcp.use_servers",
					"声明了 use_servers，但没有发现 mcp.yaml")
			case HasEffectiveNames(resolved.MCPSelection.ExcludeServers):
				addIssue(ProfileSpecIssueWarning, "mcp.exclude_servers",
					"声明了 exclude_servers，但没有发现 mcp.yaml（exclude 冗余）")
			}
		} else {
			servers, err := LoadMCPServerNames(resolved.MCPConfig)
			if err != nil {
				addIssue(ProfileSpecIssueError, "mcp", "mcp.yaml 解析失败：%v", err)
			} else {
				serverSet := make(map[string]struct{}, len(servers))
				for _, name := range servers {
					serverSet[strings.ToLower(name)] = struct{}{}
				}
				if len(servers) == 0 {
					addIssue(ProfileSpecIssueWarning, "mcp", "mcp.yaml 中没有配置任何 server")
				}
				for _, name := range resolved.MCPSelection.UseServers {
					name = strings.TrimSpace(name)
					if name == "" || name == "*" {
						continue
					}
					if _, ok := serverSet[strings.ToLower(name)]; !ok {
						addIssue(ProfileSpecIssueError, "mcp.use_servers",
							"MCP server 不存在：%q（已配置：%s）", name, FormatNameList(servers))
					}
				}
				for _, name := range resolved.MCPSelection.ExcludeServers {
					name = strings.TrimSpace(name)
					if name == "" || name == "*" {
						continue
					}
					if _, ok := serverSet[strings.ToLower(name)]; !ok {
						addIssue(ProfileSpecIssueWarning, "mcp.exclude_servers",
							"MCP server 不存在，exclude 冗余：%q", name)
					}
				}
			}
		}
	}

	// 5. prompt 文件可读性 / 空文件。
	promptFiles := []struct {
		path  string
		label string
	}{
		{resolved.Prompts.System, "prompts.system"},
		{resolved.Prompts.Role, "prompts.role"},
		{resolved.Prompts.Tools, "prompts.tools"},
	}
	for _, file := range promptFiles {
		path := strings.TrimSpace(file.path)
		if path == "" {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			addIssue(ProfileSpecIssueError, file.label, "prompt 文件不可读：%v", err)
			continue
		}
		if strings.TrimSpace(string(raw)) == "" {
			addIssue(ProfileSpecIssueWarning, file.label,
				"prompt 文件为空，不会注入任何内容：%s", path)
		}
	}

	for _, issue := range result.Issues {
		if issue.Severity == ProfileSpecIssueError {
			result.ErrorCount++
		} else {
			result.WarningCount++
		}
	}
	result.Valid = result.ErrorCount == 0
	return result, nil
}

// DiscoverSkillNames 扫描技能目录：目录本身含 SKILL.md 时以目录名作为技能名，
// 否则以含 SKILL.md 的子目录名为技能名。返回排序去重后的名字。
func DiscoverSkillNames(dirs []string) []string {
	names := make([]string, 0, 8)
	seen := make(map[string]struct{})
	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, exists := seen[name]; exists {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		if manifest, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil && !manifest.IsDir() {
			appendName(filepath.Base(dir))
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if info, err := os.Stat(filepath.Join(dir, name, "SKILL.md")); err == nil && !info.IsDir() {
				appendName(name)
			}
		}
	}
	sort.Strings(names)
	return names
}

// LoadMCPServerNames 复用 MCP 官方 loader 读取 mcp.yaml 的 server 名（无第二套方言）。
func LoadMCPServerNames(configPath string) ([]string, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return nil, nil
	}
	loaded, err := mcpconfig.NewLoader(configPath).Load()
	if err != nil {
		return nil, fmt.Errorf("load mcp config %s: %w", configPath, err)
	}
	if loaded == nil || len(loaded.MCPServers) == 0 {
		return nil, nil
	}
	names := make([]string, 0, len(loaded.MCPServers))
	for name := range loaded.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// FormatNameList 渲染名字清单（空清单给出显式"（无）"，避免误读为"未检查"）。
func FormatNameList(names []string) string {
	if len(names) == 0 {
		return "（无）"
	}
	return strings.Join(names, ", ")
}

// HasEffectiveNames reports whether a selection list carries a concrete
// requirement, i.e. at least one non-blank, non-wildcard name. Wildcard-only
// lists are no-ops when the target set is empty, so they must not raise noise.
func HasEffectiveNames(names []string) bool {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name != "" && name != "*" {
			return true
		}
	}
	return false
}
