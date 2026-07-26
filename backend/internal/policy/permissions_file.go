package policy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Project permissions file names under <project>/.aicli/.
const (
	DefaultPermissionsFileName = "permissions.yaml"
	permissionsFileNameYML     = "permissions.yml"
)

// PermissionsFile is the on-disk schema for project/global permission rules.
//
// Example (.aicli/permissions.yaml):
//
//	version: 1
//	deny_tools: [shell, aicli_exec]
//	allow_tools: []          # optional hard allowlist (empty = no allowlist)
//	rules:
//	  - name: deny-network
//	    tools: [web_search, fetch, download]
//	    decision: deny
//	    reason: project_blocks_network
//	  - name: ask-writes
//	    tools: [write, edit, apply_patch]
//	    decision: ask
//	    reason: review_writes
//	  - name: allow-readonly
//	    tools: [view, grep, glob, ls]
//	    decision: allow
type PermissionsFile struct {
	Version    int                    `yaml:"version,omitempty" json:"version,omitempty"`
	DenyTools  []string               `yaml:"deny_tools,omitempty" json:"deny_tools,omitempty"`
	AllowTools []string               `yaml:"allow_tools,omitempty" json:"allow_tools,omitempty"`
	Rules      []PermissionsFileRule  `yaml:"rules,omitempty" json:"rules,omitempty"`
	SourcePath string                 `yaml:"-" json:"source_path,omitempty"`
}

// PermissionsFileRule is one static rule entry in permissions.yaml.
type PermissionsFileRule struct {
	Name         string   `yaml:"name,omitempty" json:"name,omitempty"`
	Tools        []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Decision     string   `yaml:"decision" json:"decision"`
	Reason       string   `yaml:"reason,omitempty" json:"reason,omitempty"`
}

// PermissionsOverlay is the merged product surface from project file + CLI flags.
type PermissionsOverlay struct {
	// Rules feed Engine.Rules (first match wins; CLI deny first when merged).
	Rules []Rule
	// DenyTools / AllowTools feed ToolExecutionPolicy hard gates.
	DenyTools  []string
	AllowTools []string
	// SourcePath is the loaded project file path when present.
	SourcePath string
	// Sources lists human-readable origins (for /debug).
	Sources []string
}

// ResolveProjectPermissionsPath returns the first existing permissions file under projectRoot.
// Preference: .aicli/permissions.yaml → .aicli/permissions.yml.
// Missing project root or missing files return ("", nil).
func ResolveProjectPermissionsPath(projectRoot string) (string, error) {
	projectRoot = strings.TrimSpace(projectRoot)
	if projectRoot == "" {
		return "", nil
	}
	candidates := []string{
		filepath.Join(projectRoot, ".aicli", DefaultPermissionsFileName),
		filepath.Join(projectRoot, ".aicli", permissionsFileNameYML),
	}
	for _, path := range candidates {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("stat permissions file %s: %w", path, err)
		}
		if info.IsDir() {
			continue
		}
		return path, nil
	}
	return "", nil
}

// LoadPermissionsFile parses a permissions YAML file.
// Empty path or missing file returns (nil, nil).
func LoadPermissionsFile(path string) (*PermissionsFile, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read permissions file %s: %w", path, err)
	}
	file, err := ParsePermissionsFile(data)
	if err != nil {
		return nil, fmt.Errorf("parse permissions file %s: %w", path, err)
	}
	if file != nil {
		file.SourcePath = path
	}
	return file, nil
}

// LoadProjectPermissions loads .aicli/permissions.yaml from projectRoot when present.
func LoadProjectPermissions(projectRoot string) (*PermissionsFile, error) {
	path, err := ResolveProjectPermissionsPath(projectRoot)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	return LoadPermissionsFile(path)
}

// ParsePermissionsFile unmarshals permissions YAML bytes.
func ParsePermissionsFile(data []byte) (*PermissionsFile, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return &PermissionsFile{}, nil
	}
	var file PermissionsFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, err
	}
	if err := file.Validate(); err != nil {
		return nil, err
	}
	return &file, nil
}

// Validate checks decision values and normalizes empty lists.
func (f *PermissionsFile) Validate() error {
	if f == nil {
		return nil
	}
	for i, rule := range f.Rules {
		decision, err := parsePermissionsDecision(rule.Decision)
		if err != nil {
			name := strings.TrimSpace(rule.Name)
			if name == "" {
				name = fmt.Sprintf("rules[%d]", i)
			}
			return fmt.Errorf("%s: %w", name, err)
		}
		f.Rules[i].Decision = string(decision)
	}
	return nil
}

// ToRules converts file rules into engine Rules (deny/ask/allow).
// Tool denylist entries are also emitted as hard deny rules so they participate
// in the rules stage even when ToolExecutionPolicy is not applied.
func (f *PermissionsFile) ToRules(sourceLabel string) []Rule {
	if f == nil {
		return nil
	}
	sourceLabel = strings.TrimSpace(sourceLabel)
	if sourceLabel == "" {
		sourceLabel = "project_permissions"
	}
	out := make([]Rule, 0, len(f.DenyTools)+len(f.Rules))
	for _, tool := range normalizeToolNameList(f.DenyTools) {
		out = append(out, Rule{
			Name:     sourceLabel + ":deny_tools:" + tool,
			Tools:    []string{tool},
			Decision: DecisionDeny,
			Reason:   "project_deny_tool:" + tool,
		})
	}
	for i, entry := range f.Rules {
		decision, err := parsePermissionsDecision(entry.Decision)
		if err != nil {
			continue
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = fmt.Sprintf("%s:rule_%d", sourceLabel, i)
		} else {
			name = sourceLabel + ":" + name
		}
		reason := strings.TrimSpace(entry.Reason)
		if reason == "" {
			reason = string(decision)
		}
		out = append(out, Rule{
			Name:         name,
			Tools:        normalizeToolNameList(entry.Tools),
			Capabilities: parseCapabilityNames(entry.Capabilities),
			Decision:     decision,
			Reason:       reason,
		})
	}
	return out
}

// BuildPermissionsOverlay merges project file + CLI allow/deny tool lists.
// Precedence for Engine.Rules (first match wins):
//  1. CLI --deny-tool rules
//  2. project deny_tools + project rules
//  3. CLI --allow-tool rules
//
// ToolExecutionPolicy hard gates:
//   - DenyTools = union(CLI deny, project deny_tools)
//   - AllowTools = union(CLI allow, project allow_tools) when either is non-empty
func BuildPermissionsOverlay(project *PermissionsFile, cliAllowTools, cliDenyTools []string) PermissionsOverlay {
	cliAllowTools = normalizeToolNameList(cliAllowTools)
	cliDenyTools = normalizeToolNameList(cliDenyTools)

	overlay := PermissionsOverlay{}
	if project != nil {
		overlay.SourcePath = strings.TrimSpace(project.SourcePath)
		if overlay.SourcePath != "" {
			overlay.Sources = append(overlay.Sources, "project:"+overlay.SourcePath)
		} else {
			overlay.Sources = append(overlay.Sources, "project")
		}
		overlay.DenyTools = append(overlay.DenyTools, normalizeToolNameList(project.DenyTools)...)
		overlay.AllowTools = append(overlay.AllowTools, normalizeToolNameList(project.AllowTools)...)
		overlay.Rules = append(overlay.Rules, project.ToRules("project")...)
	}

	if len(cliDenyTools) > 0 {
		overlay.Sources = append(overlay.Sources, "cli:deny-tool")
		overlay.DenyTools = append(overlay.DenyTools, cliDenyTools...)
	}
	if len(cliAllowTools) > 0 {
		overlay.Sources = append(overlay.Sources, "cli:allow-tool")
		overlay.AllowTools = append(overlay.AllowTools, cliAllowTools...)
	}

	// Rebuild Rules with CLI deny first, then project (already in overlay.Rules), then CLI allow.
	projectRules := overlay.Rules
	overlay.Rules = nil
	for _, tool := range cliDenyTools {
		overlay.Rules = append(overlay.Rules, Rule{
			Name:     "cli:deny-tool:" + tool,
			Tools:    []string{tool},
			Decision: DecisionDeny,
			Reason:   "cli_deny_tool:" + tool,
		})
	}
	overlay.Rules = append(overlay.Rules, projectRules...)
	for _, tool := range cliAllowTools {
		overlay.Rules = append(overlay.Rules, Rule{
			Name:     "cli:allow-tool:" + tool,
			Tools:    []string{tool},
			Decision: DecisionAllow,
			Reason:   "cli_allow_tool:" + tool,
		})
	}

	overlay.DenyTools = uniqueToolNames(overlay.DenyTools)
	overlay.AllowTools = uniqueToolNames(overlay.AllowTools)
	return overlay
}

// ApplyPermissionsOverlayToPolicy mutates a tool execution policy with hard
// allow/deny lists from the overlay. Nil policy is left unchanged.
func ApplyPermissionsOverlayToPolicy(policy *ToolExecutionPolicy, overlay PermissionsOverlay) *ToolExecutionPolicy {
	if policy == nil {
		if len(overlay.DenyTools) == 0 && len(overlay.AllowTools) == 0 {
			return nil
		}
		policy = NewToolExecutionPolicy(nil, false)
	}
	if len(overlay.DenyTools) > 0 {
		if policy.DeniedTools == nil {
			policy.DeniedTools = map[string]bool{}
		}
		for _, name := range overlay.DenyTools {
			policy.DeniedTools[name] = true
		}
	}
	if len(overlay.AllowTools) > 0 {
		// Intersect with existing allowlist when present; otherwise enable allowlist.
		if policy.AllowlistEnabled {
			allowed := make(map[string]bool, len(overlay.AllowTools))
			for _, name := range overlay.AllowTools {
				if policy.AllowedTools[name] {
					allowed[name] = true
				}
			}
			policy.AllowedTools = allowed
		} else {
			policy.AllowlistEnabled = true
			policy.AllowedTools = buildAllowedToolsMap(overlay.AllowTools)
		}
	}
	return policy
}

// ApplyPermissionsOverlayToEngine sets Rules on the engine (prepends overlay
// rules ahead of any existing rules so product sources win over ad-hoc rules).
func ApplyPermissionsOverlayToEngine(engine *Engine, overlay PermissionsOverlay) {
	if engine == nil || len(overlay.Rules) == 0 {
		return
	}
	if len(engine.Rules) == 0 {
		engine.Rules = append([]Rule(nil), overlay.Rules...)
		return
	}
	merged := make([]Rule, 0, len(overlay.Rules)+len(engine.Rules))
	merged = append(merged, overlay.Rules...)
	merged = append(merged, engine.Rules...)
	engine.Rules = merged
}

// FormatPermissionsOverlaySummary is a compact debug line.
func FormatPermissionsOverlaySummary(overlay PermissionsOverlay) string {
	if len(overlay.Sources) == 0 && len(overlay.Rules) == 0 && len(overlay.DenyTools) == 0 && len(overlay.AllowTools) == 0 {
		return "<none>"
	}
	parts := make([]string, 0, 4)
	if len(overlay.Sources) > 0 {
		parts = append(parts, "sources="+strings.Join(overlay.Sources, ","))
	}
	if len(overlay.DenyTools) > 0 {
		parts = append(parts, "deny="+strings.Join(overlay.DenyTools, ","))
	}
	if len(overlay.AllowTools) > 0 {
		parts = append(parts, "allow="+strings.Join(overlay.AllowTools, ","))
	}
	if n := len(overlay.Rules); n > 0 {
		parts = append(parts, fmt.Sprintf("rules=%d", n))
	}
	return strings.Join(parts, " ")
}

func parsePermissionsDecision(raw string) (DecisionType, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "allow", "yes", "true":
		return DecisionAllow, nil
	case "deny", "block", "false", "no":
		return DecisionDeny, nil
	case "ask", "prompt", "approval":
		return DecisionAsk, nil
	case "":
		return "", fmt.Errorf("decision is required (allow|deny|ask)")
	default:
		return "", fmt.Errorf("unknown decision %q (want allow|deny|ask)", raw)
	}
}

func parseCapabilityNames(names []string) []Capability {
	if len(names) == 0 {
		return nil
	}
	out := make([]Capability, 0, len(names))
	seen := map[Capability]struct{}{}
	for _, name := range names {
		capName := Capability(strings.ToLower(strings.TrimSpace(name)))
		if capName == "" {
			continue
		}
		if _, ok := seen[capName]; ok {
			continue
		}
		seen[capName] = struct{}{}
		out = append(out, capName)
	}
	return out
}

func normalizeToolNameList(names []string) []string {
	if len(names) == 0 {
		return nil
	}
	out := make([]string, 0, len(names))
	seen := map[string]struct{}{}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	return out
}

func uniqueToolNames(names []string) []string {
	names = normalizeToolNameList(names)
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return names
}
