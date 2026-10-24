package remotetools

import (
	"context"
	"fmt"

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
			schema:      mutatecore.WriteParameters(),
			capability:  toolcore.CapWriteFS,
			newHandler:  mutatecore.NewWriteHandler,
		},
		{
			name:        ToolAppend,
			description: base.describe("Append text to the end of a remote file."),
			version:     "1.1.0",
			schema:      mutatecore.AppendParameters(),
			capability:  toolcore.CapWriteFS,
			newHandler:  mutatecore.NewAppendHandler,
		},
		{
			name:        ToolEdit,
			description: base.describe("Replace exact text in a remote file with an ambiguity guard."),
			version:     "1.1.0",
			schema:      mutatecore.EditParameters(),
			capability:  toolcore.CapWriteFS,
			newHandler:  mutatecore.NewEditHandler,
		},
		{
			name:        ToolApplyPatch,
			description: base.describe("Apply a Codex-style patch to remote files (add, update, delete, move)."),
			version:     "1.1.0",
			schema:      mutatecore.ApplyPatchParameters(),
			capability:  toolcore.CapWriteFS,
			newHandler:  mutatecore.NewApplyPatchHandler,
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
