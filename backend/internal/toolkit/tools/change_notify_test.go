package tools

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// Phase 5 变更源 1（edit hook）的工具侧测试：编辑类工具落盘成功后必须把被写路径
// 报告给执行 ctx 上的接收方；失败不报告；未注入接收方时零副作用（off 基线）。

// recordingNotifier 记录编辑类工具报告的变更路径。
type recordingNotifier struct {
	mu    sync.Mutex
	paths []string
}

func (r *recordingNotifier) MarkChanged(paths ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, paths...)
}

func (r *recordingNotifier) reported() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := append([]string(nil), r.paths...)
	sort.Strings(out)
	return out
}

// relativePaths 把绝对路径归一为工作区相对形式，便于断言。
func relativePaths(t *testing.T, root string, paths []string) []string {
	t.Helper()
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if filepath.IsAbs(p) {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				t.Fatalf("rel(%s): %v", p, err)
			}
			p = rel
		}
		out = append(out, filepath.ToSlash(strings.TrimPrefix(p, "./")))
	}
	sort.Strings(out)
	return out
}

func TestEditClassToolsReportWrittenPaths(t *testing.T) {
	cases := []struct {
		name    string
		seed    map[string]string
		execute func(t *testing.T, root string, ctx context.Context) *toolkit.ToolResult
		want    []string
	}{
		{
			name: "write",
			seed: map[string]string{"demo/a.go": "package demo\n"},
			execute: func(t *testing.T, root string, ctx context.Context) *toolkit.ToolResult {
				tool := NewWriteTool()
				tool.SetBasePath(root)
				result, err := tool.Execute(ctx, map[string]interface{}{
					"file_path": "demo/a.go",
					"content":   "package demo\n\nfunc Added() int { return 1 }\n",
				})
				if err != nil {
					t.Fatalf("write: %v", err)
				}
				return result
			},
			want: []string{"demo/a.go"},
		},
		{
			name: "edit",
			seed: map[string]string{"demo/a.go": "package demo\n\nfunc Target() int { return 1 }\n"},
			execute: func(t *testing.T, root string, ctx context.Context) *toolkit.ToolResult {
				tool := NewEditTool()
				tool.SetBasePath(root)
				result, err := tool.Execute(ctx, map[string]interface{}{
					"file_path":  "demo/a.go",
					"old_string": "func Target() int { return 1 }",
					"new_string": "func Renamed() int { return 2 }",
				})
				if err != nil {
					t.Fatalf("edit: %v", err)
				}
				return result
			},
			want: []string{"demo/a.go"},
		},
		{
			name: "multiedit",
			seed: map[string]string{"demo/a.go": "package demo\n\nfunc Target() int { return 1 }\n"},
			execute: func(t *testing.T, root string, ctx context.Context) *toolkit.ToolResult {
				tool := NewMultieditTool()
				tool.SetBasePath(root)
				result, err := tool.Execute(ctx, map[string]interface{}{
					"file_path": "demo/a.go",
					"edits": []interface{}{
						map[string]interface{}{"old_string": "Target", "new_string": "Renamed"},
						map[string]interface{}{"old_string": "return 1", "new_string": "return 2"},
					},
				})
				if err != nil {
					t.Fatalf("multiedit: %v", err)
				}
				return result
			},
			want: []string{"demo/a.go"},
		},
		{
			name: "apply_patch",
			seed: map[string]string{
				"demo/a.go": "package demo\n\nfunc Target() int { return 1 }\n",
			},
			execute: func(t *testing.T, root string, ctx context.Context) *toolkit.ToolResult {
				tool := NewApplyPatchTool()
				tool.SetBasePath(root)
				patch := strings.Join([]string{
					"*** Begin Patch",
					"*** Update File: demo/a.go",
					"@@",
					"-func Target() int { return 1 }",
					"+func Renamed() int { return 2 }",
					"*** Add File: demo/c.go",
					"+package demo",
					"",
					"+func Added() int { return 3 }",
					"*** End Patch",
				}, "\n")
				result, err := tool.Execute(ctx, map[string]interface{}{"patch": patch})
				if err != nil {
					t.Fatalf("apply_patch: %v", err)
				}
				return result
			},
			want: []string{"demo/a.go", "demo/c.go"},
		},
		{
			name: "append_write",
			seed: map[string]string{"demo/a.go": "package demo\n"},
			execute: func(t *testing.T, root string, ctx context.Context) *toolkit.ToolResult {
				tool := NewAppendWriteTool()
				tool.SetBasePath(root)
				result, err := tool.Execute(ctx, map[string]interface{}{
					"file_path": "demo/a.go",
					"content":   "\nfunc Appended() int { return 4 }\n",
				})
				if err != nil {
					t.Fatalf("append_write: %v", err)
				}
				return result
			},
			want: []string{"demo/a.go"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for rel, content := range tc.seed {
				requireWriteFile(t, filepath.Join(root, filepath.FromSlash(rel)), content)
			}
			notifier := &recordingNotifier{}
			ctx := toolctx.WithFileChangeNotifier(context.Background(), notifier.MarkChanged)

			result := tc.execute(t, root, ctx)
			if result == nil || !result.Success {
				t.Fatalf("工具执行失败: %+v", result)
			}
			if got := relativePaths(t, root, notifier.reported()); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("报告路径 = %v, want %v", got, tc.want)
			}
		})
	}
}

// 失败的编辑不得报告变更（否则队列会为一个没被写的文件白跑一次增量）。
func TestEditToolDoesNotReportOnFailure(t *testing.T) {
	root := t.TempDir()
	requireWriteFile(t, filepath.Join(root, "demo", "a.go"), "package demo\n")

	notifier := &recordingNotifier{}
	ctx := toolctx.WithFileChangeNotifier(context.Background(), notifier.MarkChanged)
	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(ctx, map[string]interface{}{
		"file_path":  "demo/a.go",
		"old_string": "does-not-exist",
		"new_string": "x",
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if result == nil || result.Success {
		t.Fatalf("预期失败: %+v", result)
	}
	if got := notifier.reported(); len(got) != 0 {
		t.Fatalf("失败不得报告变更: %v", got)
	}
}

// 未注入接收方（知识层 off / reader / 非会话路径）时工具行为不变。
func TestEditToolsWithoutSinkAreUnchanged(t *testing.T) {
	root := t.TempDir()
	requireWriteFile(t, filepath.Join(root, "demo", "a.go"), "package demo\n")

	tool := NewWriteTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "demo/a.go",
		"content":   "package demo\n\nvar V = 1\n",
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("无接收方时工具行为必须不变: err=%v result=%+v", err, result)
	}
}

func TestChangeNotifyHelperIsNoopWithoutSink(t *testing.T) {
	notifyFileChanges(context.Background(), "/tmp/x.go")
	notifyFileChanges(nil, "/tmp/x.go")
	notifyFileChanges(context.Background())
	if got := toolctx.FileChangeNotifierFromContext(context.Background()); got != nil {
		t.Fatal("未注入时接收方必须是 nil")
	}
}

// 端到端：编辑工具 → ctx 上的接收方（会话知识层 MarkChanged）→ 变更队列 →
// debounce 后的定向增量 → store 里新旧符号正确替换。这是 edit hook 接线的整体证明。
func TestEditHookFeedsKnowledgeChangeQueue(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	requireWriteFile(t, filepath.Join(root, "demo", "a.go"),
		"package demo\n\nfunc Target() int { return 1 }\n")

	cfg := knowledge.DefaultConfig().WithWorkspace(root)
	cfg.Mode = knowledge.ModeShadow
	act, err := knowledge.Activate(ctx, cfg, root, knowledge.ActivationOptions{SkipInitialIndex: true})
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	t.Cleanup(func() { _ = act.Close() })
	if _, err := knowledge.RunIndex(ctx, act.Layer().Store(), cfg); err != nil {
		t.Fatalf("RunIndex: %v", err)
	}

	// 装配方（agent 侧）注入会话知识层的接收方；工具只认 ctx。
	toolCtx := toolctx.WithFileChangeNotifier(ctx, act.MarkChanged)
	tool := NewWriteTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(toolCtx, map[string]interface{}{
		"file_path": "demo/a.go",
		"content":   "package demo\n\nfunc Renamed() int { return 2 }\n",
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("write: err=%v result=%+v", err, result)
	}

	store := act.Layer().Store()
	deadline := time.Now().Add(5 * time.Second)
	for {
		syms, err := store.FindSymbols(ctx, knowledge.SymbolQuery{Name: "Renamed", Exact: true, Limit: 10})
		if err != nil {
			t.Fatalf("FindSymbols: %v", err)
		}
		if len(syms) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("编辑标记未在窗口内完成增量: pending=%d last=%+v err=%v",
				act.ChangeQueue().Pending(), act.ChangeQueue().LastResult(), act.ChangeQueue().LastError())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stale, err := store.FindSymbols(ctx, knowledge.SymbolQuery{Name: "Target", Exact: true, Limit: 10}); err != nil || len(stale) != 0 {
		t.Fatalf("旧符号必须随增量消失: %+v err=%v", stale, err)
	}
}
