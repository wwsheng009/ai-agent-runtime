package connectors

// /stop：宿主能力降级、绑定会话透传与回执语义（与 TUI Esc / Web interrupt 对齐）。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeInterruptBackend 记录 Interrupt 调用并返回预设结果。
type fakeInterruptBackend struct {
	fakeBackend
	result    InterruptResult
	err       error
	requests  []InterruptRequest
	callCount int
}

func (b *fakeInterruptBackend) Interrupt(_ context.Context, req InterruptRequest) (InterruptResult, error) {
	b.callCount++
	b.requests = append(b.requests, req)
	return b.result, b.err
}

func TestStopUnsupportedHost(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}}
	store := NewMemoryStore(testConfig("c1", "basic"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "chat-1", "m1", "/stop")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "unsupported stop", func() bool { return len(provider.sentTexts()) > 0 })
	if texts := provider.sentTexts(); !strings.Contains(texts[0], "未接入停止命令") {
		t.Fatalf("reply = %v", texts)
	}
	// 命令不落进普通 turn（isCoreCommand 由 agentCommands 表驱动）。
	if len(backend.handled) != 0 {
		t.Fatalf("stop must not reach Backend.Handle: %+v", backend.handled)
	}
}

func TestStopPassesBoundSessionAndStoppedReceipt(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeInterruptBackend{result: InterruptResult{Handled: true, Stopped: true}}
	store := NewMemoryStore(testConfig("c1", "basic"))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "chat-1", "m1", "/stop")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop reply", func() bool { return len(provider.sentTexts()) > 0 })
	if backend.callCount != 1 {
		t.Fatalf("callCount = %d，期望 1", backend.callCount)
	}
	if got := backend.requests[0].Session.SessionID; got != "sess-1" {
		t.Fatalf("session = %q，期望绑定会话", got)
	}
	if texts := provider.sentTexts(); !strings.Contains(texts[0], "已请求停止当前回合") {
		t.Fatalf("reply = %v", texts)
	}
}

func TestStopIdleAndUnboundReceipts(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeInterruptBackend{result: InterruptResult{Handled: true}}
	store := NewMemoryStore(testConfig("c1", "basic"))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "chat-1", "m1", "/stop")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "idle stop reply", func() bool { return len(provider.sentTexts()) > 0 })
	if texts := provider.sentTexts(); !strings.Contains(texts[0], "没有在途回合可停止") {
		t.Fatalf("reply = %v", texts)
	}

	// 宿主无法定位会话（Handled=false）→ 更明确的“没有可停止的回合”。
	backend2 := &fakeInterruptBackend{result: InterruptResult{}}
	service2 := newTestService(t, store, backend2, provider)
	if err := service2.HandleInbound(context.Background(),
		inbound("c1", "chat-1", "m2", "/stop")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "unhandled stop reply", func() bool { return len(provider.sentTexts()) > 1 })
	if texts := provider.sentTexts(); !strings.Contains(texts[1], "没有可停止的回合") {
		t.Fatalf("reply = %v", texts)
	}
}

func TestStopHostErrorSurfaces(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeInterruptBackend{err: errors.New("hub unavailable")}
	store := NewMemoryStore(testConfig("c1", "basic"))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "chat-1", "m1", "/stop")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "stop error reply", func() bool { return len(provider.sentTexts()) > 0 })
	if texts := provider.sentTexts(); !strings.Contains(texts[0], "停止失败") {
		t.Fatalf("reply = %v", texts)
	}
}
