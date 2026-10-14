package main

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// TestIMMainChannelPredicate 锁定 D6 主通道判定与投影 hook 投递口径逐字同源：
// 绑定会话的 approval（命令回环）→ IM 主通道；completion 随 notify 开关；
// 未绑定/未登记 kind → 不占主通道（回退 Web Push）。
func TestIMMainChannelPredicate(t *testing.T) {
	if newIMMainChannelPredicate(nil) != nil {
		t.Fatal("nil store 必须返回 nil 判定（调用方一律走 push）")
	}

	// notify 未启用：approval 命令回环绑定会话仍应路由 IM；completion 不投。
	store := connectors.NewMemoryStore(connectors.Config{ID: "c1", Provider: "fake", Enabled: true})
	if err := store.PutBinding(connectors.Binding{ConnectorID: "c1", Chat: "chat-1",
		Session: connectors.SessionRef{SessionID: "s1"}}); err != nil {
		t.Fatal(err)
	}
	pred := newIMMainChannelPredicate(store)
	if pred == nil {
		t.Fatal("非 nil store 应返回可用判定")
	}
	if !pred("n1", "s1", "approval") {
		t.Fatal("绑定会话的 approval（命令回环）应占 IM 主通道")
	}
	if pred("n1", "s1", "completed") {
		t.Fatal("notify 未启用时 completed 并入门禁，不得占 IM 主通道")
	}
	if pred("n1", "s-other", "approval") {
		t.Fatal("未绑定会话不得占 IM 主通道")
	}
	if pred("n1", "s1", "waiting") {
		t.Fatal("waiting 不在投影词表（IM 不投），不得占主通道")
	}

	// notify.enabled=true：绑定会话的 completed 也应路由 IM。
	store2 := connectors.NewMemoryStore(connectors.Config{ID: "c2", Provider: "fake", Enabled: true,
		Policy: connectors.Policy{Notify: connectors.NotifyPolicy{Enabled: true}}})
	if err := store2.PutBinding(connectors.Binding{ConnectorID: "c2", Chat: "chat-2",
		Session: connectors.SessionRef{SessionID: "s2"}}); err != nil {
		t.Fatal(err)
	}
	pred2 := newIMMainChannelPredicate(store2)
	if !pred2("n1", "s2", "completed") {
		t.Fatal("notify.enabled 的绑定会话 completed 应占 IM 主通道")
	}
	// 停用的连接器不计入。
	store3 := connectors.NewMemoryStore(connectors.Config{ID: "c3", Provider: "fake", Enabled: false,
		Policy: connectors.Policy{Notify: connectors.NotifyPolicy{Enabled: true}}})
	if err := store3.PutBinding(connectors.Binding{ConnectorID: "c3", Chat: "chat-3",
		Session: connectors.SessionRef{SessionID: "s3"}}); err != nil {
		t.Fatal(err)
	}
	if newIMMainChannelPredicate(store3)("n1", "s3", "completed") {
		t.Fatal("停用连接器不得占 IM 主通道")
	}
}
