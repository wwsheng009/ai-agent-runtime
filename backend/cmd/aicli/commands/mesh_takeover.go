package commands

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// 本文件是 S15「接管（--takeover）」在 chat 进程一侧的落点：
//
//   - `aicli-mesh open --takeover` 给拉起的子进程带上 AICLI_MESH_TAKEOVER=1
//     （internal/mesh 的 spawnEnvTakeover），子进程在**首个会话激活**时显式回收
//     租约（architecture §4.4），旧节点继续运行并在下次心跳后标记 orphaned；
//   - 标记读到即清除：同一个进程后续的 /resume 切换不携带接管语义，绝不顺手
//     抢别人的租约；
//   - 接管失败只是降级（MN1）：按普通恢复继续，发一条诊断，不上报 chat 错误。

// meshEnvTakeover mirrors internal/mesh's spawnEnvTakeover.
const meshEnvTakeover = "AICLI_MESH_TAKEOVER"

var (
	meshTakeoverMu      sync.Mutex
	meshTakeoverRead    bool
	meshTakeoverPending bool
)

// meshTakeoverRequested reports whether this process was started with an explicit
// takeover request. The marker is consumed on first read (see the file comment).
func meshTakeoverRequested() bool {
	meshTakeoverMu.Lock()
	defer meshTakeoverMu.Unlock()
	if !meshTakeoverRead {
		meshTakeoverRead = true
		value := strings.TrimSpace(os.Getenv(meshEnvTakeover))
		meshTakeoverPending = value == "1" || strings.EqualFold(value, "true")
		if meshTakeoverPending {
			_ = os.Unsetenv(meshEnvTakeover)
		}
	}
	pending := meshTakeoverPending
	// 读到即消费：接管语义只属于首个会话激活。syncChatMeshSession 在每次会话
	// 绑定（/new、resume、Web 切换）都会走到这里，若不清零，后续切换就会顺手
	// 抢走别人的租约（§4.4 只允许显式、一次性接管）。
	meshTakeoverPending = false
	return pending
}

// notifyMeshTakeoverFailure surfaces a refused takeover as a diagnostic line.
// The chat keeps running with the old semantics: nothing is escalated (MN1).
func notifyMeshTakeoverFailure(sessionID string, status mesh.SessionTakeoverStatus) {
	reason := strings.TrimSpace(status.Reason)
	if reason == "" {
		reason = "unknown"
	}
	line := fmt.Sprintf("Warning: mesh: 接管会话 %s 失败（%s）：本进程按普通恢复继续，不抢占租约", sessionID, reason)
	if owner := strings.TrimSpace(status.PreviousOwnerNodeID); owner != "" {
		line += "；当前持有者 " + owner
	}
	if !NotifyChatDiagnostic(line) {
		fmt.Fprintln(os.Stderr, line)
	}
}
