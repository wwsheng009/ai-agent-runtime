package remotetools

import (
	"context"
	"fmt"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// remoteApplyPatchTool implements the Codex apply_patch format on the remote
// host through the shared mutatecore parser and transactional planner.
//
// Supported directives:
//
//	*** Begin Patch / *** End Patch
//	*** Add File: <path>        followed by + lines
//	*** Delete File: <path>
//	*** Update File: <path>     followed by @@ blocks of context/-/+ lines
//	*** Move to: <path>         inside an Update File block
//
// Update matching is exact: a block applies only when its context and removed
// lines occur exactly once in the file. That is stricter than a fuzzy matcher and
// deliberately so - a remote patch must never "approximately" rewrite a file.
type remoteApplyPatchTool struct {
	*toolkit.BaseTool
	remoteWriteToolBase
}

func newRemoteApplyPatchTool(cfg *Config, workspace *Workspace, sandbox *Sandbox) *remoteApplyPatchTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"patch": map[string]interface{}{
				"type":        "string",
				"description": "Codex-style patch: *** Begin Patch / *** Add File / *** Update File with @@ blocks / *** Delete File / *** End Patch.",
			},
		},
		"required":             []string{"patch"},
		"additionalProperties": false,
	}
	tool := &remoteApplyPatchTool{remoteWriteToolBase: remoteWriteToolBase{
		remoteToolBase: remoteToolBase{cfg: cfg, workspace: workspace},
		sandbox:        sandbox,
	}}
	tool.BaseTool = toolkit.NewBaseTool(
		ToolApplyPatch,
		tool.describe("Apply a Codex-style patch to remote files (add, update, delete, move)."),
		"1.1.0",
		parameters,
		true,
	)
	return tool
}

func (t *remoteApplyPatchTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	rawPatch, _ := params["patch"].(string)
	if strings.TrimSpace(rawPatch) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass patch", "patch is required")), nil
	}

	result, err := mutatecore.ApplyPatch(ctx, rawPatch, mutatecore.PatchBindings{
		Resolve: func(raw string) (string, error) {
			return t.authorize(ctx, raw, runtimeexecutor.OpWrite)
		},
		CheckWrite:    validateWriteTarget,
		Display:       t.cleanDisplayPath,
		CreateParents: false,
		Mode:          mutatecore.DefaultFileMode,
	})
	if err != nil {
		return remoteErrorResult(remoteMutationError(err)), nil
	}

	metadata := map[string]interface{}{
		"files_changed": result.FilesChanged,
		"created_files": result.Created,
		"updated_files": result.Updated,
		"deleted_files": result.Deleted,
		"moved_files":   result.Moved,
		"mutated_paths": result.MutatedPaths,
		"remote":        true,
		"workspace":     t.workspace.Root(),
	}
	summary := fmt.Sprintf("Applied patch to %d file(s):\n", result.FilesChanged)
	for _, path := range result.MutatedPaths {
		summary += "  - " + path + "\n"
	}
	return remoteTextResult(summary, metadata), nil
}
