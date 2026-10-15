package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/ntfy"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// flipPresence 是可翻转的在场权威：用它确定性复现"observe 判定不在场并记
// seen → Dispatch 时在场恰好续期（内核门抑制）"的双门竞态窗口
// （docs/bots/12-notify-consolidation-plan.md §2/§3 D1）。
type flipPresence struct {
	on atomic.Bool
}

func (f *flipPresence) Active(nodeID, sessionID string) bool { return f.on.Load() }

func suppressedTestNodes() []*tunnel.Node {
	return []*tunnel.Node{{
		NodeID:   "node-x",
		Sessions: []wire.SessionSummary{{SessionID: "session-y", Title: "A", Attention: "error"}},
	}}
}

// TestPushNotifierSuppressedRollsBackSeen 锁定 G1 双门收敛语义：内核在场门
// 抑制事件时，pushNotifier 必须撤销 observe 已记的 seen——否则竞态窗口内的
// 通知被 seen 永久吞掉（不推、不补推）。回滚后下一次 observe 必须把该事件
// 当作新事件重投（与"跳过且不记 seen → 切走补推一次"逐字等价）。
func TestPushNotifierSuppressedRollsBackSeen(t *testing.T) {
	// ntfy 假服务：若竞态回滚缺失，第二次 observe（补推）不会发生，
	// 假服务器收到的投递次数将停在 0 —— 计数即判定依据。
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	presence := &flipPresence{}
	n := newPushNotifier(nil, "https://gw.example.com", presence)
	n.ntfy = ntfy.NewSender(srv.URL, "topic-a", "")

	tag := "node-x|session-y"
	nodes := suppressedTestNodes()

	// 竞态窗口复现：observe（不在场 → 记 seen）之后、dispatch 的内核门判定
	// 之前，把在场翻开。窗口本身在 goroutine 里，这里用"observe 前先翻开"
	// 不行（会被语义门拦住）——改为：observe 前关、observe 后立刻翻开，
	// 并轮询等 seen 落账后再翻，保证内核门必然看到在场=true。
	presence.on.Store(false)
	n.observe(nodes)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		n.mu.Lock()
		_, recorded := n.seen[tag]
		n.mu.Unlock()
		if recorded {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// seen 已记（语义门放行），此刻翻开在场 → dispatch 的内核门必然抑制。
	presence.on.Store(true)

	// 等内核门处理完（Suppressed 回滚或错序投递，二者必居其一）。
	deadline = time.Now().Add(3 * time.Second)
	var suppressedRolledBack bool
	for time.Now().Before(deadline) {
		n.mu.Lock()
		_, still := n.seen[tag]
		h := hits.Load()
		n.mu.Unlock()
		if !still {
			suppressedRolledBack = true
			break
		}
		if h > 0 {
			// 内核门没拦住（窗口没踩中）→ 本用例前提不成立，跳过补推段。
			t.Skipf("race window not hit (delivered=%d); rerun to retry", h)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !suppressedRolledBack {
		t.Fatal("Suppressed 回执未回滚 seen：竞态窗口内通知将被永久吞掉且不补推")
	}

	// 补推段：在场释放 → 下一次 observe 必须重投（hits 0→1），且重新记 seen。
	presence.on.Store(false)
	n.observe(nodes)
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hits.Load() >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("补推应恰好投递一次，实际 %d", got)
	}
	n.mu.Lock()
	attention := n.seen[tag]
	n.mu.Unlock()
	if attention != "error" {
		t.Fatalf("补推 observe 应重新记录 seen，got %q", attention)
	}
}

// TestPushNotifierSuppressedConditionalDelete：回滚是**条件**删除——seen 已被
// 并发更新为其他 attention 值时，Suppressed 回执不得覆盖新值（只删自己的）。
func TestPushNotifierSuppressedConditionalDelete(t *testing.T) {
	n := newPushNotifier(nil, "https://gw.example.com", nil)
	n.mu.Lock()
	n.seen["k"] = "approval"
	n.mu.Unlock()

	// item 声称 attention=error，但 seen 已是 approval → 不得删除。
	n.dispatch([]pendingNotification{{
		attention: "error",
		note:      push.Notification{Title: "t", Body: "b", Tag: "k"},
	}})

	// dispatch 是异步的；给回滚分支充分时间后断言未被覆盖。
	time.Sleep(200 * time.Millisecond)
	n.mu.Lock()
	got := n.seen["k"]
	n.mu.Unlock()
	if got != "approval" {
		t.Fatalf("条件删除不得覆盖并发更新的 seen，got %q", got)
	}
}

// —— G2 账本接线（docs/bots/12-notify-consolidation-plan.md §4 G2 / §3 D2）——

// TestLedgerKeyCarriesAttention 锁定 G2 键语义（账本键 = tag|attention，
// tag = node|session）。通过 ntfy 假服务的投递计数断言三段行为：
//
//  1. approval→error 换值：**两次都投**——账本的 attention 维度保证绝不吞
//     同类后续事件（这是 D2 拒绝裸 node|session 键的原因；与 seen 快照的
//     换值重推语义逐字一致）；
//  2. 同一 attention 重复 observe（事件重放）：只投一次——跨路径幂等生效；
//  3. 重放不复活旧值：error 已投后再次 approval→error 序列重放，approval
//     不产生投递（键互不干扰）。
func TestLedgerKeyCarriesAttention(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := newPushNotifier(nil, "https://gw.example.com", nil)
	n.ntfy = ntfy.NewSender(srv.URL, "topic-a", "")

	nodesWith := func(attention string) []*tunnel.Node {
		return []*tunnel.Node{{
			NodeID:   "node-x",
			Sessions: []wire.SessionSummary{{SessionID: "s-y", Title: "A", Attention: attention}},
		}}
	}
	waitHits := func(want int32, what string) int32 {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if got := hits.Load(); got >= want {
				return got
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("%s：期望投递 ≥%d，实际 %d", what, want, hits.Load())
		return 0
	}

	// 1) approval → 投递 #1。
	n.observe(nodesWith("approval"))
	waitHits(1, "approval 首投")

	// 2) error 换值 → 投递 #2（裸 node|session 键会在此被账本吞掉）。
	n.observe(nodesWith("error"))
	waitHits(2, "approval→error 换值重投")

	// 3) 同 attention 重放：seen 命中短路（不进 dispatch）；即便绕过 seen，
	//    账本键 node-x|s-y|error 已认领 → 不重复投递。计数必须停在 2。
	n.observe(nodesWith("error"))
	time.Sleep(300 * time.Millisecond)
	if got := hits.Load(); got != 2 {
		t.Fatalf("同 attention 重放不得重复投递：期望 2，实际 %d", got)
	}

	// 4) 换回 approval 再到 error 的完整序列：approval 投 #3（新事件），
	//    error 被 seen 短路（值未变）——但注意此时 seen[node|s]=approval，
	//    error 是换值 → 投 #4。最终恰 4 次，绝无多吞。
	n.observe(nodesWith("approval"))
	waitHits(3, "approval 再次换值")
	n.observe(nodesWith("error"))
	waitHits(4, "error 再次换值")
	if got := hits.Load(); got != 4 {
		t.Fatalf("完整序列应恰投 4 次，实际 %d", got)
	}
}
