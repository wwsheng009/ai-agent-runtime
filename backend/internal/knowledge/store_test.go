package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newTestStore 打开一个临时目录下的 owner store。
func newTestStore(t *testing.T) Store {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "knowledge.db")
	store, err := OpenStore(ctx, path, false)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestOpenStoreAppliesMigrations(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != KnowledgeVersion {
		t.Fatalf("schema version = %d, want %d", version, KnowledgeVersion)
	}

	// 重复打开必须是幂等的：迁移已应用则不重复执行。
	path := filepath.Join(t.TempDir(), "reopen.db")
	first, err := OpenStore(ctx, path, false)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	second, err := OpenStore(ctx, path, false)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer second.Close()
	if v, err := second.SchemaVersion(ctx); err != nil || v != KnowledgeVersion {
		t.Fatalf("schema version after reopen = %d (err=%v)", v, err)
	}
}

func TestWorkspaceAndFileRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	root := t.TempDir()
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if wsID == "" {
		t.Fatal("EnsureWorkspace returned empty id")
	}
	// 同一 root 再次登记必须返回同一个 id（幂等）。
	again, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace(again): %v", err)
	}
	if again != wsID {
		t.Fatalf("workspace id changed: %q != %q", again, wsID)
	}

	rec := FileRecord{
		WorkspaceID: wsID,
		Path:        `internal\demo\demo.go`, // 反斜杠必须被归一化为 '/'
		Language:    "go",
		Size:        128,
		MTimeNS:     42,
		ContentHash: "hash-1",
		IndexState:  IndexLight,
	}
	fileID, err := store.UpsertFile(ctx, rec)
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	got, ok, err := store.FileByPath(ctx, wsID, "internal/demo/demo.go")
	if err != nil || !ok {
		t.Fatalf("FileByPath: ok=%v err=%v", ok, err)
	}
	if got.ID != fileID || got.ContentHash != "hash-1" || got.Path != "internal/demo/demo.go" {
		t.Fatalf("unexpected record: %+v", got)
	}

	// upsert 同一路径必须更新而不是新增。
	rec.ContentHash = "hash-2"
	if _, err := store.UpsertFile(ctx, rec); err != nil {
		t.Fatalf("UpsertFile(update): %v", err)
	}
	stats, err := store.Stats(ctx, wsID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Files != 1 {
		t.Fatalf("files = %d, want 1", stats.Files)
	}
}

func TestReplaceSymbolsAndSearch(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	root := t.TempDir()
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	fileID, err := store.UpsertFile(ctx, FileRecord{
		WorkspaceID: wsID, Path: "demo.go", Language: "go", ContentHash: "h", IndexState: IndexLight,
	})
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	sigHash := SignatureHash("func OpenFile(path string) error")
	sym := Symbol{
		ID:            SymbolID(wsID, StableKey("go", SymbolFunction, "", "", "OpenFile", sigHash)),
		WorkspaceID:   wsID,
		FileID:        fileID,
		StableKey:     StableKey("go", SymbolFunction, "", "", "OpenFile", sigHash),
		Name:          "OpenFile",
		QualifiedName: "OpenFile",
		Kind:          SymbolFunction,
		Language:      "go",
		Signature:     "func OpenFile(path string) error",
		SignatureHash: sigHash,
		Range:         Range{Start: Position{Line: 3}, End: Position{Line: 9}},
		IsExported:    true,
	}
	if err := store.ReplaceSymbols(ctx, fileID, []Symbol{sym}); err != nil {
		t.Fatalf("ReplaceSymbols: %v", err)
	}

	found, err := store.FindSymbols(ctx, SymbolQuery{Name: "Open"})
	if err != nil {
		t.Fatalf("FindSymbols: %v", err)
	}
	if len(found) != 1 || found[0].Name != "OpenFile" || !found[0].IsExported {
		t.Fatalf("FindSymbols = %+v", found)
	}

	hits, err := store.Search(ctx, SearchQuery{Text: "OpenFile"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(hits) != 1 || hits[0].SymbolID != sym.ID || hits[0].Path != "demo.go" {
		t.Fatalf("Search = %+v", hits)
	}

	// 替换必须清掉旧行（FTS 触发器随之同步）。
	if err := store.ReplaceSymbols(ctx, fileID, nil); err != nil {
		t.Fatalf("ReplaceSymbols(empty): %v", err)
	}
	hits, err = store.Search(ctx, SearchQuery{Text: "OpenFile"})
	if err != nil {
		t.Fatalf("Search(after delete): %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("Search after replace = %+v, want empty", hits)
	}
}

func TestFindRefsByName(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	caller, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "caller.go", Language: "go", ContentHash: "c"})
	if err != nil {
		t.Fatalf("UpsertFile(caller): %v", err)
	}
	target, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "target.go", Language: "go", ContentHash: "t"})
	if err != nil {
		t.Fatalf("UpsertFile(target): %v", err)
	}
	sigHash := SignatureHash("func Callee()")
	targetSym := Symbol{
		ID: SymbolID(wsID, StableKey("go", SymbolFunction, "", "", "Callee", sigHash)), WorkspaceID: wsID,
		FileID: target, StableKey: StableKey("go", SymbolFunction, "", "", "Callee", sigHash),
		Name: "Callee", QualifiedName: "Callee", Kind: SymbolFunction, Language: "go",
		Range: Range{Start: Position{Line: 1}, End: Position{Line: 2}},
	}
	if err := store.ReplaceSymbols(ctx, target, []Symbol{targetSym}); err != nil {
		t.Fatalf("ReplaceSymbols: %v", err)
	}
	ref := Reference{
		WorkspaceID: wsID, FileID: caller, ToSymbolID: targetSym.ID, Kind: RefCall,
		Line: 10, Col: 4, Snippet: "Callee()", Source: SourceBuiltin,
	}
	if err := store.ReplaceRefs(ctx, caller, []Reference{ref}); err != nil {
		t.Fatalf("ReplaceRefs: %v", err)
	}

	byID, err := store.FindRefs(ctx, RefQuery{ToSymbolID: targetSym.ID})
	if err != nil {
		t.Fatalf("FindRefs(by id): %v", err)
	}
	if len(byID) != 1 || byID[0].Line != 10 || byID[0].Kind != RefCall {
		t.Fatalf("FindRefs(by id) = %+v", byID)
	}
	if byID[0].Confidence <= 0 || byID[0].Confidence > 1 {
		t.Fatalf("confidence = %v, want (0,1]", byID[0].Confidence)
	}

	byName, err := store.FindRefs(ctx, RefQuery{ToSymbolName: "Callee"})
	if err != nil {
		t.Fatalf("FindRefs(by name): %v", err)
	}
	if len(byName) != 1 {
		t.Fatalf("FindRefs(by name) = %+v", byName)
	}

	unknown, err := store.FindRefs(ctx, RefQuery{ToSymbolName: "Nope"})
	if err != nil {
		t.Fatalf("FindRefs(unknown): %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("FindRefs(unknown) = %+v, want empty", unknown)
	}
}

func TestDeleteFileCascades(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	wsID, _ := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	fileID, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "gone.go", Language: "go", ContentHash: "h"})
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	sym := Symbol{
		ID: SymbolID(wsID, "k"), WorkspaceID: wsID, FileID: fileID, StableKey: "k",
		Name: "Gone", QualifiedName: "Gone", Kind: SymbolFunction, Language: "go",
		Range: Range{Start: Position{Line: 1}, End: Position{Line: 1}},
	}
	if err := store.ReplaceSymbols(ctx, fileID, []Symbol{sym}); err != nil {
		t.Fatalf("ReplaceSymbols: %v", err)
	}
	if err := store.DeleteFile(ctx, wsID, "gone.go"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	stats, err := store.Stats(ctx, wsID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Files != 0 || stats.Symbols != 0 {
		t.Fatalf("stats after delete = %+v, want zeros", stats)
	}
}

func TestReadOnlyStoreRejectsWrites(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "knowledge.db")

	owner, err := OpenStore(ctx, path, false)
	if err != nil {
		t.Fatalf("OpenStore(owner): %v", err)
	}
	if _, err := owner.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()}); err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close owner: %v", err)
	}

	reader, err := OpenStore(ctx, path, true)
	if err != nil {
		t.Fatalf("OpenStore(reader): %v", err)
	}
	defer reader.Close()

	if _, err := reader.SchemaVersion(ctx); err != nil {
		t.Fatalf("reader SchemaVersion: %v", err)
	}
	err = reader.DeleteFile(ctx, "w", "x")
	if !errors.Is(err, ErrReadOnlyStore) {
		t.Fatalf("DeleteFile on reader = %v, want ErrReadOnlyStore", err)
	}
	err = reader.RecordInvalidation(ctx, InvalidationEvent{WorkspaceID: "w", Reason: "manual"})
	if !errors.Is(err, ErrReadOnlyStore) {
		t.Fatalf("RecordInvalidation on reader = %v, want ErrReadOnlyStore", err)
	}
}

func TestOpenStoreUninitializedReadOnly(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "empty.db")
	// 先创建一个空库文件（无 schema），只读打开必须显式失败而不是返回空 schema。
	seed, err := OpenStore(ctx, path, false)
	if err != nil {
		t.Fatalf("seed open: %v", err)
	}
	_ = seed.Close()

	raw := filepath.Join(t.TempDir(), "raw.db")
	if err := os.WriteFile(raw, nil, 0o644); err != nil {
		t.Fatalf("write raw: %v", err)
	}
	if _, err := OpenStore(ctx, raw, true); err == nil {
		t.Fatal("OpenStore(raw, readOnly) succeeded, want error for uninitialized store")
	}
}
