package knowledge

import (
	"context"
	"testing"
)

// extract 跑一遍内置适配器，返回符号名集合与引用名集合。
func extractNames(t *testing.T, path, language, src string) (map[string]Symbol, map[string]int) {
	t.Helper()
	rec := FileRecord{WorkspaceID: "w1", Path: path, Language: language}
	ex, err := builtinAdapter{}.Extract(context.Background(), rec, []byte(src))
	if err != nil {
		t.Fatalf("Extract(%s): %v", path, err)
	}
	syms := make(map[string]Symbol, len(ex.Symbols))
	for _, s := range ex.Symbols {
		syms[s.QualifiedName] = s
	}
	refs := make(map[string]int, len(ex.Refs))
	for _, r := range ex.Refs {
		refs[r.Name]++
	}
	return syms, refs
}

// TestExtractOnlyIndexesTopLevelDeclarations 锁住 04 §2 的 Lazy 原则：
// 轻索引只覆盖"文件 + 顶层符号 + imports"，函数体内的局部变量不得成为符号。
//
// 背景（基线实测，见 reports/phase0_baseline_report.md §4.4）：builtin/2 会把
// `var output bytes.Buffer`、`const record = asRecord(raw);` 这类局部声明当成符号，
// 同目录下同名同签名时算出同一个 stable_key——本仓库 3860 个文件里这样的合并有
// 5080 行 / 1992 个键，其中 4295 行是局部变量。缩进是"非顶层"的可靠信号。
func TestExtractOnlyIndexesTopLevelDeclarations(t *testing.T) {
	goSrc := `package demo

import "bytes"

var Version = "1"

func Run() error {
	var output bytes.Buffer
	const limit = 3
	return helper(output)
}

func (c *Config) Validate() error {
	return nil
}
`
	syms, refs := extractNames(t, "demo/demo.go", "go", goSrc)

	for _, want := range []string{"Version", "Run", "Config.Validate"} {
		if _, ok := syms[want]; !ok {
			t.Fatalf("顶层符号 %q 必须被索引，实际 %v", want, keysOf(syms))
		}
	}
	for _, unwanted := range []string{"output", "limit"} {
		if _, ok := syms[unwanted]; ok {
			t.Fatalf("局部变量 %q 不得进入轻索引（04 §2 Lazy），实际 %v", unwanted, keysOf(syms))
		}
	}
	if refs["helper"] == 0 {
		t.Fatalf("缩进行里的调用点仍必须产出引用，refs=%v", refs)
	}

	tsSrc := `const record = asRecord(raw);

export function respondWith(body: unknown) {
  const record = asRecord(raw);
  return record;
}
`
	tsSyms, _ := extractNames(t, "web/app.ts", "typescript", tsSrc)
	if _, ok := tsSyms["record"]; !ok {
		t.Fatalf("顶层 const 必须被索引，实际 %v", keysOf(tsSyms))
	}
	if _, ok := tsSyms["respondWith"]; !ok {
		t.Fatalf("顶层函数必须被索引，实际 %v", keysOf(tsSyms))
	}
	// 顶层的 record 与函数内的 record 只应产出 1 个符号：局部那个必须被跳过。
	count := 0
	for name := range tsSyms {
		if name == "record" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("record 符号数 = %d, want 1（函数内的同名局部变量不得单独成符号）", count)
	}

	// Rust 的 impl 方法依赖缩进作用域，不适用"顶格"规则。
	rustSrc := `pub fn compute() -> i32 { 1 }

impl Job {
    fn run(&self) {}
}
`
	rustSyms, _ := extractNames(t, "svc/lib.rs", "rust", rustSrc)
	if _, ok := rustSyms["run"]; !ok {
		t.Fatalf("impl 内的 fn 必须被索引，实际 %v", keysOf(rustSyms))
	}

	// Python 规则表自身用 ^def/^class 锚定顶层，行为不应变化。
	pySyms, _ := extractNames(t, "svc/main.py", "python", "def run():\n    return helper()\n\n\nclass Worker:\n    pass\n")
	if _, ok := pySyms["run"]; !ok {
		t.Fatalf("顶层 def 必须被索引，实际 %v", keysOf(pySyms))
	}
	if _, ok := pySyms["Worker"]; !ok {
		t.Fatalf("顶层 class 必须被索引，实际 %v", keysOf(pySyms))
	}
}

func keysOf(m map[string]Symbol) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
