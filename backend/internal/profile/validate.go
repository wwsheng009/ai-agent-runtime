package profile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	// ErrProfileNotFound means profile.yaml was not found.
	ErrProfileNotFound = errors.New("profile not found")
	// ErrAgentUnresolved means no agent could be selected.
	ErrAgentUnresolved = errors.New("profile agent could not be resolved")
	// ErrAgentNotFound means the requested/default agent is not declared or present.
	ErrAgentNotFound = errors.New("profile agent not found")
	// ErrInvalidProfileSpec means a profile declaration is malformed or cannot
	// be interpreted (e.g. unknown prompts.mode). Callers must surface this
	// instead of silently falling back to a different behavior.
	ErrInvalidProfileSpec = errors.New("invalid profile spec")
)

// ProfileSpecIssueSeverity classifies a ValidateProfileSpec finding.
type ProfileSpecIssueSeverity string

const (
	// ProfileSpecIssueError marks a declaration that cannot be honored.
	ProfileSpecIssueError ProfileSpecIssueSeverity = "error"
	// ProfileSpecIssueWarning marks a declaration that is honored but
	// redundant or conflicting under the narrowing rules (deny/exclude wins).
	ProfileSpecIssueWarning ProfileSpecIssueSeverity = "warning"
)

// ProfileSpecIssue is one validation finding for profile.yaml.
type ProfileSpecIssue struct {
	Severity ProfileSpecIssueSeverity `json:"severity"`
	Path     string                   `json:"path"`
	Message  string                   `json:"message"`
}

// ValidateProfileSpec checks the Batch 1 selection/composition declarations.
// It returns issues (errors and warnings) without mutating the spec, so callers
// such as `profile validate` can render them; an empty slice means "valid".
func ValidateProfileSpec(spec *ProfileSpec) []ProfileSpecIssue {
	if spec == nil {
		return nil
	}
	issues := make([]ProfileSpecIssue, 0, 4)
	appendIssue := func(severity ProfileSpecIssueSeverity, path string, format string, args ...interface{}) {
		issues = append(issues, ProfileSpecIssue{
			Severity: severity,
			Path:     path,
			Message:  fmt.Sprintf(format, args...),
		})
	}

	if _, ok := NormalizePromptMode(spec.Prompts.Mode); !ok {
		appendIssue(ProfileSpecIssueError, "prompts.mode",
			"unknown prompt mode %q (allowed: %s, %s)", spec.Prompts.Mode, PromptModeReplace, PromptModeAppend)
	}

	validateSelectionLists := func(path string, allow []string, deny []string) {
		seenAllow := make(map[string]struct{}, len(allow))
		for _, name := range allow {
			trimmed := strings.TrimSpace(name)
			if trimmed == "" {
				appendIssue(ProfileSpecIssueError, path, "blank name is not allowed")
				continue
			}
			key := strings.ToLower(trimmed)
			if _, dup := seenAllow[key]; dup {
				appendIssue(ProfileSpecIssueWarning, path, "duplicate name %q", trimmed)
			}
			seenAllow[key] = struct{}{}
		}
		seenDeny := make(map[string]struct{}, len(deny))
		for _, name := range deny {
			trimmed := strings.TrimSpace(name)
			if trimmed == "" {
				appendIssue(ProfileSpecIssueError, path, "blank name is not allowed")
				continue
			}
			key := strings.ToLower(trimmed)
			if _, dup := seenDeny[key]; dup {
				appendIssue(ProfileSpecIssueWarning, path, "duplicate name %q", trimmed)
			}
			seenDeny[key] = struct{}{}
			if _, conflict := seenAllow[key]; conflict {
				appendIssue(ProfileSpecIssueWarning, path,
					"%q is declared in both allow and deny lists; deny wins", trimmed)
			}
		}
	}
	validateSelectionLists("skills.allowlist/denylist", spec.Skills.Allowlist, spec.Skills.Denylist)
	validateSelectionLists("mcp.use_servers/exclude_servers", spec.MCP.UseServers, spec.MCP.ExcludeServers)

	// Q11/V12：`mcp.merge_strategy` 已从 spec 移除（零生产消费点）。MCPSpec 的
	// inline Extras 会吞掉未知键（前向兼容），因此"删掉类型字段"本身不足以报错；
	// 这里显式拒绝该键，避免用户继续配一个不生效的开关（R9 dormant 陷阱）。
	if _, legacy := spec.MCP.Extras["merge_strategy"]; legacy {
		appendIssue(ProfileSpecIssueError, "mcp.merge_strategy",
			"该字段已移除（无生产消费点）；请改用 mcp.use_servers / mcp.exclude_servers 选择 MCP 服务器")
	}

	// D14：覆盖键白名单与运行时解析共用同一校验器（双执行）。
	issues = append(issues, ValidateOverrides(spec.Runtime.Overrides)...)

	return issues
}

// validateProfileSpecForResolve 把 ValidateProfileSpec 的 error 级问题折叠成
// 单个错误，供解析期执行同一份校验（"解析不了就报错"，绝不静默降级）。
func validateProfileSpecForResolve(spec *ProfileSpec) error {
	issues := ValidateProfileSpec(spec)
	messages := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.Severity != ProfileSpecIssueError {
			continue
		}
		messages = append(messages, fmt.Sprintf("%s: %s", issue.Path, issue.Message))
	}
	if len(messages) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInvalidProfileSpec, strings.Join(messages, "; "))
}

func normalizeRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", fmt.Errorf("profile root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve profile root %s: %w", root, err)
	}
	return abs, nil
}

func resolveAgentID(requested string, spec *ProfileSpec, rootPaths Paths) (string, error) {
	if trimmed := strings.TrimSpace(requested); trimmed != "" {
		return trimmed, nil
	}
	if spec != nil {
		if trimmed := strings.TrimSpace(spec.Profile.DefaultAgent); trimmed != "" {
			return trimmed, nil
		}
	}
	candidates := detectedAgentIDs(spec, rootPaths)
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return "", fmt.Errorf("%w: explicit agent or profile.default_agent is required", ErrAgentUnresolved)
}

func detectedAgentIDs(spec *ProfileSpec, rootPaths Paths) []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if spec != nil {
		for id := range spec.Agents {
			add(id)
		}
	}
	entries, err := os.ReadDir(rootPaths.AgentsDir)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				add(entry.Name())
			}
		}
	}
	sort.Strings(ids)
	return ids
}

func agentDeclaredOrPresent(id string, spec *ProfileSpec, paths AgentPaths) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	if spec != nil {
		if _, ok := spec.Agent(id); ok {
			return true
		}
	}
	if dirExists(paths.Dir) {
		return true
	}
	return false
}
