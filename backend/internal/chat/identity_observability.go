package chat

import (
	"sync/atomic"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// P3 fail-loud 观测（session-message-identity-protocol-20261017 §5 / §6）：
// identity_missing 计数器记录进入 canonical 对齐的 history 窗口中无 message_id
// 的行数。协议（P1/P2）落地后新写入行必有 id，该计数非零意味着：
//   1. 存在绕过创建时授予的写入路径（回归信号，必须修复）；
//   2. 或存量未 backfill 的行被重新加载后再次进入对齐（迁移噪声，backfill 后消失）。
// 观测窗完成判据：计数器增量连续 7 天为 0 且 sqlite 取证脚本复跑通过，
// 届时可删除 substance 启发式（P4）。

var identityMissingTotal atomic.Int64

// IdentityMissingTotal 返回进程启动以来观测到的无 id 行累计数（测试与诊断用）。
func IdentityMissingTotal() int64 {
	return identityMissingTotal.Load()
}

func observeIdentityMissing(missing, window int) {
	identityMissingTotal.Add(int64(missing))
	if s := logpkg.S(); s != nil {
		s.Infof("[session-history] identity_missing missing=%d window=%d (protocol: new rows must carry message_id)",
			missing, window)
	}
}
