package mutatecore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RevisionAbsent is the revision of a path that does not exist yet.
const RevisionAbsent = "absent"

// State is one file's pre-mutation snapshot.
type State struct {
	Path     string
	Exists   bool
	Mode     os.FileMode
	Size     int64
	Raw      []byte
	Text     string
	Encoding Encoding
	Revision string
}

// Inspect reads an existing regular file. A missing path is a valid new-file
// state. Directories and special files are refused before any read.
func Inspect(path string, maxBytes int64) (State, error) {
	state := State{Path: path, Revision: RevisionAbsent}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, newError(CodeInvalidArgument, "check the path and permissions", fmt.Errorf("stat %s: %w", path, err))
	}
	state.Exists = true
	state.Mode = info.Mode()
	state.Size = info.Size()
	if info.IsDir() {
		return state, errorf(CodeInvalidArgument, "pass a file path", "target is a directory: %s", path)
	}
	if !info.Mode().IsRegular() {
		return state, errorf(CodeUnsupported, "pass a regular file", "target is not a regular file (%s): %s", fileModeReason(info.Mode()), path)
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return state, &Error{
			Code:       CodeUnsupported,
			Message:    fmt.Sprintf("file size %d exceeds limit %d: %s", info.Size(), maxBytes, path),
			NextAction: "split the file or raise the configured limit",
			Metadata: map[string]any{
				"failure_class":  "file_too_large",
				"file_size":      info.Size(),
				"max_file_bytes": maxBytes,
			},
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return state, newError(CodeInvalidArgument, "check file permissions", fmt.Errorf("read %s: %w", path, err))
	}
	state.Raw = raw
	state.Text, state.Encoding = Decode(raw)
	state.Revision = Revision(true, state.Text)
	return state, nil
}

// Revision returns the stable content revision used by write preconditions.
// It is computed over decoded text so BOM/UTF-16 files can round-trip.
func Revision(exists bool, text string) string {
	if !exists {
		return RevisionAbsent
	}
	digest := sha256.Sum256([]byte(text))
	return hex.EncodeToString(digest[:])
}

// ValidRevision reports whether value is empty, absent, or a SHA-256 hex value.
func ValidRevision(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == RevisionAbsent {
		return true
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

type WriteOptions struct {
	ExpectedRevision string
	Mode             os.FileMode
	PreserveEncoding bool
	RefuseBinary     bool
	CreateParents    bool
	MaxBytes         int64
}

type WriteResult struct {
	Path        string
	Created     bool
	Replayed    bool
	OldExists   bool
	OldSize     int64
	NewSize     int64
	OldContent  string
	NewContent  string
	Encoding    Encoding
	OldRevision string
	NewRevision string
}

// Write replaces or creates one file. path must already be resolved and
// authorized by the adapter.
func Write(path, content string, opts WriteOptions) (WriteResult, error) {
	state, err := Inspect(path, opts.MaxBytes)
	if err != nil {
		return WriteResult{}, err
	}
	if opts.RefuseBinary && state.Exists && LooksBinary(state.Raw) {
		return WriteResult{}, errorf(CodeUnsupported, "use a binary-aware tool or remove the file first", "target looks binary: %s", path)
	}
	if opts.ExpectedRevision != "" && !ValidRevision(opts.ExpectedRevision) {
		return WriteResult{}, errorf(CodeInvalidArgument, "pass a 64-character SHA-256 or absent", "invalid expected revision %q", opts.ExpectedRevision)
	}
	expected := strings.ToLower(strings.TrimSpace(opts.ExpectedRevision))
	if state.Exists && state.Text == content {
		return WriteResult{
			Path:        path,
			OldExists:   true,
			OldSize:     state.Size,
			NewSize:     state.Size,
			OldContent:  state.Text,
			NewContent:  content,
			Encoding:    state.Encoding,
			OldRevision: state.Revision,
			NewRevision: state.Revision,
			Replayed:    true,
		}, nil
	}
	if expected != "" && expected != state.Revision {
		return WriteResult{}, &Error{
			Code:       CodePrecondition,
			Message:    fmt.Sprintf("write precondition failed: current revision is %s", state.Revision),
			NextAction: "re-read the file, then retry with the current revision",
			Actual:     state.Revision,
			Expected:   expected,
		}
	}
	if opts.CreateParents {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return WriteResult{}, newError(CodeInvalidArgument, "check the parent directory", fmt.Errorf("create parent directory: %w", err))
		}
	}
	encoding := UTF8
	if state.Exists && opts.PreserveEncoding {
		encoding = state.Encoding
	}
	encoded := Encode(content, encoding)
	if err := WriteAtomic(path, encoded, opts.Mode); err != nil {
		return WriteResult{}, err
	}
	return WriteResult{
		Path:        path,
		Created:     !state.Exists,
		OldExists:   state.Exists,
		OldSize:     state.Size,
		NewSize:     int64(len(encoded)),
		OldContent:  state.Text,
		NewContent:  content,
		Encoding:    encoding,
		OldRevision: state.Revision,
		NewRevision: Revision(true, content),
	}, nil
}

type AppendOptions struct {
	TruncateFirst         bool
	EnsureTrailingNewline bool
	ExpectedOffset        *int64
	CreateIfMissing       bool
	Mode                  os.FileMode
	PreserveEncoding      bool
	RefuseBinary          bool
	CreateParents         bool
	MaxBytes              int64
}

type AppendResult struct {
	Path         string
	Action       string
	Created      bool
	Replayed     bool
	Truncated    bool
	BytesWritten int64
	SizeBefore   int64
	SizeAfter    int64
	OldContent   string
	NewContent   string
	Encoding     Encoding
}

// Append appends one chunk, optionally truncating first. path must already be
// resolved and authorized by the adapter.
func Append(ctx context.Context, path, content string, opts AppendOptions) (AppendResult, error) {
	if err := contextError(ctx); err != nil {
		return AppendResult{}, err
	}
	if opts.EnsureTrailingNewline && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	state, err := Inspect(path, opts.MaxBytes)
	if err != nil {
		return AppendResult{}, err
	}
	if state.Exists && opts.RefuseBinary && LooksBinary(state.Raw) {
		return AppendResult{}, errorf(CodeUnsupported, "use a binary-aware tool or remove the file first", "target looks binary: %s", path)
	}
	if !state.Exists && !opts.TruncateFirst && !opts.CreateIfMissing {
		return AppendResult{}, errorf(CodeNotFound, "pass create_if_missing=true or use write", "file does not exist: %s", path)
	}
	encoding := UTF8
	if state.Exists && opts.PreserveEncoding {
		encoding = state.Encoding
	}
	// The file BOM is written once for the whole payload. The append chunk is
	// compared and counted without its own BOM so replay detection matches the
	// bytes that actually follow the existing content.
	_, chunk := DetectEncoding(Encode(content, encoding))
	if opts.TruncateFirst && state.Exists && state.Text == content {
		return AppendResult{Path: path, Action: "truncate_replay", Replayed: true, Truncated: true, SizeBefore: state.Size, SizeAfter: state.Size, OldContent: state.Text, NewContent: state.Text, Encoding: state.Encoding}, nil
	}
	if !opts.TruncateFirst && opts.ExpectedOffset != nil {
		expected := *opts.ExpectedOffset
		if expected < 0 {
			return AppendResult{}, errorf(CodeInvalidArgument, "pass a non-negative expected_offset", "expected_offset must be non-negative")
		}
		replayedSize := expected + int64(len(chunk))
		if state.Exists && state.Size == replayedSize && expected <= state.Size &&
			bytes.Equal(state.Raw[int(expected):], chunk) {
			return AppendResult{Path: path, Action: "append_replay", Replayed: true, SizeBefore: state.Size, SizeAfter: state.Size, OldContent: state.Text, NewContent: state.Text, Encoding: state.Encoding}, nil
		}
		if state.Size != expected {
			return AppendResult{}, &Error{
				Code:       CodePrecondition,
				Message:    fmt.Sprintf("append precondition failed: current offset is %d", state.Size),
				NextAction: "re-read the file and retry with the current offset",
				Actual:     fmt.Sprint(state.Size),
				Expected:   fmt.Sprint(expected),
			}
		}
	}
	if opts.CreateParents {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return AppendResult{}, newError(CodeInvalidArgument, "check the parent directory", fmt.Errorf("create parent directory: %w", err))
		}
	}

	newText := content
	action := "append"
	if opts.TruncateFirst {
		action = "overwrite_first_chunk"
		if !state.Exists {
			action = "create_first_chunk"
		}
	} else if state.Exists {
		newText = state.Text + content
	}
	encoded := Encode(newText, encoding)
	if err := WriteAtomic(path, encoded, opts.Mode); err != nil {
		return AppendResult{}, err
	}
	return AppendResult{
		Path:         path,
		Action:       action,
		Created:      !state.Exists,
		Truncated:    opts.TruncateFirst,
		BytesWritten: int64(len(chunk)),
		SizeBefore:   state.Size,
		SizeAfter:    int64(len(encoded)),
		OldContent:   state.Text,
		NewContent:   newText,
		Encoding:     encoding,
	}, nil
}

type EditOptions struct {
	ReplaceAll       bool
	ReplacementCount int
	Mode             os.FileMode
	PreserveEncoding bool
	MaxBytes         int64
}

type EditResult struct {
	Path         string
	Replacements int
	// Occurrences is how many times the matched text appears in the file. A
	// caller that asked for the first N of several wants to know the total.
	Occurrences int
	// MatchedNew is the replacement text as it was written, including the file's
	// own line-ending style. A caller that wants to show the edited region uses
	// it to locate the change in the new content.
	MatchedNew string
	OldContent string
	NewContent string
	Encoding   Encoding
	SizeBefore int64
	SizeAfter  int64
}

// EditMaxFileBytes bounds the file an in-place edit will read. Past it the
// recovery route is a streaming tool, not a bigger buffer.
const EditMaxFileBytes = 10 * 1024 * 1024

// Edit replaces an exact string. Ambiguous matches are refused unless the
// caller explicitly chooses replace_all or a replacement_count.
func Edit(path, oldString, newString string, opts EditOptions) (EditResult, error) {
	if oldString == "" {
		return EditResult{}, errorf(CodeInvalidArgument, "pass a non-empty old_string", "old_string is required")
	}
	if opts.ReplaceAll && opts.ReplacementCount > 0 {
		return EditResult{}, errorf(CodeInvalidArgument, "choose replace_all or replacement_count", "replace_all and replacement_count are mutually exclusive")
	}
	if opts.ReplacementCount < 0 {
		return EditResult{}, errorf(CodeInvalidArgument, "pass a positive replacement_count", "replacement_count must be greater than 0")
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = EditMaxFileBytes
	}
	state, err := Inspect(path, opts.MaxBytes)
	if err != nil {
		return EditResult{}, err
	}
	if !state.Exists {
		return EditResult{}, errorf(CodeNotFound, "verify the path with view or ls", "file not found: %s", path)
	}
	if LooksBinary(state.Raw) {
		return EditResult{}, errorf(CodeUnsupported, "edit only accepts text files", "target looks binary: %s", path)
	}
	text := state.Text
	matchedOld, matchedNew, matched := MatchEditStrings(text, oldString, newString)
	if !matched {
		// The model's old_string is not in the file even after the recovery
		// ladder. Returning the closest current lines lets it rebuild the value
		// without another read round-trip.
		diagnostic := BuildEditOldStringNotFoundError(text, oldString)
		metadata := map[string]any{"file_path": path, "failure_class": "stale_context"}
		if snippet, startLine := FindClosestEditSnippetWithLine(
			NormalizeLineEndings(text), NormalizeLineEndings(oldString),
		); snippet != "" && startLine > 0 {
			metadata["suggested_view_offset"] = startLine - 1
			metadata["suggested_view_limit"] = 40
			metadata["current_snippet"] = snippet
			metadata["current_snippet_start_line"] = startLine
		}
		return EditResult{}, &Error{
			Code:       CodeStaleContext,
			Message:    diagnostic,
			NextAction: StaleEditContextNextAction(),
			Metadata:   metadata,
		}
	}
	count := strings.Count(text, matchedOld)
	occurrences := count
	replacements := 1
	if opts.ReplaceAll {
		replacements = count
	} else if opts.ReplacementCount > 0 {
		replacements = opts.ReplacementCount
		if replacements > count {
			replacements = count
		}
	} else if count > 1 {
		lines := EditOccurrenceLineNumbers(text, matchedOld, EditAmbiguousLineSample)
		return EditResult{}, &Error{
			Code: CodeInvalidArgument,
			Message: fmt.Sprintf(
				"ambiguous edit: old_string 命中 %d 处（行 %s），未做替换。请增加上下文让 old_string 唯一，或显式传 replace_all=true / replacement_count=N。",
				count, FormatEditOccurrenceLines(lines, count),
			),
			NextAction: "ambiguous old_string: extend old_string with surrounding context until it is unique, or pass replace_all=true / replacement_count=N explicitly. Do not retry the same ambiguous old_string unchanged.",
			Metadata: map[string]any{
				"file_path":        path,
				"failure_class":    "ambiguous_edit",
				"occurrences":      count,
				"occurrence_lines": lines,
			},
		}
	}
	updated := text
	if opts.ReplaceAll {
		updated = strings.ReplaceAll(text, matchedOld, matchedNew)
	} else if opts.ReplacementCount > 0 {
		updated = strings.Replace(text, matchedOld, matchedNew, replacements)
	} else {
		updated = strings.Replace(text, matchedOld, matchedNew, 1)
	}
	encoding := state.Encoding
	if !opts.PreserveEncoding {
		encoding = UTF8
	}
	encoded := Encode(updated, encoding)
	if updated == text {
		return EditResult{Path: path, Replacements: replacements, Occurrences: occurrences, MatchedNew: matchedNew, OldContent: text, NewContent: updated, Encoding: encoding, SizeBefore: state.Size, SizeAfter: state.Size}, nil
	}
	if err := WriteAtomic(path, encoded, opts.Mode); err != nil {
		return EditResult{}, err
	}
	return EditResult{
		Path:         path,
		Replacements: replacements,
		Occurrences:  occurrences,
		MatchedNew:   matchedNew,
		OldContent:   text,
		NewContent:   updated,
		Encoding:     encoding,
		SizeBefore:   state.Size,
		SizeAfter:    int64(len(encoded)),
	}, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return newError("CANCELLED", "retry the operation if it is still needed", ctx.Err())
	default:
		return nil
	}
}

func fileModeReason(mode os.FileMode) string {
	switch {
	case mode&os.ModeCharDevice != 0:
		return "character device"
	case mode&os.ModeDevice != 0:
		return "device"
	case mode&os.ModeNamedPipe != 0:
		return "named pipe"
	case mode&os.ModeSocket != 0:
		return "socket"
	case mode&os.ModeSymlink != 0:
		return "symbolic link"
	default:
		return "special file"
	}
}
