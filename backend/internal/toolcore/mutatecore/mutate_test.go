package mutatecore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritePreservesEncodingAndRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bom.txt")
	original := Encode("hello\n", UTF8BOM)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	result, err := Write(path, "hello world\n", WriteOptions{
		Mode:             DefaultFileMode,
		PreserveEncoding: true,
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if result.Encoding != UTF8BOM {
		t.Fatalf("encoding = %s, want %s", result.Encoding, UTF8BOM)
	}
	if result.OldRevision != Revision(true, "hello\n") {
		t.Fatalf("old revision = %q", result.OldRevision)
	}
	if result.NewRevision != Revision(true, "hello world\n") {
		t.Fatalf("new revision = %q", result.NewRevision)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !bytes.Equal(stored, Encode("hello world\n", UTF8BOM)) {
		t.Fatalf("stored bytes lost the BOM: %q", stored)
	}
}

func TestWriteExpectedRevisionReplayAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.txt")
	if err := os.WriteFile(path, []byte("one\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	state, err := Inspect(path, 0)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}

	replayed, err := Write(path, "one\n", WriteOptions{})
	if err != nil {
		t.Fatalf("idempotent Write: %v", err)
	}
	if !replayed.Replayed || replayed.Created {
		t.Fatalf("replay result = %+v", replayed)
	}

	_, err = Write(path, "two\n", WriteOptions{ExpectedRevision: strings.Repeat("0", 64)})
	if !IsPrecondition(err) {
		t.Fatalf("expected WRITE_PRECONDITION_FAILED, got %v", err)
	}

	if _, err := Write(path, "two\n", WriteOptions{ExpectedRevision: state.Revision}); err != nil {
		t.Fatalf("Write with current revision: %v", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(stored) != "two\n" {
		t.Fatalf("stored = %q", stored)
	}
}

func TestWriteRefusesBinaryOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blob.bin")
	if err := os.WriteFile(path, bytes.Repeat([]byte{0}, 16), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	_, err := Write(path, "text\n", WriteOptions{RefuseBinary: true})
	if CodeOf(err) != CodeUnsupported {
		t.Fatalf("expected UNSUPPORTED, got %v", err)
	}
}

func TestAppendPreservesEncodingAndOffsetReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "utf16.txt")
	original := Encode("alpha\n", UTF16LE)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	offset := int64(len(original))

	result, err := Append(context.Background(), path, "beta\n", AppendOptions{
		ExpectedOffset:   &offset,
		Mode:             DefaultFileMode,
		PreserveEncoding: true,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if result.Encoding != UTF16LE || result.SizeAfter != int64(len(Encode("alpha\nbeta\n", UTF16LE))) {
		t.Fatalf("unexpected append result: %+v", result)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !bytes.Equal(stored, Encode("alpha\nbeta\n", UTF16LE)) {
		t.Fatalf("append lost UTF-16 encoding: %q", stored)
	}

	replay, err := Append(context.Background(), path, "beta\n", AppendOptions{
		ExpectedOffset:   &offset,
		PreserveEncoding: true,
	})
	if err != nil {
		t.Fatalf("replay Append: %v", err)
	}
	if !replay.Replayed || replay.SizeAfter != result.SizeAfter {
		t.Fatalf("expected idempotent replay, got %+v", replay)
	}

	wrong := int64(1)
	_, err = Append(context.Background(), path, "gamma\n", AppendOptions{ExpectedOffset: &wrong})
	if !IsPrecondition(err) {
		t.Fatalf("expected WRITE_PRECONDITION_FAILED, got %v", err)
	}
}

func TestEditCRLFAndAmbiguity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "crlf.txt")
	if err := os.WriteFile(path, []byte("a\r\nb\r\nc\r\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	result, err := Edit(path, "b\nc", "B\nC", EditOptions{PreserveEncoding: true})
	if err != nil {
		t.Fatalf("Edit: %v", err)
	}
	if result.Replacements != 1 {
		t.Fatalf("replacements = %d", result.Replacements)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(stored) != "a\r\nB\r\nC\r\n" {
		t.Fatalf("CRLF round-trip = %q", stored)
	}

	ambiguous := filepath.Join(t.TempDir(), "ambiguous.txt")
	if err := os.WriteFile(ambiguous, []byte("x\nx\n"), 0o644); err != nil {
		t.Fatalf("seed ambiguous: %v", err)
	}
	_, err = Edit(ambiguous, "x", "y", EditOptions{})
	if CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT for ambiguity, got %v", err)
	}

	_, err = Edit(ambiguous, "missing", "y", EditOptions{})
	if !IsStaleContext(err) {
		t.Fatalf("expected STALE_CONTEXT, got %v", err)
	}
}

func TestEditReplaceAllAndCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "many.txt")
	if err := os.WriteFile(path, []byte("x x x\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	result, err := Edit(path, "x", "y", EditOptions{ReplacementCount: 2})
	if err != nil {
		t.Fatalf("Edit count: %v", err)
	}
	if result.Replacements != 2 || result.NewContent != "y y x\n" {
		t.Fatalf("unexpected count result: %+v", result)
	}
	result, err = Edit(path, "y", "z", EditOptions{ReplaceAll: true})
	if err != nil {
		t.Fatalf("Edit replace_all: %v", err)
	}
	if result.Replacements != 2 || result.NewContent != "z z x\n" {
		t.Fatalf("unexpected replace_all result: %+v", result)
	}
	if !strings.Contains(result.NewContent, "z z") {
		t.Fatalf("unexpected content: %q", result.NewContent)
	}
}
