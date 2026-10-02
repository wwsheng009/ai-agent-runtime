//go:build live_semantic

package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// firstPreset 返回 gopls 预设（实验用；缺失即跳过）。
func firstPreset(t *testing.T) []ServerSpec {
	t.Helper()
	return PresetServersNamed([]string{"gopls"})
}

// TestLiveRootAnchoringExperiment 是**一次性**决策实验（2026-10-01 收口轮十一）：
// 诊断池当前把 gopls 锚在 workspace 根（本仓库 = repo 根，go.mod 在 backend/），
// 语义通道锚在模块根。两者要合并成单实例就必须选同一个锚点。
//
// 判据：同一份文件在两种锚点下能否拿到 type-check 级诊断。
//   - repo 根锚定：gopls 进入 adhoc/无模块模式，导入解析可能整体失效
//   - 模块根锚定：真实 go.mod 作用域
//
// 判据错误的后果不对称：把池改锚到模块根若质量下降是回归；反之不动池、
// 让语义通道降级到 repo 根锚定则是已知的精度损失。两者都要实测。
func TestLiveRootAnchoringExperiment(t *testing.T) {
	if testing.Short() {
		t.Skip("live 实验")
	}
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("abs module root: %v", err)
	}
	if _, err := os.Stat(filepath.Join(moduleRoot, "go.mod")); err != nil {
		t.Skipf("找不到 backend/go.mod（仓���布局与实验假设不符）: %v", err)
	}
	workspaceRoot := filepath.Dir(moduleRoot)
	target, err := filepath.Abs(filepath.Join(moduleRoot, "internal", "toolkit", "tools", "code_common.go"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	for _, anchor := range []struct {
		name string
		root string
	}{
		{"workspace_root", workspaceRoot},
		{"module_root", moduleRoot},
	} {
		t.Run(anchor.name, func(t *testing.T) {
			root, err := filepath.Abs(anchor.root)
			if err != nil {
				t.Fatalf("abs: %v", err)
			}
			spec := firstPreset(t)
			if len(spec) == 0 {
				t.Skip("gopls 预设缺失")
			}
			client, err := NewClient(ClientOptions{Spec: spec[0], Root: root, StartupTimeout: 60 * time.Second})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			if err := client.Start(ctx); err != nil {
				t.Fatalf("Start: %v", err)
			}
			t.Cleanup(func() {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer shutdownCancel()
				client.Shutdown(shutdownCtx)
			})
			if _, err := client.OpenOrUpdate(ctx, target, content); err != nil {
				t.Fatalf("OpenOrUpdate: %v", err)
			}
			// textDocument/diagnostic（pull）：直接拿服务端算出的诊断集合。
			raw, err := client.Call(ctx, "textDocument/diagnostic", map[string]interface{}{
				"textDocument": map[string]interface{}{"uri": PathToURI(target)},
			})
			if err != nil {
				t.Fatalf("textDocument/diagnostic: %v", err)
			}
			items := decodeDiagnosticItems(t, raw)
			t.Logf("anchor=%s pid=%d diagnostics=%d", anchor.name, client.PID(), len(items))
			for _, item := range items {
				t.Logf("  [%d:%d] %s", item.line, item.col, item.message)
			}
			if len(items) == 0 {
				t.Logf("anchor=%s: 无诊断（干净或未解析，需结合 symbol 查询判断）", anchor.name)
			}
		})
	}

	// 交叉验证：两种锚点下能否对同一符号做 definition（符号解析是否真的工作，
	// 而不是"无诊断因为根本没解析"）。
	t.Run("definition_crosscheck", func(t *testing.T) {
		for _, anchor := range []string{workspaceRoot, moduleRoot} {
			root, _ := filepath.Abs(anchor)
			spec := firstPreset(t)
			if len(spec) == 0 {
				continue
			}
			client, err := NewClient(ClientOptions{Spec: spec[0], Root: root, StartupTimeout: 60 * time.Second})
			if err != nil {
				t.Fatalf("NewClient(%s): %v", anchor, err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			if err := client.Start(ctx); err != nil {
				cancel()
				t.Fatalf("Start(%s): %v", anchor, err)
			}
			content, _ := os.ReadFile(target)
			if _, err := client.OpenOrUpdate(ctx, target, content); err != nil {
				t.Fatalf("OpenOrUpdate(%s): %v", anchor, err)
			}
			raw, err := client.Call(ctx, "textDocument/definition", map[string]interface{}{
				"textDocument": map[string]interface{}{"uri": PathToURI(target)},
				"position":     map[string]interface{}{"line": 40, "character": 10},
			})
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
			client.Shutdown(shutdownCtx)
			shutdownCancel()
			cancel()
			if err != nil {
				t.Logf("anchor=%s definition 失败: %v", anchor, err)
				continue
			}
			if isNullOrEmpty(raw) {
				t.Logf("anchor=%s definition: 空结果（符号未解析）", anchor)
				continue
			}
			t.Logf("anchor=%s definition: %s", anchor, truncateForLog(string(raw), 200))
		}
	})
}

type diagnosticItem struct {
	line, col int
	message   string
}

func decodeDiagnosticItems(t *testing.T, raw []byte) []diagnosticItem {
	t.Helper()
	if len(raw) == 0 || isNullOrEmpty(raw) {
		return nil
	}
	var resp struct {
		Items []struct {
			Range struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
			} `json:"range"`
			Message string `json:"message"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode diagnostics: %v", err)
	}
	items := make([]diagnosticItem, 0, len(resp.Items))
	for _, item := range resp.Items {
		items = append(items, diagnosticItem{line: item.Range.Start.Line, col: item.Range.Start.Character, message: item.Message})
	}
	return items
}

func isNullOrEmpty(raw []byte) bool {
	trimmed := strings.TrimSpace(string(raw))
	return len(trimmed) == 0 || trimmed == "null" || trimmed == "[]" || trimmed == "{}"
}

func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
