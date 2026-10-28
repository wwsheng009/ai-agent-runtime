package remotetools

// envd_patch.go adds apply_patch to the envd backend.
//
// The patch language is not re-implemented: parsing comes from mutatecore.ParsePatch,
// hunk application from ApplyHunks, and the summary the caller reads from
// PatchSummaryMessage + FormatEditSuccessContent + UnifiedDiff — the same
// renderers the local and ssh surfaces use, because mutatecore.PatchResult has an
// all-exported shape this backend can fill directly.
//
// What IS re-owned here is the orchestration, because mutatecore.ApplyPatch cannot
// be called: its PatchBindings expose Write/Remove seams but no Read seam — the
// read half goes through Inspect, i.e. os.ReadFile on this machine. Running it
// against an envd target would read LOCAL files and write REMOTE ones, which is
// the mixed-scope bug this merge exists to prevent. The sequencing rules
// ApplyPatch encodes are kept instead:
//
//  1. every operation is authorized (resolved through the jail) before any
//     filesystem mutation, so a patch cannot half-apply and then die on a path
//     outside the workspace;
//  2. landed mutations are journalled and rolled back in reverse when a later
//     operation fails, including files the patch had just created;
//  3. a move refuses an existing destination, and an add refuses to overwrite
//     different content.
//
// Transport limit, stated rather than hidden: content travels as one argv
// element, so a payload that encodes to bytes containing NUL (BOM/UTF-16) is
// refused here; the local backend can round-trip those through WriteAtomic.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// envdPatchPlan is one authorized operation: the resolved target, and for a move
// the resolved destination.
type envdPatchPlan struct {
	op         mutatecore.PatchOperation
	target     string
	moveTarget string
}

// envdPatchUndo is the journal of what already landed.
type envdPatchUndo struct {
	// restores are files whose previous bytes must be written back (an update,
	// a delete, or the source of a move). Restores are kept in application order
	// and unwound in reverse.
	restores []envdPatchRestore
	// created are files the patch wrote that must be removed again.
	created []string
}

type envdPatchRestore struct {
	path string
	raw  []byte
}

// envdPatchApplier carries the result while operations land, so the summary the
// caller sees counts the same fields the local surface counts.
type envdPatchApplier struct {
	backend *envdBackend
	result  mutatecore.PatchResult
	undo    envdPatchUndo
	diff    strings.Builder
}

func envdApplyPatch(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	rawPatch, hasPatch := envdParamString(params, "patch")
	if !hasPatch || strings.TrimSpace(rawPatch) == "" {
		return envdErrorResult("patch is required"), nil
	}
	operations, err := mutatecore.ParsePatch(rawPatch)
	if err != nil {
		return envdErrorResult("%s\nnext_action: %s", err.Error(), mutatecore.PatchFailureNextAction(err)), nil
	}
	if len(operations) == 0 {
		return envdErrorResult("patch contains no operations"), nil
	}

	// Phase 1: authorize every path before touching anything. A patch that names
	// a path outside the jail must fail as if nothing had happened.
	plans := make([]envdPatchPlan, 0, len(operations))
	for _, op := range operations {
		if ext := mutatecore.DocumentExtensionRefusal(op.Path); ext != "" {
			content, _ := mutatecore.DocumentRefusalContent(ToolApplyPatch, "以文本改写", op.Path, ext)
			return envdErrorResult("%s", content), nil
		}
		plan := envdPatchPlan{op: op}
		switch op.Kind {
		case mutatecore.PatchDeleteOp:
			// A delete acts on the link itself, so it uses the mutation jail.
			target, resolveErr := be.resolveMutationPath(ctx, op.Path)
			if resolveErr != nil {
				return envdErrorResult("%v", resolveErr), nil
			}
			plan.target = target
		default:
			target, resolveErr := be.resolvePath(ctx, op.Path)
			if resolveErr != nil {
				return envdErrorResult("%v", resolveErr), nil
			}
			plan.target = target
			if op.MoveTo != "" {
				destination, destErr := be.resolvePath(ctx, op.MoveTo)
				if destErr != nil {
					return envdErrorResult("move destination: %v", destErr), nil
				}
				plan.moveTarget = destination
			}
		}
		plans = append(plans, plan)
	}

	// Phase 2: apply with a journal; a failure unwinds what already landed.
	applier := &envdPatchApplier{backend: be}
	for _, plan := range plans {
		var refusal *toolkit.ToolResult
		switch plan.op.Kind {
		case mutatecore.PatchAddOp:
			refusal = applier.applyAdd(ctx, plan)
		case mutatecore.PatchDeleteOp:
			refusal = applier.applyDelete(ctx, plan)
		default:
			refusal = applier.applyUpdate(ctx, plan)
		}
		if refusal != nil {
			note := applier.rollback(ctx)
			if note != "" {
				return envdErrorResult("%s\nrollback problem: %s", envdResultText(refusal), note), nil
			}
			return refusal, nil
		}
	}

	result := applier.result
	result.Diff = applier.diff.String()
	for _, file := range result.Files {
		result.MutatedPaths = append(result.MutatedPaths, file.Path)
	}
	summary := mutatecore.FormatEditSuccessContent(mutatecore.PatchSummaryMessage(result), result.Diff)
	return &toolkit.ToolResult{
		Success: true,
		Content: summary,
		Metadata: map[string]interface{}{
			"files_changed": result.FilesChanged,
			"created_files": result.Created,
			"updated_files": result.Updated,
			"deleted_files": result.Deleted,
			"moved_files":   result.Moved,
			"mutated_paths": result.MutatedPaths,
			"patch":         result.Diff,
		},
	}, nil
}

// record files one landed operation in the result the summary is built from.
func (a *envdPatchApplier) record(file mutatecore.PatchFileResult) {
	a.result.Files = append(a.result.Files, file)
	a.result.FilesChanged++
	switch file.Kind {
	case mutatecore.PatchAddOp:
		a.result.Created++
	case mutatecore.PatchDeleteOp:
		a.result.Deleted++
	default:
		if file.WasMoved {
			a.result.Moved++
		} else {
			a.result.Updated++
		}
	}
}

// applyAdd creates a file from the patch body. Re-applying the same add is a
// replay, not an error; overwriting different content is refused, because an add
// that silently replaced a file would hide a real change from the caller.
func (a *envdPatchApplier) applyAdd(ctx context.Context, plan envdPatchPlan) *toolkit.ToolResult {
	body := strings.Join(plan.op.AddLines, "\n")
	if plan.op.NoFinalNewline {
		body = strings.TrimSuffix(body, "\n")
	} else if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	state, refusal := a.backend.load(ctx, plan.target, envdEditMaxFileBytes)
	if refusal != nil {
		return refusal
	}
	if state.exists {
		if state.text == body {
			return nil // replay: already in the desired state
		}
		return envdErrorResult("cannot add %s: the file already exists with different content (use an update hunk instead)", plan.target)
	}
	written, _, refusal := a.backend.storeBytes(ctx, plan.target, mutatecore.Encode(body, mutatecore.UTF8), true)
	if refusal != nil {
		return refusal
	}
	a.undo.created = append(a.undo.created, plan.target)
	a.diff.WriteString(mutatecore.UnifiedDiff(a.backend.workspaceRelative(plan.target), "", body))
	a.record(mutatecore.PatchFileResult{
		Kind: mutatecore.PatchAddOp, Path: plan.target, DisplayPath: a.backend.workspaceRelative(plan.target),
		WasNew: true, BytesAfter: written, NewBytes: mutatecore.Encode(body, mutatecore.UTF8),
	})
	return nil
}

// applyUpdate applies hunks and, when the operation names a MoveTo, relocates the
// result. A stale hunk refuses without mutating — that is what the context lines
// are for.
func (a *envdPatchApplier) applyUpdate(ctx context.Context, plan envdPatchPlan) *toolkit.ToolResult {
	state, refusal := a.backend.load(ctx, plan.target, envdEditMaxFileBytes)
	if refusal != nil {
		return refusal
	}
	if !state.exists {
		return envdErrorResult("%s", mutatecore.PatchNotFoundMessage(ToolApplyPatch, plan.target, a.backend.workspaceRelative(plan.target)))
	}
	if mutatecore.LooksBinary(state.raw) {
		return envdErrorResult("target looks binary: %s (apply_patch only accepts text files)", plan.target)
	}
	// A rename carries no hunks, and ApplyHunks refuses an empty hunk list on
	// purpose: without a change block there is nothing to verify against context.
	updated := state.text
	if len(plan.op.Hunks) > 0 {
		var err error
		updated, err = mutatecore.ApplyHunks(state.text, plan.op.Hunks)
		if err != nil {
			return envdErrorResult("%s\nnext_action: %s", err.Error(), mutatecore.PatchFailureNextAction(err))
		}
	}
	display := a.backend.workspaceRelative(plan.target)

	if plan.moveTarget != "" {
		// A move must not clobber: renaming onto an existing file destroys it.
		destinationState, destRefusal := a.backend.load(ctx, plan.moveTarget, envdEditMaxFileBytes)
		if destRefusal != nil {
			return destRefusal
		}
		if destinationState.exists {
			return envdErrorResult("cannot move %s -> %s: the destination already exists", plan.target, plan.moveTarget)
		}
		encoded := mutatecore.Encode(updated, state.enc)
		written, _, refusal := a.backend.storeBytes(ctx, plan.moveTarget, encoded, true)
		if refusal != nil {
			return refusal
		}
		// Journal before removing the source: if the removal fails, the caller
		// still learns the destination was written and can be undone.
		a.undo.restores = append(a.undo.restores, envdPatchRestore{path: plan.target, raw: state.raw})
		a.undo.created = append(a.undo.created, plan.moveTarget)
		if removeErr := a.backend.client.Remove(ctx, plan.target); removeErr != nil {
			return envdErrorResult("move %s -> %s failed after writing the destination: %v", plan.target, plan.moveTarget, describeEnvDError(removeErr, plan.target))
		}
		a.diff.WriteString(mutatecore.UnifiedDiff(a.backend.workspaceRelative(plan.moveTarget), state.text, updated))
		a.record(mutatecore.PatchFileResult{
			Kind: mutatecore.PatchUpdateOp, Path: plan.target, DisplayPath: display,
			MoveTo: a.backend.workspaceRelative(plan.moveTarget), MovePath: plan.moveTarget,
			WasMoved: true, BytesBefore: state.size, BytesAfter: written, NewBytes: encoded,
		})
		return nil
	}

	if updated == state.text {
		return nil // replay: the hunk describes an already-applied change
	}
	encoded := mutatecore.Encode(updated, state.enc)
	written, _, refusal := a.backend.storeBytes(ctx, plan.target, encoded, true)
	if refusal != nil {
		return refusal
	}
	a.undo.restores = append(a.undo.restores, envdPatchRestore{path: plan.target, raw: state.raw})
	a.diff.WriteString(mutatecore.UnifiedDiff(display, state.text, updated))
	a.record(mutatecore.PatchFileResult{
		Kind: mutatecore.PatchUpdateOp, Path: plan.target, DisplayPath: display,
		BytesBefore: state.size, BytesAfter: written, NewBytes: encoded,
	})
	return nil
}

// applyDelete removes a path, keeping its bytes so a later failure can put it back.
func (a *envdPatchApplier) applyDelete(ctx context.Context, plan envdPatchPlan) *toolkit.ToolResult {
	state, refusal := a.backend.load(ctx, plan.target, envdEditMaxFileBytes)
	if refusal != nil {
		return refusal
	}
	if !state.exists {
		return nil // deleting an absent path is already the desired state
	}
	if err := a.backend.client.Remove(ctx, plan.target); err != nil {
		var connectErr *envdclient.ConnectError
		if errors.As(err, &connectErr) && connectErr.IsNotFound() {
			return nil
		}
		return envdErrorResult("%s", describeEnvDError(err, plan.target))
	}
	a.undo.restores = append(a.undo.restores, envdPatchRestore{path: plan.target, raw: state.raw})
	a.record(mutatecore.PatchFileResult{
		Kind: mutatecore.PatchDeleteOp, Path: plan.target, DisplayPath: a.backend.workspaceRelative(plan.target),
		BytesBefore: state.size, NewBytes: nil,
	})
	return nil
}

// rollback undoes the journal in reverse order. It reports what it could not fix
// instead of pretending the target is clean.
func (a *envdPatchApplier) rollback(ctx context.Context) string {
	var problems []string
	for i := len(a.undo.created) - 1; i >= 0; i-- {
		path := a.undo.created[i]
		if err := a.backend.client.Remove(ctx, path); err != nil {
			var connectErr *envdclient.ConnectError
			if errors.As(err, &connectErr) && connectErr.IsNotFound() {
				continue
			}
			problems = append(problems, fmt.Sprintf("remove %s: %v", path, err))
		}
	}
	for i := len(a.undo.restores) - 1; i >= 0; i-- {
		entry := a.undo.restores[i]
		if _, _, refusal := a.backend.storeBytes(ctx, entry.path, entry.raw, true); refusal != nil {
			problems = append(problems, fmt.Sprintf("restore %s: %s", entry.path, envdResultText(refusal)))
		}
	}
	return strings.Join(problems, "; ")
}

// envdPatchSpec publishes the shared apply_patch schema verbatim, so a patch
// written for one backend parses identically on the other.
func envdPatchSpec(base remoteToolBase) envdToolSpec {
	return envdToolSpec{
		name:        ToolApplyPatch,
		description: base.describe("Apply a Codex-style patch to remote files (add, update, delete, move). Every path is authorized against the workspace jail before any file changes, and a failed hunk rolls the patch back."),
		version:     "1.1.0",
		parameters:  mutatecore.ApplyPatchParameters(),
		call:        envdApplyPatch,
	}
}

// envdResultText pulls the message out of a refusal result.
func envdResultText(result *toolkit.ToolResult) string {
	if result == nil {
		return ""
	}
	if result.Error != nil {
		return result.Error.Error()
	}
	return result.Content
}
