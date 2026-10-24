package mutatecore

import (
	"context"
	"errors"
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
	CheckDelete func(abs string) error
	// PathHint is optional and explains a missing target the way the surface
	// always has (nearby candidate paths). It receives the spelling the caller
	// used, exactly like the other file tools' hint seam.
	PathHint      func(raw string) string
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
	// MoveTo is the display spelling of the move target; MovePath is the
	// resolved one a surface needs to key its ledger on.
	MoveTo      string
	MovePath    string
	BytesBefore int64
	BytesAfter  int64
	WasNew      bool
	WasMoved    bool
	// NewBytes is the post-image the write left on disk, or nil when the
	// operation removed the file. Surfaces that maintain a read/write ledger or
	// a change feed need it per path, and re-reading the file to get it back
	// would race the next writer.
	NewBytes []byte
}

type PatchResult struct {
	FilesChanged int
	MutatedPaths []string
	Created      int
	Updated      int
	Deleted      int
	Moved        int
	// Diff is the unified diff of everything the patch changed, in the same
	// "a/… b/…" form edit publishes. Callers render it; an empty diff means the
	// patch produced no textual change.
	Diff  string
	Files []PatchFileResult
}

// PatchHunkError carries the structured data a surface needs to render its own
// recovery diagnostic (expected lines, current file lines, closest window).
// Error() stays short and stable for non-interactive consumers.
type PatchHunkError struct {
	Code       string
	Message    string
	NextAction string
	HunkIndex  int
	Header     string
	// Path is the display path of the file whose hunk missed, stamped by
	// ApplyPatch so the diagnostic can name the file it is talking about.
	Path      string
	Expected  []string
	Actual    []string
	Closest   []string
	StartLine int
}

func (e *PatchHunkError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
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
				return PatchResult{}, missingTargetError(bindings, op.Path, "文件不存在，无法删除", "verify the path with ls")
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
				return PatchResult{}, missingTargetError(bindings, op.Path, "文件不存在，无法更新", "verify the path with view or ls")
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
					// Name the file the diagnostic is about. Without this the
					// caller sees line numbers and a snippet but has to guess
					// which of the patch's targets they belong to.
					var hunkErr *PatchHunkError
					if errors.As(applyErr, &hunkErr) {
						hunkErr.Path = item.display
					}
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
			MovePath:    item.moveAbs,
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
			fileResult.NewBytes = encoded
			// An add has no old side: the diff says /dev/null, not "empty file".
			var before *string
			if !item.wasNew {
				old := item.oldBody
				before = &old
			}
			body := item.newBody
			result.Diff += UnifiedDiffStates(item.absPath, before, &body)
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
				// A move shows twice: the source loses its body, the target
				// gains it. Publishing only one half reads as a delete.
				result.Diff += UnifiedDiffStates(item.moveAbs, nil, &item.newBody)
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
			result.Diff += UnifiedDiffStates(item.absPath, &item.oldBody, nil)
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

// missingTargetError reports a target the patch requires but the workspace does
// not have, in the same shape the other local file tools use: the prefix names
// the operation, and the surface's candidate hint (when it has one) follows on
// its own line so a typo is usually self-correcting.
func missingTargetError(bindings PatchBindings, rawPath, prefix, nextAction string) error {
	hint := ""
	if bindings.PathHint != nil {
		hint = bindings.PathHint(rawPath)
	}
	return &Error{
		Code:       CodeNotFound,
		NextAction: nextAction,
		Message:    PatchNotFoundMessage(prefix, rawPath, hint),
	}
}

// ParsePatch parses the Codex patch grammar.
// ApplyHunks applies @@ blocks. Matching uses the shared recovery ladder
// (exact, trailing-space, trim, punctuation/whitespace collapse), supports a
// leading @@ context anchor, EOF anchoring, and blank-run drift. Each old-side
// block must match exactly once; ambiguity is an error.
func ApplyHunks(body string, hunks []PatchHunk) (string, error) {
	if len(hunks) == 0 {
		return "", errorf(CodeInvalidArgument, "include at least one @@ block", "update block has no hunks")
	}
	newline := detectNewline(body)
	normalized := body
	if newline == "\r\n" {
		normalized = strings.ReplaceAll(normalized, "\r\n", "\n")
	}
	lines, trailingNewline := splitPatchBody(normalized)
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
		searchCursor := cursor
		if contextLine := HunkContextLine(hunk.Header); contextLine != "" {
			if contextStart := locateHunk(lines, []string{contextLine}, searchCursor, false); contextStart >= 0 {
				searchCursor = contextStart + 1
			}
		}
		position := locateHunk(lines, oldLines, searchCursor, hunk.EndOfFile)
		matchedOld := oldLines
		matchedEnd := -1
		if position < 0 {
			if start, end, ok := locateBlankRunSpan(lines, oldLines); ok {
				position = start
				matchedEnd = end
			}
		}
		occurrences := 0
		if position >= 0 {
			occurrences = 1
		}
		if occurrences == 0 {
			startLine, closest := ClosestPatchWindow(lines, oldLines)
			return "", &PatchHunkError{
				Code:       CodeStaleContext,
				Message:    PatchHunkDiagnostic(hunk.Header, oldLines, closest, startLine, PatchStaleReason),
				NextAction: PatchStaleContextNextAction(),
				HunkIndex:  index + 1,
				Header:     hunk.Header,
				Expected:   append([]string(nil), oldLines...),
				Actual:     append([]string(nil), lines...),
				Closest:    closest,
				StartLine:  startLine,
			}
		}
		replaceLines := newLines
		oldSpanLen := len(matchedOld)
		if matchedEnd >= position {
			replaceLines = rebuildBlankRunReplacement(lines[position:matchedEnd+1], matchedOld, newLines)
			oldSpanLen = matchedEnd - position + 1
		}
		replaced := make([]string, 0, len(lines)-oldSpanLen+len(replaceLines))
		replaced = append(replaced, lines[:position]...)
		replaced = append(replaced, replaceLines...)
		replaced = append(replaced, lines[position+oldSpanLen:]...)
		lines = replaced
		cursor = position + len(replaceLines)
		if hunk.EndOfFile {
			trailingNewline = true
		}
	}
	result := joinPatchBody(lines, trailingNewline, "\n")
	if newline == "\r\n" {
		result = strings.ReplaceAll(result, "\n", "\r\n")
	}
	return result, nil
}

func locateHunk(lines, expected []string, cursor int, eof bool) int {
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(lines) {
		cursor = len(lines)
	}
	if len(expected) == 0 {
		if eof {
			return len(lines)
		}
		return cursor
	}
	if len(expected) > len(lines) {
		return -1
	}
	maxStart := len(lines) - len(expected)
	if eof {
		tailStart := maxStart
		for _, matcher := range LineMatchers() {
			if SliceMatches(lines[tailStart:tailStart+len(expected)], expected, matcher) {
				return tailStart
			}
		}
		return -1
	}
	for _, matcher := range LineMatchers() {
		for _, searchRange := range hunkSearchRanges(cursor, maxStart) {
			for start := searchRange.start; start <= searchRange.end; start++ {
				if SliceMatches(lines[start:start+len(expected)], expected, matcher) {
					return start
				}
			}
		}
	}
	return -1
}

type hunkSearchRange struct {
	start int
	end   int
}

func hunkSearchRanges(cursor, maxStart int) []hunkSearchRange {
	if maxStart < 0 {
		return nil
	}
	if cursor > maxStart {
		cursor = maxStart + 1
	}
	ranges := make([]hunkSearchRange, 0, 2)
	if cursor <= maxStart {
		ranges = append(ranges, hunkSearchRange{start: cursor, end: maxStart})
	}
	if cursor > 0 {
		ranges = append(ranges, hunkSearchRange{start: 0, end: cursor - 1})
	}
	return ranges
}

// HunkContextLine extracts the model-supplied text after "@@" (Codex style) or
// after a unified-diff range. It returns "" for a bare "@@" or a pure range.
func HunkContextLine(header string) string {
	header = strings.TrimRight(header, "\r")
	if header == "@@" || !strings.HasPrefix(header, "@@") {
		return ""
	}
	rest := strings.TrimSpace(strings.TrimPrefix(header, "@@"))
	if strings.HasSuffix(rest, "@@") {
		rest = strings.TrimSpace(strings.TrimSuffix(rest, "@@"))
	}
	if rest == "" {
		return ""
	}
	parts := strings.Fields(rest)
	i := 0
	for i < len(parts) && isUnifiedRangePart(parts[i]) {
		i++
	}
	if i == 0 {
		return rest
	}
	if i < len(parts) && parts[i] == "@@" {
		i++
	}
	return strings.Join(parts[i:], " ")
}

func isUnifiedRangePart(part string) bool {
	if part == "" || (part[0] != '-' && part[0] != '+') {
		return false
	}
	body := part[1:]
	if body == "" {
		return false
	}
	comma := strings.IndexByte(body, ',')
	if comma < 0 {
		return allDigits(body)
	}
	return allDigits(body[:comma]) && allDigits(body[comma+1:])
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// locateBlankRunSpan finds the unique span whose non-blank lines match the
// expected non-blank lines under comparable normalization. Blank-run length may
// differ. It requires at least two significant lines to avoid over-healing.
func locateBlankRunSpan(lines, expected []string) (start, end int, ok bool) {
	expectedNB := NonBlankLines(expected)
	if len(expectedNB) < 2 {
		return 0, 0, false
	}
	type span struct{ start, end int }
	found := make([]span, 0, 1)
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		if NormalizeComparableLine(lines[i]) != NormalizeComparableLine(expectedNB[0]) {
			continue
		}
		cursor := i
		matched := true
		for k := 1; k < len(expectedNB); k++ {
			cursor++
			for cursor < len(lines) && strings.TrimSpace(lines[cursor]) == "" {
				cursor++
			}
			if cursor >= len(lines) ||
				NormalizeComparableLine(lines[cursor]) != NormalizeComparableLine(expectedNB[k]) {
				matched = false
				break
			}
		}
		if matched {
			found = append(found, span{start: i, end: cursor})
			if len(found) > 1 {
				return 0, 0, false
			}
		}
	}
	if len(found) != 1 {
		return 0, 0, false
	}
	return found[0].start, found[0].end, true
}

// rebuildBlankRunReplacement keeps the file's blank-run shape and exact bytes
// for unchanged non-blank lines, applying the model's changed bodies with the
// file's indentation.
func rebuildBlankRunReplacement(fileSpan, oldLines, newLines []string) []string {
	if len(newLines) == 0 {
		return nil
	}
	fileNB := NonBlankLines(fileSpan)
	oldNB := NonBlankLines(oldLines)
	newNB := NonBlankLines(newLines)
	if len(newNB) == 0 {
		return append([]string(nil), fileSpan...)
	}
	if len(newNB) != len(fileNB) || len(oldNB) != len(fileNB) {
		return append([]string(nil), newLines...)
	}
	out := make([]string, 0, len(fileSpan)+2)
	nbIdx := 0
	for _, fileLine := range fileSpan {
		if strings.TrimSpace(fileLine) == "" {
			out = append(out, fileLine)
			continue
		}
		if nbIdx >= len(newNB) {
			out = append(out, fileLine)
			continue
		}
		modelLine := newNB[nbIdx]
		if NormalizeComparableLine(modelLine) == NormalizeComparableLine(fileLine) ||
			NormalizeComparableLine(modelLine) == NormalizeComparableLine(oldNB[nbIdx]) {
			out = append(out, fileLine)
		} else {
			newWS := LeadingWhitespace(modelLine)
			body := strings.TrimPrefix(modelLine, newWS)
			oldWS := LeadingWhitespace(oldNB[nbIdx])
			fileWS := LeadingWhitespace(fileLine)
			rel := newWS
			if oldWS != "" && strings.HasPrefix(newWS, oldWS) {
				rel = strings.TrimPrefix(newWS, oldWS)
			} else if newWS == oldWS {
				rel = ""
			} else if oldWS == "" {
				rel = newWS
			} else {
				out = append(out, modelLine)
				nbIdx++
				continue
			}
			out = append(out, fileWS+rel+body)
		}
		nbIdx++
	}
	for ; nbIdx < len(newNB); nbIdx++ {
		line := newNB[nbIdx]
		if len(fileNB) > 0 {
			fileWS0 := LeadingWhitespace(fileNB[0])
			oldWS0 := ""
			if len(oldNB) > 0 {
				oldWS0 = LeadingWhitespace(oldNB[0])
			}
			newWS := LeadingWhitespace(line)
			if oldWS0 == "" && fileWS0 != "" && !strings.HasPrefix(newWS, fileWS0) {
				out = append(out, fileWS0+line)
				continue
			}
		}
		out = append(out, line)
	}
	return out
}

func splitPatchBody(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	trailing := strings.HasSuffix(content, "\n")
	if trailing {
		content = strings.TrimSuffix(content, "\n")
	}
	if content == "" {
		return []string{""}, trailing
	}
	return strings.Split(content, "\n"), trailing
}

func joinPatchBody(lines []string, trailing bool, newline string) string {
	if len(lines) == 0 {
		if trailing {
			return newline
		}
		return ""
	}
	joined := strings.Join(lines, newline)
	if trailing {
		joined += newline
	}
	return joined
}

func pathFromDirective(line, prefix string) string {
	return SanitizePatchPath(strings.TrimPrefix(line, prefix))
}

// SanitizePatchPath trims whitespace and common model punctuation that leaks
// into Add/Update/Delete headers (quotes, trailing sentence/JSON crumbs). It
// does not rewrite valid relative/absolute paths or intentional inner spaces.
func SanitizePatchPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if len(path) >= 2 {
		first, last := path[0], path[len(path)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') || (first == '`' && last == '`') {
			path = strings.TrimSpace(path[1 : len(path)-1])
		}
	}
	for {
		trimmed := strings.TrimRight(path, " \t\"'`.,;:)]}>")
		if trimmed == path {
			break
		}
		path = strings.TrimSpace(trimmed)
	}
	return strings.TrimSpace(strings.TrimLeft(path, " \t\"'`([{<,"))
}

func isPatchChangeLine(line string) bool {
	if line == "" {
		return true
	}
	switch line[0] {
	case ' ', '+', '-':
		return true
	default:
		return false
	}
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
