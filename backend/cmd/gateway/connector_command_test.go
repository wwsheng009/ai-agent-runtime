// gateway 连接器注意力出站（FR-8）与命令闭环（FR-7）的宿主接线测试：
// 用投影 hook 驱动断言去重/清记录/D5 过滤/出站；用 fakeProxy 驱动 CommandBackend，
// 断言 /approve /answer 的 invoke prompt 形状与会话透传、命令回执映射。
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
)

// stubConnectorBackend 满足 connectors.Backend（出站测试不经过入站路径；
// 与 runtime-server 同名替身同构）。
type stubConnectorBackend struct{}

func (stubConnectorBackend) Name() string { return "stub" }

func (stubConnectorBackend) Handle(context.Context, connectors.BackendRequest, connectors.ReplySink) (connectors.BackendResult, error) {
	return connectors.BackendResult{}, nil
}

// ── 注意力出站：投影 hook ───────────────────────────────────────────────────

type recordingNotifier struct {
	mu    sync.Mutex
	calls []connectors.AttentionNotice
}

func (r *recordingNotifier) NotifyAttention(_ context.Context, notice connectors.AttentionNotice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, notice)
	return nil
}

func notifierSnapshot(r *recordingNotifier) []connectors.AttentionNotice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]connectors.AttentionNotice(nil), r.calls...)
}

// newHookTestService 保留给后续端到端用例（真实 Service 出站链）。
func newHookTestService(store connectors.Store) *connectors.Service {
	return connectors.New(connectors.Deps{
		Store:   store,
		Backend: stubConnectorBackend{},
		Logf:    func(string, ...any) {},
	})
}
func TestGatewayAttentionHookDedupesAndClearsOnClear(t *testing.T) {
	notifier := &recordingNotifier{}
	// notify.enabled=true：completed 投影（completion）随开关（2026-10-14 静音
	// 口径）；本用例要覆盖 completed 的重投影去重，故显式开启。
	store := connectors.NewMemoryStore(connectors.Config{ID: "c1", Provider: "fake", Enabled: true,
		Policy: connectors.Policy{Notify: connectors.NotifyPolicy{Enabled: true}}})
	if err := store.PutBinding(connectors.Binding{ConnectorID: "c1", Chat: "chat-1", Session: connectors.SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	// hook 的可注入形态直接接 recordingNotifier（不依赖 Provider.Send 真实链路）。
	hook := newGatewayAttentionHookNotify(context.Background(), store, func(_ context.Context, _ []string, notice connectors.AttentionNotice) error {
		return notifier.NotifyAttention(context.Background(), notice)
	}, func(string, ...any) {})

	// 同一 kind 重复投影只推一次。
	hook("node-a", "sess-1", "running", "approval")
	hook("node-a", "sess-1", "running", "approval")
	waitForNotifier(t, notifier, 1)

	// kind 变化（approval → completed）推第二次。
	hook("node-a", "sess-1", "idle", "completed")
	waitForNotifier(t, notifier, 2)

	// attention 消失清记录：再次 approval 是新事件。
	hook("node-a", "sess-1", "idle", "")
	hook("node-a", "sess-1", "running", "approval")
	waitForNotifier(t, notifier, 3)
}

func TestGatewayAttentionHookRespectsNotifyPolicy(t *testing.T) {
	notifier := &recordingNotifier{}
	// D5：notify 未启用（零值）时，「未绑定的其它会话」事件必须被抑制
	//（绑定会话的 approval 命令回环始终接收；completion 随开关）。
	store := connectors.NewMemoryStore(connectors.Config{ID: "c1", Provider: "fake", Enabled: true})
	if err := store.PutBinding(connectors.Binding{ConnectorID: "c1", Chat: "chat-1", Session: connectors.SessionRef{SessionID: "sess-1"}}); err != nil {
		t.Fatal(err)
	}
	hook := newGatewayAttentionHookNotify(context.Background(), store, func(_ context.Context, _ []string, notice connectors.AttentionNotice) error {
		return notifier.NotifyAttention(context.Background(), notice)
	}, func(string, ...any) {})
	// 未绑定会话 sess-other：notify.enabled=false → 不推。
	hook("node-a", "sess-other", "running", "approval")
	time.Sleep(150 * time.Millisecond)
	if got := notifierSnapshot(notifier); len(got) != 0 {
		t.Fatalf("notify disabled must suppress unbound-session delivery, got %d", len(got))
	}
	// 绑定会话 sess-1 的 approval：即使 notify 未开启也送达（命令回环）。
	hook("node-a", "sess-1", "running", "approval")
	waitForNotifier(t, notifier, 1)
}

func waitForNotifier(t *testing.T, notifier *recordingNotifier, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(notifierSnapshot(notifier)) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("notifier did not reach %d calls in time (got %d)", want, len(notifierSnapshot(notifier)))
}

// ── 命令闭环：CommandBackend 经隧道转发 ─────────────────────────────────────

func TestGatewayNodeBackendCommandForwarding(t *testing.T) {
	proxy := &fakeProxy{respond: func(_ string, r *http.Request) (int, string) {
		// 请求体已由 fakeProxy.ServeProxy 统一记录在 calls（respond 阶段 body
		// 已被消费）；prompt/session 从 lastCall 的 body 解析。
		return http.StatusOK, `{"status":"completed","assistant":{"role":"assistant","content":"已批准：apr-1（已恢复运行）"}}`
	}}
	backend := newGatewayNodeBackend(
		newFakeRegistry(&tunnel.Node{NodeID: "node-a", Stale: false}),
		proxy,
		commandFakeStore{},
	)

	result, err := backend.DecideApproval(context.Background(), connectors.CommandRequest{
		Command: "approve",
		Args:    []string{"apr-1"},
		Session: connectors.SessionRef{NodeID: "node-a", SessionID: "sess-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Handled || len(result.Reply) == 0 || !strings.Contains(result.Reply[0].Text, "已批准") {
		t.Fatalf("approve result = %+v", result)
	}
	call := proxy.lastCall(t)
	var req1 gatewayNodeInvokeRequest
	if err := json.Unmarshal(call.body, &req1); err != nil {
		t.Fatalf("approve invoke body: %v", err)
	}
	if req1.Prompt != "/approve apr-1" {
		t.Fatalf("approve prompt = %q", req1.Prompt)
	}
	if req1.SessionID != "sess-1" {
		t.Fatalf("session passthrough = %q", req1.SessionID)
	}

	if _, err := backend.AnswerQuestion(context.Background(), connectors.CommandRequest{
		Command: "answer",
		Args:    []string{"1", "use staging"},
		Session: connectors.SessionRef{NodeID: "node-a", SessionID: "sess-1"},
	}); err != nil {
		t.Fatal(err)
	}
	call = proxy.lastCall(t)
	var req2 gatewayNodeInvokeRequest
	if err := json.Unmarshal(call.body, &req2); err != nil {
		t.Fatalf("answer invoke body: %v", err)
	}
	if req2.Prompt != "/answer 1 use staging" {
		t.Fatalf("answer prompt = %q", req2.Prompt)
	}
}

func TestGatewayNodeBackendCommandNodeErrors(t *testing.T) {
	// 空 registry（无在线节点）→ 明确错误，不猜节点。
	backend := newGatewayNodeBackend(newFakeRegistry(), &fakeProxy{}, commandFakeStore{})
	if _, err := backend.DecideApproval(context.Background(), connectors.CommandRequest{Command: "approve"}); err == nil {
		t.Fatal("expected node resolution error")
	}
}

// commandFakeStore 满足 gatewayConnectorStore：无显式 node_id 设置（pickNode
// 走在线节点唯一性自动选择）。
type commandFakeStore struct{}

func (commandFakeStore) GetConfig(string) (connectors.Config, bool) {
	return connectors.Config{}, false
}
