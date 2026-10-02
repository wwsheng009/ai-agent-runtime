package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
)

// rewriteWithExtraCall 在文件末尾追加一个调用 Alpha 的新函数，模拟"索引之后
// 磁盘上新增了内容"。
func (f *freshnessFixture) rewriteWithExtraCall(t *testing.T) {
	t.Helper()
	updated := freshnessContent() + "\nfunc Gamma() {\n\tAlpha()\n}\n"
	if err := os.WriteFile(f.abs(t), []byte(updated), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
}

func (f *freshnessFixture) inspect(t *testing.T, params map[string]interface{}) codeTestEnvelope {
	t.Helper()
	tool := NewCodeInspectTool()
	tool.SetBasePath(f.root)
	tool.SetCodeIndexResolver(f.resolver)
	res, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return decodeCodeEnvelope(t, res)
}

func (f *freshnessFixture) navigate(t *testing.T, params map[string]interface{}) codeTestEnvelope {
	t.Helper()
	tool := NewCodeNavigateTool()
	tool.SetBasePath(f.root)
	tool.SetCodeIndexResolver(f.resolver)
	res, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return decodeCodeEnvelope(t, res)
}

// 未改动的文件：code_inspect 正常按符号行号范围返回。
func TestCodeInspectFreshFileReturnsRange(t *testing.T) {
	f := newFreshnessFixture(t)
	env := f.inspect(t, map[string]interface{}{"symbol": "Alpha"})
	if env.Source != codeSourceIndex {
		t.Fatalf("source = %q, want %q（env=%+v）", env.Source, codeSourceIndex, env)
	}
	if env.Range == nil || env.Range.StartLine != 3 {
		t.Fatalf("range = %+v, want start=3", env.Range)
	}
}

// 核心回归（定义类）：文件改写后，索引里的行号范围会指向磁盘上完全不同的
// 代码。返回它等于给模型一段**看起来合理但是错的**实现——比报错危险得多。
// 必须降级为实时读取，并且不得再输出那个不可信的范围。
func TestCodeInspectStaleFileDoesNotTrustRange(t *testing.T) {
	f := newFreshnessFixture(t)
	f.rewriteWithExtraCall(t)

	env := f.inspect(t, map[string]interface{}{"symbol": "Alpha"})
	if env.Source != codeSourceFallback {
		t.Fatalf("source = %q, want %q：改写后的文件不得按索引行号返回正文（env=%+v）", env.Source, codeSourceFallback, env)
	}
	if env.Fallback == nil || env.Fallback.Reason != codeFallbackStaleFile {
		t.Fatalf("fallback.reason = %+v, want %q", env.Fallback, codeFallbackStaleFile)
	}
	if env.Range != nil {
		t.Errorf("降级后仍输出了不可信的符号范围：%+v", env.Range)
	}
	// 实时读取应覆盖磁盘当前内容（含索引之后新增的 Gamma）。
	if !strings.Contains(env.Fallback.Output, "Gamma") {
		t.Errorf("fallback 未返回磁盘当前内容：%q", env.Fallback.Output)
	}
}

func TestCodeNavigateDefinitionStaleFileFallsBack(t *testing.T) {
	f := newFreshnessFixture(t)
	f.rewriteWithExtraCall(t)

	env := f.navigate(t, map[string]interface{}{"direction": "definition", "symbol": "Alpha"})
	if env.Source != codeSourceFallback {
		t.Fatalf("source = %q, want %q", env.Source, codeSourceFallback)
	}
	if env.Fallback == nil || env.Fallback.Reason != codeFallbackStaleFile {
		t.Fatalf("fallback.reason = %+v, want %q", env.Fallback, codeFallbackStaleFile)
	}
	if env.Range != nil {
		t.Errorf("降级后仍输出了不可信的行号范围：%+v", env.Range)
	}
}

func TestCodeNavigateMembersStaleFileFallsBack(t *testing.T) {
	f := newFreshnessFixture(t)
	f.rewriteWithExtraCall(t)

	env := f.navigate(t, map[string]interface{}{"direction": "members", "file_path": f.fileRel})
	if env.Source != codeSourceFallback {
		t.Fatalf("source = %q, want %q（env=%+v）", env.Source, codeSourceFallback, env)
	}
	if env.Fallback == nil || env.Fallback.Reason != codeFallbackStaleFile {
		t.Fatalf("fallback.reason = %+v, want %q", env.Fallback, codeFallbackStaleFile)
	}
}

func TestCodeNavigateDefinitionFreshFileStaysOnIndex(t *testing.T) {
	f := newFreshnessFixture(t)
	env := f.navigate(t, map[string]interface{}{"direction": "definition", "symbol": "Alpha"})
	if env.Source != codeSourceIndex {
		t.Fatalf("source = %q, want %q（env=%+v）", env.Source, codeSourceIndex, env)
	}
	if !strings.Contains(env.Explanation, "文件级新鲜度") {
		t.Errorf("成功路径未说明新鲜度：%s", env.Explanation)
	}
}

// 读盘预算：mtime 全被刷新（git checkout 的典型形态）时，超过预算的文件必须
// 计入"未校验"而不是默认新鲜——报告口径必须如实。
func TestStaleRelFilesRespectsReadBudget(t *testing.T) {
	f := newFreshnessFixture(t)

	// 40 个 file id 指向同一个真实文件，指纹的 MTimeNS 置 0（强制走读盘算哈希）。
	stamps := make(map[string]FileStamp, 40)
	ids := make([]string, 0, 40)
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("f-budget-%02d", i)
		ids = append(ids, id)
		f.handle.FilePaths[id] = f.fileRel
		stamps[id] = FileStamp{Size: int64(len(freshnessContent())), ContentHash: diskHash(t, f.abs(t))}
	}
	f.handle.FileStamps = stamps

	stale, unverified := staleRelFiles(f.handle, ids)
	if len(stale) != 0 {
		t.Errorf("stale = %v, want 空（内容未变）", stale)
	}
	if unverified != 40-codeFreshnessMaxReads {
		t.Errorf("unverified = %d, want %d", unverified, 40-codeFreshnessMaxReads)
	}

	// 说明里必须出现"未参与校验"，否则调用方无从判断可信度。
	note := freshnessNote(f.handle, unverified)
	if !strings.Contains(note, "未参与校验") {
		t.Errorf("freshnessNote 未披露未校验文件数：%s", note)
	}

	// 无根目录 → 不可判定（不得把相对路径当绝对路径去 stat 而误判为陈旧）。
	f.handle.Root = ""
	if known, _ := fileFresh(f.handle, ids[0]); known {
		t.Error("无根目录时应报 unknown（known=false），而不是判定陈旧或新鲜")
	}
}

func diskHash(t *testing.T, abs string) string {
	t.Helper()
	content, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}