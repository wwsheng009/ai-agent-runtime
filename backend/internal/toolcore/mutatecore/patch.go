package mutatecore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	PatchBegin  = "*** " + "Begin Patch"
	PatchEnd    = "*** " + "End Patch"
	PatchAdd    = "*** " + "Add File: "
	PatchDelete = "*** " + "Delete File: "
	PatchUpdate = "*** " + "Update File: "
	PatchMoveTo = "*** " + "Move to: "
	PatchEOF    = "*** " + "End of File"
)

type PatchOperationKind string

const (
	PatchAddOp    PatchOperationKind = "add"
	PatchDeleteOp PatchOperationKind = "delete"
	PatchUpdateOp PatchOperationKind = "update"
)

type PatchHunkLine struct {
	Kind byte
	Text string
}

type PatchHunk struct {
	Header    string
	Lines     []PatchHunkLine
	EndOfFile bool
}

type PatchOperation struct {
	Kind           PatchOperationKind
	Path           string
	MoveTo         string
	AddLines       []string
	NoFinalNewline bool
	Hunks          []PatchHunk
}

type PatchBindings struct {
	Resolve    func(raw string) (string, error)
	CheckWrite func(abs string) error
	// CheckDelete is optional. Nil falls back to CheckWrite. Move operations
	// need the delete permission on the source, which is not the same grant as
	// write on the target.
	CheckDelete   func(abs string) error
	Display       func(abs string) string
	CreateParents bool
	Mode          os.FileMode
	// Write and Remove are optional IO seams. Nil uses WriteAtomic / os.Remove.
	// They exist so rollback can be tested with a deterministic failure without
	// adding a second patch implementation.
	Write  func(abs string, data []byte, mode os.FileMode) error
	Remove func(abs string) error
}

type PatchFileResult struct {
	Kind        PatchOperationKind
	Path        string
	DisplayPath string
	MoveTo      string
	BytesBefore int64
	BytesAfter  int64
	WasNew      bool
	WasMoved    bool
}

type PatchResult struct {
	FilesChanged int
	MutatedPaths []string
	Created      int
	Updated      int
	Deleted      int
	Moved        int
	Files        []PatchFileResult
}

// ApplyPatch parses a Codex-style patch, authorizes every operation before any
// filesystem mutation, then applies add/update/delete/move operations.
func ApplyPatch(ctx context.Context, raw string, bindings PatchBindings) (PatchResult, error) {
	if err := contextError(ctx); err != nil {
		return PatchResult{}, err
	}
	operations, err := ParsePatch(raw)
	if err != nil {
		return PatchResult{}, err
	}
	if len(operations) == 0 {
		return PatchResult{}, errorf(CodeInvalidArgument, "include at least one Add/Update/Delete block", "patch contains no operations")
	}
	if bindings.Resolve == nil {
		return PatchResult{}, errorf(CodeInternal, "configure the path resolver", "patch resolver is not configured")
	}
	if bindings.CheckWrite == nil {
		bindings.CheckWrite = func(string) error { return nil }
	}
	checkDelete := bindings.CheckDelete
	if checkDelete == nil {
		checkDelete = bindings.CheckWrite
	}
	if bindings.Display == nil {
		bindings.Display = func(abs string) string { return abs }
	}
	if bindings.Mode == 0 {
		bindings.Mode = DefaultFileMode
	}
	writeFile := bindings.Write
	if writeFile == nil {
		writeFile = WriteAtomic
	}
	removeFile := bindings.Remove
	if removeFile == nil {
		removeFile = os.Remove
	}

	type plannedFile struct {
		operation PatchOperation
		absPath   string
		moveAbs   string
		display   string
		moveDisp  string
		newBody   string
		oldBody   string
		oldSize   int64
		encoding  Encoding
		wasNew    bool
		wasMoved  bool
	}
	planned := make([]plannedFile, 0, len(operations))
	seen := map[string]bool{}
	for _, op := range operations {
		absPath, resolveErr := bindings.Resolve(op.Path)
		if resolveErr != nil {
			return PatchResult{}, resolveErr
		}
		if checkErr := bindings.CheckWrite(absPath); checkErr != nil {
			return PatchResult{}, checkErr
		}
		if op.Kind == PatchDeleteOp {
			if checkErr := checkDelete(absPath); checkErr != nil {
				return PatchResult{}, checkErr
			}
		}
		if seen[absPath] {
			return PatchResult{}, errorf(CodeInvalidArgument, "combine operations on the same file into one update block", "patch contains duplicate operations for %s", bindings.Display(absPath))
		}
		seen[absPath] = true
		item := plannedFile{operation: op, absPath: absPath, display: bindings.Display(absPath)}
		if strings.TrimSpace(op.MoveTo) != "" {
			moveAbs, moveErr := bindings.Resolve(op.MoveTo)
			if moveErr != nil {
				return PatchResult{}, moveErr
			}
			if checkErr := checkDelete(absPath); checkErr != nil {
				return PatchResult{}, checkErr
			}
			if checkErr := bindings.CheckWrite(moveAbs); checkErr != nil {
				return PatchResult{}, checkErr
			}
			if moveAbs != absPath {
				if seen[moveAbs] {
					return PatchResult{}, errorf(CodeInvalidArgument, "combine operations on the same file into one update block", "patch move target is used more than once: %s", bindings.Display(moveAbs))
				}
				if _, statErr := os.Stat(moveAbs); statErr == nil {
					return PatchResult{}, errorf(CodeInvalidArgument, "choose a new move target or delete it first", "move target already exists: %s", bindings.Display(moveAbs))
				} else if !os.IsNotExist(statErr) {
					return PatchResult{}, newError(CodeInvalidArgument, "check the move target path", statErr)
				}
				seen[moveAbs] = true
				item.moveAbs = moveAbs
				item.moveDisp = bindings.Display(moveAbs)
				item.wasMoved = true
			}
		}
		switch op.Kind {
		case PatchAddOp:
			if _, statErr := os.Stat(absPath); statErr == nil {
				return PatchResult{}, errorf(CodeInvalidArgument, "delete the existing file first or use an update block", "cannot add existing file: %s", item.display)
			} else if !os.IsNotExist(statErr) {
				return PatchResult{}, newError(CodeInvalidArgument, "check the target path", statErr)
			}
			body := strings.Join(op.AddLines, "\n")
			if body != "" && !op.NoFinalNewline && !strings.HasSuffix(body, "\n") {
				body += "\n"
			}
			item.newBody = body
			item.encoding = UTF8
			item.wasNew = true
		case PatchDeleteOp:
			state, inspectErr := Inspect(absPath, 0)
			if inspectErr != nil {
				return PatchResult{}, inspectErr
			}
			if !state.Exists {
				return PatchResult{}, errorf(CodeNotFound, "check the path", "cannot delete missing file: %s", item.display)
			}
			item.oldBody = state.Text
			item.oldSize = state.Size
			item.encoding = state.Encoding
		case PatchUpdateOp:
			state, inspectErr := Inspect(absPath, 0)
			if inspectErr != nil {
				return PatchResult{}, inspectErr
			}
			if !state.Exists {
				return PatchResult{}, errorf(CodeNotFound, "verify the path with view", "cannot update missing file: %s", item.display)
			}
			updated := state.Text
			if len(op.Hunks) > 0 {
				newline := detectNewline(state.Text)
				normalized := state.Text
				if newline == "\r\n" {
					normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
				}
				var applyErr error
				updated, applyErr = ApplyHunks(normalized, op.Hunks)
				if applyErr != nil {
					return PatchResult{}, applyErr
				}
				if newline == "\r\n" {
					updated = strings.ReplaceAll(updated, "\n", "\r\n")
				}
			}
			item.oldBody = state.Text
			item.newBody = updated
			item.oldSize = state.Size
			item.encoding = state.Encoding
		default:
			return PatchResult{}, errorf(CodeInvalidArgument, "check the patch syntax", "unknown operation %q", op.Kind)
		}
		planned = append(planned, item)
	}

	type undoStep struct {
		path    string
		exists  bool
		content []byte
		mode    os.FileMode
	}
	undo := make([]undoStep, 0, len(planned)*2)
	rollback := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			step := undo[i]
			if step.exists {
				_ = writeFile(step.path, step.content, step.mode)
			} else {
				_ = removeFile(step.path)
			}
		}
	}

	result := PatchResult{}
	for _, item := range planned {
		if bindings.CreateParents && item.operation.Kind != PatchDeleteOp {
			if err := os.MkdirAll(filepath.Dir(item.absPath), 0o755); err != nil {
				return PatchResult{}, newError(CodeInvalidArgument, "check the parent directory", fmt.Errorf("create parent directory: %w", err))
			}
		}
		fileResult := PatchFileResult{
			Kind:        item.operation.Kind,
			Path:        item.absPath,
			DisplayPath: item.display,
			MoveTo:      item.moveDisp,
			BytesBefore: item.oldSize,
			WasNew:      item.wasNew,
			WasMoved:    item.wasMoved,
		}
		switch item.operation.Kind {
		case PatchAddOp, PatchUpdateOp:
			encoded := Encode(item.newBody, item.encoding)
			if item.operation.Kind == PatchAddOp {
				undo = append(undo, undoStep{path: item.absPath})
			} else {
				oldBytes := Encode(item.oldBody, item.encoding)
				undo = append(undo, undoStep{path: item.absPath, exists: true, content: oldBytes, mode: bindings.Mode})
			}
			if err := writeFile(item.absPath, encoded, bindings.Mode); err != nil {
				rollback()
				return PatchResult{}, err
			}
			fileResult.BytesAfter = int64(len(encoded))
			if item.wasMoved {
				if bindings.CreateParents {
					if err := os.MkdirAll(filepath.Dir(item.moveAbs), 0o755); err != nil {
						rollback()
						return PatchResult{}, newError(CodeInvalidArgument, "check the move target directory", fmt.Errorf("create move target directory: %w", err))
					}
				}
				undo = append(undo, undoStep{path: item.moveAbs})
				if err := writeFile(item.moveAbs, encoded, bindings.Mode); err != nil {
					rollback()
					return PatchResult{}, err
				}
				// The source is still on disk; removing it is recorded by the
				// update undo entry above (it restores the old body).
				if err := removeFile(item.absPath); err != nil {
					rollback()
					return PatchResult{}, newError(CodeInvalidArgument, "check permissions", fmt.Errorf("remove moved source %s: %w", item.display, err))
				}
				result.Moved++
				result.MutatedPaths = append(result.MutatedPaths, item.display)
				result.MutatedPaths = append(result.MutatedPaths, item.moveDisp)
			} else {
				result.MutatedPaths = append(result.MutatedPaths, item.display)
			}
		case PatchDeleteOp:
			oldBytes := Encode(item.oldBody, item.encoding)
			undo = append(undo, undoStep{path: item.absPath, exists: true, content: oldBytes, mode: bindings.Mode})
			if err := removeFile(item.absPath); err != nil {
				rollback()
				return PatchResult{}, newError(CodeInvalidArgument, "check permissions", fmt.Errorf("delete %s: %w", item.display, err))
			}
			result.Deleted++
			result.MutatedPaths = append(result.MutatedPaths, item.display)
		}
		switch item.operation.Kind {
		case PatchAddOp:
			result.Created++
		case PatchUpdateOp:
			result.Updated++
		}
		result.Files = append(result.Files, fileResult)
	}
	result.FilesChanged = len(result.MutatedPaths)
	return result, nil
}

// ParsePatch parses the Codex patch grammar.
func ParsePatch(raw string) ([]PatchOperation, error) {
	normalized := strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	var (
		operations []PatchOperation
		current    *PatchOperation
		inHunk     bool
	)
	flush := func() {
		if current != nil {
			operations = append(operations, *current)
			current = nil
			inHunk = false
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			if current != nil && current.Kind == PatchAddOp {
				current.AddLines = append(current.AddLines, "")
			} else if current != nil && current.Kind == PatchUpdateOp && inHunk {
				current.Hunks[len(current.Hunks)-1].Lines = append(current.Hunks[len(current.Hunks)-1].Lines, PatchHunkLine{Kind: ' ', Text: ""})
			}
			continue
		case trimmed == PatchBegin:
			flush()
			continue
		case trimmed == PatchEnd:
			flush()
			continue
		case strings.HasPrefix(trimmed, PatchAdd):
			flush()
			current = &PatchOperation{Kind: PatchAddOp, Path: pathFromDirective(trimmed, PatchAdd)}
			continue
		case strings.HasPrefix(trimmed, PatchDelete):
			flush()
			current = &PatchOperation{Kind: PatchDeleteOp, Path: pathFromDirective(trimmed, PatchDelete)}
			continue
		case strings.HasPrefix(trimmed, PatchUpdate):
			flush()
			current = &PatchOperation{Kind: PatchUpdateOp, Path: pathFromDirective(trimmed, PatchUpdate)}
			continue
		case strings.HasPrefix(trimmed, PatchMoveTo) && current != nil:
			current.MoveTo = pathFromDirective(trimmed, PatchMoveTo)
			continue
		case trimmed == PatchEOF && current != nil:
			switch current.Kind {
			case PatchAddOp:
				current.NoFinalNewline = true
			case PatchUpdateOp:
				if inHunk && len(current.Hunks) > 0 {
					current.Hunks[len(current.Hunks)-1].EndOfFile = true
					inHunk = false
				}
			}
			continue
		}
		if current == nil {
			continue
		}
		if current.Kind == PatchAddOp {
			current.AddLines = append(current.AddLines, strings.TrimPrefix(line, "+"))
			continue
		}
		if current.Kind == PatchUpdateOp {
			if strings.HasPrefix(trimmed, "@@") {
				current.Hunks = append(current.Hunks, PatchHunk{Header: trimmed})
				inHunk = true
				continue
			}
			if !inHunk {
				continue
			}
			kind := byte(' ')
			text := line
			if len(line) > 0 {
				switch line[0] {
				case ' ', '-', '+':
					kind = line[0]
					text = line[1:]
				default:
					if strings.HasPrefix(trimmed, "\\ No newline at end of file") {
						current.NoFinalNewline = true
						continue
					}
				}
			}
			current.Hunks[len(current.Hunks)-1].Lines = append(current.Hunks[len(current.Hunks)-1].Lines, PatchHunkLine{Kind: kind, Text: text})
		}
	}
	flush()
	if len(operations) == 0 {
		return nil, errorf(CodeInvalidArgument, "wrap the patch in Begin/End markers and include an Add/Update/Delete block", "patch has no recognized operations")
	}
	for _, op := range operations {
		if strings.TrimSpace(op.Path) == "" {
			return nil, errorf(CodeInvalidArgument, "name a path in every block", "patch operation is missing a path")
		}
		if op.Kind == PatchUpdateOp && len(op.Hunks) == 0 && strings.TrimSpace(op.MoveTo) == "" {
			return nil, errorf(CodeInvalidArgument, "include at least one @@ block or a Move to directive", "update block has no hunks for %s", op.Path)
		}
	}
	return operations, nil
}

// ApplyHunks applies @@ blocks with exact line matching. Each old-side block
// must match exactly once; ambiguity is an error.
func ApplyHunks(body string, hunks []PatchHunk) (string, error) {
	if len(hunks) == 0 {
		return "", errorf(CodeInvalidArgument, "include at least one @@ block", "update block has no hunks")
	}
	lines := strings.Split(body, "\n")
	cursor := 0
	for index, hunk := range hunks {
		var oldLines, newLines []string
		for _, line := range hunk.Lines {
			switch line.Kind {
			case ' ':
				oldLines = append(oldLines, line.Text)
				newLines = append(newLines, line.Text)
			case '-':
				oldLines = append(oldLines, line.Text)
			case '+':
				newLines = append(newLines, line.Text)
			default:
				oldLines = append(oldLines, line.Text)
				newLines = append(newLines, line.Text)
			}
		}
		if len(oldLines) == 0 {
			lines = append(lines, newLines...)
			cursor = len(lines)
			continue
		}
		position, occurrences := findBlock(lines, oldLines, cursor, hunk.EndOfFile)
		if occurrences == 0 {
			return "", &Error{
				Code:       CodeStaleContext,
				Message:    fmt.Sprintf("hunk %d does not match the file", index+1),
				NextAction: "re-read the file and rebuild the hunk from current context",
			}
		}
		if occurrences > 1 {
			return "", errorf(CodeInvalidArgument, "add more context lines to make the hunk unique", "hunk %d matches %d locations", index+1, occurrences)
		}
		replaced := make([]string, 0, len(lines)-len(oldLines)+len(newLines))
		replaced = append(replaced, lines[:position]...)
		replaced = append(replaced, newLines...)
		replaced = append(replaced, lines[position+len(oldLines):]...)
		lines = replaced
		cursor = position + len(newLines)
	}
	return strings.Join(lines, "\n"), nil
}

func findBlock(lines, oldLines []string, cursor int, eof bool) (int, int) {
	first, count := -1, 0
	if cursor < 0 {
		cursor = 0
	}
	if eof {
		end := len(lines)
		if end > 0 && lines[end-1] == "" {
			end--
		}
		start := end - len(oldLines)
		if start >= cursor && start >= 0 && blockMatches(lines[start:start+len(oldLines)], oldLines) {
			return start, 1
		}
		return -1, 0
	}
	for start := cursor; start+len(oldLines) <= len(lines); start++ {
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

func blockMatches(lines, expected []string) bool {
	for index, line := range expected {
		if lines[index] != line {
			return false
		}
	}
	return true
}

func pathFromDirective(line, prefix string) string {
	return strings.TrimSpace(strings.TrimPrefix(line, prefix))
}

// detectNewline returns the dominant line ending used by body. A file without
// any newline is treated as LF so new text uses the platform-neutral patch
// grammar rather than inheriting an arbitrary choice.
func detectNewline(body string) string {
	if strings.Contains(body, "\r\n") {
		return "\r\n"
	}
	return "\n"
}
