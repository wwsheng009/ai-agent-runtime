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
// session_id），回复经 poller 出站；派发失败**不回放本机**（绑定属远端节点，
// 2026-10-17 真机 qq-bot1 双 actor 复盘）：IM 得到诚实提示，绑定保持远端。
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
	// 回归（2026-10-15 真机）：R1 必须携带绑定会话——runner 据此续用原会话，
	// 空 SessionID 会触发 runner 侧 auto-create（conn-<bot>-<chat>）并覆盖绑定。
	if got.Session.SessionID != "sess-9" || got.Session.NodeID != "aicli:node-remote" {
		mu.Unlock()
		t.Fatalf("dispatch must carry bound session, got %+v", got.Session)
	}
	mu.Unlock()
	if requests := backend.requests(); len(requests) != 0 {
		t.Fatalf("local backend must not be called on remote success, got %d", len(requests))
	}
	// 绑定回写：runner 返回的分叉 SessionID 不得覆盖已绑定会话（守卫），
	// NodeID 归一化为路由目标 peer（R3 无绑定场景才采纳 runner 的 auto-create）。
	if binding, ok := store.GetBinding("c1", "chat-1"); !ok ||
		binding.Session.SessionID != "sess-9" || binding.Session.NodeID != "aicli:node-remote" {
		t.Fatalf("binding after dispatch = %+v ok=%v", binding, ok)
	}

	// 远程失败：远端拥有的会话不回放本机——同 ID 在本进程重放会建双 actor
	// （历史分叉）并重放用户刚取消的回合。期望：IM 收到提示、绑定保持远端、
	// 本机 backend 零调用。
	remoteFail = true
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "again")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "dispatch failure notice", func() bool {
		for _, text := range provider.sentTexts() {
			if strings.Contains(text, "未在本机重放") {
				return true
			}
		}
		return false
	})
	if requests := backend.requests(); len(requests) != 0 {
		t.Fatalf("remote-owned binding failure must not replay locally, got %d local calls", len(requests))
	}
	if binding, ok := store.GetBinding("c1", "chat-1"); !ok ||
		binding.Session.NodeID != "aicli:node-remote" || binding.Session.SessionID != "sess-9" {
		t.Fatalf("binding must stay on remote node after dispatch failure, got %+v", binding.Session)
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
	// backend 续用绑定会话（接管语义：同一 session_id 在本机继续）；NodeID
	// 模拟真实本地 backend 形态（只回 SessionID）——空 NodeID 不得抹掉绑定
	// 已记录的旧归属（2026-10-17 真机 qq-bot1 复盘）。
	backend.session = SessionRef{SessionID: "sess-kept"}

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
	failoverNotices := func() int {
		n := 0
		for _, text := range provider.sentTexts() {
			if strings.Contains(text, "接管") {
				n++
			}
		}
		return n
	}
	if failoverNotices() != 1 {
		t.Fatalf("failover notice must fire once, sent = %v", provider.sentTexts())
	}
	// 第二条消息：绑定已是本机 + backend 仍只回 SessionID —— 不得二次提示
	// 或二次改写（空 NodeID 抹空会导致下一条消息再次假 R2a）。
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "again")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second reply", func() bool { return len(provider.finalMessages()) >= 2 })
	if failoverNotices() != 1 {
		t.Fatalf("blank backend NodeID must not retrigger failover, sent = %v", provider.sentTexts())
	}
	if binding, ok := store.GetBinding("c1", "chat-1"); !ok ||
		binding.Session.NodeID != "runtime-server" || binding.Session.SessionID != "sess-kept" {
		t.Fatalf("binding must stay local-owned after second message, got %+v", binding.Session)
	}
}

// TestServiceUnboundRemoteFailureFallsBackLocal：R3（无绑定）选举到远端但
// 派发失败 → 仍回落本机（可用性优先；本地 auto-create 派生会话，不存在
// 远端会话归属分叉，2026-10-17 双 actor 复盘的边界收敛）。
func TestServiceUnboundRemoteFailureFallsBackLocal(t *testing.T) {
	provider, backend, store, service := newDispatchTestSetup(t, func(context.Context, DispatchRequest) (DispatchResult, error) {
		return DispatchResult{}, errors.New("node unreachable")
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
	waitFor(t, "fallback reply", func() bool {
		for _, m := range provider.finalMessages() {
			if m.Text == "local-reply" {
				return true
			}
		}
		return false
	})
	if requests := backend.requests(); len(requests) != 1 {
		t.Fatalf("unbound remote failure must fall back local once, got %d", len(requests))
	}
	if binding, ok := store.GetBinding("c1", "chat-1"); !ok || binding.Session.SessionID != "sess-local" {
		t.Fatalf("auto-create binding after local fallback = %+v ok=%v", binding, ok)
	}
}

// TestServiceLocalBackendBlankNodeIDKeepsBindingOwner：本地 backend 只回
// SessionID（历史实现形态）时，绑定已记录的 NodeID 不得被抹空——抹空会让
// 下一条消息落进假 R2a 接管（2026-10-17 真机 qq-bot1：逐条消息抹空 → R2a
// 反复回写 + 「原节点 (空) 掉线」提示）。
func TestServiceLocalBackendBlankNodeIDKeepsBindingOwner(t *testing.T) {
	provider, backend, store, service := newDispatchTestSetup(t, func(context.Context, DispatchRequest) (DispatchResult, error) {
		t.Fatal("local-affinity binding must not remote-dispatch")
		return DispatchResult{}, nil
	})
	// 绑定归属本机（runtime-server）；backend 回包不带 NodeID。
	if err := store.PutBinding(Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: SessionRef{NodeID: "runtime-server", SessionID: "sess-keep"}}); err != nil {
		t.Fatal(err)
	}
	backend.session = SessionRef{SessionID: "sess-keep"}

	want := 1
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m1", "hi")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "first reply", func() bool { return len(provider.finalMessages()) >= want })
	want = 2
	if err := service.HandleInbound(context.Background(), inbound("c1", "chat-1", "m2", "hi again")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second reply", func() bool { return len(provider.finalMessages()) >= want })

	binding, ok := store.GetBinding("c1", "chat-1")
	if !ok || binding.Session.NodeID != "runtime-server" || binding.Session.SessionID != "sess-keep" {
		t.Fatalf("blank backend NodeID must not wipe binding owner, got %+v", binding.Session)
	}
	for _, text := range provider.sentTexts() {
		if strings.Contains(text, "接管") {
			t.Fatalf("blank NodeID must not trigger fake R2a takeover, sent = %v", provider.sentTexts())
		}
	}
}
