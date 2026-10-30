package remotetools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
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

func (t *prefixedTool) Name() string        { return t.name }
func (t *prefixedTool) Description() string { return t.description }
func (t *prefixedTool) Version() string     { return t.inner.Version() }

// Parameters exposes the inner schema, adding an optional workdir to tools that
// take filesystem paths.
//
// Tools that already declare workdir themselves (shell, job_start) are left
// untouched, and tools without any path parameter are left untouched, so the
// advertised surface only grows where the parameter actually means something.
func (t *prefixedTool) Parameters() map[string]interface{} {
	schema := t.inner.Parameters()
	if schema == nil {
		return nil
	}
	if _, already := schema["properties"].(map[string]interface{})["workdir"]; already {
		return schema
	}
	if !declaresPathParam(schema) {
		return schema
	}
	return withWorkdirProperty(schema)
}

func (t *prefixedTool) CanDirectCall() bool { return t.inner.CanDirectCall() }

func (t *prefixedTool) DefinitionMetadata() map[string]interface{} {
	if provider, ok := t.inner.(toolkit.ToolDefinitionMetadataProvider); ok {
		if metadata := provider.DefinitionMetadata(); len(metadata) > 0 {
			return metadata
		}
	}
	return nil
}

// Execute forwards to the inner tool, carrying an optional per-call workdir in
// the context so that remoteToolBase.resolve anchors relative paths there.
//
// Tools that already declare workdir (shell, job_start) read the parameter
// themselves, so they must be delegated untouched: putting the value in the
// context as well would apply it twice and resolve "sub" to "<root>/sub/sub".
//
// For every other path-taking tool the key is stripped from the map before
// delegation, because handlers decode their argument struct strictly and would
// reject a key they never asked for; the context value still reaches path
// resolution.
//
// The derived context is local to this call, so nothing becomes sticky.
func (t *prefixedTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	workdir, _ := params["workdir"].(string)
	if workdir == "" {
		return t.inner.Execute(ctx, params)
	}

	innerParams := t.inner.Parameters()
	props, _ := innerParams["properties"].(map[string]interface{})
	if _, nativeWorkdir := props["workdir"]; nativeWorkdir {
		// The tool resolves workdir itself; do not also set it on the context.
		return t.inner.Execute(ctx, params)
	}

	callParams := make(map[string]interface{}, len(params))
	for k, v := range params {
		if k == "workdir" {
			continue
		}
		callParams[k] = v
	}
	return t.inner.Execute(WithCallWorkdir(ctx, workdir), callParams)
}

// buildResult bundles the registry with the stores that need lifecycle management.
type buildResult struct {
	registry  *toolkit.Registry
	jobs      *background.Manager
	artifacts *ArtifactStore
	// envd is set only by the envd backend; it holds the connection (and, from
	// M2 on, the live pty sessions) that Close must release.
	envd *envdBackend
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
	// One core background manager backs both the shell job-first path and the
	// remote job_* tools. It is only constructed when the effective surface can
	// actually use it, but there is never a second remote job backend.
	var jobs *background.Manager
	if configNeedsJobRuntime(cfg) {
		jobs, err = newRemoteBackgroundManager(cfg, sandbox)
		if err != nil {
			return nil, err
		}
	}
	keepJobs := false
	defer func() {
		if !keepJobs {
			_ = closeBackgroundManager(jobs)
		}
	}()
	artifacts, err := NewArtifactStore(cfg.Artifacts.Root, cfg.Artifacts.MaxBytes)
	if err != nil {
		return nil, err
	}
	registry := toolkit.NewRegistry()
	// Fail closed: a required result-contract dependency that cannot be exposed
	// (denied, or not auto-exposable) means the surface would hand the model a
	// reference it cannot consume. Refuse to build rather than ship a broken
	// surface.
	if _, resolution := cfg.resolveEnabledTools(); resolution.Failed() {
		return nil, fmt.Errorf("remote tool surface: %w", errors.Join(resolution.Errors...))
	}
	enabled := map[string]bool{}
	for _, name := range cfg.enabledTools() {
		enabled[name] = true
	}
	lsBase := remoteToolBase{cfg: cfg, workspace: workspace}
	// One ls tool means one schema. depth defaults to 1, so the remote surface
	// keeps its old one-level behavior while gaining the local tree listing
	// instead of declaring a parameter that only one side could honour.
	lsSchema, err := lsInputSchema()
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
	// One glob tool means one schema: the remote surface publishes exactly the
	// parameter list the local tool publishes, because both run the same handler.
	globSchema, err := globInputSchema()
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
	grepSchema, err := grepInputSchema()
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
		{name: ToolShell, tool: newRemoteShellTool(cfg, workspace, sandbox, artifacts, jobs)},
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
	optionalTools, err := buildOptionalTools(cfg, workspace, sandbox)
	if err != nil {
		return nil, err
	}
	for _, tool := range optionalTools {
		candidates = append(candidates, registration{name: tool.Name(), tool: tool})
	}

	for _, candidate := range candidates {
		if !enabled[candidate.name] {
			continue
		}
		wrapped := newPrefixedTool(candidate.tool, cfg.ToolPrefix)
		if err := registry.Register(wrapped); err != nil {
			return nil, fmt.Errorf("register %s: %w", candidate.name, err)
		}
	}
	keepJobs = true
	return &buildResult{registry: registry, jobs: jobs, artifacts: artifacts}, nil
}

// configNeedsJobRuntime reports whether the effective remote surface includes a
// tool that needs the shared job runtime. A read-only or file-only surface must
// not create a job store just because the default jobs.root exists.
func configNeedsJobRuntime(cfg *Config) bool {
	if cfg == nil || strings.TrimSpace(cfg.Jobs.Root) == "" {
		return false
	}
	for _, name := range cfg.enabledTools() {
		if name == ToolShell {
			return true
		}
		for _, jobTool := range jobTools {
			if name == jobTool {
				return true
			}
		}
	}
	return false
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
	// The audit layer wraps the registry, so it sees the original argument map
	// before prefixedTool strips workdir and moves it into the context. Reading
	// the parameter here is therefore the only way to record it.
	if workdir, ok := params["workdir"].(string); ok && strings.TrimSpace(workdir) != "" {
		record.Workdir = workdir
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
