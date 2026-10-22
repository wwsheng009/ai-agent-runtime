package remotetools

import (
	"context"
	"fmt"
	"os"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// remoteApplyPatchTool implements the Codex apply_patch format on the remote host.
//
// Supported directives:
//
//	*** Begin Patch / *** End Patch
//	*** Add File: <path>        followed by + lines
//	*** Delete File: <path>
//	*** Update File: <path>     followed by @@ blocks of context/-/+ lines
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
		tool.describe("Apply a Codex-style patch to remote files (add, update, delete)."),
		"1.0.0",
		parameters,
		true,
	)
	return tool
}

// patchOperation is one parsed file operation.
type patchOperation struct {
	kind  string // add | update | delete
	path  string
	lines []string   // add: content lines; update: raw hunk lines
}

const (
	patchBegin  = "*** Begin Patch"
	patchEnd    = "*** End Patch"
	patchAdd    = "*** Add File: "
	patchDelete = "*** Delete File: "
	patchUpdate = "*** Update File: "
)

func (t *remoteApplyPatchTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if err := contextErr(ctx); err != nil {
		return remoteErrorResult(err), nil
	}
	rawPatch, _ := params["patch"].(string)
	if strings.TrimSpace(rawPatch) == "" {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "pass patch", "patch is required")), nil
	}
	operations, err := parseRemotePatch(rawPatch)
	if err != nil {
		return remoteErrorResult(err), nil
	}
	if len(operations) == 0 {
		return remoteErrorResult(newToolError(CodeInvalidArgument, "include at least one *** Add/Update/Delete File block", "patch contains no operations")), nil
	}

	// Phase 1: authorize and plan every file before touching any of them, so a
	// denied path cannot leave the workspace half-patched.
	type plannedFile struct {
		op       patchOperation
		absPath  string
		newBody  string
		mutated  bool
	}
	planned := make([]plannedFile, 0, len(operations))
	for _, op := range operations {
		absPath, authErr := t.authorize(ctx, op.path, runtimeexecutor.OpWrite)
		if authErr != nil {
			return remoteErrorResult(authErr), nil
		}
		item := plannedFile{op: op, absPath: absPath}
		switch op.kind {
		case "add":
			body := strings.Join(op.lines, "\n")
			if !strings.HasSuffix(body, "\n") && body != "" {
				body += "\n"
			}
			item.newBody = body
			item.mutated = true
		case "delete":
			if !fileExists(absPath) {
				return remoteErrorResult(newToolError(CodeNotFound, "check the path", "cannot delete missing file: %s", t.cleanDisplayPath(absPath))), nil
			}
			item.mutated = true
		case "update":
			existing, readErr := os.ReadFile(absPath)
			if readErr != nil {
				if os.IsNotExist(readErr) {
					return remoteErrorResult(newToolError(CodeNotFound, "verify the path with view", "cannot update missing file: %s", t.cleanDisplayPath(absPath))), nil
				}
				return remoteErrorResult(newToolError(CodeInvalidArgument, "check permissions", "cannot read %s: %v", t.cleanDisplayPath(absPath), readErr)), nil
			}
			updated, updateErr := applyPatchHunks(string(existing), op.lines)
			if updateErr != nil {
				return remoteErrorResult(updateErr), nil
			}
			item.newBody = updated
			item.mutated = true
		default:
			return remoteErrorResult(newToolError(CodeInvalidArgument, "check the patch syntax", "unknown operation %q", op.kind)), nil
		}
		planned = append(planned, item)
	}

	// Phase 2: apply.
	mutated := make([]string, 0, len(planned))
	for _, item := range planned {
		if !item.mutated {
			continue
		}
		display := t.cleanDisplayPath(item.absPath)
		switch item.op.kind {
		case "add", "update":
			if err := validateWriteTarget(item.absPath); err != nil {
				return remoteErrorResult(err), nil
			}
			if err := writeFileAtomic(item.absPath, []byte(item.newBody), 0o644); err != nil {
				return remoteErrorResult(err), nil
			}
		case "delete":
			if err := os.Remove(item.absPath); err != nil {
				return remoteErrorResult(newToolError(CodeInvalidArgument, "check permissions", "cannot delete %s: %v", display, err)), nil
			}
		}
		mutated = append(mutated, display)
	}

	metadata := map[string]interface{}{
		"files_changed": len(mutated),
		"mutated_paths": mutated,
		"remote":        true,
		"workspace":     t.workspace.Root(),
	}
	summary := fmt.Sprintf("Applied patch to %d file(s):\n", len(mutated))
	for _, path := range mutated {
		summary += "  - " + path + "\n"
	}
	return remoteTextResult(summary, metadata), nil
}

// parseRemotePatch parses the Codex patch grammar into operations.
func parseRemotePatch(raw string) ([]patchOperation, error) {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")

	var (
		operations []patchOperation
		current    *patchOperation
		started    bool
	)
	flush := func() {
		if current != nil {
			operations = append(operations, *current)
			current = nil
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "" && current == nil:
			continue
		case trimmed == patchBegin:
			started = true
			continue
		case trimmed == patchEnd:
			flush()
			started = false
			continue
		case strings.HasPrefix(trimmed, patchAdd):
			flush()
			current = &patchOperation{kind: "add", path: strings.TrimSpace(strings.TrimPrefix(trimmed, patchAdd))}
			continue
		case strings.HasPrefix(trimmed, patchDelete):
			flush()
			current = &patchOperation{kind: "delete", path: strings.TrimSpace(strings.TrimPrefix(trimmed, patchDelete))}
			continue
		case strings.HasPrefix(trimmed, patchUpdate):
			flush()
			current = &patchOperation{kind: "update", path: strings.TrimSpace(strings.TrimPrefix(trimmed, patchUpdate))}
			continue
		}
		if current == nil {
			continue
		}
		if current.kind == "add" {
			// Add blocks conventionally prefix every content line with '+'.
			current.lines = append(current.lines, strings.TrimPrefix(line, "+"))
			continue
		}
		if current.kind == "update" {
			current.lines = append(current.lines, line)
		}
	}
	flush()
	if !started && len(operations) == 0 {
		return nil, newToolError(CodeInvalidArgument, "wrap the patch in *** Begin Patch / *** End Patch", "patch has no recognized operations")
	}
	for _, op := range operations {
		if strings.TrimSpace(op.path) == "" {
			return nil, newToolError(CodeInvalidArgument, "name a path in every block", "patch operation is missing a path")
		}
	}
	return operations, nil
}

// applyPatchHunks applies @@ blocks with exact matching.
//
// Each block's old side is the ordered sequence of ' ' (context) and '-'
// (removed) lines. It must occur exactly once in the file; the matched span is
// replaced by the block's ' ' and '+' lines. Ambiguity is an error rather than a
// best-effort guess.
func applyPatchHunks(body string, hunkLines []string) (string, error) {
	type block struct {
		oldLines []string
		newLines []string
	}
	var (
		blocks  []block
		current *block
	)
	flush := func() {
		if current != nil {
			blocks = append(blocks, *current)
			current = nil
		}
	}
	for _, line := range hunkLines {
		if strings.HasPrefix(line, "@@") {
			flush()
			current = &block{}
			continue
		}
		if current == nil {
			// Lines before the first @@ are ignored (file header noise).
			continue
		}
		if line == "" {
			// A blank line in the grammar is an empty context line.
			current.oldLines = append(current.oldLines, "")
			current.newLines = append(current.newLines, "")
			continue
		}
		switch line[0] {
		case ' ':
			current.oldLines = append(current.oldLines, line[1:])
			current.newLines = append(current.newLines, line[1:])
		case '-':
			current.oldLines = append(current.oldLines, line[1:])
		case '+':
			current.newLines = append(current.newLines, line[1:])
		default:
			// Unrecognized line inside a hunk: treat as context to stay tolerant of
			// editors that drop the leading space.
			current.oldLines = append(current.oldLines, line)
			current.newLines = append(current.newLines, line)
		}
	}
	flush()
	if len(blocks) == 0 {
		return "", newToolError(CodeInvalidArgument, "include at least one @@ block", "update block has no hunks")
	}

	// Work on a line slice so block matching is line-exact, not substring-based.
	lines := strings.Split(body, "\n")
	for index, item := range blocks {
		if len(item.oldLines) == 0 {
			// Pure insertion: append at the end is the only unambiguous choice.
			lines = append(lines, item.newLines...)
			continue
		}
		position, occurrences := findBlock(lines, item.oldLines)
		if occurrences == 0 {
			return "", newToolError(CodeNotFound, "re-read the file and update the patch context", "hunk %d does not match the file", index+1)
		}
		if occurrences > 1 {
			return "", newToolError(CodeInvalidArgument, "add more context lines to make the hunk unique", "hunk %d matches %d locations", index+1, occurrences)
		}
		replaced := make([]string, 0, len(lines)-len(item.oldLines)+len(item.newLines))
		replaced = append(replaced, lines[:position]...)
		replaced = append(replaced, item.newLines...)
		replaced = append(replaced, lines[position+len(item.oldLines):]...)
		lines = replaced
	}
	return strings.Join(lines, "\n"), nil
}

// findBlock returns the first line offset where oldLines occurs and how many
// times it occurs in total.
func findBlock(lines, oldLines []string) (int, int) {
	first := -1
	count := 0
	for start := 0; start+len(oldLines) <= len(lines); start++ {
		matched := true
		for offset, expected := range oldLines {
			if lines[start+offset] != expected {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		if first < 0 {
			first = start
		}
		count++
	}
	return first, count
}