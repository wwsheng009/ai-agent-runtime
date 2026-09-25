package mesh

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// S15：接管（§4.4）——租约显式易主，旧节点继续运行并标 orphaned
// ---------------------------------------------------------------------------

// takeoverTestHost starts a real host on the fake clock. HeartbeatInterval is
// effectively infinite, so the heartbeat only runs when a test drives it.
func takeoverTestHost(t *testing.T, clock *fakeClock, warnings *[]string) *Host {
	t.Helper()
	cfg := HostConfig{Kind: "chat", Origin: "resume", Now: clock.Now, HeartbeatInterval: time.Hour}
	if warnings != nil {
		cfg.Warn = func(format string, args ...any) {
			*warnings = append(*warnings, fmt.Sprintf(format, args...))
		}
	}
	host := NewHost(cfg)
	if err := host.Start(); err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	t.Cleanup(host.Close)
	return host
}

func takeoverNodeView(t *testing.T, view MeshView, nodeID string) NodeView {
	t.Helper()
	for i := range view.Nodes {
		if view.Nodes[i].NodeID == nodeID {
			return view.Nodes[i]
		}
	}
	t.Fatalf("视图里找不到节点 %s: %+v", nodeID, view.Nodes)
	return NodeView{}
}

func takeoverWarningMentions(warnings []string, needle string) bool {
	for _, line := range warnings {
		if strings.Contains(line, needle) {
			return true
		}
	}
	return false
}

// TestHostTakeoverSessionReclaimsLeaseAndOrphansOwner 锁定 §4.4 的完整语义：
// 显式接管拿到租约；旧节点下次心跳发现自己不再是持有者 → 档案标 orphaned、
// journal 记 lease.degraded、给操作者一条指名接管者的 warning，但进程继续运行
// （网格绝不杀节点）；视图把该会话渲染成 state=orphaned。
func TestHostTakeoverSessionReclaimsLeaseAndOrphansOwner(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	sessionID := "session_takeover"

	var ownerWarnings []string
	owner := takeoverTestHost(t, clock, &ownerWarnings)
	owner.SetSession(&SessionInfo{ID: sessionID, Title: "mine"})
	if lease, ok := ReadLease(paths.LeasePath(LeasePurposeSession, sessionID)); !ok || lease.OwnerNodeID != owner.NodeID() {
		t.Fatalf("前置失败：owner 未持有租约: %+v", lease)
	}

	thief := takeoverTestHost(t, clock, nil)
	status := thief.TakeoverSession(sessionID)
	if !status.OK {
		t.Fatalf("接管被拒: %+v", status)
	}
	if !status.Reclaimed || status.PreviousOwnerNodeID != owner.NodeID() {
		t.Fatalf("接管结果 = %+v, want reclaimed by %s", status, owner.NodeID())
	}
	if lease, ok := ReadLease(paths.LeasePath(LeasePurposeSession, sessionID)); !ok || lease.OwnerNodeID != thief.NodeID() {
		t.Fatalf("接管后租约必须归新节点: %+v", lease)
	}
	if st := thief.SessionLeaseStatus(); !st.Held || st.Lost || st.Key != sessionID {
		t.Fatalf("接管方租约状态 = %+v", st)
	}
	if kinds := journalKinds(t, paths, thief.NodeID()); countKind(kinds, JournalLeaseReclaimed) != 1 {
		t.Fatalf("接管方 journal = %v, want one lease.reclaimed", kinds)
	}

	// 旧节点：下一次心跳发现自己不再是持有者 → 标 orphaned + 提示，但不退出。
	clock.Advance(30 * time.Second)
	owner.heartbeatOnce()
	if st := owner.SessionLeaseStatus(); st.Held || !st.Lost {
		t.Fatalf("旧节点租约状态 = %+v, want lost", st)
	}
	record := readRecord(t, paths, owner.NodeID())
	if record.Session == nil || !record.Session.Orphaned || record.Session.OrphanedBy != thief.NodeID() {
		t.Fatalf("旧节点档案必须标 orphaned/by=%s: %+v", thief.NodeID(), record.Session)
	}
	if kinds := journalKinds(t, paths, owner.NodeID()); countKind(kinds, JournalLeaseDegraded) != 1 {
		t.Fatalf("旧节点 journal = %v, want one lease.degraded", kinds)
	}
	if !takeoverWarningMentions(ownerWarnings, sessionID) || !takeoverWarningMentions(ownerWarnings, thief.NodeID()) {
		t.Fatalf("旧节点必须给操作者一条指名接管者的 warning: %v", ownerWarnings)
	}

	// 视图：state=orphaned + orphaned_by（前端横幅的数据源）。
	view := BuildView(paths, ViewOptions{Now: clock.Now(), SelfNodeID: thief.NodeID()})
	node := takeoverNodeView(t, view, owner.NodeID())
	if node.Session == nil || node.Session.State != SessionStateOrphaned || node.Session.OrphanedBy != thief.NodeID() {
		t.Fatalf("视图会话状态 = %+v, want orphaned", node.Session)
	}

	// 后续心跳不得复活租约，也不得清掉 orphaned。
	clock.Advance(30 * time.Second)
	owner.heartbeatOnce()
	again := readRecord(t, paths, owner.NodeID())
	if again.Session == nil || !again.Session.Orphaned {
		t.Fatalf("后续心跳不得清掉 orphaned: %+v", again.Session)
	}
	if lease, ok := ReadLease(paths.LeasePath(LeasePurposeSession, sessionID)); !ok || lease.OwnerNodeID != thief.NodeID() {
		t.Fatalf("接管者的租约不得被旧节点抢回: %+v", lease)
	}
	if count := countKind(journalKinds(t, paths, owner.NodeID()), JournalLeaseDegraded); count != 1 {
		t.Fatalf("重复心跳不得重复记 lease.degraded（幂等）: count=%d", count)
	}
}

// TestHostTakeoverSessionRefusals 覆盖拒绝路径：没有网格 / 没有会话 id。
func TestHostTakeoverSessionRefusals(t *testing.T) {
	var disabled *Host
	if st := disabled.TakeoverSession("session_x"); st.OK || st.Reason != "mesh-disabled" {
		t.Fatalf("网格关闭必须拒绝: %+v", st)
	}

	paths := testMeshPaths(t)
	clock := newFakeClock()
	host := takeoverTestHost(t, clock, nil)
	if st := host.TakeoverSession("   "); st.OK || st.Reason != "no-session" {
		t.Fatalf("空会话必须拒绝: %+v", st)
	}
	if _, ok := ReadLease(paths.LeasePath(LeasePurposeSession, "   ")); ok {
		t.Fatal("被拒的接管不得留下租约")
	}
}

// TestHostTakeoverSessionClearsOwnOrphanedRecord：被别人接管过、随后显式收回
// 的进程必须把档案恢复到正常状态（否则视图会永远显示 orphaned）。
func TestHostTakeoverSessionClearsOwnOrphanedRecord(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	sessionID := "session_back"

	first := takeoverTestHost(t, clock, nil)
	first.SetSession(&SessionInfo{ID: sessionID})
	second := takeoverTestHost(t, clock, nil)
	if st := second.TakeoverSession(sessionID); !st.OK || !st.Reclaimed {
		t.Fatalf("接管失败: %+v", st)
	}
	clock.Advance(30 * time.Second)
	first.heartbeatOnce()
	if rec := readRecord(t, paths, first.NodeID()); rec.Session == nil || !rec.Session.Orphaned {
		t.Fatalf("前置失败：first 应已 orphaned: %+v", rec.Session)
	}

	back := first.TakeoverSession(sessionID)
	if !back.OK || !back.Reclaimed || back.PreviousOwnerNodeID != second.NodeID() {
		t.Fatalf("收回失败: %+v", back)
	}
	rec := readRecord(t, paths, first.NodeID())
	if rec.Session == nil || rec.Session.Orphaned || rec.Session.OrphanedBy != "" {
		t.Fatalf("收回后必须清掉 orphaned: %+v", rec.Session)
	}
	if st := first.SessionLeaseStatus(); !st.Held || st.Lost || st.Key != sessionID {
		t.Fatalf("收回后租约状态 = %+v", st)
	}
}

// ---------------------------------------------------------------------------
// spawn：--takeover 跳过复用、带接管标记、不抢旧节点的端口
// ---------------------------------------------------------------------------

func TestSpawnTakeoverStartsNewNodeAndSkipsReuse(t *testing.T) {
	paths := testMeshPaths(t)
	clock := newFakeClock()
	owner := spawnTestLiveNode(t, clock, "sess-takeover", 55140)

	// 对照组：不带 takeover 时复用活节点（旧语义逐字不变）。
	plain := Spawn(SpawnRequest{SessionID: "sess-takeover"}, spawnTestOptions(t, paths, clock,
		func(SpawnLaunchSpec) (int, error) { return 0, errors.New("must not launch") }))
	if plain.Status != SpawnStatusReused || plain.NodeID != owner.NodeID() {
		t.Fatalf("对照组 status = %q (node %q), want reused/%s", plain.Status, plain.NodeID, owner.NodeID())
	}

	var captured SpawnLaunchSpec
	opts := spawnTestOptions(t, paths, clock, func(spec SpawnLaunchSpec) (int, error) {
		captured = spec
		writeSpawnTestRecord(t, paths, clock, "node-takeover", "sess-takeover", os.Getpid(), 55141)
		return 999, nil
	})
	result := Spawn(SpawnRequest{SessionID: "sess-takeover", Origin: "cli", Takeover: true}, opts)

	if result.Status != SpawnStatusStarted {
		t.Fatalf("接管必须真的拉新进程，status = %q (reason %q)", result.Status, result.Reason)
	}
	if result.NodeID != "node-takeover" || result.Port != 55141 {
		t.Fatalf("node/port = %s/%d, want node-takeover/55141", result.NodeID, result.Port)
	}
	// 结果必须是新窗口，而不是正被顶替的那个（否则用户点开的是旧窗口）。
	if strings.Contains(result.URL, "55140") || !strings.Contains(result.URL, "55141") {
		t.Fatalf("接管结果必须指向新节点窗口: %q", result.URL)
	}
	if !containsEnv(captured.Env, spawnEnvTakeover+"=1") {
		t.Fatalf("接管子进程必须带接管标记: %v", captured.Env)
	}
	if !containsEnv(captured.Env, spawnEnvSpawnedBy+"=node-spawner") {
		t.Fatalf("接管子进程仍要带父节点标记: %v", captured.Env)
	}
	// 旧节点还在监听绑定的端口：接管不得把它再交给子进程。
	for _, arg := range captured.Args {
		if arg == "--web-port" {
			t.Fatalf("接管必须让子进程自选空闲端口，args = %v", captured.Args)
		}
	}
	if !containsArgPair(captured.Args, "--web-host", "127.0.0.1") {
		t.Fatalf("接管不得改变其余启动参数: %v", captured.Args)
	}
}

func TestResolveSpawnPortHonorsExplicitRequestOnly(t *testing.T) {
	// 绑定偏好不在父进程解析后回传：子进程自己走粘性端口（命中即复用，被占则
	// 随机兜底）。显式请求仍原样透传；非法值一律当作未指定。
	if got := resolveSpawnPort(0); got != 0 {
		t.Fatalf("缺省必须交给子进程自选端口，got %d", got)
	}
	if got := resolveSpawnPort(55200); got != 55200 {
		t.Fatalf("显式端口必须原样透传，got %d", got)
	}
	for _, bad := range []int{-1, 70000} {
		if got := resolveSpawnPort(bad); got != 0 {
			t.Fatalf("非法端口 %d 必须视为未指定，got %d", bad, got)
		}
	}
}

func TestSpawnEnvForChildTakeoverMarker(t *testing.T) {
	// 陈旧值必须被清掉：接管标记只描述「本次拉起」，不描述环境里留下的历史。
	t.Setenv(spawnEnvTakeover, "1")
	t.Setenv(spawnEnvSpawnedBy, "node-stale")

	plain := spawnEnvForChild("node-parent", false)
	if containsEnv(plain, spawnEnvTakeover+"=1") {
		t.Fatalf("非接管不得带接管标记: %v", plain)
	}
	if containsEnv(plain, spawnEnvSpawnedBy+"=node-stale") {
		t.Fatalf("陈旧父节点标记必须被清掉: %v", plain)
	}
	if !containsEnv(plain, spawnEnvSpawnedBy+"=node-parent") {
		t.Fatalf("父节点标记 = %v, want %s=node-parent", plain, spawnEnvSpawnedBy)
	}

	takeover := spawnEnvForChild("node-parent", true)
	if !containsEnv(takeover, spawnEnvTakeover+"=1") {
		t.Fatalf("接管必须带标记: %v", takeover)
	}
	if containsEnv(takeover, spawnEnvSpawnedBy+"=node-stale") {
		t.Fatalf("接管也不得泄漏陈旧父节点标记: %v", takeover)
	}

	empty := spawnEnvForChild("", true)
	if containsEnv(empty, spawnEnvSpawnedBy+"=node-stale") {
		t.Fatalf("selfNodeID 为空也要清理陈旧标记: %v", empty)
	}
}
