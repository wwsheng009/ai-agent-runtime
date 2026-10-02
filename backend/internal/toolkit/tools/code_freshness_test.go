package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// 文件级新鲜度守卫的回归测试（ADR-0004 §4.1/§5 D1 的粒度补齐）。
//
// 覆盖的失效模式：索引持续给别的文件写入 → 全局 staleness_seconds 停在新鲜档
// → 但本次查询涉及的文件已改写 → 引用集合静默漏掉磁盘上已存在的调用点。
// 守卫必须把这种结果降级为实时 grep，而不是照样返回"命中 N 条"。

type freshnessFixture struct {
	root     string
	fileRel  string
	handle   *CodeIndexHandle
	resolver CodeIndexResolver
}

func freshnessContent() string {
	return strings.Join([]string{
		"package pkg",
		"",
		"func Alpha() {}",
		"",
		"func Beta() {",
		"\tAlpha()",
		"}",
	}, "\n") + "\n"
}

// newFreshnessFixture 建一个"索引与磁盘一致"的真实索引夹具：指纹取自磁盘真实
// 的 size / mtime / sha256(content)，与索引器写入 files 表的口径一致。
func newFreshnessFixture(t *testing.T) *freshnessFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	fileRel := "pkg/demo.go"
	content := freshnessContent()
	abs := filepath.Join(root, filepath.FromSlash(fileRel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	sum := sha256.Sum256([]byte(content))
	hash := hex.EncodeToString(sum[:])

	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeOn
	store, err := knowledge.OpenStore(ctx, knowledge.StorePathFor(cfg), false)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	wsID, err := store.EnsureWorkspace(ctx, knowledge.Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	fileID, err := store.UpsertFile(ctx, knowledge.FileRecord{
		WorkspaceID: wsID,
		Path:        fileRel,
		Language:    "go",
		Size:        info.Size(),
		MTimeNS:     info.ModTime().UnixNano(),
		ContentHash: hash,
		IndexedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	if err := store.ReplaceSymbols(ctx, fileID, []knowledge.Symbol{{
		FileID: fileID, WorkspaceID: wsID, Name: "Alpha", QualifiedName: "pkg.Alpha",
		StableKey: knowledge.StableKey("go", knowledge.SymbolKind("function"), "pkg", "", "Alpha", ""),
		Kind:      knowledge.SymbolKind("function"), Language: "go", Signature: "func Alpha()",
		Range: knowledge.Range{
			Start: knowledge.Position{Line: 3, Column: 1},
			End:   knowledge.Position{Line: 3, Column: 16},
		},
	}}); err != nil {
		t.Fatalf("ReplaceSymbols: %v", err)
	}
	resolved, err := store.FindSymbols(ctx, knowledge.SymbolQuery{Name: "Alpha", Exact: true, Limit: 5})
	if err != nil || len(resolved) == 0 {
		t.Fatalf("FindSymbols(Alpha): err=%v n=%d", err, len(resolved))
	}
	if err := store.ReplaceRefs(ctx, fileID, []knowledge.Reference{{
		ID:           knowledge.RefID(wsID, fileID, 6, 2, knowledge.RefKind("call")),
		FileID:       fileID,
		WorkspaceID:  wsID,
		ToSymbolID:   resolved[0].ID,
		ToSymbolName: "Alpha",
		Kind:         knowledge.RefKind("call"),
		Line:         6,
		Col:          2,
		Snippet:      "Alpha()",
		Confidence:   0.55,
		Source:       knowledge.RefSource("regex_builtin"),
	}}); err != nil {
		t.Fatalf("ReplaceRefs: %v", err)
	}

	handle := &CodeIndexHandle{
		Index:       store,
		Mode:        knowledge.ModeOn,
		WorkspaceID: wsID,
		Root:        root,
		FilePaths:   map[string]string{fileID: fileRel},
		FileIDsByPath: map[string]string{
			fileRel: fileID,
		},
		FileStamps: map[string]FileStamp{fileID: {
			MTimeNS:     info.ModTime().UnixNano(),
			Size:        info.Size(),
			ContentHash: hash,
		}},
		SnapshotTS:  time.Now().Unix(),
		Tier:        CodeIndexTierAll,
		Writer:      true,
	}
	return &freshnessFixture{
		root:     root,
		fileRel:  fileRel,
		handle:   handle,
		resolver: func(context.Context) (*CodeIndexHandle, bool) { return handle, true },
	}
}

func (f *freshnessFixture) abs(t *testing.T) string {
	t.Helper()
	return filepath.Join(f.root, filepath.FromSlash(f.fileRel))
}

func (f *freshnessFixture) callers(t *testing.T, symbol string) codeTestEnvelope {
	t.Helper()
	tool := NewCodeCallersTool()
	tool.SetBasePath(f.root)
	tool.SetCodeIndexResolver(f.resolver)
	res, err := tool.Execute(context.Background(), map[string]interface{}{"symbol": symbol})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return decodeCodeEnvelope(t, res)
}

// 未改动的文件走索引路径，且解释里必须自陈已做过校验（Gap D：成功路径原来
// 不暴露任何新鲜度信息，消费方无从判断该不该复核）。
func TestCodeCallersFreshFileUsesIndex(t *testing.T) {
	f := newFreshnessFixture(t)
	env := f.callers(t, "Alpha")
	if env.Source != codeSourceIndex {
		t.Fatalf("source = %q, want %q（env=%+v）", env.Source, codeSourceIndex, env)
	}
	if !strings.Contains(env.Explanation, "文件级新鲜度：已校验，一致") {
		t.Errorf("成功路径未说明新鲜度校验：%s", env.Explanation)
	}
	if env.Degraded || env.Fallback != nil {
		t.Errorf("fresh 文件不应降级：degraded=%v fallback=%+v", env.Degraded, env.Fallback)
	}
}

// 核心回归：文件在索引之后被改写 → 必须降级为实时 grep，并在解释里点名文件。
func TestCodeCallersStaleFileFallsBackToGrep(t *testing.T) {
	f := newFreshnessFixture(t)
	// 模拟"索引之后新增了一个调用点"：磁盘内容变了，索引仍是旧版本。
	updated := freshnessContent() + "\nfunc Gamma() {\n\tAlpha()\n}\n"
	if err := os.WriteFile(f.abs(t), []byte(updated), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	env := f.callers(t, "Alpha")
	if env.Source != codeSourceFallback {
		t.Fatalf("source = %q, want %q：改写后的文件不得返回索引引用（env=%+v）", env.Source, codeSourceFallback, env)
	}
	if env.Fallback == nil || env.Fallback.Reason != codeFallbackStaleFile {
		t.Fatalf("fallback.reason = %+v, want %q", env.Fallback, codeFallbackStaleFile)
	}
	if !strings.Contains(env.Explanation, f.fileRel) {
		t.Errorf("降级解释未点名陈旧文件：%s", env.Explanation)
	}
	if !env.Degraded {
		t.Error("降级结果必须标记 degraded=true")
	}
	// grep 的实时输出应当包含磁盘上新增的那个调用点——这正是降级的意义。
	if env.Fallback.Output == "" || !strings.Contains(env.Fallback.Output, "Alpha") {
		t.Errorf("fallback 输出未包含实时结果：%q", env.Fallback.Output)
	}
}

// mtime 变了但内容没变（checkout / touch / 编辑器保存无改动）不得误降级：
// 指纹的 sha256 兜底路径必须把它判为新鲜。
func TestCodeCallersTouchedSameContentStaysOnIndex(t *testing.T) {
	f := newFreshnessFixture(t)
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(f.abs(t), future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	env := f.callers(t, "Alpha")
	if env.Source != codeSourceIndex {
		t.Fatalf("source = %q, want %q：内容未变不应误降级（env=%+v）", env.Source, codeSourceIndex, env)
	}
	if env.Fallback != nil {
		t.Errorf("内容未变不应触发 fallback：%+v", env.Fallback)
	}
}

// 句柄没有指纹时是"无法校验"而非"新鲜"：不得声称已校验，也不得假装
// 自己降级过——解释必须如实说明。
func TestCodeCallersWithoutStampsReportsUnverified(t *testing.T) {
	f := newFreshnessFixture(t)
	f.handle.FileStamps = nil

	env := f.callers(t, "Alpha")
	if env.Source != codeSourceIndex {
		t.Fatalf("source = %q, want %q", env.Source, codeSourceIndex)
	}
	if !strings.Contains(env.Explanation, "未做文件级新鲜度校验") {
		t.Errorf("无指纹时未如实说明未校验：%s", env.Explanation)
	}
}

// fileFresh 的单点口径：size 不符直接判陈旧；指纹缺失返回 known=false。
func TestFileFreshStamps(t *testing.T) {
	f := newFreshnessFixture(t)
	fileID := ""
	for id := range f.handle.FilePaths {
		fileID = id
	}
	known, fresh := fileFresh(f.handle, fileID)
	if !known || !fresh {
		t.Errorf("fileFresh = (%v, %v), want (true, true)", known, fresh)
	}

	if known, fresh := fileFresh(f.handle, "no-such-file"); known || fresh {
		t.Errorf("未知文件 fileFresh = (%v, %v), want (false, false)", known, fresh)
	}

	if known, fresh := fileFresh(nil, fileID); known || fresh {
		t.Errorf("nil 句柄 fileFresh = (%v, %v), want (false, false)", known, fresh)
	}

	// 文件从磁盘消失：索引记录是残留，必须判陈旧。
	if err := os.Remove(f.abs(t)); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if known, fresh := fileFresh(f.handle, fileID); !known || fresh {
		t.Errorf("文件消失后 fileFresh = (%v, %v), want (true, false)", known, fresh)
	}
}

