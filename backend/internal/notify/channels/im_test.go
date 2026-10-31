package channels

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

// outboundRecorder 记录发往全局 target 的出站（线程安全：投递是并发的）。
type outboundRecorder struct {
	mu   sync.Mutex
	seen []string // "connector|chat|text"
	fail bool
}

func (r *outboundRecorder) send(_ context.Context, connectorID, chat string, msgs []connectors.OutboundMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	text := ""
	if len(msgs) > 0 {
		text = msgs[0].Text
	}
	r.seen = append(r.seen, connectorID+"|"+chat+"|"+text)
	if r.fail {
		return errors.New("send failed")
	}
	return nil
}

func (r *outboundRecorder) calls() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.seen...)
}

// TestIMNoTargetsKeepsLegacySilence 锁定"未配置 bot 行为零变化"：没有 target
// 时不返回 ErrNoRecipient，Dispatcher 不会因 IM 通道而误判无收件人
// （docs/gateway/18 §5.1/§5.2 的关键约束）。
func TestIMNoTargetsKeepsLegacySilence(t *testing.T) {
	im := &IM{SendOutbound: (&outboundRecorder{}).send}
	if err := im.Deliver(context.Background(), notify.Event{Kind: "completed", Target: "s1"}); err != nil {
		t.Fatalf("未配置 target 时应静默跳过而非返回错误：%v", err)
	}
}

// TestIMNodeTargetDelegatesPerNodeAndFiltersKinds 锁定节点委托语义：
//   - 同一节点多个 target 只发一次；
//   - 默认 kinds=error/waiting（节点本地已发 approval/question/completion）；
//   - 事件节点不匹配的 target 不命中；
//   - 无 target 受理时返回 ErrNoRecipient（交给回退链）。
func TestIMNodeTargetDelegatesPerNodeAndFiltersKinds(t *testing.T) {
	var got []string
	im := &IM{
		Targets: []IMTarget{
			{ID: "n1-a", Source: "node", NodeID: "n1", Scope: "all"},
			{ID: "n1-b", Source: "node", NodeID: "n1", Scope: "all"},
			{ID: "n2", Source: "node", NodeID: "n2", Scope: "all"},
		},
		SendNodeAttention: func(_ context.Context, nodeID string, notice connectors.AttentionNotice) error {
			got = append(got, nodeID+"|"+notice.Kind+"|"+notice.Session.SessionID+"|"+notice.Text)
			return nil
		},
	}

	err := im.Deliver(context.Background(), notify.Event{
		Kind: "error", Target: "s1", Node: "n1",
		Notification: notify.Notification{Title: "err", Body: "boom"},
	})
	if err != nil {
		t.Fatalf("节点委托投递不应报错：%v", err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "n1|error|s1|") {
		t.Fatalf("同节点应只委托一次：%v", got)
	}

	got = nil
	err = im.Deliver(context.Background(), notify.Event{
		Kind: "approval", Target: "s1", Node: "n1",
		Notification: notify.Notification{Title: "approval"},
	})
	if !errors.Is(err, notify.ErrNoRecipient) {
		t.Fatalf("approval 不在节点默认 kinds 内，应交给回退链；got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("approval 不应委托：%v", got)
	}

	im.Targets = []IMTarget{{ID: "n1", Source: "node", NodeID: "n1", Kinds: []string{"approval"}}}
	got = nil
	if err := im.Deliver(context.Background(), notify.Event{
		Kind: "approval", Target: "s1", Node: "n1",
		Notification: notify.Notification{Title: "approval"},
	}); err != nil {
		t.Fatalf("显式 kinds 应允许 approval：%v", err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "n1|approval|s1|") {
		t.Fatalf("显式 kinds 委托异常：%v", got)
	}

	got = nil
	err = im.Deliver(context.Background(), notify.Event{
		Kind: "error", Target: "s1", Node: "n2",
		Notification: notify.Notification{Title: "err"},
	})
	if !errors.Is(err, notify.ErrNoRecipient) {
		t.Fatalf("跨节点事件不应命中，应交给回退链；got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("跨节点事件不应委托：%v", got)
	}
}

// TestIMGlobalTargetMatchesScopeAll 验证 scope=all 的全局 target 收到事件，
// 且正文优先用投影提示（含可操作指令）。
func TestIMGlobalTargetMatchesScopeAll(t *testing.T) {
	rec := &outboundRecorder{}
	im := &IM{
		SendOutbound: rec.send,
		Targets:      []IMTarget{{ID: "ops", ConnectorID: "c1", Chat: "chat-1", Scope: "all"}},
	}
	err := im.Deliver(context.Background(), notify.Event{Kind: "approval", Target: "s1", Node: "n1"})
	if err != nil {
		t.Fatalf("全局 target 投递不应报错：%v", err)
	}
	got := rec.calls()
	if len(got) != 1 {
		t.Fatalf("应投递一次，实际 %d：%v", len(got), got)
	}
	if !strings.HasPrefix(got[0], "c1|chat-1|") || !strings.Contains(got[0], "待审批") {
		t.Fatalf("应发到 c1/chat-1 并带审批提示：%s", got[0])
	}
}

// TestIMScopeNodesAndSessions 验证 scope=nodes/sessions 只命中对应事件。
func TestIMScopeNodesAndSessions(t *testing.T) {
	rec := &outboundRecorder{}
	im := &IM{
		SendOutbound: rec.send,
		Targets: []IMTarget{
			{ID: "by-node", ConnectorID: "c1", Chat: "n-chat", Scope: "nodes", Nodes: []string{"n1"}},
			{ID: "by-sess", ConnectorID: "c1", Chat: "s-chat", Scope: "sessions", Sessions: []string{"s1"}},
		},
	}
	// 命中节点目标（会话不匹配）。
	if err := im.Deliver(context.Background(), notify.Event{Kind: "completed", Target: "other", Node: "n1"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.calls(); len(got) != 1 || !strings.Contains(got[0], "n-chat") {
		t.Fatalf("应只命中 n-chat：%v", got)
	}
	// 命中会话目标。
	rec2 := &outboundRecorder{}
	im2 := &IM{SendOutbound: rec2.send, Targets: im.Targets}
	if err := im2.Deliver(context.Background(), notify.Event{Kind: "completed", Target: "s1", Node: "other"}); err != nil {
		t.Fatal(err)
	}
	if got := rec2.calls(); len(got) != 1 || !strings.Contains(got[0], "s-chat") {
		t.Fatalf("应只命中 s-chat：%v", got)
	}
}

// TestIMDedupesSameConnectorChat：同一 (connector, chat) 的多个 target 只发一次。
func TestIMDedupesSameConnectorChat(t *testing.T) {
	rec := &outboundRecorder{}
	im := &IM{
		SendOutbound: rec.send,
		Targets: []IMTarget{
			{ID: "a", ConnectorID: "c1", Chat: "same", Scope: "all"},
			{ID: "b", ConnectorID: "c1", Chat: "same", Scope: "all"},
		},
	}
	if err := im.Deliver(context.Background(), notify.Event{Kind: "completed", Target: "s1"}); err != nil {
		t.Fatal(err)
	}
	if got := rec.calls(); len(got) != 1 {
		t.Fatalf("同 connector+chat 应去重为一次：%v", got)
	}
}

// TestIMUnknownScopeDoesNotMatch：未知 scope 不命中（配置漂移不得导致误发）。
func TestIMUnknownScopeDoesNotMatch(t *testing.T) {
	if (IMTarget{Scope: "bogus"}).Matches("n1", "s1") {
		t.Fatal("未知 scope 不应命中")
	}
	if (IMTarget{Scope: ""}).Matches("n1", "s1") != true {
		t.Fatal("空 scope 应按 all 命中")
	}
}

// TestIMReturnsNoRecipientWhenConfiguredButNothingMatched：配置了 bot 但无
// target 命中 → ErrNoRecipient，触发 Dispatcher 回退到 push。
func TestIMReturnsNoRecipientWhenConfiguredButNothingMatched(t *testing.T) {
	rec := &outboundRecorder{}
	im := &IM{
		SendOutbound: rec.send,
		Targets:      []IMTarget{{ID: "by-node", ConnectorID: "c1", Chat: "x", Scope: "nodes", Nodes: []string{"n1"}}},
	}
	err := im.Deliver(context.Background(), notify.Event{Kind: "completed", Target: "s1", Node: "n9"})
	if !errors.Is(err, notify.ErrNoRecipient) {
		t.Fatalf("无 target 命中应返回 ErrNoRecipient，实际 %v", err)
	}
	if len(rec.calls()) != 0 {
		t.Fatal("未命中不应发送")
	}
}

// TestIMTargetsFnTakesPrecedence：动态来源优先，配置热加载即时生效。
func TestIMTargetsFnTakesPrecedence(t *testing.T) {
	rec := &outboundRecorder{}
	im := &IM{
		SendOutbound: rec.send,
		Targets:      []IMTarget{{ID: "stale", ConnectorID: "c1", Chat: "stale"}},
		TargetsFn:    func() []IMTarget { return []IMTarget{{ID: "live", ConnectorID: "c1", Chat: "live", Scope: "all"}} },
	}
	if err := im.Deliver(context.Background(), notify.Event{Kind: "completed", Target: "s1"}); err != nil {
		t.Fatal(err)
	}
	got := rec.calls()
	if len(got) != 1 || !strings.Contains(got[0], "live") {
		t.Fatalf("应使用 TargetsFn 的实时配置：%v", got)
	}
}

// TestIMTruncateKeepsURL 锁定超长策略：截断正文但保留可点击 URL（§5.7）。
func TestIMTruncateKeepsURL(t *testing.T) {
	long := strings.Repeat("长", 5000)
	url := "https://example.test/x"
	out := imTruncateKeepURL(long, url, 200)
	if !strings.HasSuffix(out, url) {
		t.Fatalf("截断后必须保留 URL：%s", out)
	}
	if len([]rune(out)) > 200 {
		t.Fatalf("截断后长度不得超限：%d", len([]rune(out)))
	}
	// 未超限时原样返回并保留 URL。
	short := imTruncateKeepURL("hi", url, 200)
	if short != "hi\n"+url {
		t.Fatalf("未超限应原样拼接：%q", short)
	}
	// 上限连 URL 都放不下：只发 URL，保证入口可用。
	if got := imTruncateKeepURL(long, url, 5); got != url {
		t.Fatalf("上限过小时应只发 URL：%q", got)
	}
}

// TestIMPartialFailureReportsError：部分 target 失败时既有成功也不吞掉错误。
func TestIMPartialFailureReportsError(t *testing.T) {
	var (
		mu     sync.Mutex
		failed bool
		ok     int
	)
	send := func(_ context.Context, connectorID, chat string, _ []connectors.OutboundMessage) error {
		mu.Lock()
		defer mu.Unlock()
		if chat == "bad" {
			failed = true
			return errors.New("boom")
		}
		ok++
		return nil
	}
	im := &IM{
		SendOutbound: send,
		Targets: []IMTarget{
			{ID: "good", ConnectorID: "c1", Chat: "good", Scope: "all"},
			{ID: "bad", ConnectorID: "c1", Chat: "bad", Scope: "all"},
		},
	}
	err := im.Deliver(context.Background(), notify.Event{Kind: "completed", Target: "s1"})
	if err == nil {
		t.Fatal("部分失败应报告错误")
	}
	if failed != true || ok != 1 {
		t.Fatalf("应一成一败：failed=%v ok=%d", failed, ok)
	}
	// 部分成功不属于"无收件人"：不得触发回退，避免重复投递。
	if errors.Is(err, notify.ErrNoRecipient) {
		t.Fatal("部分成功不应返回 ErrNoRecipient")
	}
}

// fakeDeliveryLedger 是进程内交付账本替身（生产为 gateway 持久化实现）。
type fakeDeliveryLedger struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func newFakeDeliveryLedger() *fakeDeliveryLedger {
	return &fakeDeliveryLedger{seen: make(map[string]struct{})}
}

func (f *fakeDeliveryLedger) Claim(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.seen[key]; ok {
		return false
	}
	f.seen[key] = struct{}{}
	return true
}

func (f *fakeDeliveryLedger) Release(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.seen, key)
}

// TestIMDeliveryLedgerDedupsPerChannel 锁定 P1：同一通道实例+chat+事件只发一次，
// 不同通道（不同 connector）各自投递；新事件重新投递。
func TestIMDeliveryLedgerDedupsPerChannel(t *testing.T) {
	rec := &outboundRecorder{}
	im := &IM{
		SendOutbound:   rec.send,
		DeliveryLedger: newFakeDeliveryLedger(),
		Targets: []IMTarget{
			{ID: "c1-a", ConnectorID: "c1", Chat: "chat-1", Scope: "all"},
			{ID: "c1-b", ConnectorID: "c1", Chat: "chat-1", Scope: "all"},
			{ID: "c2", ConnectorID: "c2", Chat: "chat-1", Scope: "all"},
		},
	}
	ev := notify.Event{
		Kind: "error", Target: "s1", Node: "n1", EventID: "e1",
		Notification: notify.Notification{Title: "err"},
	}
	if err := im.Deliver(context.Background(), ev); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got := rec.calls(); len(got) != 2 {
		t.Fatalf("两个不同通道各发一次，实际 %d：%v", len(got), got)
	}
	if err := im.Deliver(context.Background(), ev); err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	if got := rec.calls(); len(got) != 2 {
		t.Fatalf("同一事件必须被账本去重，实际 %d：%v", len(got), got)
	}
	ev.EventID = "e2"
	if err := im.Deliver(context.Background(), ev); err != nil {
		t.Fatalf("new event: %v", err)
	}
	if got := rec.calls(); len(got) != 4 {
		t.Fatalf("新事件应重新投递，实际 %d：%v", len(got), got)
	}
}

// TestIMNodeDeliveryLedgerDedupsPerNode 锁定节点委托的 P1：同一节点同一事件
// 只委托一次（同节点多 target 合并）。
func TestIMNodeDeliveryLedgerDedupsPerNode(t *testing.T) {
	var calls []string
	im := &IM{
		DeliveryLedger: newFakeDeliveryLedger(),
		SendNodeAttention: func(_ context.Context, nodeID string, notice connectors.AttentionNotice) error {
			calls = append(calls, nodeID+"|"+notice.Kind+"|"+notice.Session.SessionID)
			return nil
		},
		Targets: []IMTarget{
			{ID: "n1-a", Source: "node", NodeID: "n1", Scope: "all"},
			{ID: "n1-b", Source: "node", NodeID: "n1", Scope: "all"},
		},
	}
	ev := notify.Event{
		Kind: "error", Target: "s1", Node: "n1", EventID: "e1",
		Notification: notify.Notification{Title: "err"},
	}
	if err := im.Deliver(context.Background(), ev); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("同节点应只委托一次：%v", calls)
	}
	if err := im.Deliver(context.Background(), ev); err != nil {
		t.Fatalf("redeliver: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("同一事件必须被账本去重：%v", calls)
	}
	ev.EventID = "e2"
	if err := im.Deliver(context.Background(), ev); err != nil {
		t.Fatalf("new event: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("新事件应重新委托：%v", calls)
	}
}

// TestIMDeliveryLedgerReleasesOnFailure：发送失败必须释放认领，允许后续重试。
func TestIMDeliveryLedgerReleasesOnFailure(t *testing.T) {
	rec := &outboundRecorder{fail: true}
	im := &IM{
		SendOutbound:   rec.send,
		DeliveryLedger: newFakeDeliveryLedger(),
		Targets:        []IMTarget{{ID: "c1", ConnectorID: "c1", Chat: "chat-1", Scope: "all"}},
	}
	ev := notify.Event{
		Kind: "error", Target: "s1", Node: "n1", EventID: "e1",
		Notification: notify.Notification{Title: "err"},
	}
	if err := im.Deliver(context.Background(), ev); err == nil {
		t.Fatal("发送失败应返回错误")
	}
	rec.mu.Lock()
	rec.fail = false
	rec.mu.Unlock()
	if err := im.Deliver(context.Background(), ev); err != nil {
		t.Fatalf("失败后应允许重试：%v", err)
	}
	if got := rec.calls(); len(got) != 2 {
		t.Fatalf("应记录失败+成功两次：%v", got)
	}
}
