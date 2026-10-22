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
	jobs, err := NewJobManager(cfg.Jobs.Root)
	if err != nil {
		return nil, err
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

	type registration struct {
		name string
		tool toolkit.Tool
	}
	candidates := []registration{
		{name: ToolView, tool: newRemoteViewTool(cfg, workspace)},
		{name: ToolLs, tool: newRemoteLsTool(cfg, workspace)},
		{name: ToolGlob, tool: newRemoteGlobTool(cfg, workspace)},
		{name: ToolGrep, tool: newRemoteGrepTool(cfg, workspace)},
		{name: ToolWrite, tool: newRemoteWriteTool(cfg, workspace, sandbox)},
		{name: ToolAppend, tool: newRemoteAppendTool(cfg, workspace, sandbox)},
		{name: ToolEdit, tool: newRemoteEditTool(cfg, workspace, sandbox)},
		{name: ToolApplyPatch, tool: newRemoteApplyPatchTool(cfg, workspace, sandbox)},
		{name: ToolShell, tool: newRemoteShellTool(cfg, workspace, sandbox, artifacts)},
		{name: ToolJobStart, tool: newRemoteJobStartTool(cfg, workspace, sandbox, jobs)},
		{name: ToolJobOutput, tool: newRemoteJobOutputTool(cfg, workspace, jobs)},
		{name: ToolJobCancel, tool: newRemoteJobCancelTool(cfg, workspace, jobs)},
		{name: ToolJobList, tool: newRemoteJobListTool(cfg, workspace, jobs)},
		{name: ToolArtifactRead, tool: newRemoteArtifactReadTool(cfg, workspace, artifacts)},
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
