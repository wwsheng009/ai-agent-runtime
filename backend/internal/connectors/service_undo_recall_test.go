package connectors

// /undo 与消息撤回的顺序契约：先回退历史，再撤回消息；撤回失败不影响回退。

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// recallProvider 记录回溯与撤回的调用顺序。
type recallProvider struct {
	basicProvider
	events       []string
	recallable   bool
	retractErr   error
	retractCalls []string
}

func (p *recallProvider) LastRecallableMessage(string) (string, bool) {
	p.events = append(p.events, "last-recallable")
	if !p.recallable {
		return "", false
	}
	return "msg-1", true
}

func (p *recallProvider) RetractMessage(_ context.Context, _ Config, _ string, messageID string) error {
	p.events = append(p.events, "retract")
	p.retractCalls = append(p.retractCalls, messageID)
	return p.retractErr
}

type orderedBacktrackBackend struct {
	fakeBackend
	events []string
	result BacktrackResult
}

func (b *orderedBacktrackBackend) Backtrack(context.Context, BacktrackRequest) (BacktrackResult, error) {
	b.events = append(b.events, "backtrack")
	return b.result, nil
}

func TestUndoBacktracksBeforeRetract(t *testing.T) {
	provider := &recallProvider{recallable: true}
	backend := &orderedBacktrackBackend{result: BacktrackResult{Handled: true, RemovedTurns: 1}}
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
	waitFor(t, "undo done", func() bool { return len(provider.sentTexts()) > 0 })

	if len(backend.events) != 1 || backend.events[0] != "backtrack" {
		t.Fatalf("backend events = %v", backend.events)
	}
	if len(provider.retractCalls) != 1 || provider.retractCalls[0] != "msg-1" {
		t.Fatalf("retract calls = %v", provider.retractCalls)
	}
}

func TestUndoRetractFailureDoesNotFailBacktrack(t *testing.T) {
	provider := &recallProvider{recallable: true, retractErr: errors.New("40064004")}
	backend := &orderedBacktrackBackend{result: BacktrackResult{Handled: true, RemovedTurns: 1}}
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
	waitFor(t, "undo done", func() bool { return len(provider.sentTexts()) > 0 })
	texts := provider.sentTexts()
	if !strings.Contains(texts[0], "已回退") {
		t.Fatalf("reply must report backtrack success: %v", texts)
	}
	if !strings.Contains(texts[0], "撤回失败") {
		t.Fatalf("reply must mention recall failure: %v", texts)
	}
}

func TestUndoExpiredRecallWindow(t *testing.T) {
	provider := &recallProvider{recallable: false}
	backend := &orderedBacktrackBackend{result: BacktrackResult{Handled: true, RemovedTurns: 1}}
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
	waitFor(t, "undo done", func() bool { return len(provider.sentTexts()) > 0 })
	texts := provider.sentTexts()
	if !strings.Contains(texts[0], "超过 2 分钟") {
		t.Fatalf("reply = %v", texts)
	}
	if len(provider.retractCalls) != 0 {
		t.Fatalf("expired message must not be retracted: %v", provider.retractCalls)
	}
}
