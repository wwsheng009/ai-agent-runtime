package remotetools

import (
	"context"
	"fmt"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// Optional tool names are the names already published by internal/toolkit/tools.
// remote-tools only decides whether to expose the existing implementation; it
// does not keep a second copy of the tool body.
const (
	ToolMultiedit   = "multiedit"
	ToolDownload    = "download"
	ToolFetch       = "fetch"
	ToolSourcegraph = "sourcegraph"
)

// optionalToolSpec is one row of the remote opt-in surface.
//
// The implementation comes from internal/toolkit/tools. The row only adds the
// remote-specific policy: minimum profile tier, network capability and path
// parameters that must be checked against the per-call workspace.
type optionalToolSpec struct {
	name            string
	tier            Profile
	requiresNetwork bool
	pathParams      []string
	newTool         func() toolkit.Tool
}

var optionalToolSpecs = []optionalToolSpec{
	{
		name:       ToolMultiedit,
		tier:       ProfileStandard,
		pathParams: []string{"file_path"},
		newTool:    func() toolkit.Tool { return runtimetools.NewMultieditTool() },
	},
	{
		name:            ToolDownload,
		tier:            ProfileStandard,
		requiresNetwork: true,
		pathParams:      []string{"file_path"},
		newTool:         func() toolkit.Tool { return runtimetools.NewDownloadTool() },
	},
	{
		name:            ToolFetch,
		tier:            ProfileOps,
		requiresNetwork: true,
		newTool:         func() toolkit.Tool { return runtimetools.NewFetchTool() },
	},
	{
		name:            ToolSourcegraph,
		tier:            ProfileOps,
		requiresNetwork: true,
		newTool:         func() toolkit.Tool { return runtimetools.NewSourcegraphTool() },
	},
}

func optionalToolByName(name string) (optionalToolSpec, bool) {
	name = strings.TrimSpace(strings.ToLower(name))
	for _, spec := range optionalToolSpecs {
		if spec.name == name {
			return spec, true
		}
	}
	return optionalToolSpec{}, false
}

// optionalToolRequested reports whether the operator explicitly opted into the
// tool. Both optionalTools.enabled and a tools: entry count as an explicit
// request; profile remains the ceiling, never the loader.
func (c *Config) optionalToolRequested(name string) bool {
	if c == nil {
		return false
	}
	if containsToolName(c.OptionalTools.Enabled, name) {
		return true
	}
	return containsExactToolName(c.Tools, name)
}

func containsToolName(names []string, wanted string) bool {
	wanted = strings.TrimSpace(strings.ToLower(wanted))
	for _, name := range names {
		trimmed := strings.TrimSpace(strings.ToLower(name))
		if trimmed == "*" || trimmed == wanted {
			return true
		}
	}
	return false
}

func containsExactToolName(names []string, wanted string) bool {
	wanted = strings.TrimSpace(strings.ToLower(wanted))
	for _, name := range names {
		if strings.EqualFold(strings.TrimSpace(name), wanted) {
			return true
		}
	}
	return false
}

// optionalToolEnabled is the single visibility rule for an optional tool.
//
// It deliberately fails closed:
//   - optional tools are local-backend only; the existing tools act on the
//     process filesystem, which is the target host only for BackendLocal;
//   - the operator must name the tool in optionalTools.enabled or tools;
//   - the profile must be at least the tool's tier;
//   - network tools additionally require network.enabled;
//   - readOnly removes every optional tool.
func (c *Config) optionalToolEnabled(spec optionalToolSpec) bool {
	if c == nil || spec.name == "" {
		return false
	}
	if c.Backend != "" && c.Backend != BackendLocal {
		return false
	}
	if c.ReadOnly || !c.optionalToolRequested(spec.name) {
		return false
	}
	if spec.requiresNetwork && !c.Network.Enabled {
		return false
	}
	if len(c.Tools) > 0 && !containsExactToolName(c.Tools, spec.name) {
		return false
	}
	if c.Profile == ProfileCustom {
		// Custom is an exact list. optionalTools.enabled alone must not widen it.
		return containsExactToolName(c.Tools, spec.name)
	}
	return profileRank(c.Profile) >= profileRank(spec.tier)
}

func (c *Config) enabledOptionalTools() []string {
	names := make([]string, 0, len(optionalToolSpecs))
	for _, spec := range optionalToolSpecs {
		if c.optionalToolEnabled(spec) {
			names = append(names, spec.name)
		}
	}
	return names
}

// OptionalToolNames returns the optional tools that are actually visible after
// profile, opt-in, readOnly, backend and network gating. It is used by doctor
// and tests so diagnostics cannot drift from the registry.
func (c *Config) OptionalToolNames() []string {
	return c.enabledOptionalTools()
}

// buildOptionalTools constructs the existing toolkit tools and applies the
// remote workspace/sandbox policy around them. No tool body is copied here.
func buildOptionalTools(cfg *Config, workspace *Workspace, sandbox *Sandbox) ([]toolkit.Tool, error) {
	if cfg == nil || workspace == nil {
		return nil, fmt.Errorf("config and workspace are required")
	}
	tools := make([]toolkit.Tool, 0, len(optionalToolSpecs))
	for _, spec := range optionalToolSpecs {
		if !cfg.optionalToolEnabled(spec) {
			continue
		}
		inner := spec.newTool()
		if inner == nil {
			return nil, fmt.Errorf("optional tool %q constructed nil", spec.name)
		}
		if setter, ok := inner.(interface {
			SetSandbox(*runtimeexecutor.Sandbox)
		}); ok && sandbox != nil {
			setter.SetSandbox(sandbox.ExecutorSandbox())
		}
		if setter, ok := inner.(interface{ SetBasePath(string) }); ok {
			setter.SetBasePath(workspace.Root())
		}
		description := (remoteToolBase{cfg: cfg, workspace: workspace}).describe(inner.Description())
		tools = append(tools, &remoteOptionalTool{
			Tool:        inner,
			workspace:   workspace,
			pathParams:  append([]string(nil), spec.pathParams...),
			description: description,
		})
	}
	return tools, nil
}

// remoteOptionalTool adds the remote-only call context to an existing toolkit
// tool:
//   - per-call MCP roots narrowing is passed through toolctx.WorkspaceRoot;
//   - file-bearing parameters are preflight-checked against the narrowed
//     workspace before the existing implementation resolves them;
//   - the description gets the same remote target prefix as the core surface.
type remoteOptionalTool struct {
	toolkit.Tool
	workspace   *Workspace
	pathParams  []string
	description string
}

func (t *remoteOptionalTool) Description() string { return t.description }

func (t *remoteOptionalTool) DefinitionMetadata() map[string]interface{} {
	if provider, ok := t.Tool.(toolkit.ToolDefinitionMetadataProvider); ok {
		return provider.DefinitionMetadata()
	}
	return nil
}

// pathValues normalizes a path parameter that may arrive as a string, []string
// or []interface{}. A single type assertion silently skipped array-valued
// arguments, so the pre-check passed for every path it never looked at.
func pathValues(v interface{}) []string {
	switch typed := v.(type) {
	case string:
		return []string{typed}
	case []string:
		return typed
	case []interface{}:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func (t *remoteOptionalTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	effective := workspaceForCall(ctx, t.workspace)
	if effective != nil {
		base := CallWorkdir(ctx)
		for _, key := range t.pathParams {
			if base == "" {
				raw, _ := params[key].(string)
				if strings.TrimSpace(raw) == "" {
					continue
				}
				if _, err := effective.Resolve(raw); err != nil {
					return &toolkit.ToolResult{
						Success:    false,
						OutputKind: toolresult.KindText,
						Error: newToolError(CodePathDenied,
							"use a path inside the current workspace",
							"%s denied by the remote workspace: %v", key, err),
					}, nil
				}
				continue
			}
			// With a workdir the inner tool still resolves against the workspace
			// root, so the base has to be folded into the argument. Rewriting to
			// an absolute path keeps the jail check meaningful and makes the
			// advertised workdir actually change where the call lands.
			values := pathValues(params[key])
			if len(values) == 0 {
				continue
			}
			rewritten := make([]string, 0, len(values))
			for _, raw := range values {
				trimmed := strings.TrimSpace(raw)
				if trimmed == "" {
					rewritten = append(rewritten, raw)
					continue
				}
				resolved, err := effective.ResolveFrom(base, trimmed)
				if err != nil {
					return &toolkit.ToolResult{
						Success:    false,
						OutputKind: toolresult.KindText,
						Error: newToolError(CodePathDenied,
							"use a path inside the current workspace",
							"%s denied by the remote workspace: %v", key, err),
					}, nil
				}
				rewritten = append(rewritten, resolved)
			}
			if _, isString := params[key].(string); isString {
				params[key] = rewritten[0]
			} else {
				params[key] = rewritten
			}
		}
		ctx = toolctx.WithWorkspaceRoot(ctx, effective.Root())
	}
	result, err := t.Tool.Execute(ctx, params)
	if result != nil {
		if result.Metadata == nil {
			result.Metadata = map[string]interface{}{}
		}
		result.Metadata["remote"] = true
		if effective != nil {
			result.Metadata["workspace"] = effective.Root()
		}
	}
	return result, err
}
