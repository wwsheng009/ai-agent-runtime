package feishu

// 回复目标记忆的跨重启持久化测试（feishu_state.go）：
//   - rememberReplyTarget 变更检测（未变化不落盘）；
//   - 快照落盘 → 新 provider 附着同一存储 → 回复目标直接可用（无需等下一条
//     入站才有 quote 目标）；
//   - 未附着状态视图的 provider 行为不变（进程内记忆照常工作）；
//   - 内存已有值优先于快照（附着回填不覆盖更新的进程内记忆）。

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func TestRememberReplyTargetChangeDetection(t *testing.T) {
	fake := newFakeFeishu(t)
	store := connectors.NewMemoryStore()
	provider := newTestProvider(t, fake)
	provider.AttachState("conn-1", store.PollingState("conn-1"))

	provider.rememberReplyTarget("conn-1", "oc_chat_1", "om_1")
	set, _ := store.PollingState("conn-1").Get(replyTargetsStateKey)
	if !strings.Contains(set, `"oc_chat_1":"om_1"`) {
		t.Fatalf("snapshot after first remember = %q", set)
	}
	provider.rememberReplyTarget("conn-1", "oc_chat_1", "om_1") // 未变化不落盘
	same, _ := store.PollingState("conn-1").Get(replyTargetsStateKey)
	if same != set {
		t.Fatalf("unchanged remember rewrote snapshot: %q != %q", same, set)
	}
	provider.rememberReplyTarget("conn-1", "oc_chat_1", "om_2")
	updated, _ := store.PollingState("conn-1").Get(replyTargetsStateKey)
	if !strings.Contains(updated, `"oc_chat_1":"om_2"`) {
		t.Fatalf("snapshot after change = %q", updated)
	}
	if got := provider.replyTarget("conn-1", "oc_chat_1"); got != "om_2" {
		t.Fatalf("reply target = %q, want om_2", got)
	}
}

func TestReplyTargetPersistsAndRestoresAcrossRestart(t *testing.T) {
	store := connectors.NewMemoryStore()
	first := newTestProvider(t, newFakeFeishu(t))
	first.AttachState("conn-1", store.PollingState("conn-1"))
	first.rememberReplyTarget("conn-1", "oc_chat_1", "om_persist_1")
	first.rememberReplyTarget("conn-1", "oc_chat_2", "om_persist_2")

	raw, ok := store.PollingState("conn-1").Get(replyTargetsStateKey)
	if !ok || !strings.Contains(raw, "om_persist_1") || !strings.Contains(raw, "om_persist_2") {
		t.Fatalf("reply target snapshot missing: ok=%v raw=%q", ok, raw)
	}

	restarted := newTestProvider(t, newFakeFeishu(t))
	restarted.AttachState("conn-1", store.PollingState("conn-1"))
	if got := restarted.replyTarget("conn-1", "oc_chat_1"); got != "om_persist_1" {
		t.Fatalf("restored target = %q, want om_persist_1", got)
	}
	if got := restarted.replyTarget("conn-1", "oc_chat_2"); got != "om_persist_2" {
		t.Fatalf("restored target = %q, want om_persist_2", got)
	}
	// 其它连接器隔离：附着另一 connector 无快照 → 空。
	if got := restarted.replyTarget("conn-2", "oc_chat_1"); got != "" {
		t.Fatalf("target for other connector = %q, want empty", got)
	}
	// 未附着状态视图的 provider 不受影响（进程内记忆照常）。
	plain := newTestProvider(t, newFakeFeishu(t))
	plain.rememberReplyTarget("conn-1", "oc_chat_1", "om_plain")
	if got := plain.replyTarget("conn-1", "oc_chat_1"); got != "om_plain" {
		t.Fatalf("in-memory target = %q, want om_plain", got)
	}
}

func TestAttachStatePrefersInMemoryOverSnapshot(t *testing.T) {
	store := connectors.NewMemoryStore()
	// 先写入一份较旧的快照（模拟重启前落盘）。
	first := newTestProvider(t, newFakeFeishu(t))
	first.AttachState("conn-1", store.PollingState("conn-1"))
	first.rememberReplyTarget("conn-1", "oc_chat_1", "om_old")

	// 新 provider 先有更新的进程内记忆（重启后又收到一条入站），再附着状态
	// 视图——回填不得覆盖更新的内存值。
	restarted := newTestProvider(t, newFakeFeishu(t))
	restarted.rememberReplyTarget("conn-1", "oc_chat_1", "om_new")
	restarted.AttachState("conn-1", store.PollingState("conn-1"))
	if got := restarted.replyTarget("conn-1", "oc_chat_1"); got != "om_new" {
		t.Fatalf("target after attach = %q, want om_new (in-memory wins)", got)
	}
}
