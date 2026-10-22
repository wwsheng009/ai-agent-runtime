package remotetools

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
	toolcoreoutput "github.com/wwsheng009/ai-agent-runtime/internal/toolcore/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// Mutation tool names (canonical, unprefixed).
const (
	ToolWrite      = "write"
	ToolEdit       = "edit"
	ToolAppend     = "append_write"
	ToolApplyPatch = "apply_patch"
)

// buildMutationTools builds the remote mutation surface from the shared
// mutatecore Handlers. Only schema, description and Runtime wiring live here.
func buildMutationTools(cfg *Config, workspace *Workspace, sandbox *Sandbox, artifacts *ArtifactStore) ([]toolkit.Tool, error) {
	base := remoteToolBase{cfg: cfg, workspace: workspace}
	runtimeFactory := func(ctx context.Context) (toolcore.Runtime, error) {
		return newRemoteRuntime(ctx, cfg, workspace, sandbox, artifacts), nil
	}
	type spec struct {
		name        string
		description string
		version     string
		schema      map[string]interface{}
		capability  toolcore.Capability
		newHandler  func(toolcore.Definition) toolcore.Handler
	}
	specs := []spec{
		{
			name:        ToolWrite,
			description: base.describe("Write a complete file on the remote host (create or overwrite)."),
			version:     "1.1.0",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Target file, relative to the remote workspace.",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "Complete file content. This replaces the file.",
					},
				},
				"required":             []string{"file_path", "content"},
				"additionalProperties": false,
			},
			capability: toolcore.CapWriteFS,
			newHandler: mutatecore.NewWriteHandler,
		},
		{
			name:        ToolAppend,
			description: base.describe("Append text to the end of a remote file."),
			version:     "1.1.0",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Target file, relative to the remote workspace.",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "Text appended to the end of the file.",
					},
					"create_if_missing": map[string]interface{}{
						"type":        "boolean",
						"description": "Create the file when it does not exist. Default false.",
					},
				},
				"required":             []string{"file_path", "content"},
				"additionalProperties": false,
			},
			capability: toolcore.CapWriteFS,
			newHandler: mutatecore.NewAppendHandler,
		},
		{
			name:        ToolEdit,
			description: base.describe("Replace exact text in a remote file with an ambiguity guard."),
			version:     "1.1.0",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "Target file, relative to the remote workspace.",
					},
					"old_string": map[string]interface{}{
						"type":        "string",
						"description": "Exact text to replace. Must occur exactly once unless replace_all/replacement_count is set.",
					},
					"new_string": map[string]interface{}{
						"type":        "string",
						"description": "Replacement text.",
					},
					"replace_all": map[string]interface{}{
						"type":        "boolean",
						"description": "Replace every match. Mutually exclusive with replacement_count.",
					},
					"replacement_count": map[string]interface{}{
						"type":        "integer",
						"description": "Replace only the first N matches. Mutually exclusive with replace_all.",
					},
				},
				"required":             []string{"file_path", "old_string", "new_string"},
				"additionalProperties": false,
			},
			capability: toolcore.CapWriteFS,
			newHandler: mutatecore.NewEditHandler,
		},
		{
			name:        ToolApplyPatch,
			description: base.describe("Apply a Codex-style patch to remote files (add, update, delete, move)."),
			version:     "1.1.0",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"patch": map[string]interface{}{
						"type": "string",
						"description": "Codex-style patch: " + mutatecore.PatchBegin + " / " +
							strings.TrimSuffix(mutatecore.PatchAdd, ": ") + " / " +
							strings.TrimSuffix(mutatecore.PatchUpdate, ": ") +
							" with @@ blocks / " + strings.TrimSuffix(mutatecore.PatchDelete, ": ") + " / " +
							mutatecore.PatchEnd + ".",
					},
				},
				"required":             []string{"patch"},
				"additionalProperties": false,
			},
			capability: toolcore.CapWriteFS,
			newHandler: mutatecore.NewApplyPatchHandler,
		},
	}

	tools := make([]toolkit.Tool, 0, len(specs))
	for _, spec := range specs {
		schema, err := toolkit.SchemaFromParameters(spec.schema)
		if err != nil {
			return nil, fmt.Errorf("%s schema: %w", spec.name, err)
		}
		handler := spec.newHandler(toolcore.Definition{
			Name:                spec.name,
			Description:         spec.description,
			Version:             spec.version,
			InputSchema:         schema,
			Capabilities:        []toolcore.Capability{spec.capability},
			DefaultOutputBudget: toolcoreoutput.ToolBudget(spec.name),
			DirectCall:          true,
		})
		tool, err := toolkit.NewRuntimeTool(handler, runtimeFactory)
		if err != nil {
			return nil, fmt.Errorf("%s handler: %w", spec.name, err)
		}
		tools = append(tools, tool)
	}
	return tools, nil
}
