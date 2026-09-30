package tools

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/lsp"
)

func TestLSPObserverHubSetAndEmit(t *testing.T) {
	hub := &lspObserverHub{}
	hub.emit(lsp.Event{Kind: lsp.EventRequest, Outcome: "injected"}) // 未注入：静默丢弃

	var got []lsp.Event
	hub.set(func(event lsp.Event) { got = append(got, event) })
	hub.emit(lsp.Event{Kind: lsp.EventRequest, Outcome: "injected", DurationMS: 12})
	if len(got) != 1 || got[0].Outcome != "injected" || got[0].DurationMS != 12 {
		t.Fatalf("events = %#v, want one injected/12ms", got)
	}

	hub.set(nil)
	hub.emit(lsp.Event{Kind: lsp.EventRequest, Outcome: "clean"})
	if len(got) != 1 {
		t.Fatalf("nil host 后不应继续投递，got %d", len(got))
	}
}

func TestManagerSetLSPObserverNilSafe(t *testing.T) {
	// 未启用 LSP 的 manager 也能安全接线/停止接线（构造期 hub 已存在）。
	manager := NewDefaultManagerWithRuntimeConfig(nil, nil)
	manager.SetLSPObserver(func(lsp.Event) {})
	manager.SetLSPObserver(nil)
	if snapshot, ok := manager.LSPMetrics(); ok {
		t.Fatalf("未启用池时 LSPMetrics ok=true（snapshot=%#v）", snapshot)
	}
}
