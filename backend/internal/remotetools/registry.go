package remotetools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/shellrisk"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/lscore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/viewcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// executionScopeRemote labels audit records produced by this server.
const executionScopeRemote = "remote"

// prefixedTool wraps a toolkit tool and rewrites its advertised name.
//
// The prefix is what keeps a remote tool from ever colliding with a local
// builtin: tools.Manager prefers the local toolkit for view/ls/glob/grep, so an
// unprefixed remote `view` would be silently dropped from the model surface
// (review G4).
type prefixedTool struct {
	inner       toolkit.Tool
	name        string
	description string
}

func newPrefixedTool(inner toolkit.Tool, prefix string) *prefixedTool {
	name := inner.Name()
	if prefix != "" {
		name = prefix + "__" + inner.Name()
	}
	return &prefixedTool{
		inner:       inner,
		name:        name,
		description: inner.Description(),
	}
}

func (t *prefixedTool) Name() string                       { return t.name }
func (t *prefixedTool) Description() string                { return t.description }
func (t *prefixedTool) Version() string                    { return t.inner.Version() }
func (t *prefixedTool) Parameters() map[string]interface{} { return t.inner.Parameters() }
func (t *prefixedTool) CanDirectCall() bool                { return t.inner.CanDirectCall() }

func (t *prefixedTool) DefinitionMetadata() map[string]interface{} {
	if provider, ok := t.inner.(toolkit.ToolDefinitionMetadataProvider); ok {
		if metadata := provider.DefinitionMetadata(); len(metadata) > 0 {
			return metadata
		}
	}
	return nil
}

func (t *prefixedTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	return t.inner.Execute(ctx, params)
}

// buildResult bundles the registry with the stores that need lifecycle management.
type buildResult struct {
	registry  *toolkit.Registry
	jobs      *JobManager
	artifacts *ArtifactStore
}

// buildRegistry constructs the exposed tool registry for this configuration.
//
// Every tool is registered through the same prefixing wrapper so the name
// rewrite cannot be forgotten for a newly added tool.
func buildRegistry(cfg *Config, workspace *Workspace) (*buildResult, error) {
	if cfg == nil || workspace == nil {
		return nil, fmt.Errorf("config and workspace are required")
	}
	sandbox, err := NewSandbox(cfg, workspace)
	if err != nil {
		return nil, err
	}
	// The job manager is constructed only for the explicit legacy opt-in. A
	// configured jobs root without that flag must not even create directories:
	// the v1 remote surface treats background jobs as local-only.
	var jobs *JobManager
	if cfg.Jobs.LegacyRemoteJobs {
		jobs, err = NewJobManager(cfg.Jobs.Root)
		if err != nil {
			return nil, err
		}
	}
	artifacts, err := NewArtifactStore(cfg.Artifacts.Root, cfg.Artifacts.MaxBytes)
	if err != nil {
		return nil, err
	}
	registry := toolkit.NewRegistry()
	enabled := map[string]bool{}
	for _, name := range cfg.enabledTools() {
		enabled[name] = true
	}
	lsBase := remoteToolBase{cfg: cfg, workspace: workspace}
	lsSchema, err := toolkit.SchemaFromParameters(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Directory to list, relative to the remote workspace. Default: workspace root.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "Maximum entries to return. Default 200.",
			},
		},
		"additionalProperties": false,
	})
	if err != nil {
		return nil, fmt.Errorf("ls schema: %w", err)
	}
	lsTool, err := toolkit.NewRuntimeTool(lscore.NewHandler(toolcore.Definition{
		Name:                ToolLs,
		Description:         lsBase.describe("List a remote directory (one level) with entry type and size."),
		Version:             "1.0.0",
		InputSchema:         lsSchema,
		Capabilities:        []toolcore.Capability{toolcore.CapReadFS},
		DefaultOutputBudget: toolcoreoutput.ToolBudget(ToolLs),
		DirectCall:          true,
	}), func(ctx context.Context) (toolcore.Runtime, error) {
		return newRemoteRuntime(ctx, cfg, workspace, sandbox, artifacts), nil
	})
	if err != nil {
		return nil, fmt.Errorf("ls handler: %w", err)
	}
	viewBase := remoteToolBase{cfg: cfg, workspace: workspace}
	// One schema for every surface. The remote runtime has no code index, so
	// `symbol` is filtered out of the model-facing schema instead of being
	// declared and rejected later. files/compact are handler-level features and
	// therefore available remotely too.
	viewSchema, err := toolkit.SchemaFromParameters(viewcore.Schema())
	if err != nil {
		return nil, fmt.Errorf("view schema: %w", err)
	}
	viewSchema, err = toolcore.FilterSchemaParams(viewSchema, toolcore.ParamSymbol)
	if err != nil {
		return nil, fmt.Errorf("view schema capability filter: %w", err)
	}
	viewTool, err := toolkit.NewRuntimeTool(viewcore.NewHandler(toolcore.Definition{
		Name:                ToolView,
		Description:         viewBase.describe("Read a window of a remote text file with stable line numbers and truncation metadata."),
		Version:             "1.0.0",
		InputSchema:         viewSchema,
		Capabilities:        []toolcore.Capability{toolcore.CapReadFS},
		DefaultOutputBudget: toolcoreoutput.ToolBudget(ToolView),
		DirectCall:          true,
	}), func(ctx context.Context) (toolcore.Runtime, error) {
		return newRemoteRuntime(ctx, cfg, workspace, sandbox, artifacts), nil
	})
	if err != nil {
		return nil, fmt.Errorf("view handler: %w", err)
	}
	globSchema, err := toolkit.SchemaFromParameters(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Glob pattern, e.g. **/*.go. Relative to the remote workspace.",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "Base directory for the pattern. Default: workspace root.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "Maximum matches to return. Default 500.",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "Skip the first N matches, then apply limit. Use next_offset to page.",
			},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	})
	if err != nil {
		return nil, fmt.Errorf("glob schema: %w", err)
	}
	globBase := remoteToolBase{cfg: cfg, workspace: workspace}
	globTool, err := toolkit.NewRuntimeTool(searchcore.NewGlobHandler(toolcore.Definition{
		Name:                ToolGlob,
		Description:         globBase.describe("Find remote files by glob pattern; respects the workspace boundary."),
		Version:             "1.0.0",
		InputSchema:         globSchema,
		Capabilities:        []toolcore.Capability{toolcore.CapReadFS},
		DefaultOutputBudget: toolcoreoutput.ToolBudget(ToolGlob),
		DirectCall:          true,
	}), func(ctx context.Context) (toolcore.Runtime, error) {
		return newRemoteRuntime(ctx, cfg, workspace, sandbox, artifacts), nil
	})
	if err != nil {
		return nil, fmt.Errorf("glob handler: %w", err)
	}
	grepSchema, err := toolkit.SchemaFromParameters(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"pattern": map[string]interface{}{
				"type":        "string",
				"description": "Regular expression to search for (ripgrep syntax).",
			},
			"regexp": map[string]interface{}{
				"type":        "string",
				"description": "兼容 rg 的 --regexp/-e 单模式写法；等价于 pattern。",
			},
			"patterns": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "多个模式按 OR 语义匹配，等价于多次 rg -e。可传字符串数组，也可传单个字符串。",
			},
			"path": map[string]interface{}{
				"type":        "string",
				"description": "File or directory to search. Default: workspace root.",
			},
			"paths": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "批量搜索多个文件或目录，按顺序在 path 之后搜索，等价于 rg pattern path1 path2 ...。可传字符串数组，也可传单个字符串（字符串按整条路径处理，不按逗号拆分）。每个根都必须位于工作区内，否则整次调用被拒绝。",
			},
			"glob": map[string]interface{}{
				"type":        "string",
				"description": "Optional file filter, e.g. *.go.",
			},
			"include": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "包含的文件名 glob 模式，例如 *.go；无斜杠的模式匹配任意目录下的文件名，含斜杠的模式匹配相对搜索根的路径。可传字符串数组，或逗号分隔的单个字符串（*.{go,ts} 这类 brace 形式不会被拆分）。",
			},
			"exclude": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{
						"type":  "array",
						"items": map[string]interface{}{"type": "string"},
					},
				},
				"description": "排除的文件名 glob 模式，例如 *_test.go；语义与 include 相同，且优先于 include。",
			},
			"case_insensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容别名；等价于 ignore_case=true。",
			},
			"literal": map[string]interface{}{
				"type":        "boolean",
				"description": "是否为字面文本搜索，关闭正则特殊字符解释（默认为 false）。",
			},
			"fixed_strings": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --fixed-strings/-F；等价于 literal=true。",
			},
			"ignore_case": map[string]interface{}{
				"type":        "boolean",
				"description": "是否忽略大小写；兼容 rg 的 --ignore-case/-i。",
			},
			"case_sensitive": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --case-sensitive/-s。true 时会覆盖 ignore_case/smart_case，强制区分大小写。",
			},
			"smart_case": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --smart-case/-S。仅当模式中没有大写字母时自动忽略大小写。",
			},
			"word": map[string]interface{}{
				"type":        "boolean",
				"description": "是否匹配完整单词（默认为 false）。例如搜 err 不会匹配 error/stderr。",
			},
			"word_regexp": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --word-regexp/-w；等价于 word=true。",
			},
			"line_regexp": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --line-regexp/-x：要求整行匹配。",
			},
			"invert_match": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --invert-match/-v：输出不匹配模式的行。",
			},
			"context": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "显示匹配行前后各 N 行上下文（兼容 rg 的 --context/-C，上限 10）。匹配行保持 path:line:text，上下文行用 rg 的 path-line-text，块之间用 -- 分隔。",
			},
			"before_context": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "兼容 rg 的 --before-context/-B：显示匹配行前 N 行（上限 10）。",
			},
			"after_context": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "兼容 rg 的 --after-context/-A：显示匹配行后 N 行（上限 10）。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "Maximum matches to return. Default 200.",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "Skip the first N matches, then apply limit. Use the returned next_offset to page.",
			},
			"mode": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"content", "files", "count"},
				"description": "输出模式：content=显示匹配行（默认），files=仅列出包含匹配的文件，count=每个文件的匹配行数（path:count）。files/count 模式下 limit/offset 以「文件」为单位，next_offset/has_more 同口径。files_without 模式当前不可用。",
			},
			"files_with_matches": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --files-with-matches/-l；等价于 mode=files。",
			},
			"count": map[string]interface{}{
				"type":        "boolean",
				"description": "兼容 rg 的 --count/-c；等价于 mode=count（统计匹配行数）。",
			},
		},
		// At least one pattern spelling is required, but which one is a
		// handler-level rule: `patterns` is a valid substitute for `pattern`,
		// so binding `required` to the single-value spelling would reject
		// calls the shared implementation fully supports.
		"required":             []string{},
		"additionalProperties": false,
	})
	if err != nil {
		return nil, fmt.Errorf("grep schema: %w", err)
	}
	grepBase := remoteToolBase{cfg: cfg, workspace: workspace}
	grepTool, err := toolkit.NewRuntimeTool(searchcore.NewGrepHandler(toolcore.Definition{
		Name:                ToolGrep,
		Description:         grepBase.describe("Search remote file contents with a regular expression; results are file:line:text."),
		Version:             "1.0.0",
		InputSchema:         grepSchema,
		Capabilities:        []toolcore.Capability{toolcore.CapReadFS},
		DefaultOutputBudget: toolcoreoutput.ToolBudget(ToolGrep),
		DirectCall:          true,
	}), func(ctx context.Context) (toolcore.Runtime, error) {
		return newRemoteRuntime(ctx, cfg, workspace, sandbox, artifacts), nil
	})
	if err != nil {
		return nil, fmt.Errorf("grep handler: %w", err)
	}

	type registration struct {
		name string
		tool toolkit.Tool
	}
	candidates := []registration{
		{name: ToolView, tool: viewTool},
		{name: ToolLs, tool: lsTool},
		{name: ToolGlob, tool: globTool},
		{name: ToolGrep, tool: grepTool},
		{name: ToolShell, tool: newRemoteShellTool(cfg, workspace, sandbox, artifacts)},
		{name: ToolJobStart, tool: newRemoteJobStartTool(cfg, workspace, sandbox, jobs)},
		{name: ToolJobOutput, tool: newRemoteJobOutputTool(cfg, workspace, jobs)},
		{name: ToolJobCancel, tool: newRemoteJobCancelTool(cfg, workspace, jobs)},
		{name: ToolJobList, tool: newRemoteJobListTool(cfg, workspace, jobs)},
	}
	mutationTools, err := buildMutationTools(cfg, workspace, sandbox, artifacts)
	if err != nil {
		return nil, err
	}
	for _, tool := range mutationTools {
		candidates = append(candidates, registration{name: tool.Name(), tool: tool})
	}
	artifactSchema, err := toolkit.SchemaFromParameters(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"artifact_ref": map[string]interface{}{
				"type":        "string",
				"description": "Namespaced artifact ref (remote:<target>:art_…) or bare art_… id.",
			},
			"artifact_id": map[string]interface{}{
				"type":        "string",
				"description": "Bare artifact id (art_…) from a truncated tool result.",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"minimum":     0,
				"description": "0-based byte offset. Default 0.",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"minimum":     1,
				"description": "Maximum bytes to return. Default 65536.",
			},
		},
		"additionalProperties": false,
	})
	if err != nil {
		return nil, fmt.Errorf("artifact_read schema: %w", err)
	}
	artifactBase := remoteToolBase{cfg: cfg, workspace: workspace}
	artifactTool, err := toolkit.NewRuntimeTool(toolcore.NewArtifactReadHandler(toolcore.Definition{
		Name:                ToolArtifactRead,
		Description:         artifactBase.describe("Read a byte window of a full remote tool capture that was folded before reaching the model."),
		Version:             "1.0.0",
		InputSchema:         artifactSchema,
		Capabilities:        []toolcore.Capability{toolcore.CapArtifact},
		DefaultOutputBudget: toolcoreoutput.ToolBudget(ToolArtifactRead),
		DirectCall:          true,
	}), func(ctx context.Context) (toolcore.Runtime, error) {
		return newRemoteRuntime(ctx, cfg, workspace, sandbox, artifacts), nil
	})
	if err != nil {
		return nil, fmt.Errorf("artifact_read handler: %w", err)
	}
	candidates = append(candidates, registration{name: ToolArtifactRead, tool: artifactTool})

	for _, candidate := range candidates {
		if !enabled[candidate.name] {
			continue
		}
		wrapped := newPrefixedTool(candidate.tool, cfg.ToolPrefix)
		if err := registry.Register(wrapped); err != nil {
			return nil, fmt.Errorf("register %s: %w", candidate.name, err)
		}
	}
	return &buildResult{registry: registry, jobs: jobs, artifacts: artifacts}, nil
}

// auditTool wraps a tool so every invocation produces one audit record.
//
// Auditing sits in the registry wrapper rather than inside each tool: a new tool
// then cannot forget it, and the record shape stays identical across tools
// (design §7.5). The wrapper records the decision, the model-visible byte count
// and the truncation flag, never the file contents or the full command line.
type auditTool struct {
	inner        toolkit.Tool
	sink         AuditSink
	metrics      *Metrics
	target       string
	part         string
	workspace    string
	previewBytes int
}

func newAuditTool(inner toolkit.Tool, sink AuditSink, metrics *Metrics, cfg *Config, workspace *Workspace, transport string) *auditTool {
	return &auditTool{
		inner:        inner,
		sink:         sink,
		metrics:      metrics,
		target:       cfg.TargetName,
		part:         transport,
		workspace:    workspace.Root(),
		previewBytes: cfg.Audit.CommandPreviewBytes,
	}
}

func (t *auditTool) Name() string                       { return t.inner.Name() }
func (t *auditTool) Description() string                { return t.inner.Description() }
func (t *auditTool) Version() string                    { return t.inner.Version() }
func (t *auditTool) Parameters() map[string]interface{} { return t.inner.Parameters() }
func (t *auditTool) CanDirectCall() bool                { return t.inner.CanDirectCall() }

func (t *auditTool) DefinitionMetadata() map[string]interface{} {
	if provider, ok := t.inner.(toolkit.ToolDefinitionMetadataProvider); ok {
		return provider.DefinitionMetadata()
	}
	return nil
}

func (t *auditTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	started := time.Now()
	result, err := t.inner.Execute(ctx, params)
	duration := time.Since(started)
	if t.sink == nil && t.metrics == nil {
		return result, err
	}
	info := RequestInfoFromContext(ctx)
	record := AuditRecord{
		Principal:    info.Principal,
		RemoteAddr:   info.RemoteAddr,
		SessionID:    info.SessionID,
		ToolCallID:   info.ToolCallID,
		Transport:    t.part,
		Target:       t.target,
		Tool:         t.inner.Name(),
		Decision:     "allow",
		Workspace:    t.workspace,
		DurationMS:   duration.Milliseconds(),
		ExecutionEnv: string(executionScopeRemote),
	}
	if command, ok := params["command"].(string); ok && strings.TrimSpace(command) != "" {
		fields := shellrisk.Fields(command)
		if len(fields) > 0 {
			record.Program = commandBaseName(fields[0])
		}
		normalized := normalizeCommandLine(command)
		sum := sha256.Sum256([]byte(normalized))
		record.CommandHash = hex.EncodeToString(sum[:])
		if t.previewBytes > 0 {
			preview, _ := truncateUTF8Safe(command, t.previewBytes)
			preview = strings.ReplaceAll(preview, "\r", " ")
			preview = strings.ReplaceAll(preview, "\n", " ")
			record.CommandPreview = preview
		}
	}
	failed := err != nil
	denied := false
	if err != nil {
		record.Decision = "error"
		record.Reason = err.Error()
	} else if result != nil {
		record.BytesOut = len(result.Content)
		if result.Metadata != nil {
			if truncated, ok := result.Metadata["is_truncated"].(bool); ok {
				record.Truncated = truncated
			}
			if code, ok := result.Metadata["error_code"].(string); ok {
				record.Reason = code
				if isDeniedResult(result.Metadata) {
					record.Decision = "deny"
					denied = true
				} else if record.Decision == "allow" {
					record.Decision = "error"
				}
			}
			if paths, ok := result.Metadata["mutated_paths"].([]string); ok {
				record.Paths = paths
			}
			if rule, ok := result.Metadata["policy_rule"].(string); ok {
				record.PolicyRule = rule
			}
			switch code := result.Metadata["exit_code"].(type) {
			case int:
				record.ExitCode = code
			case int64:
				record.ExitCode = int(code)
			}
		}
		if !result.Success && record.Decision == "allow" {
			record.Decision = "error"
		}
		if !result.Success {
			failed = true
		}
	}
	if t.metrics != nil {
		t.metrics.Observe(t.inner.Name(), duration, record.BytesOut, failed, denied)
	}
	// Best effort: an audit write failure must not fail the tool call, but it is
	// surfaced on stderr so the operator notices a broken sink.
	if t.sink != nil {
		if writeErr := t.sink.Write(record); writeErr != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: audit write failed: %v\n", writeErr)
		}
	}
	return result, err
}

// exposedToolNames lists the model-visible (prefixed) tool names.
func exposedToolNames(registry *toolkit.Registry) []string {
	if registry == nil {
		return nil
	}
	list := registry.List()
	names := make([]string, 0, len(list))
	for _, tool := range list {
		names = append(names, tool.Name())
	}
	return names
}

// validateToolNames rejects names that would overflow the MCP canonical name
// budget or fall outside the portable character set.
//
// registry.CanonicalToolName builds `mcp__<server>__<tool>` and truncates with a
// hash past 64 characters, which makes the name unreadable to the model and hard
// to correlate in audit logs (review MT-R2). Failing at startup is better than
// shipping a silently hashed name.
func validateToolNames(serverName string, toolNames []string) error {
	const maxCanonical = 64
	for _, name := range toolNames {
		if !isPortableToolPrefix(name) {
			return fmt.Errorf("tool name %q is not portable ([A-Za-z0-9_-])", name)
		}
		if length := canonicalLength(serverName, name); length > maxCanonical {
			return fmt.Errorf(
				"tool %q with server %q produces a %d-character canonical name (limit %d); shorten the target name or the tool prefix",
				name, serverName, length, maxCanonical)
		}
	}
	return nil
}

// canonicalLength mirrors registry.CanonicalToolName's length arithmetic.
func canonicalLength(serverName, toolName string) int {
	return len("mcp__") + len(sanitizePortableLength(serverName)) + len("__") + len(sanitizePortableLength(toolName))
}

// sanitizePortableLength mirrors registry.portableNamePart for length purposes:
// an unchanged name keeps its length, a rewritten one appends a 7-char suffix.
func sanitizePortableLength(value string) string {
	raw := strings.TrimSpace(value)
	var builder strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_' || r == '-':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	portable := strings.Trim(builder.String(), "_")
	if portable == "" {
		return "unnamed"
	}
	if portable != raw {
		return portable + "_xxxxxxx"
	}
	return portable
}
