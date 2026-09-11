package workspaceregistry

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	storePath := filepath.Join(t.TempDir(), "workspace_directories.yaml")
	return LoadFrom(storePath), storePath
}

func mustDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", name, err)
	}
	return dir
}

func TestNormalizePathCleansAndTrims(t *testing.T) {
	dir := mustDir(t, "demo")
	// Trailing separator + inner dot + surrounding spaces must collapse.
	messy := "  " + dir + string(filepath.Separator) + "." + string(filepath.Separator) + "  "
	cleaned, err := NormalizePath(messy)
	if err != nil {
		t.Fatalf("NormalizePath(%q): %v", messy, err)
	}
	if cleaned != filepath.Clean(dir) {
		t.Fatalf("expected %q, got %q", filepath.Clean(dir), cleaned)
	}
}

func TestNormalizePathRejectsEmptyAndRelative(t *testing.T) {
	for _, path := range []string{"", "   ", "relative/dir", "."} {
		if _, err := NormalizePath(path); !errors.Is(err, ErrValidationFailed) {
			t.Fatalf("NormalizePath(%q) error = %v, want ErrValidationFailed", path, err)
		}
	}
}

func TestNormalizePathMissingAndFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if _, err := NormalizePath(missing); !errors.Is(err, ErrDirectoryNotFound) {
		t.Fatalf("missing dir error = %v, want ErrDirectoryNotFound", err)
	}
	// Add must not create the directory either.
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("Add path must not create directories, stat err = %v", err)
	}

	file := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if _, err := NormalizePath(file); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("file error = %v, want ErrNotDirectory", err)
	}
}

func TestAddListRoundtripAndIdempotent(t *testing.T) {
	store, storePath := newTestStore(t)
	dir := mustDir(t, "proj")

	rec, existing, err := store.Add(dir, "proj alias")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if existing {
		t.Fatal("first Add must not be existing")
	}
	if rec.ID == "" || rec.Path != filepath.Clean(dir) || rec.Name != "proj alias" {
		t.Fatalf("unexpected record: %+v", rec)
	}
	if len(store.List()) != 1 {
		t.Fatalf("List len = %d, want 1", len(store.List()))
	}

	// Re-adding the same path is idempotent and returns the same record.
	same, existing, err := store.Add(dir+""+string(filepath.Separator)+".", "ignored")
	if err != nil {
		t.Fatalf("re-Add: %v", err)
	}
	if !existing || same.ID != rec.ID || same.Name != "proj alias" {
		t.Fatalf("re-Add existing=%v record=%+v want existing with original record", existing, same)
	}
	if store.Len() != 1 {
		t.Fatalf("Len = %d, want 1 after duplicate Add", store.Len())
	}

	// Persistence roundtrip: a fresh store over the same file sees the record.
	reloaded := LoadFrom(storePath)
	got, ok := reloaded.Get(rec.ID)
	if !ok || got.Path != rec.Path || got.Name != rec.Name {
		t.Fatalf("roundtrip Get = %+v ok=%v", got, ok)
	}
}

func TestAddWindowsCaseInsensitiveDedup(t *testing.T) {
	store, _ := newTestStore(t)
	dir := mustDir(t, "CaseDir")

	rec, _, err := store.Add(dir, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	var variant string
	if runtime.GOOS == "windows" {
		variant = strings.ToLower(dir)
	} else {
		variant = dir
	}
	again, existing, err := store.Add(variant, "")
	if err != nil {
		t.Fatalf("case-variant Add: %v", err)
	}
	if !existing {
		t.Fatalf("case-variant Add should be idempotent (variant=%q)", variant)
	}
	if again.ID != rec.ID {
		t.Fatalf("case-variant id = %q, want %q", again.ID, rec.ID)
	}
}

func TestRenameRemoveTouch(t *testing.T) {
	store, storePath := newTestStore(t)
	dir := mustDir(t, "rename-me")

	rec, _, err := store.Add(dir, "old")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	renamed, err := store.Rename(rec.ID, "new alias")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Name != "new alias" || renamed.Path != rec.Path {
		t.Fatalf("renamed record = %+v", renamed)
	}

	if err := store.Touch(rec.ID); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	touched, ok := LoadFrom(storePath).Get(rec.ID)
	if !ok || touched.LastUsedAt == 0 {
		t.Fatalf("Touch did not persist LastUsedAt: %+v ok=%v", touched, ok)
	}

	removed, found, err := store.Remove(rec.ID)
	if err != nil || !found || removed.ID != rec.ID {
		t.Fatalf("Remove = %+v found=%v err=%v", removed, found, err)
	}
	if store.Len() != 0 {
		t.Fatalf("Len = %d, want 0 after Remove", store.Len())
	}
	if _, found := store.Get(rec.ID); found {
		t.Fatal("Get after Remove must miss")
	}
}

func TestUnknownIDErrors(t *testing.T) {
	store, _ := newTestStore(t)
	if _, err := store.Rename("nope", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Rename unknown = %v, want ErrNotFound", err)
	}
	if err := store.Touch("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Touch unknown = %v, want ErrNotFound", err)
	}
	if _, found, err := store.Remove("nope"); err != nil || found {
		t.Fatalf("Remove unknown = found=%v err=%v, want found=false err=nil", found, err)
	}
}

func TestNoHomeStoreFailsClosed(t *testing.T) {
	store := LoadFrom("   ")
	if store.Path() != "" {
		t.Fatalf("empty path store Path = %q", store.Path())
	}
	if got := store.List(); len(got) != 0 {
		t.Fatalf("no-home List = %v, want empty", got)
	}
	dir := mustDir(t, "nohome")
	if _, _, err := store.Add(dir, ""); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("no-home Add = %v, want ErrStoreUnavailable", err)
	}
	if err := store.Touch("x"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("no-home Touch = %v, want ErrStoreUnavailable", err)
	}
	if _, err := store.Rename("x", "y"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("no-home Rename = %v, want ErrStoreUnavailable", err)
	}
}

func TestCorruptedDocumentFailsOpenEmpty(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "workspace_directories.yaml")
	if err := os.WriteFile(storePath, []byte(":::: not yaml ["), 0o600); err != nil {
		t.Fatalf("write corrupt doc: %v", err)
	}
	store := LoadFrom(storePath)
	if len(store.List()) != 0 {
		t.Fatalf("corrupted doc List = %v, want empty", store.List())
	}
	// A subsequent Add rewrites a valid document.
	dir := mustDir(t, "recovery")
	if _, _, err := store.Add(dir, ""); err != nil {
		t.Fatalf("Add after corruption: %v", err)
	}
	if len(LoadFrom(storePath).List()) != 1 {
		t.Fatal("expected persisted record after recovery Add")
	}
}

func TestPathMatches(t *testing.T) {
	dir := mustDir(t, "match")
	if !PathMatches(dir, dir+string(filepath.Separator)+".") {
		t.Fatal("cleaned equivalents must match")
	}
	if PathMatches("", dir) || PathMatches(dir, "") {
		t.Fatal("empty paths must never match")
	}
	if PathMatches(dir, dir+"_other") {
		t.Fatal("distinct paths must not match")
	}
	if runtime.GOOS == "windows" && !PathMatches(dir, strings.ToLower(dir)) {
		t.Fatal("windows compare must fold case")
	}
}

func TestFindByPath(t *testing.T) {
	store, _ := newTestStore(t)
	dir := mustDir(t, "find-me")
	rec, _, err := store.Add(dir, "")
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	got, ok := store.FindByPath(dir)
	if !ok || got.ID != rec.ID {
		t.Fatalf("FindByPath = %+v ok=%v", got, ok)
	}
	if _, ok := store.FindByPath(filepath.Dir(dir)); ok {
		t.Fatal("parent dir must not match")
	}
}
