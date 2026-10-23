package mutatecore

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testPatchBindings(root string) PatchBindings {
	return PatchBindings{
		Resolve: func(raw string) (string, error) {
			return filepath.Join(root, filepath.FromSlash(raw)), nil
		},
		Display: func(abs string) string {
			relative, err := filepath.Rel(root, abs)
			if err != nil {
				return abs
			}
			return filepath.ToSlash(relative)
		},
		CreateParents: true,
		Mode:          DefaultFileMode,
	}
}

func TestApplyPatchAddUpdateDelete(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# demo\nTODO: fix me\n"), 0o644); err != nil {
		t.Fatalf("seed README: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatalf("seed src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("seed main.go: %v", err)
	}

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: added.txt",
		"+first",
		"+second",
		"*** Update File: README.md",
		"@@",
		" # demo",
		"-TODO: fix me",
		"+DONE",
		"*** Delete File: src/main.go",
		"*** End Patch",
	}, "\n")

	result, err := ApplyPatch(context.Background(), patch, testPatchBindings(root))
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if result.FilesChanged != 3 || result.Created != 1 || result.Updated != 1 || result.Deleted != 1 {
		t.Fatalf("unexpected summary: %+v", result)
	}
	assertMutationFile(t, filepath.Join(root, "added.txt"), "first\nsecond\n")
	assertMutationFile(t, filepath.Join(root, "README.md"), "# demo\nDONE\n")
	if _, err := os.Stat(filepath.Join(root, "src", "main.go")); !os.IsNotExist(err) {
		t.Fatalf("main.go should be deleted, got %v", err)
	}
}

func TestApplyPatchIsTransactional(t *testing.T) {
	root := t.TempDir()
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: created.txt",
		"+new",
		"*** Update File: missing.txt",
		"@@",
		"-old",
		"+new",
		"*** End Patch",
	}, "\n")

	_, err := ApplyPatch(context.Background(), patch, testPatchBindings(root))
	if CodeOf(err) != CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "created.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("transaction created a partial file: %v", statErr)
	}
}

// TestApplyPatchRollsBackCompletedWritesOnIOFailure forces the second write to
// fail after the first file was already written. The first file must be
// restored (or removed if it was newly created).
func TestApplyPatchRollsBackCompletedWritesOnIOFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "existing.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed existing: %v", err)
	}
	bindings := testPatchBindings(root)
	writeCalls := 0
	bindings.Write = func(abs string, data []byte, mode os.FileMode) error {
		writeCalls++
		if writeCalls == 2 {
			return &Error{Code: CodeInternal, Message: "injected write failure"}
		}
		return WriteAtomic(abs, data, mode)
	}
	patch := strings.Join([]string{
		PatchBegin,
		PatchUpdate + "existing.txt",
		"@@",
		"-old",
		"+new",
		PatchAdd + "created.txt",
		"+created",
		PatchEnd,
	}, "\n")
	_, err := ApplyPatch(context.Background(), patch, bindings)
	if err == nil {
		t.Fatal("expected injected failure")
	}
	assertMutationFile(t, filepath.Join(root, "existing.txt"), "old\n")
	if _, statErr := os.Stat(filepath.Join(root, "created.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("new file survived rollback: %v", statErr)
	}
}

func TestApplyPatchRollsBackDeleteOnIOFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "deleted.txt"), []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("seed deleted: %v", err)
	}
	bindings := testPatchBindings(root)
	removeCalls := 0
	bindings.Remove = func(abs string) error {
		removeCalls++
		if removeCalls == 1 {
			return os.ErrPermission
		}
		return os.Remove(abs)
	}
	patch := strings.Join([]string{
		PatchBegin,
		PatchDelete + "deleted.txt",
		PatchEnd,
	}, "\n")
	_, err := ApplyPatch(context.Background(), patch, bindings)
	if err == nil {
		t.Fatal("expected injected remove failure")
	}
	assertMutationFile(t, filepath.Join(root, "deleted.txt"), "keep\n")
}

func TestApplyPatchChecksDeletePermissionForDeleteAndMove(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "delete.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("seed delete: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "move.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("seed move: %v", err)
	}
	for _, tc := range []struct {
		name  string
		patch string
	}{
		{"delete", strings.Join([]string{PatchBegin, PatchDelete + "delete.txt", PatchEnd}, "\n")},
		{"move", strings.Join([]string{
			PatchBegin, PatchUpdate + "move.txt", PatchMoveTo + "moved.txt", PatchEnd,
		}, "\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bindings := testPatchBindings(root)
			deleteChecks := 0
			bindings.CheckDelete = func(string) error {
				deleteChecks++
				return os.ErrPermission
			}
			_, err := ApplyPatch(context.Background(), tc.patch, bindings)
			if err == nil {
				t.Fatal("expected delete permission denial")
			}
			if deleteChecks == 0 {
				t.Fatal("CheckDelete was not consulted")
			}
		})
	}
}

func TestApplyPatchPreservesCRLFAndBOM(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "encoded.txt")
	original := Encode("a\r\nb\r\n", UTF8BOM)
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatalf("seed encoded file: %v", err)
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: encoded.txt",
		"@@",
		"-a",
		"+A",
		"*** End Patch",
	}, "\n")

	result, err := ApplyPatch(context.Background(), patch, testPatchBindings(root))
	if err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	expected := Encode("A\r\nb\r\n", UTF8BOM)
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !bytes.Equal(stored, expected) {
		t.Fatalf("encoded patch bytes = %q, want %q", stored, expected)
	}
	if result.Files[0].BytesBefore != int64(len(original)) || result.Files[0].BytesAfter != int64(len(expected)) {
		t.Fatalf("unexpected byte accounting: %+v", result.Files[0])
	}
}

func TestApplyPatchMovesFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("body\n"), 0o644); err != nil {
		t.Fatalf("seed old file: %v", err)
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: old.txt",
		"*** Move to: nested/new.txt",
		"@@",
		" body",
		"*** End Patch",
	}, "\n")

	result, err := ApplyPatch(context.Background(), patch, testPatchBindings(root))
	if err != nil {
		t.Fatalf("ApplyPatch move: %v", err)
	}
	if result.FilesChanged != 2 || result.Moved != 1 || len(result.MutatedPaths) != 2 {
		t.Fatalf("unexpected move result: %+v", result)
	}
	if result.MutatedPaths[0] != "old.txt" || result.MutatedPaths[1] != "nested/new.txt" {
		t.Fatalf("unexpected moved paths: %v", result.MutatedPaths)
	}
	if _, err := os.Stat(filepath.Join(root, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("move source still exists: %v", err)
	}
	assertMutationFile(t, filepath.Join(root, "nested", "new.txt"), "body\n")
}

func TestApplyPatchStaleHunkDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("unchanged\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: file.txt",
		"@@",
		"-not present",
		"+replacement",
		"*** End Patch",
	}, "\n")

	_, err := ApplyPatch(context.Background(), patch, testPatchBindings(root))
	if !IsStaleContext(err) {
		t.Fatalf("expected STALE_CONTEXT, got %v", err)
	}
	assertMutationFile(t, path, "unchanged\n")
}

func TestApplyPatchMoveOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("body\n"), 0o644); err != nil {
		t.Fatalf("seed old file: %v", err)
	}
	patch := strings.Join([]string{
		PatchBegin,
		PatchUpdate + "old.txt",
		PatchMoveTo + "nested/new.txt",
		PatchEnd,
	}, "\n")

	result, err := ApplyPatch(context.Background(), patch, testPatchBindings(root))
	if err != nil {
		t.Fatalf("ApplyPatch move-only: %v", err)
	}
	if result.FilesChanged != 2 || result.Updated != 1 || result.Moved != 1 {
		t.Fatalf("unexpected move-only result: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("move source still exists: %v", err)
	}
	assertMutationFile(t, filepath.Join(root, "nested", "new.txt"), "body\n")
}

func TestApplyPatchRefusesExistingMoveTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "old.txt"), []byte("old\n"), 0o644); err != nil {
		t.Fatalf("seed old file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("target\n"), 0o644); err != nil {
		t.Fatalf("seed target file: %v", err)
	}
	patch := strings.Join([]string{
		PatchBegin,
		PatchUpdate + "old.txt",
		PatchMoveTo + "target.txt",
		PatchEnd,
	}, "\n")

	_, err := ApplyPatch(context.Background(), patch, testPatchBindings(root))
	if CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("expected INVALID_ARGUMENT, got %v", err)
	}
	assertMutationFile(t, filepath.Join(root, "old.txt"), "old\n")
	assertMutationFile(t, filepath.Join(root, "target.txt"), "target\n")
}

func TestApplyPatchEndOfFileAnchor(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("head\ntail\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	patch := strings.Join([]string{
		PatchBegin,
		PatchUpdate + "file.txt",
		"@@",
		"-tail",
		"+TAIL",
		PatchEOF,
		PatchEnd,
	}, "\n")

	if _, err := ApplyPatch(context.Background(), patch, testPatchBindings(root)); err != nil {
		t.Fatalf("ApplyPatch EOF anchor: %v", err)
	}
	assertMutationFile(t, path, "head\nTAIL\n")

	if err := os.WriteFile(path, []byte("tail\nhead\n"), 0o644); err != nil {
		t.Fatalf("reseed file: %v", err)
	}
	if _, err := ApplyPatch(context.Background(), patch, testPatchBindings(root)); !IsStaleContext(err) {
		t.Fatalf("expected STALE_CONTEXT for non-tail match, got %v", err)
	}
	assertMutationFile(t, path, "tail\nhead\n")
}

func assertMutationFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
