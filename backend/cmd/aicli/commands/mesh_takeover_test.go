package commands

import (
	"os"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// S15：`aicli-mesh open --takeover` 拉起的进程带 AICLI_MESH_TAKEOVER=1，本文件
// 锁定 chat 进程一侧的消费语义。

// resetMeshTakeoverMarker 把「读到即消费」的包级状态复位，避免测试之间互相污染。
func resetMeshTakeoverMarker() {
	meshTakeoverMu.Lock()
	meshTakeoverRead, meshTakeoverPending = false, false
	meshTakeoverMu.Unlock()
}

// TestMeshTakeoverRequestedConsumesMarkerOnce：接管语义只属于**首个会话激活**，
// 读到即清除——同一个进程后续的 /resume 绝不能顺手抢别人的租约。
func TestMeshTakeoverRequestedConsumesMarkerOnce(t *testing.T) {
	resetMeshTakeoverMarker()
	defer resetMeshTakeoverMarker()

	t.Setenv(meshEnvTakeover, "1")
	if !meshTakeoverRequested() {
		t.Fatal("AICLI_MESH_TAKEOVER=1 必须被识别为接管请求")
	}
	if got := os.Getenv(meshEnvTakeover); got != "" {
		t.Fatalf("标记必须读到即消费，仍留在环境里: %q", got)
	}
	if meshTakeoverRequested() {
		t.Fatal("同一进程的第二次读取不得再携带接管语义")
	}
}

// TestMeshTakeoverRequestedOnlyAcceptsTruthy 覆盖取值口径：只有 1 / true
// （大小写不敏感）算接管；其余值一律不抢租约。
func TestMeshTakeoverRequestedOnlyAcceptsTruthy(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{" true ", true},
		{"0", false},
		{"false", false},
		{"yes", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			resetMeshTakeoverMarker()
			defer resetMeshTakeoverMarker()
			t.Setenv(meshEnvTakeover, tc.value)
			if got := meshTakeoverRequested(); got != tc.want {
				t.Fatalf("AICLI_MESH_TAKEOVER=%q → %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// TestNotifyMeshTakeoverFailureDoesNotPanic：接管失败只降级为一条诊断（MN1），
// 绝不把 chat 拖进错误路径。
func TestNotifyMeshTakeoverFailureDoesNotPanic(t *testing.T) {
	notifyMeshTakeoverFailure("session-x", mesh.SessionTakeoverStatus{Reason: "held", PreviousOwnerNodeID: "node-peer"})
	notifyMeshTakeoverFailure("session-x", mesh.SessionTakeoverStatus{})
}
