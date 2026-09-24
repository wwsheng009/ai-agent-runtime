package mesh

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// S7 扇入单测（实施方案 §7「验证与证据」：go test ./internal/mesh/... -run 'Fanin|Seq|Loop'）。
//
// 三条硬契约：
//  1. 防环：`mesh.peer.event` 绝不二次转发，来源必须就是该 peer 自己
//     （§6.3 / §12.1「扇入防环与 seq」）；
//  2. seq 同源单调：SSE 帧序号与 journal 行共用同一计数器（§6.3）；
//  3. 背压：每 peer 令牌桶超限丢帧并计数（§6.4），客户端缓冲溢出以
//     `mesh.lagged` 提示消费方做全量兜底。

func newTestFanin(t *testing.T) *Fanin {
	t.Helper()
	return newTestFaninWithConfig(t, FaninConfig{})
}

func newTestFaninWithConfig(t *testing.T, cfg FaninConfig) *Fanin {
	t.Helper()
	paths := testMeshPaths(t)
	if strings.TrimSpace(cfg.NodeID) == "" {
		cfg.NodeID = "node-self"
	}
	if cfg.Now == nil {
		cfg.Now = newFakeClock().Now
	}
	if cfg.Journal == nil {
		cfg.Journal = OpenJournal(paths, cfg.NodeID, cfg.Now)
	}
	fanin := NewFanin(cfg)
	t.Cleanup(fanin.Close)
	return fanin
}

func decodeFaninFrame(t *testing.T, payload []byte) Frame {
	t.Helper()
	var frame Frame
	if err := json.Unmarshal(payload, &frame); err != nil {
		t.Fatalf("帧不是合法 JSON: %v (%s)", err, string(payload))
	}
	return frame
}

func nextFaninMessage(t *testing.T, client *FaninClient, timeout time.Duration) FaninFramePayload {
	t.Helper()
	select {
	case message, ok := <-client.Payloads():
		if !ok {
			t.Fatal("客户端队列已关闭")
		}
		return message
	case <-time.After(timeout):
		t.Fatal("等待扇入帧超时")
		return FaninFramePayload{}
	}
}

// seq 与 journal 同源：帧占用的序号与 journal.NextSeq 是同一计数器。
func TestFaninSeqSharesJournalCounter(t *testing.T) {
	paths := testMeshPaths(t)
	journal := OpenJournal(paths, "node-self", newFakeClock().Now)
	fanin := NewFanin(FaninConfig{NodeID: "node-self", Journal: journal})
	defer fanin.Close()

	if !fanin.Enabled() {
		t.Fatal("有 journal 时扇入必须可用")
	}
	for want := uint64(1); want <= 3; want++ {
		if seq := fanin.PublishLocal(FramePeerUpdated, map[string]any{"index": want}); seq != want {
			t.Fatalf("PublishLocal seq = %d, want %d", seq, want)
		}
	}
	// 3 帧之后，journal 的下一次分配必须是 4（同源同计数器）。
	if next := journal.NextSeq(); next != 4 {
		t.Fatalf("journal.NextSeq = %d, want 4（帧与 journal 必须共用计数器）", next)
	}
	// 连接级帧不占序号（mesh.ready / mesh.lagged 由消费方按内容处理）。
	ready, ok := fanin.ConnectionFrame(FrameReady, map[string]any{"node_id": "node-self"})
	if !ok || ready.Seq != 0 {
		t.Fatalf("连接级帧 = %+v ok=%v, want seq=0", ready, ok)
	}
	if next := journal.NextSeq(); next != 5 {
		t.Fatalf("连接级帧不得消耗序号: journal.NextSeq = %d, want 5", next)
	}
}

// 防环：peer 转手的事件与 `mesh.peer.event` 本身绝不二次转发（C9）。
func TestFaninRelayRejectsEchoAndThirdParty(t *testing.T) {
	fanin := newTestFanin(t)
	client, err := fanin.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer client.Close()

	// 1) mesh.peer.event 不二次转发。
	if fanin.PublishPeerFrame("node-peer", Frame{Type: FramePeerEvent, SourceNodeID: "node-peer", Seq: 9}) {
		t.Fatal("mesh.peer.event 必须被丢弃（防环硬约束）")
	}
	// 2) 来源不等于该 peer（第三方转手）→ 丢弃。
	if fanin.PublishPeerFrame("node-peer", Frame{Type: "turn_start", SourceNodeID: "node-third"}) {
		t.Fatal("source != peer 的帧必须被丢弃")
	}
	// 3) peer 自己的 mesh.lagged 是它的连接背压，与本节点无关 → 丢弃。
	if fanin.PublishPeerFrame("node-peer", Frame{Type: FrameLagged, SourceNodeID: "node-peer"}) {
		t.Fatal("mesh.lagged 必须被丢弃")
	}
	// 4) 白名单外的 peer 本地事件（逐字 delta / 状态栏）→ 丢弃。
	if fanin.PublishPeerFrame("node-peer", Frame{Type: "assistant_delta", SourceNodeID: "node-peer"}) {
		t.Fatal("非白名单事件必须被丢弃")
	}
	// 5) 白名单事件包一层 mesh.peer.event，内层保留原始帧。
	if !fanin.PublishPeerFrame("node-peer", Frame{
		Type:         "turn_start",
		SourceNodeID: "node-peer",
		Seq:          7,
		TS:           time.Unix(1_700_000_000, 0).UTC(),
		Data:         map[string]any{"turn_id": "turn-1"},
	}) {
		t.Fatal("白名单事件必须并入")
	}
	// 6) 档案类 mesh.* 帧原样并入（外层 seq 换成本节点计数器）。
	if !fanin.PublishPeerFrame("node-peer", Frame{
		Type:         FramePeerUpdated,
		SourceNodeID: "node-peer",
		Data:         map[string]any{"state": "busy"},
	}) {
		t.Fatal("mesh.peer.updated 必须并入")
	}

	wrapped := decodeFaninFrame(t, nextFaninMessage(t, client, time.Second).Payload)
	if wrapped.Type != FramePeerEvent {
		t.Fatalf("帧类型 = %q, want %q", wrapped.Type, FramePeerEvent)
	}
	// source_node_id 记录事件来源节点（peer），seq 是本节点计数器（§6.3）。
	if wrapped.SourceNodeID != "node-peer" || wrapped.Seq == 0 {
		t.Fatalf("外层帧必须记录来源 peer 与本节点 seq: %+v", wrapped)
	}
	if peer, _ := wrapped.Data["peer_node_id"].(string); peer != "node-peer" {
		t.Fatalf("peer_node_id = %q, want node-peer", peer)
	}
	inner, ok := wrapped.Data["frame"].(map[string]any)
	if !ok {
		t.Fatalf("mesh.peer.event 必须保留内层原始帧: %+v", wrapped.Data)
	}
	if innerType, _ := inner["type"].(string); innerType != "turn_start" {
		t.Fatalf("内层 type = %q, want turn_start", innerType)
	}
	if innerSource, _ := inner["source_node_id"].(string); innerSource != "node-peer" {
		t.Fatalf("内层 source_node_id = %q, want node-peer", innerSource)
	}

	updated := decodeFaninFrame(t, nextFaninMessage(t, client, time.Second).Payload)
	if updated.Type != FramePeerUpdated || updated.SourceNodeID != "node-peer" {
		t.Fatalf("mesh.peer.updated 帧 = %+v", updated)
	}
	if state, _ := updated.Data["state"].(string); state != "busy" {
		t.Fatalf("载荷丢失: %+v", updated.Data)
	}
}

// 令牌桶：每 peer 超限丢帧并计数，本节点自产帧不受影响（§6.4）。
func TestFaninPeerTokenBucketDropsAndCounts(t *testing.T) {
	fanin := newTestFaninWithConfig(t, FaninConfig{PeerRatePerSec: 1, PeerBurst: 2})
	for i := 0; i < 10; i++ {
		fanin.PublishPeerFrame("node-peer", Frame{
			Type:         "turn_start",
			SourceNodeID: "node-peer",
			Seq:          uint64(i + 1),
			Data:         map[string]any{"index": i},
		})
	}
	dropped := fanin.Dropped("node-peer")
	if dropped == 0 {
		t.Fatal("超限必须丢帧并计数")
	}
	// burst=2：只有前两帧通过。
	if dropped != 8 {
		t.Fatalf("Dropped = %d, want 8（burst=2 / 10 帧）", dropped)
	}
	if total := fanin.DroppedTotal(); total != dropped {
		t.Fatalf("DroppedTotal = %d, want %d（只有该 peer 超限）", total, dropped)
	}
	if counts := fanin.DroppedCounts(); counts["node-peer"] != dropped {
		t.Fatalf("DroppedCounts = %v, want node-peer=%d", counts, dropped)
	}
	// 本节点自产帧不经过 peer 令牌桶。
	if seq := fanin.PublishLocal(FramePeerUpdated, nil); seq == 0 {
		t.Fatal("本节点帧必须照常发布")
	}
	if dropped != 0 && fanin.Dropped("node-self") != 0 {
		t.Fatalf("本节点不应被计为丢弃源: %v", fanin.DroppedCounts())
	}
}

// 客户端上限 32（可配）：超限拒绝新连接，且不影响既有连接（§6.4）。
func TestFaninClientLimitKeepsExistingConnections(t *testing.T) {
	fanin := newTestFaninWithConfig(t, FaninConfig{MaxClients: 1, QueueCapacity: 2})
	first, err := fanin.Subscribe()
	if err != nil {
		t.Fatalf("首个订阅必须成功: %v", err)
	}
	defer first.Close()
	if fanin.Clients() != 1 {
		t.Fatalf("Clients = %d, want 1", fanin.Clients())
	}
	if _, err := fanin.Subscribe(); !errors.Is(err, ErrFaninClientLimit) {
		t.Fatalf("超限必须返回 ErrFaninClientLimit, got %v", err)
	}

	// 既有连接照常收帧。
	fanin.PublishLocal(FramePeerUpdated, map[string]any{"state": "busy"})
	if message := nextFaninMessage(t, first, time.Second); message.Type != FramePeerUpdated {
		t.Fatalf("既有连接收帧异常: %+v", message)
	}

	// 缓冲溢出（容量 2）→ 丢帧 + lagged 计数，且 TakeLagged 清零。
	for i := 0; i < 5; i++ {
		fanin.PublishLocal(FramePeerUpdated, map[string]any{"index": i})
	}
	lagged := first.Lagged()
	skipped := first.TakeLagged()
	if skipped == 0 || lagged != skipped {
		t.Fatal("缓冲溢出必须计入 lagged")
	}
	if again := first.TakeLagged(); again != 0 {
		t.Fatalf("TakeLagged 必须清零, got %d", again)
	}
	if first.Lagged() != 0 {
		t.Fatalf("TakeLagged 之后 Lagged = %d, want 0", first.Lagged())
	}

	// 关闭后释放名额。
	first.Close()
	second, err := fanin.Subscribe()
	if err != nil {
		t.Fatalf("关闭后必须能再订阅: %v", err)
	}
	second.Close()
}

// 无 journal 时 fail-closed：不产生帧、不订阅（§4.7 降级）。
func TestFaninDisabledWithoutJournal(t *testing.T) {
	fanin := NewFanin(FaninConfig{NodeID: "node-self"})
	if fanin.Enabled() {
		t.Fatal("无 journal 必须 fail-closed")
	}
	if _, err := fanin.Subscribe(); !errors.Is(err, ErrFaninDisabled) {
		t.Fatalf("Subscribe = %v, want ErrFaninDisabled", err)
	}
	if seq := fanin.PublishLocal(FramePeerUpdated, nil); seq != 0 {
		t.Fatalf("禁用时不得分配序号, got %d", seq)
	}
	if frame, ok := fanin.ConnectionFrame(FrameReady, nil); ok || frame.Seq != 0 {
		t.Fatalf("禁用时不得产生连接级帧: %+v ok=%v", frame, ok)
	}
	// 关闭后的 hub 同样拒绝发布（幂等，不 panic）。
	fanin.Close()
	fanin.Close()
	if seq := fanin.PublishLocal(FramePeerUpdated, nil); seq != 0 {
		t.Fatalf("关闭后不得发布, got %d", seq)
	}
}

// 渲染：`id:` 行只在有 seq 时出现，`data:` 行是帧 JSON 本身（§6.3 信封）。
func TestRenderFrameCarriesIDAndEnvelope(t *testing.T) {
	frame := Frame{
		SchemaVersion: FrameSchemaVersion,
		Seq:           17,
		TS:            time.Unix(1_700_000_000, 0).UTC(),
		SourceNodeID:  "node-self",
		Type:          FramePeerUpdated,
		Data:          map[string]any{"state": "busy"},
	}
	rendered, err := RenderFrame(frame)
	if err != nil {
		t.Fatalf("RenderFrame: %v", err)
	}
	text := string(rendered)
	for _, want := range []string{
		"id: 17\n",
		"event: mesh.peer.updated\n",
		`"seq":17`,
		`"source_node_id":"node-self"`,
		`"schema_version":1`,
		"\n\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("渲染结果缺少 %q:\n%s", want, text)
		}
	}
	payload, err := MarshalFrame(frame)
	if err != nil {
		t.Fatalf("MarshalFrame: %v", err)
	}
	if !strings.Contains(text, "data: "+string(payload)+"\n") {
		t.Fatalf("data 行必须与 MarshalFrame 一致:\n%s", text)
	}

	// 连接级帧（无 seq）不写 id 行。
	ready, err := RenderFrame(Frame{Type: FrameReady, SourceNodeID: "node-self"})
	if err != nil {
		t.Fatalf("RenderFrame(ready): %v", err)
	}
	if strings.Contains(string(ready), "id: ") {
		t.Fatalf("无 seq 的帧不得写 id 行:\n%s", string(ready))
	}
}

// 白名单判定：turn.* / session.* 边界转发，delta 与状态栏不转发（§6.3）。
func TestIsRelayWhitelistType(t *testing.T) {
	allow := []string{"turn_start", "turn_end", "session_start", "session_end", "session_switched"}
	deny := []string{"", "assistant_delta", "turn_delta", "dynamic_status", "screen_refresh", "mesh.peer.updated"}
	for _, name := range allow {
		if !IsRelayWhitelistType(name) {
			t.Fatalf("%q 应在白名单内", name)
		}
	}
	for _, name := range deny {
		if IsRelayWhitelistType(name) {
			t.Fatalf("%q 不应在白名单内", name)
		}
	}
	if !IsMeshFrameType("mesh.peer.updated") || IsMeshFrameType("turn_start") {
		t.Fatal("IsMeshFrameType 判定错误")
	}
}
