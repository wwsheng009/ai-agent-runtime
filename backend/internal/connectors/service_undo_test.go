package connectors

// /undo：轮次换算、宿主能力降级、先回退后撤回的顺序。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestResolveUndoTurnIndex(t *testing.T) {
	cases := []struct {
		total, before int
		want          int
		ok            bool
	}{
		{3, 0, 2, true},  // 回退最近一轮
		{3, 1, 1, true},  // 保留 1 轮
		{3, 2, 0, true},  // 保留 2 轮
		{3, 3, 0, false}, // 全保留 = 无回退
		{1, 0, 0, true},
		{0, 0, 0, false},
		{2, -1, 1, true}, // 负数按 0 处理
	}
	for _, tc := range cases {
		got, ok := ResolveUndoTurnIndex(tc.total, tc.before)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("total=%d before=%d → (%d,%v), want (%d,%v)",
				tc.total, tc.before, got, ok, tc.want, tc.ok)
		}
	}
}

func TestParseUndoTurns(t *testing.T) {
	for _, raw := range []string{"", "  ", "0", "1", "3"} {
		if _, err := parseUndoTurns(raw); err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
	}
	for _, raw := range []string{"-1", "abc", "1.5"} {
		if _, err := parseUndoTurns(raw); err == nil {
			t.Fatalf("%q must be rejected", raw)
		}
	}
}

// fakeBacktrackBackend 记录调用并返回预设结果。
type fakeBacktrackBackend struct {
	fakeBackend
	result    BacktrackResult
	err       error
	requests  []BacktrackRequest
	callCount int
}

func (b *fakeBacktrackBackend) Backtrack(_ context.Context, req BacktrackRequest) (BacktrackResult, error) {
	b.callCount++
	b.requests = append(b.requests, req)
	return b.result, b.err
}

func TestUndoUnsupportedHost(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeBackend{session: SessionRef{SessionID: "s1"}}
	store := NewMemoryStore(testConfig("c1", "basic"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "chat-1", "m1", "/undo")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "unsupported undo", func() bool { return len(provider.sentTexts()) > 0 })
	if texts := provider.sentTexts(); !strings.Contains(texts[0], "不支持") {
		t.Fatalf("reply = %v", texts)
	}
}

func TestUndoRequiresBinding(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeBacktrackBackend{}
	store := NewMemoryStore(testConfig("c1", "basic"))
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "chat-1", "m1", "/undo")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "no binding reply", func() bool { return len(provider.sentTexts()) > 0 })
	if texts := provider.sentTexts(); !strings.Contains(texts[0], "尚未建立对话") {
		t.Fatalf("reply = %v", texts)
	}
	if backend.callCount != 0 {
		t.Fatalf("backend must not be called without binding")
	}
}

func TestUndoSuccess(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeBacktrackBackend{result: BacktrackResult{
		Handled: true, RemovedTurns: 1, AnchorPreview: "上一个问题",
	}}
	store := NewMemoryStore(testConfig("c1", "basic"))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "chat-1", "m1", "/undo")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "undo reply", func() bool { return len(provider.sentTexts()) > 0 })
	if backend.callCount != 1 || backend.requests[0].TurnsBefore != 0 {
		t.Fatalf("requests = %+v", backend.requests)
	}
	if backend.requests[0].Session.SessionID != "sess-1" {
		t.Fatalf("session = %+v", backend.requests[0].Session)
	}
	if texts := provider.sentTexts(); !strings.Contains(texts[0], "已回退 1 轮") {
		t.Fatalf("reply = %v", texts)
	}
}

func TestUndoWithTurnsBefore(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeBacktrackBackend{result: BacktrackResult{Handled: true, RemovedTurns: 2}}
	store := NewMemoryStore(testConfig("c1", "basic"))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "chat-1", "m1", "/undo 1")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "undo reply", func() bool { return len(provider.sentTexts()) > 0 })
	if backend.requests[0].TurnsBefore != 1 {
		t.Fatalf("turnsBefore = %d", backend.requests[0].TurnsBefore)
	}
}

func TestUndoNoTurnsLeft(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeBacktrackBackend{result: BacktrackResult{Handled: false}}
	store := NewMemoryStore(testConfig("c1", "basic"))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "chat-1", "m1", "/undo")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "no turns reply", func() bool { return len(provider.sentTexts()) > 0 })
	if texts := provider.sentTexts(); !strings.Contains(texts[0], "没有可回退") {
		t.Fatalf("reply = %v", texts)
	}
}

func TestUndoBacktrackFailure(t *testing.T) {
	provider := &basicProvider{}
	backend := &fakeBacktrackBackend{err: errors.New("lease conflict")}
	store := NewMemoryStore(testConfig("c1", "basic"))
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	service := newTestService(t, store, backend, provider)

	if err := service.HandleInbound(context.Background(),
		inbound("c1", "chat-1", "m1", "/undo")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "failure reply", func() bool { return len(provider.sentTexts()) > 0 })
	if texts := provider.sentTexts(); !strings.Contains(texts[0], "回退失败") {
		t.Fatalf("reply = %v", texts)
	}
}

// /undo 必须是核心命令（不进 agent turn）。
func TestUndoIsCoreCommand(t *testing.T) {
	if !isCoreCommand("/", "/undo") {
		t.Fatal("/undo must be a core command")
	}
	if !isCoreCommand("/", "/undo 2") {
		t.Fatal("/undo with args must be a core command")
	}
}
