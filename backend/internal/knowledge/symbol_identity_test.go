package knowledge

import (
	"context"
	"testing"
)

// TestSymbolNamespaceSeparatesDirectories 锁住 04 §4.4 的 namespace 分量：
// 跨目录的同名同签名符号必须得到不同的 stable_key，否则每个 cmd/*/main.go 里的
// `func main() {` 都会塌缩成同一个身份，撞 idx_symbols_stable 唯一约束。
func TestSymbolNamespaceSeparatesDirectories(t *testing.T) {
	line := "func main() {"

	fileA := FileRecord{WorkspaceID: "w1", Path: "backend/cmd/a/main.go", Language: "go"}
	fileB := FileRecord{WorkspaceID: "w1", Path: "backend/cmd/b/main.go", Language: "go"}
	fileA2 := FileRecord{WorkspaceID: "w1", Path: "backend/cmd/a/other.go", Language: "go"}

	symA := buildSymbol(fileA, line, 1, "main", "", symbolPattern{kind: SymbolFunction})
	symB := buildSymbol(fileB, line, 1, "main", "", symbolPattern{kind: SymbolFunction})
	symA2 := buildSymbol(fileA2, line, 1, "main", "", symbolPattern{kind: SymbolFunction})

	if symA.StableKey == symB.StableKey {
		t.Fatalf("不同目录的同名符号必须有不同的 stable_key，都是 %s", symA.StableKey)
	}
	if symA.StableKey != symA2.StableKey {
		t.Fatalf("同目录内文件移动不应改变身份：%s vs %s", symA.StableKey, symA2.StableKey)
	}
	if got := symbolNamespace(FileRecord{Path: "main.go"}); got != "" {
		t.Fatalf("工作区根目录下的文件 namespace = %q, want \"\"", got)
	}
	if got := symbolNamespace(FileRecord{Path: `backend\cmd\a\main.go`}); got != "backend/cmd/a" {
		t.Fatalf("Windows 分隔符应被归一：%q", got)
	}
}

// TestReplaceSymbolsResolvesStableKeyConflict 锁住 04 §4.4 的歧义规则：
// 同 workspace 内 stable_key 冲突是 adapter 缺陷，写入不能整份失败，必须保留
// 一个身份行并记录 adapter_conflict 事件。
func TestReplaceSymbolsResolvesStableKeyConflict(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	fileA, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "pkg/a/x.go", Language: "go", IndexState: IndexLight})
	if err != nil {
		t.Fatalf("UpsertFile(a): %v", err)
	}
	fileB, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "pkg/b/y.go", Language: "go", IndexState: IndexLight})
	if err != nil {
		t.Fatalf("UpsertFile(b): %v", err)
	}

	// 两个文件产出同一个身份（模拟 adapter 缺陷：namespace 缺失）。
	stableKey := StableKey("go", SymbolFunction, "", "", "main", SignatureHash("func main() {"))
	newSym := func(fileID string, line int) Symbol {
		return Symbol{
			ID:            SymbolID(wsID, stableKey),
			WorkspaceID:   wsID,
			FileID:        fileID,
			StableKey:     stableKey,
			Name:          "main",
			QualifiedName: "main",
			Kind:          SymbolFunction,
			Language:      "go",
			Range:         Range{Start: Position{Line: line}, End: Position{Line: line}},
		}
	}

	if err := store.ReplaceSymbols(ctx, fileA, []Symbol{newSym(fileA, 1)}); err != nil {
		t.Fatalf("ReplaceSymbols(a): %v", err)
	}
	if err := store.ReplaceSymbols(ctx, fileB, []Symbol{newSym(fileB, 7)}); err != nil {
		t.Fatalf("ReplaceSymbols(b) 必须容忍 stable_key 冲突，实际 %v", err)
	}

	syms, err := store.FindSymbols(ctx, SymbolQuery{Name: "main"})
	if err != nil {
		t.Fatalf("FindSymbols: %v", err)
	}
	if len(syms) != 1 {
		t.Fatalf("同一 stable_key 只应保留一行，实际 %d 行", len(syms))
	}
	if syms[0].FileID != fileB {
		t.Fatalf("身份行应改挂到最近一次写入的文件，file_id=%s want %s", syms[0].FileID, fileB)
	}
	if syms[0].Range.Start.Line != 7 {
		t.Fatalf("位置应随宿主更新，start_line=%d want 7", syms[0].Range.Start.Line)
	}

	sqlite, ok := store.(*sqliteStore)
	if !ok {
		t.Fatalf("store 类型 = %T", store)
	}
	var events int
	if err := sqlite.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM invalidation_events WHERE workspace_id = ? AND reason = ?`,
		wsID, InvalidationReasonAdapterConflict).Scan(&events); err != nil {
		t.Fatalf("count invalidation_events: %v", err)
	}
	if events != 1 {
		t.Fatalf("adapter_conflict 事件数 = %d, want 1", events)
	}
}

// TestReplaceSymbolsKeepsNonConflictingRows 保证正常路径没有被冲突处理改写：
// 同文件内的多个符号照常写入，且不会产生冲突事件。
func TestReplaceSymbolsKeepsNonConflictingRows(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	fileID, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "pkg/a/x.go", Language: "go", IndexState: IndexLight})
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	mk := func(name string) Symbol {
		key := StableKey("go", SymbolFunction, "pkg/a", "", name, SignatureHash("func "+name+"() {"))
		return Symbol{
			ID: SymbolID(wsID, key), WorkspaceID: wsID, FileID: fileID, StableKey: key,
			Name: name, QualifiedName: name, Kind: SymbolFunction, Language: "go",
			Range: Range{Start: Position{Line: 1}, End: Position{Line: 1}},
		}
	}
	if err := store.ReplaceSymbols(ctx, fileID, []Symbol{mk("Alpha"), mk("Beta")}); err != nil {
		t.Fatalf("ReplaceSymbols: %v", err)
	}
	syms, err := store.FindSymbols(ctx, SymbolQuery{PathPrefix: "pkg/a"})
	if err != nil {
		t.Fatalf("FindSymbols: %v", err)
	}
	if len(syms) != 2 {
		t.Fatalf("符号数 = %d, want 2", len(syms))
	}

	sqlite := store.(*sqliteStore)
	var events int
	if err := sqlite.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM invalidation_events WHERE workspace_id = ?`, wsID).Scan(&events); err != nil {
		t.Fatalf("count invalidation_events: %v", err)
	}
	if events != 0 {
		t.Fatalf("无冲突时不应产生失效事件，实际 %d 条", events)
	}
}
