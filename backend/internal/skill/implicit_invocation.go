package skill

import (
	"path/filepath"
	"strings"
	"sync"
)

// InvocationKind 描述一次技能调用的触发方式。
//
//   - implicit：模型没有点名 skill 函数，而是直接使用既有工具取用技能资源
//     （跑 scripts/ 里的脚本、读 SKILL.md）——SK-3 判定，也是 document 模式的观测口径。
//   - explicit：模型点名了 `skill__<name>` 函数或用户 /skill 显式执行。
type InvocationKind string

const (
	InvocationKindImplicit InvocationKind = "implicit"
	InvocationKindExplicit InvocationKind = "explicit"
)

// ImplicitInvocationBasis 描述隐式调用判定的依据（诊断用，比 kind 更细）。
type ImplicitInvocationBasis string

const (
	ImplicitBasisScriptsDir ImplicitInvocationBasis = "scripts_dir" // 命令落在技能 scripts/ 目录
	ImplicitBasisDocPath    ImplicitInvocationBasis = "doc_path"    // 读取了 SKILL.md / skill.yaml
	ImplicitBasisMention    ImplicitInvocationBasis = "mention"     // 显式点名（由调用方标记）
)

// ImplicitInvocation 描述一次技能调用命中（隐式判定或显式标记）。
type ImplicitInvocation struct {
	Name  string                  `json:"name"`            // 技能名称
	Scope string                  `json:"scope"`           // repo/user/system/admin/unknown
	Path  string                  `json:"path"`            // 命中的技能目录或文档路径
	Kind  InvocationKind          `json:"kind"`            // implicit | explicit
	Basis ImplicitInvocationBasis `json:"basis,omitempty"` // scripts_dir | doc_path | mention
	Tool  string                  `json:"tool,omitempty"`  // 触发工具（run_shell_command / read_file 等）
}

// NewExplicitInvocation 构造一次显式调用标记（由点名 skill__<name> 的调用方使用）。
func NewExplicitInvocation(name, scope, path string) ImplicitInvocation {
	return ImplicitInvocation{
		Name:  strings.TrimSpace(name),
		Scope: strings.TrimSpace(scope),
		Path:  strings.TrimSpace(path),
		Kind:  InvocationKindExplicit,
		Basis: ImplicitBasisMention,
	}
}

// implicitInvocationKey 用于 turn 级去重（scope:path:name）。
func implicitInvocationKey(inv ImplicitInvocation) string {
	return inv.Scope + ":" + inv.Path + ":" + inv.Name
}

// InvocationKey 返回该次隐式调用的去重键（包外可复用）。
func InvocationKey(inv ImplicitInvocation) string {
	return implicitInvocationKey(inv)
}

// SkillInvokedEventType 是技能调用命中发布的运行时事件类型（SK-3）。
// 事件契约单一事实源：skills API 与会话宿主（chat actor 等）共用。
const SkillInvokedEventType = "skills.invoked"

// SkillInvokedEventPayload 构造 skills.invoked 的事件载荷。
// name/scope/path/kind 必有；basis/tool 仅非空时携带；
// session_id 由调用方按宿主语义补充（事件本身保持总线级）。
func SkillInvokedEventPayload(inv ImplicitInvocation) map[string]interface{} {
	payload := map[string]interface{}{
		"name":  inv.Name,
		"scope": inv.Scope,
		"path":  inv.Path,
		"kind":  string(inv.Kind),
	}
	if inv.Basis != "" {
		payload["basis"] = string(inv.Basis)
	}
	if inv.Tool != "" {
		payload["tool"] = inv.Tool
	}
	return payload
}

// ImplicitInvocationIndex 在载入期按 path 建立反向索引，用于 turn 级隐式调用判定。
// 一次构建，随 registry 失效重建（调用方负责缓存与失效）。
type ImplicitInvocationIndex struct {
	mu           sync.RWMutex
	byScriptsDir map[string]*SkillSummary // scripts 目录（clean）→ 技能摘要
	byDocPath    map[string]*SkillSummary // SKILL.md / skill.yaml 路径（clean）→ 技能摘要
}

// ImplicitInvocationIndexOptions 控制索引构建口径。
type ImplicitInvocationIndexOptions struct {
	// IncludeDocumentMode 把文档模式技能（无执行器，正文走 injection path）也纳入判定。
	// 默认 false：执行器内部工具循环只覆盖可执行技能；主循环观测需要 true，才能把
	// read_file SKILL.md / run_shell_command 落在 scripts/ 的调用归因到技能（SK-7 观测链）。
	IncludeDocumentMode bool
}

// BuildImplicitInvocationIndex 从摘要列表构建索引（默认不含文档模式技能）。
// 仅 Codex 兼容技能参与隐式调用判定；allow_implicit_invocation=false 的技能被排除。
func BuildImplicitInvocationIndex(summaries []*SkillSummary) *ImplicitInvocationIndex {
	return BuildImplicitInvocationIndexWithOptions(summaries, ImplicitInvocationIndexOptions{})
}

// BuildImplicitInvocationIndexWithOptions 从摘要列表按指定口径构建索引。
func BuildImplicitInvocationIndexWithOptions(summaries []*SkillSummary, opts ImplicitInvocationIndexOptions) *ImplicitInvocationIndex {
	idx := &ImplicitInvocationIndex{
		byScriptsDir: make(map[string]*SkillSummary),
		byDocPath:    make(map[string]*SkillSummary),
	}
	for _, summary := range summaries {
		if summary == nil {
			continue
		}
		// 仅 Codex 格式技能参与隐式调用判定。
		if !isCodexSummary(summary) {
			continue
		}
		// allow_implicit_invocation=false 显式排除。
		if !allowImplicitInvocation(summary) {
			continue
		}
		// 文档模式技能默认不参与（执行器内部无执行器可观测）；主循环观测口径下必须纳入，
		// 否则 SK-7 的观测链恰好断在"正文注入、脚本由主循环执行"这条路径上。
		if !opts.IncludeDocumentMode && summary.IsDocumentMode() {
			continue
		}

		if summary.Source != nil {
			dir := strings.TrimSpace(summary.Source.Dir)
			if dir != "" {
				scriptsDir := filepath.Clean(filepath.Join(dir, "scripts"))
				idx.byScriptsDir[scriptsDir] = summary
				// 同时索引技能根目录本身（命令 cwd 可能直接落在根目录）。
				idx.byScriptsDir[filepath.Clean(dir)] = summary
			}
			// 文档路径（SKILL.md / skill.yaml）。
			if docPath := strings.TrimSpace(summary.Source.Path); docPath != "" {
				idx.byDocPath[filepath.Clean(docPath)] = summary
			}
		}
	}
	return idx
}

// DetectImplicitInvocations 判定一次工具调用是否命中隐式调用索引。
//
// 判定口径：
//   - 类 shell 工具（run_shell_command / bash / execute_command / exec 等）：
//     命令字符串或 cwd 落在某技能的 scripts 目录（或根目录）→ scripts_dir。
//   - 类文件读取工具（read_file / file_read 等）：
//     路径匹配某技能的文档路径（SKILL.md / skill.yaml）→ doc_path。
//
// 返回的切片已按 name 稳定排序；调用方负责 turn 级去重。
func DetectImplicitInvocations(index *ImplicitInvocationIndex, toolName string, args map[string]interface{}) []ImplicitInvocation {
	if index == nil {
		return nil
	}
	if args == nil {
		args = map[string]interface{}{}
	}
	var matched []ImplicitInvocation
	switch classifyImplicitTool(toolName) {
	case implicitToolShell:
		matched = index.detectShell(args)
	case implicitToolRead:
		matched = index.detectRead(args)
	}
	if len(matched) == 0 {
		return nil
	}
	// 统一补充触发工具名（判定层不关心工具名，观测层需要）。
	for i := range matched {
		if matched[i].Tool == "" {
			matched[i].Tool = toolName
		}
	}
	return matched
}

// DedupeInvocations 对隐式调用序列做 turn 级去重（键 scope:path:name）。
func DedupeInvocations(invocations []ImplicitInvocation) []ImplicitInvocation {
	if len(invocations) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(invocations))
	result := make([]ImplicitInvocation, 0, len(invocations))
	for _, inv := range invocations {
		key := implicitInvocationKey(inv)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, inv)
	}
	return result
}

// --- 内部实现 ---

type implicitToolClass int

const (
	implicitToolUnknown implicitToolClass = iota
	implicitToolShell
	implicitToolRead
)

// implicitShellToolNames 参与 scripts_dir 判定的 shell 类工具名（小写）。
var implicitShellToolNames = map[string]struct{}{
	"run_shell_command": {},
	"bash":              {},
	"sh":                {},
	"shell":             {},
	"execute_command":   {},
	"exec":              {},
	"command":           {},
	"run_command":       {},
}

// implicitReadToolNames 参与 doc_path 判定的文件读取类工具名（小写）。
var implicitReadToolNames = map[string]struct{}{
	"read_file": {},
	"file_read": {},
	"read":      {},
	"cat":       {},
	"head":      {},
	"tail":      {},
}

func classifyImplicitTool(toolName string) implicitToolClass {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name == "" {
		return implicitToolUnknown
	}
	if _, ok := implicitShellToolNames[name]; ok {
		return implicitToolShell
	}
	if _, ok := implicitReadToolNames[name]; ok {
		return implicitToolRead
	}
	// 兼容带命名空间或后缀的工具名（例如 mcp__bash、run_shell_command_v2）。
	for key := range implicitShellToolNames {
		if strings.Contains(name, key) {
			return implicitToolShell
		}
	}
	for key := range implicitReadToolNames {
		if strings.Contains(name, key) {
			return implicitToolRead
		}
	}
	return implicitToolUnknown
}

// detectShell 判定 shell 类工具调用是否落在某技能的脚本目录。
func (idx *ImplicitInvocationIndex) detectShell(args map[string]interface{}) []ImplicitInvocation {
	command := strings.TrimSpace(toStringArg(args, "command"))
	cwd := strings.TrimSpace(toStringArg(args, "cwd", "working_directory", "workdir"))
	_ = command

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	// 收集候选路径：cwd 优先，其次从命令里抽取脚本路径。
	candidates := make([]string, 0, 2)
	if cwd != "" {
		candidates = append(candidates, filepath.Clean(cwd))
	}
	for _, part := range extractScriptCandidates(command) {
		candidates = append(candidates, filepath.Clean(part))
	}

	matched := make([]ImplicitInvocation, 0, 2)
	seen := make(map[string]struct{}, 2)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		// 候选可能是脚本目录本身，也可能是目录内某文件：向上回溯父目录匹配。
		if summary, ok := idx.matchScriptsDir(candidate); ok && summary != nil {
			// Path 用技能锚点（根目录）而非瞬时脚本文件：同一技能跑多个脚本
			// 仍归并为一条 turn 级命中（去重键 scope:path:name）。
			anchor := candidate
			if summary.Source != nil && strings.TrimSpace(summary.Source.Dir) != "" {
				anchor = filepath.Clean(strings.TrimSpace(summary.Source.Dir))
			}
			inv := ImplicitInvocation{
				Name:  summary.Name,
				Scope: codexSummaryScope(summary),
				Path:  anchor,
				Kind:  InvocationKindImplicit,
				Basis: ImplicitBasisScriptsDir,
			}
			key := implicitInvocationKey(inv)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			matched = append(matched, inv)
		}
	}
	return matched
}

// matchScriptsDir 在索引中查找覆盖 path 的技能脚本目录（含父目录回溯）。
func (idx *ImplicitInvocationIndex) matchScriptsDir(path string) (*SkillSummary, bool) {
	if path == "" {
		return nil, false
	}
	for current := filepath.Clean(path); current != "" && current != "." && current != "/"; {
		if summary, ok := idx.byScriptsDir[current]; ok {
			return summary, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return nil, false
}

// detectRead 判定文件读取类工具是否命中某技能的文档路径。
func (idx *ImplicitInvocationIndex) detectRead(args map[string]interface{}) []ImplicitInvocation {
	path := strings.TrimSpace(toStringArg(args, "path", "file_path", "filename", "file"))
	if path == "" {
		return nil
	}
	cleanPath := filepath.Clean(path)

	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if summary, ok := idx.byDocPath[cleanPath]; ok && summary != nil {
		return []ImplicitInvocation{{
			Name:  summary.Name,
			Scope: codexSummaryScope(summary),
			Path:  cleanPath,
			Kind:  InvocationKindImplicit,
			Basis: ImplicitBasisDocPath,
		}}
	}
	return nil
}

// extractScriptCandidates 从 shell 命令字符串里抽取可能的脚本路径候选。
// 轻量启发式：按空白切分，保留看起来像路径的片段。
func extractScriptCandidates(command string) []string {
	if command == "" {
		return nil
	}
	parts := strings.Fields(command)
	candidates := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		// 跳过常见选项与重定向。
		if strings.HasPrefix(trimmed, "-") || strings.HasPrefix(trimmed, ">") || strings.HasPrefix(trimmed, "<") {
			continue
		}
		// 仅保留含路径分隔符或脚本扩展名的片段。
		if strings.ContainsAny(trimmed, `/\`) || strings.HasSuffix(trimmed, ".sh") ||
			strings.HasSuffix(trimmed, ".py") || strings.HasSuffix(trimmed, ".js") ||
			strings.HasSuffix(trimmed, ".bash") {
			candidates = append(candidates, trimmed)
		}
	}
	return candidates
}

// toStringArg 从 args 中按优先级取第一个非空字符串值。
func toStringArg(args map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if v, ok := args[key]; ok {
			switch val := v.(type) {
			case string:
				if strings.TrimSpace(val) != "" {
					return val
				}
			default:
				if v != nil {
					if s := strings.TrimSpace(strings.TrimLeft(strings.TrimRight(sprintArg(v), "{}"), "\"`")); s != "" {
						return s
					}
				}
			}
		}
	}
	return ""
}

func sprintArg(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	default:
		return strings.TrimSpace(strings.TrimLeft(strings.TrimRight(anyString(v), "{}"), "\"`"))
	}
}

func anyString(v interface{}) string {
	defer func() { _ = recover() }()
	s, _ := v.(string)
	return s
}

// isCodexSummary 报告摘要是否来自 Codex 兼容技能。
func isCodexSummary(summary *SkillSummary) bool {
	if summary == nil {
		return false
	}
	if summary.Codex != nil {
		return true
	}
	if summary.Source != nil && summary.Source.Format == SkillSourceFormatCodex {
		return true
	}
	return false
}

// allowImplicitInvocation 报告技能是否允许隐式调用（默认 true）。
func allowImplicitInvocation(summary *SkillSummary) bool {
	if summary == nil || summary.Codex == nil || summary.Codex.Policy == nil || summary.Codex.Policy.AllowImplicitInvocationValue == nil {
		return true
	}
	return *summary.Codex.Policy.AllowImplicitInvocationValue
}

// codexSummaryScope 返回技能的作用域标签（未知时回退 "unknown"）。
func codexSummaryScope(summary *SkillSummary) string {
	if summary == nil {
		return "unknown"
	}
	if summary.Codex != nil && strings.TrimSpace(summary.Codex.Scope) != "" {
		return strings.TrimSpace(summary.Codex.Scope)
	}
	if summary.Source != nil {
		switch strings.ToLower(strings.TrimSpace(summary.Source.Layer)) {
		case "system":
			return string(CodexSkillScopeSystem)
		case "user":
			return string(CodexSkillScopeUser)
		case "repo":
			return string(CodexSkillScopeRepo)
		case "admin":
			return string(CodexSkillScopeAdmin)
		}
	}
	return "unknown"
}
