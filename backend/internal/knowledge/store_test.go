package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/migrate"
	"github.com/wwsheng009/ai-agent-runtime/internal/sqliteutil"
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

// TestOpenStoreRefusesNewerSchema 钉住 04 §5 Phase 5 交付 6 / 风险 R12：
// 库由**更新的二进制**写下（schema 版本高于本二进制）时，写入与只读两条
// 打开路径都必须拒绝——降级读会按旧列集解读新结构，静默给错比失败更糟。
func TestOpenStoreRefusesNewerSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "future.db")

	store, err := OpenStore(ctx, path, false)
	if err != nil {
		t.Fatalf("OpenStore(seed): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	future := migrations[len(migrations)-1].Version + 1
	db, err := sqliteutil.OpenFileCtx(ctx, path, true)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, 'future', ?)`,
		future, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("seed future migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	// 写入路径：Apply 收口处拒绝。
	if _, err := OpenStore(ctx, path, false); err == nil || !errors.Is(err, migrate.ErrSchemaNewer) {
		t.Fatalf("writer on newer store = %v, want migrate.ErrSchemaNewer", err)
	}
	// 只读路径：verifyInitialized 拒绝，且消息带上两侧版本号。
	_, err = OpenStore(ctx, path, true)
	if err == nil || !errors.Is(err, migrate.ErrSchemaNewer) {
		t.Fatalf("reader on newer store = %v, want migrate.ErrSchemaNewer", err)
	}
	if !strings.Contains(err.Error(), "newer binary") {
		t.Fatalf("reader error = %q, want actionable hint", err.Error())
	}
}

func TestOpenStoreAppliesMigrations(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)

	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	// schema_migrations 的版本号与 KnowledgeVersion（磁盘契约版本，DB 文件名的
	// 一部分）不是同一个概念：前者每加一条迁移就前进，后者只在契约破坏时升级。
	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	wantVersion := migrations[len(migrations)-1].Version
	if version != wantVersion {
		t.Fatalf("schema version = %d, want %d", version, wantVersion)
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
	if v, err := second.SchemaVersion(ctx); err != nil || v != wantVersion {
		t.Fatalf("schema version after reopen = %d (err=%v)", v, err)
	}
}

// TestSoftDeleteMigrationUpgradesV1Store 钉住 0002 的升级路径：既有的 v1 库
// （只应用过 0001，磁盘上已积累数据）再次由 owner 打开时必须补上
// files.deleted_at，且既有行不受影响。
func TestSoftDeleteMigrationUpgradesV1Store(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "v1.db")

	migrations, err := Migrations()
	if err != nil {
		t.Fatalf("Migrations: %v", err)
	}
	var v1 []migrate.Migration
	for _, m := range migrations {
		if m.Version > 1 {
			break
		}
		v1 = append(v1, m)
	}
	if len(v1) == 0 || v1[len(v1)-1].Version != 1 {
		t.Fatalf("fixture migrations = %+v, want up to v1", v1)
	}

	db, err := sqliteutil.OpenFileCtx(ctx, path, true)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if err := migrate.Apply(ctx, db, v1); err != nil {
		t.Fatalf("apply v1 migrations: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, root_path, created_at, updated_at) VALUES ('w1', '/tmp/legacy', 0, 0)`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO files (id, workspace_id, path, content_hash) VALUES ('f1', 'w1', 'legacy.go', 'h1')`); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	// 只读角色先于 owner 打开旧库：必须显式报"schema 落后"，而不是半可用。
	if _, err := OpenStore(ctx, path, true); err == nil || !strings.Contains(err.Error(), "older than this binary") {
		t.Fatalf("reader on v1 store = %v, want explicit schema-too-old error", err)
	}

	store, err := OpenStore(ctx, path, false)
	if err != nil {
		t.Fatalf("OpenStore(upgrade): %v", err)
	}
	defer store.Close()

	latest := migrations[len(migrations)-1].Version
	if v, err := store.SchemaVersion(ctx); err != nil || v != latest {
		t.Fatalf("schema version after upgrade = %d (err=%v), want %d", v, err, latest)
	}
	rec, ok, err := store.FileByPath(ctx, "w1", "legacy.go")
	if err != nil || !ok {
		t.Fatalf("legacy row lost: ok=%v err=%v", ok, err)
	}
	if rec.ContentHash != "h1" || rec.DeletedAt != 0 {
		t.Fatalf("legacy row = %+v, want content_hash=h1 deleted_at=0", rec)
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
	// Path 必须由 FindRefs 联表填充：Phase1-shadow 的 refs 候选通道依赖
	// (path,line) 与 baseline 对齐（见 shadow_ref_candidate_test.go）。
	if byName[0].Path == "" {
		t.Fatalf("FindRefs(by name) path empty: %+v", byName[0])
	}

	unknown, err := store.FindRefs(ctx, RefQuery{ToSymbolName: "Nope"})
	if err != nil {
		t.Fatalf("FindRefs(unknown): %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("FindRefs(unknown) = %+v, want empty", unknown)
	}
}

func TestFindRefsResolvesFromSymbolName(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	callerFile, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "caller.go", Language: "go", ContentHash: "c"})
	if err != nil {
		t.Fatalf("UpsertFile(caller): %v", err)
	}
	targetFile, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "target.go", Language: "go", ContentHash: "t"})
	if err != nil {
		t.Fatalf("UpsertFile(target): %v", err)
	}

	sigHash := SignatureHash("func Callee()")
	targetSym := Symbol{
		ID: SymbolID(wsID, StableKey("go", SymbolFunction, "", "", "Callee", sigHash)), WorkspaceID: wsID,
		FileID: targetFile, StableKey: StableKey("go", SymbolFunction, "", "", "Callee", sigHash),
		Name: "Callee", QualifiedName: "Callee", Kind: SymbolFunction, Language: "go",
		Range: Range{Start: Position{Line: 1}, End: Position{Line: 2}},
	}
	// 两个不同函数，各自调用一次 Callee：v1 的 from_symbol 只给 32 位 hex
	// 稳定键，调用方无法区分这两处归属（真机 remote 测试里就是被折叠成一条）。
	firstSym := Symbol{
		ID: "sym-first", WorkspaceID: wsID, FileID: callerFile, StableKey: "k-first",
		Name: "FirstCaller", QualifiedName: "FirstCaller", Kind: SymbolFunction, Language: "go",
		Range: Range{Start: Position{Line: 1}, End: Position{Line: 5}},
	}
	secondSym := Symbol{
		ID: "sym-second", WorkspaceID: wsID, FileID: callerFile, StableKey: "k-second",
		Name: "SecondCaller", QualifiedName: "SecondCaller", Kind: SymbolFunction, Language: "go",
		Range: Range{Start: Position{Line: 6}, End: Position{Line: 9}},
	}
	if err := store.ReplaceSymbols(ctx, callerFile, []Symbol{firstSym, secondSym}); err != nil {
		t.Fatalf("ReplaceSymbols: %v", err)
	}
	if err := store.ReplaceSymbols(ctx, targetFile, []Symbol{targetSym}); err != nil {
		t.Fatalf("ReplaceSymbols(target): %v", err)
	}

	refs := []Reference{
		{WorkspaceID: wsID, FileID: callerFile, FromSymbolID: firstSym.ID, ToSymbolID: targetSym.ID,
			Kind: RefCall, Line: 3, Col: 2, Snippet: "Callee()", Source: SourceBuiltin},
		{WorkspaceID: wsID, FileID: callerFile, FromSymbolID: secondSym.ID, ToSymbolID: targetSym.ID,
			Kind: RefCall, Line: 7, Col: 2, Snippet: "Callee()", Source: SourceBuiltin},
		// 文件级引用（import 口径，from_symbol_id 为空）：名字应留空而不是回退成别的值。
		{WorkspaceID: wsID, FileID: callerFile, ToSymbolID: targetSym.ID,
			Kind: RefImport, Line: 1, Col: 1, Snippet: `import "x"`, Source: SourceBuiltin},
	}
	if err := store.ReplaceRefs(ctx, callerFile, refs); err != nil {
		t.Fatalf("ReplaceRefs: %v", err)
	}

	byID, err := store.FindRefs(ctx, RefQuery{ToSymbolID: targetSym.ID, Kind: RefCall})
	if err != nil {
		t.Fatalf("FindRefs: %v", err)
	}
	if len(byID) != 2 {
		t.Fatalf("FindRefs = %d hits, want 2: %+v", len(byID), byID)
	}
	got := map[string]int{}
	for _, r := range byID {
		if r.FromSymbolName == "" {
			t.Fatalf("FromSymbolName empty for line %d: %+v", r.Line, r)
		}
		got[r.FromSymbolName] = r.Line
	}
	if got["FirstCaller"] != 3 || got["SecondCaller"] != 7 {
		t.Fatalf("FromSymbolName mapping = %+v, want FirstCaller:3 SecondCaller:7", got)
	}
	// 两处调用必须映射到**不同**名字：v1 只给 hex 稳定键时调用方无法区分，
	// 这正是真机 remote 测试里 7 个 call 点被折叠成 5 个名字的成因。
	if len(got) != 2 {
		t.Fatalf("FromSymbolName collapsed distinct callers: %+v", got)
	}
}

// TestStatsIndexedAtTracksFullReconciliation 锁定 IndexedAt 的口径：
// 只认"遍历过整个工作区"的 light 运行，不认 incremental，也不认单文件写入。
//
// 为什么这条重要（真机观测）：indexed_at 若取 MAX(files.indexed_at)，
// 改 5,058 个文件里的任意一个就会把它推到 now，陈旧度归零、分级闸门放行
// tier=all，关系类查询以 confidence=1.0/completeness=full 作答——而其余
// 5,057 个文件从未被校验。这正是静默 fail-open。
func TestStatsIndexedAtTracksFullReconciliation(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	// 先落一个文件，让"light 之前/之后都有文件"两种口径都能取到非零值——
	// 这样下面"增量不得推进时钟"的断言才是唯一能区分新旧口径的那一条，
	// 而不是因为旧口径碰巧返回 0 才失败。
	if _, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "seed.go", Language: "go", ContentHash: "s"}); err != nil {
		t.Fatalf("UpsertFile(seed): %v", err)
	}

	// 一次成功的 light 运行 = 一次全工作区对账。
	lightID, err := store.StartIndexJob(ctx, IndexJob{WorkspaceID: wsID, Kind: IndexJobKindLight})
	if err != nil {
		t.Fatalf("StartIndexJob(light): %v", err)
	}
	if err := store.FinishIndexJob(ctx, lightID, IndexJobStatusDone, 2, 2, ""); err != nil {
		t.Fatalf("FinishIndexJob(light): %v", err)
	}
	afterLight, err := store.Stats(ctx, wsID)
	if err != nil {
		t.Fatalf("Stats(after light): %v", err)
	}
	if afterLight.IndexedAt <= 0 {
		t.Fatalf("IndexedAt after a successful light run = %d, want > 0", afterLight.IndexedAt)
	}

	// 拉开毫秒时间戳，避免同毫秒写入让断言失去意义。
	time.Sleep(5 * time.Millisecond)

	// 一次 incremental 运行（IndexPaths 只处理被标记的少数文件）+ 一次文件写入。
	// 两者都不构成全工作区对账，都不该推进时钟。
	incID, err := store.StartIndexJob(ctx, IndexJob{WorkspaceID: wsID, Kind: IndexJobKindIncremental})
	if err != nil {
		t.Fatalf("StartIndexJob(incremental): %v", err)
	}
	if err := store.FinishIndexJob(ctx, incID, IndexJobStatusDone, 1, 1, ""); err != nil {
		t.Fatalf("FinishIndexJob(incremental): %v", err)
	}
	if _, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "later.go", Language: "go", ContentHash: "l"}); err != nil {
		t.Fatalf("UpsertFile(later): %v", err)
	}
	afterIncremental, err := store.Stats(ctx, wsID)
	if err != nil {
		t.Fatalf("Stats(after incremental): %v", err)
	}
	if afterIncremental.IndexedAt != afterLight.IndexedAt {
		t.Fatalf("IndexedAt moved %d -> %d after an incremental run + a single-file write; "+
			"only a full light reconciliation may advance it",
			afterLight.IndexedAt, afterIncremental.IndexedAt)
	}

	// 失败的 light 运行不算对账：它没能证明索引与磁盘一致。
	time.Sleep(5 * time.Millisecond)
	failID, err := store.StartIndexJob(ctx, IndexJob{WorkspaceID: wsID, Kind: IndexJobKindLight})
	if err != nil {
		t.Fatalf("StartIndexJob(failed light): %v", err)
	}
	if err := store.FinishIndexJob(ctx, failID, IndexJobStatusFailed, 2, 1, "boom"); err != nil {
		t.Fatalf("FinishIndexJob(failed light): %v", err)
	}
	afterFailed, err := store.Stats(ctx, wsID)
	if err != nil {
		t.Fatalf("Stats(after failed light): %v", err)
	}
	if afterFailed.IndexedAt != afterLight.IndexedAt {
		t.Fatalf("IndexedAt moved %d -> %d after a FAILED light run; "+
			"a failed reconciliation proves nothing and must not renew the snapshot",
			afterLight.IndexedAt, afterFailed.IndexedAt)
	}

	// 预算截断的 light 运行同样不算对账。
	//
	// collectIndexableFiles 撞到 maxIndexFiles 就 fs.SkipAll 提前结束遍历，
	// 工作区里其余文件本次根本没被看过；但 indexer 只按 err==nil 落库，
	// 截断信号 result.Truncated 不进 index_jobs，于是这类运行被记成
	// status='done'。若不额外排除，就会重演 incremental 那条 fail-open：
	// 一次只看了前 maxIndexFiles 个文件的扫描，把全部未校验文件的陈旧度清零。
	//
	// 真机工作区 5,058 文件远低于 maxIndexFiles=20000，所以这条不会在本地触发，
	// 但大仓库必然触发——正因如此必须有回归锁。
	time.Sleep(5 * time.Millisecond)
	truncID, err := store.StartIndexJob(ctx, IndexJob{WorkspaceID: wsID, Kind: IndexJobKindLight})
	if err != nil {
		t.Fatalf("StartIndexJob(truncated light): %v", err)
	}
	if err := store.FinishIndexJob(ctx, truncID, IndexJobStatusDone, maxIndexFiles, maxIndexFiles, ""); err != nil {
		t.Fatalf("FinishIndexJob(truncated light): %v", err)
	}
	afterTruncated, err := store.Stats(ctx, wsID)
	if err != nil {
		t.Fatalf("Stats(after truncated light): %v", err)
	}
	if afterTruncated.IndexedAt != afterLight.IndexedAt {
		t.Fatalf("IndexedAt moved %d -> %d after a budget-truncated light run; "+
			"a partial scan never proves the unvisited files match disk",
			afterLight.IndexedAt, afterTruncated.IndexedAt)
	}
}

// TestStatsIndexedAtFallsBackWhenNoLightRun 覆盖旧库：从未跑过 light 的
// workspace（只有 incremental / 手工写入）必须回退到 MAX(files.indexed_at)，
// 而不是退回 0——0 会被状态面读成"尚未索引"，把可用索引误报成空。
func TestStatsIndexedAtFallsBackWhenNoLightRun(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	if _, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "a.go", Language: "go", ContentHash: "a"}); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	stats, err := store.Stats(ctx, wsID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.IndexedAt <= 0 {
		t.Fatalf("IndexedAt = %d with files present but no light run; "+
			"the legacy fallback must keep it non-zero", stats.IndexedAt)
	}
}

func TestFindRefsFileLevelRefHasNoFromSymbolName(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: t.TempDir()})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	fileID, err := store.UpsertFile(ctx, FileRecord{WorkspaceID: wsID, Path: "a.go", Language: "go", ContentHash: "h"})
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	ref := Reference{WorkspaceID: wsID, FileID: fileID, ToSymbolName: "X", Kind: RefImport,
		Line: 1, Col: 1, Snippet: `import "x"`, Source: SourceBuiltin}
	if err := store.ReplaceRefs(ctx, fileID, []Reference{ref}); err != nil {
		t.Fatalf("ReplaceRefs: %v", err)
	}
	hits, err := store.FindRefs(ctx, RefQuery{ToSymbolName: "X"})
	if err != nil {
		t.Fatalf("FindRefs: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("FindRefs = %+v, want 1", hits)
	}
	// LEFT JOIN 未命中时必须是空串（omitempty 后字段消失），不能是 from_symbol_id。
	if hits[0].FromSymbolName != "" || hits[0].FromSymbolID != "" {
		t.Fatalf("file-level ref carries a from symbol: %+v", hits[0])
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

// TestMarkFilesDeletedIsIdempotent 钉住软删除的 store 语义（04 §5 Phase 1 交付 3）：
// 标记保留文件行、符号同步不可见、重复标记幂等；UpsertFile 复活时清空标记。
func TestMarkFilesDeletedIsIdempotent(t *testing.T) {
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

	marked, err := store.MarkFilesDeleted(ctx, wsID, []string{"gone.go", "never-existed.go"}, time.Now())
	if err != nil {
		t.Fatalf("MarkFilesDeleted: %v", err)
	}
	if marked != 1 {
		t.Fatalf("marked = %d, want 1（未知路径不得计数）", marked)
	}
	rec, ok, err := store.FileByPath(ctx, wsID, "gone.go")
	if err != nil || !ok {
		t.Fatalf("FileByPath: ok=%v err=%v", ok, err)
	}
	if rec.DeletedAt == 0 || rec.IndexState != IndexStale {
		t.Fatalf("rec = %+v, want deleted_at!=0 + stale", rec)
	}
	if syms, err := store.FindSymbols(ctx, SymbolQuery{Name: "Gone", Exact: true}); err != nil || len(syms) != 0 {
		t.Fatalf("FindSymbols after soft delete = %+v (err=%v), want empty", syms, err)
	}
	if active, err := store.ListActiveFiles(ctx, wsID); err != nil || len(active) != 0 {
		t.Fatalf("ListActiveFiles = %+v (err=%v), want empty", active, err)
	}
	if again, err := store.MarkFilesDeleted(ctx, wsID, []string{"gone.go"}, time.Now()); err != nil || again != 0 {
		t.Fatalf("second MarkFilesDeleted = %d (err=%v), want 0", again, err)
	}

	// 复活：同一 (workspace, path) 重新 UpsertFile 必须清空软删除标记。
	if _, err := store.UpsertFile(ctx, FileRecord{
		WorkspaceID: wsID, Path: "gone.go", Language: "go", ContentHash: "h2", IndexState: IndexLight,
	}); err != nil {
		t.Fatalf("UpsertFile(revive): %v", err)
	}
	rec, ok, err = store.FileByPath(ctx, wsID, "gone.go")
	if err != nil || !ok || rec.DeletedAt != 0 {
		t.Fatalf("revive failed: ok=%v rec=%+v err=%v", ok, rec, err)
	}
	if active, err := store.ListActiveFiles(ctx, wsID); err != nil || len(active) != 1 {
		t.Fatalf("ListActiveFiles after revive = %+v (err=%v), want 1", active, err)
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
	if _, err := reader.MarkFilesDeleted(ctx, "w", []string{"x"}, time.Now()); !errors.Is(err, ErrReadOnlyStore) {
		t.Fatalf("MarkFilesDeleted on reader = %v, want ErrReadOnlyStore", err)
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
