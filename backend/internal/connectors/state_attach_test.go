package connectors

import (
	"context"
	"sync"
	"testing"
)

// stateAwareFakeProvider 在 fakeProvider 上叠加 StateAwareProvider 能力。
type stateAwareFakeProvider struct {
	*fakeProvider

	mu       sync.Mutex
	attached map[string]PollingState
}

func (p *stateAwareFakeProvider) AttachState(connectorID string, state PollingState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.attached == nil {
		p.attached = make(map[string]PollingState)
	}
	p.attached[connectorID] = state
}

func (p *stateAwareFakeProvider) attachedFor(connectorID string) (PollingState, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.attached[connectorID]
	return state, ok
}

// TestServiceSendOutboundAttachesProviderState 锁定 StateAwareProvider 接线：
// SendOutbound 前把该连接器的持久化视图附着给 provider（附着的是活视图——
// 经它写入的值立即对 Store 可见）；非能力 provider 行为不受影响。
func TestServiceSendOutboundAttachesProviderState(t *testing.T) {
	store := NewMemoryStore(Config{ID: "c1", Provider: "fake", Enabled: true})
	provider := &stateAwareFakeProvider{fakeProvider: &fakeProvider{}}
	service := New(Deps{Store: store})
	if err := service.Register(provider); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := service.SendOutbound(context.Background(), "c1", "u1", []OutboundMessage{{Text: "hi"}}); err != nil {
		t.Fatalf("SendOutbound: %v", err)
	}
	attached, ok := provider.attachedFor("c1")
	if !ok || attached == nil {
		t.Fatalf("state was not attached before Send")
	}
	if err := attached.Set("probe", "1"); err != nil {
		t.Fatalf("attached state Set: %v", err)
	}
	if got, ok := store.PollingState("c1").Get("probe"); !ok || got != "1" {
		t.Fatalf("attached state is not the live connector view: ok=%v got=%q", ok, got)
	}

	// 非能力 provider：不报错、行为与现状一致。
	plain := &fakeProvider{}
	plainService := New(Deps{Store: NewMemoryStore(Config{ID: "c2", Provider: "fake", Enabled: true})})
	if err := plainService.Register(plain); err != nil {
		t.Fatalf("Register plain: %v", err)
	}
	if err := plainService.SendOutbound(context.Background(), "c2", "u1", []OutboundMessage{{Text: "hi"}}); err != nil {
		t.Fatalf("SendOutbound plain: %v", err)
	}
}
