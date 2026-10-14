package connectors

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// ── P2 切片 B：路由决策接入（processMessage → DecideRoute → RemoteDispatch）──

// dispatchRecordingBackend 记录本地 Handle 调用（回落路径断言用）。
type dispatchRecordingBackend struct {
	fakeBackend
}

func newDispatchTestSetup(t *testing.T, remote func(ctx context.Context, req DispatchRequest) (DispatchResult, error)) (*fakeProvider, *fakeBackend, *MemoryStore, *Service) {
	t.Helper()
	provider := &fakeProvider{}
	backend := &fakeBackend{session: SessionRef{NodeID: "runtime-server", SessionID: "sess-local"}, reply: []OutboundMessage{{Text: "local-reply"}}}
	store := NewMemoryStore(testConfig("c1", "fake"))
	var deps []func(*Deps)
	if remote != nil {
		deps = append(deps, func(d *Deps) {
			d.HostID = "runtime-server"
			d.Peers = func() []RunnerPeer {
				return []RunnerPeer{
					{HostKind: HostKindRuntimeServer, NodeID: "runtime-server", Alive: true, Local: true},
					{HostKind: HostKindAICLI, NodeID: "aicli:node-remote", Alive: true},
				}
			}
			d.RemoteDispatch = remote
		})
	}
	service := newTestService(t, store, backend, provider, deps...)
	return provider, backend, store, service
}

// TestServiceUnboundDefaultsToLocal：默认偏好（runtime-server 优先）+ 本机是
// runtime-server → 无绑定消息本机执行（R3 命中本机），不触发远程派发。
func TestServiceUnboundDefaultsToLocal(t *testing.T) {
	remoteCalled := false
	provider, backend, store, service := newDispatchTestSetup(t, func(context.Context, DispatchRequest) (DispatchResult, error) {
		remoteCalled = true
		return DispatchResult{}, nil
	})

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "local reply", func() bool { return len(provider.finalMessages()) > 0 })
	if remoteCalled {
		t.Fatal("default preference must keep unbound traffic local (runtime-server local)")
	}
	if requests := backend.requests(); len(requests) != 1 {
		t.Fatalf("backend requests = %d, want 1", len(requests))
	}
	// auto-create 绑定落库（既有路径）。
	if _, ok := store.GetBinding("c1", "chat-1"); !ok {
		t.Fatal("binding must be persisted after local handle")
	}
}

// TestServiceR1AffinityDispatch：绑定指向远端 aicli 节点 → 远程派发（带
// session_id），回复经 poller 出站；派发失败回落本机。
func TestServiceR1AffinityDispatch(t *testing.T) {
	var mu sync.Mutex
	var got DispatchRequest
	remoteErr := error(nil)
	remoteFail := false
	provider, backend, store, service := newDispatchTestSetup(t, func(_ context.Context, req DispatchRequest) (DispatchResult, error) {
		t.Logf("remote dispatch called: target=%s text=%q", req.Target.NodeID, req.Text)
		mu.Lock()
		got = req
		mu.Unlock()
		if remoteFail {
			return DispatchResult{}, errors.New("node unreachable")
		}
		return DispatchResult{
			Reply:   []OutboundMessage{{Text: "remote-reply"}},
			Session: SessionRef{NodeID: "aicli:node-remote", SessionID: "sess-remote"},
		}, nil
	})
	_ = remoteErr

	// 预置绑定：会话在远端 aicli 节点。
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{NodeID: "aicli:node-remote", SessionID: "sess-9"}}); err != nil {
		t.Fatal(err)
	}

	// 远程成功：回复来自 remote-reply（派发路径直接 provider.Send，不走 streaming），本地 backend 不被调用。
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "remote reply", func() bool {
		for _, text := range provider.sentTexts() {
			if text == "remote-reply" {
				return true
			}
		}
		return false
	})
	mu.Lock()
	if got.Target.NodeID != "aicli:node-remote" || got.Text != "hi" || got.MessageID != "m1" {
		mu.Unlock()
		t.Fatalf("dispatch request = %+v", got)
	}
	mu.Unlock()
	if requests := backend.requests(); len(requests) != 0 {
		t.Fatalf("local backend must not be called on remote success, got %d", len(requests))
	}
	// 绑定回写（runner 侧返回的 Session）。
	if binding, ok := store.GetBinding("c1", "chat-1"); !ok || binding.Session.SessionID != "sess-remote" {
		t.Fatalf("binding after dispatch = %+v ok=%v", binding, ok)
	}

	// 远程失败：回落本机执行（backend 被调用，回复 local-reply 走 streaming Final）。
	remoteFail = true
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "again")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "fallback reply", func() bool {
		for _, m := range provider.finalMessages() {
			if m.Text == "local-reply" {
				return true
			}
		}
		return false
	})
	if requests := backend.requests(); len(requests) != 1 {
		t.Fatalf("fallback must hit local backend once, got %d", len(requests))
	}
}

// TestServiceUnboundAICLIPreferenceDispatchesRemote：aicli 偏好 + 无绑定 →
// 远程派发到 aicli 节点（auto-create；回写新绑定）。
func TestServiceUnboundAICLIPreferenceDispatchesRemote(t *testing.T) {
	provider, _, store, service := newDispatchTestSetup(t, func(_ context.Context, req DispatchRequest) (DispatchResult, error) {
		return DispatchResult{
			Reply:   []OutboundMessage{{Text: "aicli-reply"}},
			Session: SessionRef{NodeID: "aicli:node-remote", SessionID: "sess-auto"},
		}, nil
	})
	// per-connector settings 覆盖位（09 §10.4）：settings.runner_preference=aicli。
	cfg, ok := store.GetConfig("c1")
	if !ok {
		t.Fatal("config c1 missing")
	}
	cfg.Settings = map[string]string{RunnerPreferenceSettingKey: "aicli"}
	if err := store.PutConfig(cfg); err != nil {
		t.Fatal(err)
	}

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "aicli reply", func() bool {
		for _, text := range provider.sentTexts() {
			if text == "aicli-reply" {
				return true
			}
		}
		return false
	})
	if binding, ok := store.GetBinding("c1", "chat-1"); !ok || binding.Session.SessionID != "sess-auto" {
		t.Fatalf("auto-create binding = %+v ok=%v", binding, ok)
	}
}

// TestServiceNoPeersMeansLocalAlways：Peers 未装配（单机模式）→ 永不远程派发，
// 行为与历史一致。
func TestServiceNoPeersMeansLocalAlways(t *testing.T) {
	provider, backend, _, service := newDispatchTestSetup(t, nil)
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reply", func() bool { return len(provider.finalMessages()) > 0 })
	if requests := backend.requests(); len(requests) != 1 {
		t.Fatalf("standalone mode must stay local, requests = %d", len(requests))
	}
}

// TestServiceR2aFailoverTakeover：绑定 NodeID 掉线 → 本机接管（切片 C）：
// 本机执行、回写 NodeID 为本宿主身份、IM 提示一次。
func TestServiceR2aFailoverTakeover(t *testing.T) {
	provider, backend, store, service := newDispatchTestSetup(t, func(context.Context, DispatchRequest) (DispatchResult, error) {
		t.Fatal("R2a must not remote-dispatch")
		return DispatchResult{}, nil
	})
	// 绑定指向已掉线节点。
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{NodeID: "aicli:node-stale", SessionID: "sess-kept"}}); err != nil {
		t.Fatal(err)
	}
	// backend 续用绑定会话（接管语义：同一 session_id 在本机继续）。
	backend.session = SessionRef{NodeID: "aicli:node-stale", SessionID: "sess-kept"}

	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reply", func() bool { return len(provider.sentTexts()) > 0 })
	// NodeID 回写为本宿主。
	binding, ok := store.GetBinding("c1", "chat-1")
	if !ok || binding.Session.NodeID != "runtime-server" || binding.Session.SessionID != "sess-kept" {
		t.Fatalf("binding after failover = %+v", binding.Session)
	}
	// IM 提示一次（接管文案）。
	notified := false
	for _, text := range provider.sentTexts() {
		if strings.Contains(text, "接管") {
			notified = true
			break
		}
	}
	if !notified {
		t.Fatalf("failover notice missing, sent = %v", provider.sentTexts())
	}
}
