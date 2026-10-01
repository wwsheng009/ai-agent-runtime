package knowledge

// P0 收口回归（2026-10-01，评审 docs/plan/code-tools-gap-review-and-fix-plan-20260930.md §2/§6）：
//
//  1. 无关键字的方法声明（接口体/抽象方法）不得被抽成 kind=call 引用——
//     实测 planner.go 的 `Planner` / `ExplorationNodeReader` 接口方法声明行
//     曾被当成调用点（builtin/4 修复）；
//  2. return 行里的调用（复合字面量内 / 多值返回）必须被抽出并绑定——
//     这是评审 P0「引用索引漏报」的现场形态（planner.go:211/213）；
//  3. 增量重写后绑定稳定：陈旧索引曾让现场观测到"生产调用点缺失"。

import (
	"context"
	"testing"
)

func extractRefs(t *testing.T, path, src string) Extraction {
	t.Helper()
	file := FileRecord{ID: "f1", WorkspaceID: "ws", Path: path, Language: "go"}
	ex, err := builtinAdapter{}.Extract(context.Background(), file, []byte(src))
	if err != nil {
		t.Fatalf("Extract(%s): %v", path, err)
	}
	return ex
}

func TestBuiltinExtractorSkipsInterfaceMethodDeclarations(t *testing.T) {
	src := `package demo

type Planner interface {
	Plan(ctx context.Context, in PlanInput) (Plan, error)
	Lookup(ctx context.Context) ([]Node, error)
}

type TSShape interface {
	search(q: string): Promise<Hit[]>;
}
`
	ex := extractRefs(t, "pkg/iface.go", src)
	for _, r := range ex.Refs {
		switch r.Line {
		case 4, 5, 9:
			t.Fatalf("接口方法声明行不得成为引用: line=%d name=%q snippet=%q", r.Line, r.Name, r.Snippet)
		}
	}

	// 正例：同名真调用仍必须抽出（接口体之外）。
	callSrc := `package demo

func run(ctx context.Context) (Plan, error) {
	return Plan(ctx, nil, nil), nil
}
`
	callEx := extractRefs(t, "pkg/call.go", callSrc)
	found := false
	for _, r := range callEx.Refs {
		if r.Line == 4 && r.Name == "Plan" && r.Kind == RefCall {
			found = true
		}
	}
	if !found {
		t.Fatalf("接口体外的同名真调用必须抽出: %+v", callEx.Refs)
	}
}

func TestBuiltinExtractorKeepsCallsInReturnStatements(t *testing.T) {
	src := `package demo

func (p *storePlanner) Plan(ctx context.Context, in PlanInput) (Plan, error) {
	if err := load(ctx); err != nil {
		return Plan{Degraded: true, Reason: classifyStoreError(ctx, err)}, nil
	}
	return EvaluatePlan(in, nodes, opts.Config), nil
}
`
	ex := extractRefs(t, "pkg/return.go", src)
	want := map[string]int{"load": 4, "classifyStoreError": 5, "EvaluatePlan": 7}
	for name, line := range want {
		found := false
		for _, r := range ex.Refs {
			if r.Name == name && r.Line == line && r.Kind == RefCall {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("return 行里的调用必须抽出: name=%s line=%d refs=%+v", name, line, ex.Refs)
		}
	}
}

// 端到端：return 行调用绑定 + 增量重写后绑定稳定（评审 P0 的完整现场形态）。
func TestReferenceBindingStableAcrossIncrementalRewrites(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "pkg/def.go", `package demo

type Planner interface {
	Plan(ctx context.Context) (Plan, error)
}

func EvaluatePlan(in int) Plan { return Plan{} }

func classifyStoreError(err error) string { return "" }
`)
	caller := `package demo

func run() (Plan, error) {
	if err := load(); err != nil {
		return Plan{Degraded: true, Reason: classifyStoreError(err)}, nil
	}
	return EvaluatePlan(1), nil
}
`
	writeTree(t, root, "pkg/caller.go", caller)

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)
	if _, err := RunIndex(ctx, store, cfg); err != nil {
		t.Fatalf("RunIndex(initial): %v", err)
	}

	check := func(stage string) {
		t.Helper()
		for _, name := range []string{"EvaluatePlan", "classifyStoreError"} {
			refs, err := store.FindRefs(ctx, RefQuery{ToSymbolName: name, Kind: RefCall, Limit: 50})
			if err != nil {
				t.Fatalf("FindRefs(%s): %v", name, err)
			}
			bound := 0
			for _, r := range refs {
				if r.ToSymbolID != "" {
					bound++
				}
			}
			if bound == 0 {
				t.Fatalf("%s: return 行调用必须绑定到符号（stage=%s, refs=%+v）", name, stage, refs)
			}
		}
		// 接口方法声明行不得出现在引用表里（builtin/4）。
		refs, err := store.FindRefs(ctx, RefQuery{ToSymbolName: "Plan", Kind: RefCall, Limit: 50})
		if err != nil {
			t.Fatalf("FindRefs(Plan): %v", err)
		}
		for _, r := range refs {
			if r.Line == 4 {
				t.Fatalf("接口方法声明行不得成为引用: %+v", r)
			}
		}
	}
	check("initial")

	for i := 0; i < 3; i++ {
		writeTree(t, root, "pkg/caller.go", caller+"\n// rewrite probe\n")
		if _, err := RunIndex(ctx, store, cfg); err != nil {
			t.Fatalf("RunIndex(rewrite %d): %v", i, err)
		}
		check("rewrite")
	}
}
