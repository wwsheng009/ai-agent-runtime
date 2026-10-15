package schedulehost_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

// startPresenceNode 在隔离的 mesh 根里启动一个存活节点（写档案 + 心跳），
// 返回节点与其解析后的路径；用法与 newTestLeader 的 EnvMeshDir 隔离一致。
func startPresenceNode(t *testing.T, capabilities []string) (*mesh.Host, mesh.Paths) {
	t.Helper()
	return startPresenceNodeKind(t, "scheduled", capabilities)
}

// startPresenceNodeKind 同上，但指定节点 kind（S5 优先级让位测试需要
// daemon/runtime-server/aicli 三种角色）。
func startPresenceNodeKind(t *testing.T, kind string, capabilities []string) (*mesh.Host, mesh.Paths) {
	t.Helper()
	t.Setenv(mesh.EnvMeshDir, t.TempDir())
	host := mesh.NewHost(mesh.HostConfig{
		Kind:         kind,
		Capabilities: capabilities,
		Warn:         func(string, ...any) {},
	})
	if err := host.Start(); err != nil {
		t.Fatalf("mesh host start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = host.Stop(ctx, mesh.StopRequest{Target: host.NodeID()})
	})
	return host, mesh.ResolvePaths()
}

func acquirePresenceLease(t *testing.T, paths mesh.Paths, nodeID string) {
	t.Helper()
	outcome := mesh.Acquire(paths, schedulehost.LeasePurpose, schedulehost.LeaseKey,
		mesh.LeaseOwner{NodeID: nodeID, PID: os.Getpid()}, mesh.AcquireOptions{TTL: time.Minute})
	if !outcome.Acquired {
		t.Fatalf("acquire lease for %s: %+v", nodeID, outcome)
	}
}

// TestProbePresenceAbsentWithoutLease：无租约即无人调度（即使 mesh 里可能有
// 别的节点）——创建回执必须给出可操作警告。
func TestProbePresenceAbsentWithoutLease(t *testing.T) {
	t.Setenv(mesh.EnvMeshDir, t.TempDir())
	presence := schedulehost.ProbePresenceWithPaths(mesh.ResolvePaths(), schedule.SupportedSchemaVersion())
	if presence.State != schedulehost.PresenceAbsent {
		t.Fatalf("state = %q, want %q (%+v)", presence.State, schedulehost.PresenceAbsent, presence)
	}
	if presence.Ghost {
		t.Fatalf("no lease must not be reported as ghost: %+v", presence)
	}
	if !strings.Contains(presence.Notice(), "无调度者在岗") {
		t.Fatalf("notice = %q, want actionable driver-absent warning", presence.Notice())
	}
}

// TestProbePresenceOnlineWithCompatibleDriver：租约 + 存活节点 + 兼容标记 →
// online；在线时 Notice 必须为空（不给正常路径加噪音）。
func TestProbePresenceOnlineWithCompatibleDriver(t *testing.T) {
	host, paths := startPresenceNode(t, []string{
		"mesh",
		schedulehost.CapabilityScheduler,
		schedulehost.SchedulerCapability(schedule.SupportedSchemaVersion()),
	})
	acquirePresenceLease(t, paths, host.NodeID())

	presence := schedulehost.ProbePresenceWithPaths(paths, schedule.SupportedSchemaVersion())
	if presence.State != schedulehost.PresenceOnline {
		t.Fatalf("state = %q, want online (%+v)", presence.State, presence)
	}
	if presence.HolderNodeID != host.NodeID() || presence.HolderPID != os.Getpid() {
		t.Fatalf("holder = %s/%d, want %s/%d", presence.HolderNodeID, presence.HolderPID, host.NodeID(), os.Getpid())
	}
	if presence.Notice() != "" {
		t.Fatalf("online notice must be empty, got %q", presence.Notice())
	}
}

// TestProbePresenceIncompatibleOlderMarker：在岗但声明的 store 档位低于当前 →
// driver_incompatible（创建回执要求升级，而不是静默接受）。
func TestProbePresenceIncompatibleOlderMarker(t *testing.T) {
	host, paths := startPresenceNode(t, []string{
		"mesh", schedulehost.CapabilityScheduler, schedulehost.SchedulerCapability(3),
	})
	acquirePresenceLease(t, paths, host.NodeID())

	presence := schedulehost.ProbePresenceWithPaths(paths, schedule.SupportedSchemaVersion())
	if presence.State != schedulehost.PresenceIncompatible {
		t.Fatalf("state = %q, want incompatible (%+v)", presence.State, presence)
	}
	if presence.DriverStoreSchema != 3 {
		t.Fatalf("driver_store_schema = %d, want 3", presence.DriverStoreSchema)
	}
	if !strings.Contains(presence.Notice(), "过旧") {
		t.Fatalf("notice = %q, want upgrade hint", presence.Notice())
	}
}

// TestProbePresenceIncompatibleWithoutMarker：旧版（无兼容档）调度者无法判定
// 版本 → 一律按不兼容提示升级。
func TestProbePresenceIncompatibleWithoutMarker(t *testing.T) {
	host, paths := startPresenceNode(t, []string{"mesh", schedulehost.CapabilityScheduler})
	acquirePresenceLease(t, paths, host.NodeID())

	presence := schedulehost.ProbePresenceWithPaths(paths, schedule.SupportedSchemaVersion())
	if presence.State != schedulehost.PresenceIncompatible {
		t.Fatalf("state = %q, want incompatible (%+v)", presence.State, presence)
	}
	if presence.DriverStoreSchema != 0 {
		t.Fatalf("driver_store_schema = %d, want 0 (undecleared)", presence.DriverStoreSchema)
	}
	if !strings.Contains(presence.Notice(), "未声明") {
		t.Fatalf("notice = %q, want undeclared-marker hint", presence.Notice())
	}
}

// TestProbePresenceGhostWhenHolderMissing：租约存在但持有者不在节点档案
// （幽灵 leader）→ absent + ghost 标记 + 接管说明。
func TestProbePresenceGhostWhenHolderMissing(t *testing.T) {
	t.Setenv(mesh.EnvMeshDir, t.TempDir())
	paths := mesh.ResolvePaths()
	acquirePresenceLease(t, paths, "node-ghost")

	presence := schedulehost.ProbePresenceWithPaths(paths, schedule.SupportedSchemaVersion())
	if presence.State != schedulehost.PresenceAbsent || !presence.Ghost {
		t.Fatalf("presence = %+v, want absent+ghost", presence)
	}
	if !strings.Contains(presence.Notice(), "幽灵") {
		t.Fatalf("notice = %q, want ghost hint", presence.Notice())
	}
}

// TestProbePresenceGhostWhenHolderStale：租约持有者档案存在但进程已死
// （pid 不可达）→ 有效存活判定为 stale → 同样按幽灵处理。
func TestProbePresenceGhostWhenHolderStale(t *testing.T) {
	t.Setenv(mesh.EnvMeshDir, t.TempDir())
	paths := mesh.ResolvePaths()
	if err := os.MkdirAll(paths.Nodes, 0o700); err != nil {
		t.Fatalf("mkdir nodes: %v", err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	record := fmt.Sprintf(
		`{"schema_version":2,"node_id":"node-dead","pid":99999999,"kind":"scheduled",`+
			`"capabilities":["mesh","scheduler","%s"],`+
			`"liveness":{"started_at":%q,"updated_at":%q,"heartbeat_at":%q,"heartbeat_ttl_sec":90,"state":"live"}}`,
		schedulehost.SchedulerCapability(schedule.SupportedSchemaVersion()), now, now, now)
	if err := os.WriteFile(paths.NodePath("node-dead"), []byte(record), 0o600); err != nil {
		t.Fatalf("write node record: %v", err)
	}
	acquirePresenceLease(t, paths, "node-dead")

	presence := schedulehost.ProbePresenceWithPaths(paths, schedule.SupportedSchemaVersion())
	if presence.State != schedulehost.PresenceAbsent || !presence.Ghost {
		t.Fatalf("presence = %+v, want absent+ghost for stale holder", presence)
	}
	if !strings.Contains(presence.Message, "非 live") {
		t.Fatalf("message = %q, want stale detail", presence.Message)
	}
}

// TestSchedulerCapabilityFormat 钉住标记线格式（能力位是跨进程契约）。
func TestSchedulerCapabilityFormat(t *testing.T) {
	if got := schedulehost.SchedulerCapability(6); got != "scheduler:store-v6" {
		t.Fatalf("SchedulerCapability(6) = %q", got)
	}
}

// TestProbePresenceUsesInjectedSchema 钉住"档位判定方向"：调度者声明 v7、
// 当前 store v6 → 兼容（更高版本可读旧库）。
func TestProbePresenceDriverNewerThanStore(t *testing.T) {
	host, paths := startPresenceNode(t, []string{
		"mesh", schedulehost.CapabilityScheduler, schedulehost.SchedulerCapability(7),
	})
	acquirePresenceLease(t, paths, host.NodeID())
	presence := schedulehost.ProbePresenceWithPaths(paths, 6)
	if presence.State != schedulehost.PresenceOnline {
		t.Fatalf("state = %q, want online (%+v)", presence.State, presence)
	}
}

// TestSchedulerKindPriority 钉住 S5 优先级词表（daemon > runtime-server > aicli；
// 未知 kind = 0 视为最高——保守方向：宁可让位）。
func TestSchedulerKindPriority(t *testing.T) {
	cases := map[string]int{
		"scheduled":      1,
		"daemon":         1,
		"runtime-server": 2,
		"chat":           3,
		"aicli":          3,
		"unknown-kind":   0,
	}
	for kind, want := range cases {
		if got := schedulehost.SchedulerKindPriority(kind); got != want {
			t.Fatalf("SchedulerKindPriority(%q) = %d, want %d", kind, got, want)
		}
	}
}

// TestHigherPrioritySchedulerOnline 钉住让位判定的输入面：只有"live + 带调度
// 能力位 + 优先级更高"的**其他**节点才算；自己、非 live、无调度位一律不算。
func TestHigherPrioritySchedulerOnline(t *testing.T) {
	caps := []string{"mesh", schedulehost.CapabilityScheduler, schedulehost.SchedulerCapability(schedule.SupportedSchemaVersion())}
	daemon, paths := startPresenceNodeKind(t, "scheduled", caps)

	if higher, kind := schedulehost.HigherPrioritySchedulerOnline(paths, "chat", "node-me"); !higher || kind != "scheduled" {
		t.Fatalf("aicli must defer to a live daemon: higher=%v kind=%q", higher, kind)
	}
	if higher, _ := schedulehost.HigherPrioritySchedulerOnline(paths, "runtime-server", "node-me"); !higher {
		t.Fatal("runtime-server must defer to a live daemon")
	}
	if higher, _ := schedulehost.HigherPrioritySchedulerOnline(paths, "scheduled", daemon.NodeID()); higher {
		t.Fatal("daemon must not defer to anyone")
	}
	if higher, _ := schedulehost.HigherPrioritySchedulerOnline(paths, "chat", daemon.NodeID()); higher {
		t.Fatal("a node must not defer to itself")
	}

	// 节点离开（档案移除）→ 不再让位。直接删档案：mesh.Stop 拒绝停止"调用方
	// 自身节点"（fixture 与测试同 pid），而本用例只关心"档案不再 live"。
	if err := os.Remove(paths.NodePath(daemon.NodeID())); err != nil {
		t.Fatalf("remove fixture record: %v", err)
	}
	if higher, kind := schedulehost.HigherPrioritySchedulerOnline(paths, "chat", "node-me"); higher {
		t.Fatalf("a stopped daemon must not block aicli: kind=%q", kind)
	}
}
