package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	toolkit "github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

// code_navigate 按位置查定义（direction=definition + file_path/line[/col]）：
// 语义通道优先，索引反查兜底（该行引用 → 目标符号；否则所在符号）。

type navEnvelope struct {
	Source      string  `json:"source"`
	Confidence  float64 `json:"confidence"`
	Degraded    bool    `json:"degraded"`
	Explanation string  `json:"explanation"`
	Results     []struct {
		Path  string `json:"path"`
		Name  string `json:"name"`
		Range struct {
			StartLine int `json:"start_line"`
			StartCol  int `json:"start_col"`
			EndLine   int `json:"end_line"`
			EndCol    int `json:"end_col"`
		} `json:"range"`
	} `json:"results"`
	Fallback *struct {
		Tool   string `json:"tool"`
		Reason string `json:"reason"`
	} `json:"fallback"`
}

func decodeNavEnvelope(t *testing.T, content string) navEnvelope {
	t.Helper()
	var env navEnvelope
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		t.Fatalf("decode: %v\n%s", err, content)
	}
	return env
}

// writeDemoTree 写一个最小 workspace：调用行 + 目标定义文件。
func writeDemoTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "internal/demo/demo.go", "package demo\n\nfunc use() {\n\tconsume(Target())\n}\n")
	writeFile(t, root, "internal/demo/target.go", "package demo\n\n// 行 4/5 填充\n\n\nfunc Target() {}\n")
	return root
}

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func buildNavigateTool(t *testing.T, root string, index *fakeCodeIndex, sem *fakeSemanticAdapter) *toolkit.CodeNavigateTool {
	t.Helper()
	tool := toolkit.NewCodeNavigateTool()
	tool.SetBasePath(root)
	handle := &toolkit.CodeIndexHandle{
		Index:       index,
		Mode:        knowledge.ModeOn,
		WorkspaceID: "ws-1",
		Root:        root,
		Semantic:    sem,
		FilePaths:   map[string]string{"f1": "internal/demo/demo.go", "f2": "internal/demo/target.go"},
	}
	tool.SetCodeIndexResolver(func(context.Context) (*toolkit.CodeIndexHandle, bool) {
		return handle, true
	})
	return tool
}

func TestCodeNavigateDefinitionByPositionPrefersSemantic(t *testing.T) {
	ctx := context.Background()
	root := writeDemoTree(t)
	sem := &fakeSemanticAdapter{
		defs: []knowledge.SemanticLocation{{Path: "internal/demo/target.go", Line: 5, Col: 5}},
	}
	tool := buildNavigateTool(t, root, &fakeCodeIndex{}, sem)

	result, err := tool.Execute(ctx, map[string]interface{}{
		"direction": "definition",
		"file_path": "internal/demo/demo.go",
		"line":      4, // 1-based：`\tconsume(Target())`
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute: err=%v result=%+v", err, result)
	}
	env := decodeNavEnvelope(t, result.Content)
	if env.Source != "lsp" {
		t.Fatalf("source=%q, want lsp", env.Source)
	}
	if len(env.Results) != 1 || env.Results[0].Name != "Target" {
		t.Fatalf("results=%+v, want 唯一 Target（从目标位置反读标识符）", env.Results)
	}
	if env.Results[0].Range.StartLine != 6 || env.Results[0].Range.StartCol != 5 {
		t.Fatalf("range=%d:%d, want 6:5（canonical 5 → 1-based 6）",
			env.Results[0].Range.StartLine, env.Results[0].Range.StartCol)
	}
	// col 缺省：应取该行第一个标识符（tab 后 consume → 0-based 1）。
	if len(sem.defCalls) == 0 || sem.defCalls[0].col != 1 || sem.defCalls[0].line != 3 {
		t.Fatalf("语义查询位置 = %+v, want line0=3 col=1（首个标识符）", sem.defCalls)
	}
}

func TestCodeNavigateDefinitionByPositionExplicitCol(t *testing.T) {
	ctx := context.Background()
	root := writeDemoTree(t)
	sem := &fakeSemanticAdapter{
		defs: []knowledge.SemanticLocation{{Path: "internal/demo/target.go", Line: 5, Col: 5}},
	}
	tool := buildNavigateTool(t, root, &fakeCodeIndex{}, sem)

	result, err := tool.Execute(ctx, map[string]interface{}{
		"direction": "definition",
		"file_path": "internal/demo/demo.go",
		"line":      4,
		"col":       10, // 1-based → canonical 9
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute: err=%v result=%+v", err, result)
	}
	if len(sem.defCalls) == 0 || sem.defCalls[0].col != 9 {
		t.Fatalf("语义查询列 = %+v, want 9（显式 col 1-based → 0-based）", sem.defCalls)
	}
}

func TestCodeNavigateDefinitionByPositionFallsBackToIndexRef(t *testing.T) {
	ctx := context.Background()
	root := writeDemoTree(t)
	index := &fakeCodeIndex{
		symbols: []knowledge.Symbol{{
			ID: "s1", Name: "Target", FileID: "f2", Kind: knowledge.SymbolFunction,
			Range: knowledge.Range{Start: knowledge.Position{Line: 6, Column: 6}, End: knowledge.Position{Line: 6, Column: 14}},
		}},
		refs: []knowledge.Reference{{
			FileID: "f1", Line: 4, Col: 10, Kind: knowledge.RefCall,
			ToSymbolID: "s1", ToSymbolName: "Target",
		}},
	}
	tool := buildNavigateTool(t, root, index, nil) // 无语义通道 → 索引反查

	result, err := tool.Execute(ctx, map[string]interface{}{
		"direction": "definition",
		"file_path": "internal/demo/demo.go",
		"line":      4,
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute: err=%v result=%+v", err, result)
	}
	env := decodeNavEnvelope(t, result.Content)
	if env.Source != "index" || env.Confidence != 1.0 {
		t.Fatalf("source=%q confidence=%v, want index/1.0", env.Source, env.Confidence)
	}
	if len(env.Results) != 1 || env.Results[0].Path != "internal/demo/target.go" || env.Results[0].Name != "Target" {
		t.Fatalf("results=%+v, want target.go 的 Target", env.Results)
	}
	if !strings.Contains(env.Explanation, "索引反查") {
		t.Fatalf("explanation=%q, want 含「索引反查」", env.Explanation)
	}
}

func TestCodeNavigateDefinitionByPositionFallsBackToEnclosingSymbol(t *testing.T) {
	ctx := context.Background()
	root := writeDemoTree(t)
	index := &fakeCodeIndex{
		allSymbols: []knowledge.Symbol{{
			ID: "s2", Name: "use", FileID: "f1", Kind: knowledge.SymbolFunction,
			Range: knowledge.Range{Start: knowledge.Position{Line: 3, Column: 1}, End: knowledge.Position{Line: 5, Column: 2}},
		}},
	}
	tool := buildNavigateTool(t, root, index, nil)

	result, err := tool.Execute(ctx, map[string]interface{}{
		"direction": "definition",
		"file_path": "internal/demo/demo.go",
		"line":      4,
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("Execute: err=%v result=%+v", err, result)
	}
	env := decodeNavEnvelope(t, result.Content)
	if env.Source != "index" || env.Confidence != 0.9 {
		t.Fatalf("source=%q confidence=%v, want index/0.9（所在符号口径）", env.Source, env.Confidence)
	}
	if len(env.Results) != 1 || env.Results[0].Name != "use" {
		t.Fatalf("results=%+v, want 所在符号 use", env.Results)
	}
	if !strings.Contains(env.Explanation, "所在符号") {
		t.Fatalf("explanation=%q, want 含「所在符号」", env.Explanation)
	}
}

func TestCodeNavigateDefinitionByPositionRequiresLine(t *testing.T) {
	ctx := context.Background()
	tool := buildNavigateTool(t, writeDemoTree(t), &fakeCodeIndex{}, nil)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"direction": "definition",
		"file_path": "internal/demo/demo.go",
	})
	if err != nil || result == nil {
		t.Fatalf("Execute: err=%v result=%+v", err, result)
	}
	if result.Success {
		t.Fatalf("缺 line 必须参数错误，实际成功：%s", result.Content)
	}
}
