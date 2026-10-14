package connectors

import "testing"

func testPeers() []RunnerPeer {
	return []RunnerPeer{
		{HostKind: HostKindRuntimeServer, NodeID: "runtime-server", Alive: true, Local: true},
		{HostKind: HostKindAICLI, NodeID: "aicli:node-remote", Alive: true},
		{HostKind: HostKindAICLI, NodeID: "aicli:node-stale", Alive: false},
	}
}

// TestDecideRouteR1Affinity：绑定指向远端活节点 → 亲和派发（带会话）。
func TestDecideRouteR1Affinity(t *testing.T) {
	binding := SessionRef{NodeID: "aicli:node-remote", SessionID: "sess-9"}
	d, _ := DecideRoute("", testPeers(), "runtime-server", binding, 0)
	if !d.Remote || !d.Affinity || d.Peer.NodeID != "aicli:node-remote" {
		t.Fatalf("R1 decision = %+v", d)
	}
}

// TestDecideRouteLocalAffinity：绑定指向本机 → 本机执行且保持亲和。
func TestDecideRouteLocalAffinity(t *testing.T) {
	binding := SessionRef{NodeID: "runtime-server", SessionID: "sess-1"}
	d, _ := DecideRoute("", testPeers(), "runtime-server", binding, 0)
	if d.Remote || !d.Affinity {
		t.Fatalf("local binding decision = %+v", d)
	}
}

// TestDecideRouteR2Fallback：绑定 NodeID 掉线 → 本机执行（切片 C 升级接管）。
func TestDecideRouteR2Fallback(t *testing.T) {
	binding := SessionRef{NodeID: "aicli:node-stale", SessionID: "sess-2"}
	d, _ := DecideRoute("", testPeers(), "runtime-server", binding, 0)
	if d.Remote || !d.Affinity || !d.Failover {
		t.Fatalf("R2a decision = %+v, want local failover with affinity", d)
	}
	// 纯单机（无 peers 视图）：Failover 不置位（历史行为静默）。
	d, _ = DecideRoute("", nil, "runtime-server", binding, 0)
	if d.Remote || d.Failover {
		t.Fatalf("standalone R2 must not set Failover, got %+v", d)
	}
}

// TestDecideRouteR3Preference：无绑定按偏好选举；默认选 runtime-server（本机
// → 本地执行），aicli 偏好选远端 aicli（远程派发，auto-create）。
func TestDecideRouteR3Preference(t *testing.T) {
	// 默认偏好 → 本机（runtime-server 在候选中且偏好命中）。
	d, _ := DecideRoute("", testPeers(), "runtime-server", SessionRef{}, 0)
	if d.Remote || !d.Unbound {
		t.Fatalf("R3 default = %+v", d)
	}
	// aicli 偏好 → 远端 aicli 节点。
	d, _ = DecideRoute("aicli", testPeers(), "runtime-server", SessionRef{}, 0)
	if !d.Remote || !d.Unbound || d.Affinity || d.Peer.NodeID != "aicli:node-remote" {
		t.Fatalf("R3 aicli = %+v", d)
	}
}

// TestDecideRouteR3RoundRobin：round-robin 在活 runner 间轮转（含本机）。
func TestDecideRouteR3RoundRobin(t *testing.T) {
	// 稳定排序（HostKind,NodeID）：aicli:node-remote < runtime-server。
	d1, next := DecideRoute("round-robin", testPeers(), "runtime-server", SessionRef{}, 0)
	// 稳定排序（HostKind,NodeID）：aicli:node-remote < runtime-server → 首轮选远端 aicli。
	if !d1.Remote || !d1.Unbound || d1.Peer.NodeID != "aicli:node-remote" {
		t.Fatalf("rr first = %+v, want remote aicli:node-remote", d1)
	}
	d2, next2 := DecideRoute("round-robin", testPeers(), "runtime-server", SessionRef{}, next)
	if d2.Remote {
		t.Fatalf("rr second should hit local runtime-server, got %+v", d2)
	}
	// 回绕再取 aicli。
	d3, _ := DecideRoute("round-robin", testPeers(), "runtime-server", SessionRef{}, next2)
	if !d3.Remote || d3.Peer.NodeID != "aicli:node-remote" {
		t.Fatalf("rr wrap = %+v", d3)
	}
}

// TestDecideRouteNoPeers：mesh 缺席（空 peers）→ 本机兜底，连接器不拒收。
func TestDecideRouteNoPeers(t *testing.T) {
	d, _ := DecideRoute("", nil, "runtime-server", SessionRef{NodeID: "gone", SessionID: "s"}, 0)
	if d.Remote {
		t.Fatalf("no peers must be local, got %+v", d)
	}
	d, _ = DecideRoute("aicli", nil, "runtime-server", SessionRef{}, 0)
	if d.Remote || !d.Unbound {
		t.Fatalf("no peers unbound = %+v", d)
	}
}

// TestDecideRouteLocalBindingNoFalseFailover 锁定 2026-10-15 真机（qq-bot1）
// 修复：绑定 NodeID 指向本机（实例唯一 HostID / 裸 kind）时，即使 peers
// 快照不含 self（或使用不同格式），也必须是本机亲和执行——不得触发假
// R2a（重复回写 NodeID + 向用户发送「原节点已下线」提示）。
func TestDecideRouteLocalBindingNoFalseFailover(t *testing.T) {
	// 快照只含其它活 runner（不含 self）——老代码会判「原节点掉线」。
	peers := []RunnerPeer{
		{HostKind: HostKindRuntimeServer, NodeID: "runtime-server", Alive: true},
	}

	// 实例唯一 HostID 自绑定（R2a 回写后的格式）。
	d, _ := DecideRoute("", peers, "aicli:node-self", SessionRef{NodeID: "aicli:node-self", SessionID: "sess-1"}, 0)
	if d.Remote || !d.Affinity || d.Failover {
		t.Fatalf("self HostID binding = %+v, want local affinity without failover", d)
	}

	// 裸 kind 自绑定（runtime-server 单实例 / 历史回写格式）。
	d, _ = DecideRoute("", peers, "runtime-server", SessionRef{NodeID: "runtime-server", SessionID: "sess-2"}, 0)
	if d.Remote || !d.Affinity || d.Failover {
		t.Fatalf("bare-kind local binding = %+v, want local affinity without failover", d)
	}

	// 其它实例的陈旧绑定不受短路影响：仍走 R2a 接管。
	d, _ = DecideRoute("", peers, "aicli:node-self", SessionRef{NodeID: "aicli:node-gone", SessionID: "sess-3"}, 0)
	if d.Remote || !d.Failover {
		t.Fatalf("foreign stale binding = %+v, want failover", d)
	}
}
