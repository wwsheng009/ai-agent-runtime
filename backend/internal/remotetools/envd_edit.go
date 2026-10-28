package remotetools

// envd_edit.go adds the in-place content edits to the envd backend.
//
// The semantics are NOT re-implemented here: matching, the CRLF/whitespace
// ladder, the ambiguity guard and the stale-context diagnostic all come from
// mutatecore's pure functions (MatchEditStrings, EditOccurrenceLineNumbers,
// BuildEditOldStringNotFoundError, FindClosestEditSnippetWithLine, Decode,
// Encode, UnifiedDiff). Only the I/O half is envd-specific — the file is read
// through the target, edited in this process, and written back through the
// target.
//
// Why not call mutatecore.Edit directly: it opens the path with os.Stat /
// os.ReadFile, which on this backend would touch the LOCAL filesystem — exactly
// the bypass the design warns about. That is also why these tools must resolve
// through the jail before any read: a read-modify-write that skipped the jail
// would overwrite a file outside the workspace using bytes read from inside it.
//
// Known limit, stated rather than silent: content travels as one argv element,
// so it cannot carry NUL bytes. UTF-16 / BOM files therefore refuse to edit
// here (the local backend can round-trip them via WriteAtomic); the caller is
// told to use write or shell instead.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/mutatecore"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// envdEditMaxFileBytes bounds one read-modify-write. It reuses the shared cap so
// a file editable over ssh is editable over envd, and past it the recovery route
// is a streaming tool rather than a bigger buffer.
const envdEditMaxFileBytes = mutatecore.EditMaxFileBytes

// envdFileState is the backend's equivalent of mutatecore.State, built from
// envd primitives instead of the local filesystem.
type envdFileState struct {
	exists bool
	size   int64
	raw    []byte
	text   string
	enc    mutatecore.Encoding
}

// load reads one jailed path through envd: Stat for the size guard, then cat for
// the bytes. A missing file is a valid new-file state, mirroring Inspect.
func (be *envdBackend) load(ctx context.Context, target string, maxBytes int64) (*envdFileState, *toolkit.ToolResult) {
	state := &envdFileState{enc: mutatecore.UTF8}
	entry, err := be.client.Stat(ctx, target)
	if err != nil {
		var connectErr *envdclient.ConnectError
		if errors.As(err, &connectErr) && connectErr.IsNotFound() {
			return state, nil
		}
		return nil, envdErrorResult("%s", describeEnvDError(err, target))
	}
	if entry.IsDir() {
		return nil, envdErrorResult("target is a directory: %s", target)
	}
	size := entry.SizeBytes()
	if maxBytes > 0 && size > maxBytes {
		return nil, envdErrorResult("file size %d exceeds limit %d: %s (split the file or raise the configured limit)", size, maxBytes, target)
	}
	stdout, stderr, code, err := be.run(ctx, "/bin/cat", []string{target})
	if err != nil {
		return nil, envdErrorResult("read %s: %v", target, err)
	}
	if code != 0 {
		return nil, envdErrorResult("read %s failed (exit %d): %s", target, code, strings.TrimSpace(string(stderr)))
	}
	state.exists = true
	state.size = size
	state.raw = stdout
	state.text, state.enc = mutatecore.Decode(stdout)
	return state, nil
}

// store writes text back through envd, then confirms the landed byte count.
//
// The NUL check is a property of the transport, not a policy: argv cannot carry
// a NUL byte, so a UTF-16 round-trip would silently corrupt. The refusal names
// the reason and the alternative.
func (be *envdBackend) store(ctx context.Context, target, text string, enc mutatecore.Encoding, preserveEncoding bool) (int64, *toolkit.ToolResult) {
	writeEncoding := mutatecore.UTF8
	if preserveEncoding {
		writeEncoding = enc
	}
	encoded := mutatecore.Encode(text, writeEncoding)
	sizeAfter, refusal := be.storeBytes(ctx, target, encoded, false)
	if refusal != nil {
		return 0, refusal
	}
	return sizeAfter, nil
}

// envdEditParams reads one string parameter that must be present verbatim.
// Unlike envdStringParam it does not trim: old_string and content are data.
func envdParamString(params map[string]interface{}, keys ...string) (string, bool) {
	for _, key := range keys {
		if value, ok := params[key].(string); ok {
			return value, true
		}
	}
	return "", false
}

func envdParamBool(params map[string]interface{}, key string) bool {
	value, _ := params[key].(bool)
	return value
}

func envdParamInt(params map[string]interface{}, key string) (int, bool) {
	switch number := params[key].(type) {
	case float64:
		return int(number), true
	case int:
		return number, true
	default:
		return 0, false
	}
}

// envdEditToolSpecs publishes the shared mutation schemas verbatim: one tool has
// one contract across surfaces, so a parameter list is never forked per backend.
func envdEditToolSpecs(base remoteToolBase) []envdToolSpec {
	return []envdToolSpec{
		{
			name:        ToolEdit,
			description: base.describe("Replace exact text in a remote file with an ambiguity guard."),
			version:     "1.1.0",
			parameters:  mutatecore.EditParameters(),
			call:        envdEdit,
		},
		{
			name:        ToolAppend,
			description: base.describe("Append text to the end of a remote file."),
			version:     "1.1.0",
			parameters:  mutatecore.AppendParameters(),
			call:        envdAppend,
		},
	}
}

// envdEdit replaces exact text. Ambiguous matches are refused unless the caller
// explicitly picks replace_all or a replacement_count — the guard, the wording
// and the line numbers all come from the shared edit layer.
func envdEdit(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	raw := envdStringParam(params, "file_path", "path")
	if raw == "" {
		return envdErrorResult("file_path is required"), nil
	}
	oldString, hasOld := envdParamString(params, "old_string")
	if !hasOld || oldString == "" {
		return envdErrorResult("old_string is required"), nil
	}
	newString, hasNew := envdParamString(params, "new_string")
	if !hasNew {
		return envdErrorResult("new_string is required"), nil
	}
	replaceAll := envdParamBool(params, "replace_all")
	countValue, countPresent := envdParamInt(params, "replacement_count")
	if replaceAll && countPresent && countValue > 0 {
		return envdErrorResult("replace_all and replacement_count are mutually exclusive"), nil
	}
	if countPresent && countValue < 0 {
		return envdErrorResult("replacement_count must be greater than 0"), nil
	}

	target, err := be.resolvePath(ctx, raw)
	if err != nil {
		return envdErrorResult("%v", err), nil
	}
	state, refusal := be.load(ctx, target, envdEditMaxFileBytes)
	if refusal != nil {
		return refusal, nil
	}
	if !state.exists {
		return envdErrorResult("file not found: %s (verify the path with view or ls)", target), nil
	}
	if mutatecore.LooksBinary(state.raw) {
		return envdErrorResult("target looks binary: %s (edit only accepts text files)", target), nil
	}

	text := state.text
	matchedOld, matchedNew, matched := mutatecore.MatchEditStrings(text, oldString, newString)
	if !matched {
		diagnostic := mutatecore.BuildEditOldStringNotFoundError(text, oldString)
		snippet, startLine := mutatecore.FindClosestEditSnippetWithLine(
			mutatecore.NormalizeLineEndings(text), mutatecore.NormalizeLineEndings(oldString),
		)
		if snippet != "" && startLine > 0 {
			diagnostic += fmt.Sprintf("\n最接近的当前内容（第 %d 行附近，可直接据此重建 old_string）:\n%s",
				startLine, mutatecore.FormatEditClosestLines(snippet, startLine))
		}
		return envdErrorResult("%s\nnext_action: %s", diagnostic, mutatecore.StaleEditContextNextAction()), nil
	}

	occurrences := strings.Count(text, matchedOld)
	replacements := 1
	switch {
	case replaceAll:
		replacements = occurrences
	case countPresent && countValue > 0:
		replacements = countValue
		if replacements > occurrences {
			replacements = occurrences
		}
	case occurrences > 1:
		lines := mutatecore.EditOccurrenceLineNumbers(text, matchedOld, mutatecore.EditAmbiguousLineSample)
		return envdErrorResult("ambiguous edit: old_string 命中 %d 处（行 %s），未做替换。请增加上下文让 old_string 唯一，或显式传 replace_all=true / replacement_count=N。\nnext_action: ambiguous old_string: extend old_string with surrounding context until it is unique, or pass replace_all=true / replacement_count=N explicitly. Do not retry the same ambiguous old_string unchanged.",
			occurrences, mutatecore.FormatEditOccurrenceLines(lines, occurrences)), nil
	}

	var updated string
	switch {
	case replaceAll:
		updated = strings.ReplaceAll(text, matchedOld, matchedNew)
	case countPresent && countValue > 0:
		updated = strings.Replace(text, matchedOld, matchedNew, replacements)
	default:
		updated = strings.Replace(text, matchedOld, matchedNew, 1)
	}
	if updated == text {
		return envdTextResult(fmt.Sprintf("no change: old_string and new_string are identical in %s", target)), nil
	}

	sizeAfter, refusal := be.store(ctx, target, updated, state.enc, true)
	if refusal != nil {
		return refusal, nil
	}
	diff := mutatecore.UnifiedDiff(be.workspaceRelative(target), text, updated)
	summary := fmt.Sprintf("Edited %s: %d replacement(s) in %s, %d bytes on disk",
		target, replacements, be.workspaceRelative(target), sizeAfter)
	return envdTextResult(mutatecore.FormatEditSuccessContent(summary, diff)), nil
}

// envdAppend adds text to the end of a file. Replay detection and the
// expected_offset precondition are kept: a model that retries an append after a
// dropped connection must not double-write.
func envdAppend(ctx context.Context, be *envdBackend, params map[string]interface{}) (*toolkit.ToolResult, error) {
	raw := envdStringParam(params, "file_path", "path")
	if raw == "" {
		return envdErrorResult("file_path is required"), nil
	}
	content, hasContent := envdParamString(params, "content")
	if !hasContent {
		return envdErrorResult("content is required"), nil
	}
	createIfMissing, hasCreateIfMissing := params["create_if_missing"].(bool)
	if !hasCreateIfMissing {
		// The shared contract defaults this to true; an absent parameter must not
		// become a stricter backend than the ssh surface.
		createIfMissing = true
	}
	truncateFirst := envdParamBool(params, "truncate_first")
	ensureTrailingNewline := envdParamBool(params, "ensure_trailing_newline")
	expectedOffset, hasExpectedOffset := envdParamInt(params, "expected_offset")

	if ensureTrailingNewline && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	target, err := be.resolvePath(ctx, raw)
	if err != nil {
		return envdErrorResult("%v", err), nil
	}
	state, refusal := be.load(ctx, target, envdEditMaxFileBytes)
	if refusal != nil {
		return refusal, nil
	}
	if !state.exists && !truncateFirst && !createIfMissing {
		return envdErrorResult("file does not exist: %s (pass create_if_missing=true or use write)", target), nil
	}
	if state.exists && mutatecore.LooksBinary(state.raw) {
		return envdErrorResult("target looks binary: %s (use a binary-aware tool or remove the file first)", target), nil
	}

	if truncateFirst && state.exists && state.text == content {
		return envdTextResult(fmt.Sprintf("append replayed (truncate_first): %s already holds exactly this first chunk (%d bytes); no write performed", target, state.size)), nil
	}
	if !truncateFirst && hasExpectedOffset && state.exists {
		if expectedOffset < 0 {
			return envdErrorResult("expected_offset must be non-negative"), nil
		}
		replayedSize := int64(expectedOffset + len(content))
		if state.size == replayedSize && int64(expectedOffset) <= state.size &&
			string(state.raw[expectedOffset:]) == content {
			return envdTextResult(fmt.Sprintf("append replayed: %s already contains this chunk at offset %d; size stays %d bytes", target, expectedOffset, state.size)), nil
		}
		if state.size != int64(expectedOffset) {
			return envdErrorResult("append precondition failed: current offset is %d, expected %d (next: re-read the file and retry with the current offset)",
				state.size, expectedOffset), nil
		}
	}

	action := "append"
	newText := content
	switch {
	case truncateFirst:
		if state.exists {
			action = "overwrite_first_chunk"
		} else {
			action = "create_first_chunk"
		}
	case state.exists:
		newText = state.text + content
	}

	sizeAfter, refusal := be.store(ctx, target, newText, state.enc, state.exists)
	if refusal != nil {
		return refusal, nil
	}
	return envdTextResult(fmt.Sprintf("%s: appended %d bytes to %s (%d bytes on disk)", action, len(content), target, sizeAfter)), nil
}
