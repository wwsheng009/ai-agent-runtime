package commands

import (
	"strings"
	"sync/atomic"
)

// 进程级诊断消息出口（mesh 等后台子系统的 warning 通道）。
//
// 背景：网格订阅循环这类长期运行的 goroutine 会在 active session 期间发警告。
// 两条错误路线都已排除：
//   - 直接写 os.Stderr：字节落在 FixedBottomSurface 的底部保留区（状态栏 /
//     输入行）上，把状态栏覆盖成半截文本——渲染字节只能由 TerminalSession
//     产生（§8.5 stderr/stdout 契约）；
//   - 作为语义补充 cell 写进 transcript：历史信息流只承载会话语义，peer
//     断连/重连的降级告警会持续刷屏，淹没正文。
//
// 因此交互式会话在构造 coordinator 时把自己登记为唯一出口：诊断消息只进
// 动态栏——单行、临时、整行覆盖活动行、到期自动清除（见
// chatInteractionCoordinator.ShowDiagnosticNotice）。没有登记（启动阶段 /
// 非交互 / JSON 模式）时返回 false，调用方保留 stderr 兜底。
var chatDiagnosticSink atomic.Pointer[chatInteractionCoordinator]

// NotifyChatDiagnostic 把一条诊断消息投递给当前交互式会话的动态栏。返回
// false 表示当前没有可接收的交互式会话，调用方应回退到 stderr。
//
// 该方法可被后台 goroutine 调用：ShowDiagnosticNotice 内部自行加锁，只更新
// 状态行缓存并投递 surface action，不产生裸终端字节。
func NotifyChatDiagnostic(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	coord := chatDiagnosticSink.Load()
	if coord == nil {
		return false
	}
	return coord.ShowDiagnosticNotice(line)
}

// registerChatDiagnosticSink 由会话在启动时登记；非交互 / JSON 模式直接
// 忽略（它们的输出由调用方的 stderr 兜底）。最后登记者胜出：终端同一时刻
// 只应有一个交互式会话拥有。
func registerChatDiagnosticSink(coord *chatInteractionCoordinator) {
	if coord == nil || coord.session == nil || coord.session.NoInteractive || coord.session.JSONOutput {
		return
	}
	chatDiagnosticSink.Store(coord)
}

// unregisterChatDiagnosticSink 只清除自己登记的出口。resume / 切换会话会
// 先建新 coordinator 再关旧的，无条件清零会把后来者的登记一起抹掉。
func unregisterChatDiagnosticSink(coord *chatInteractionCoordinator) {
	if coord == nil {
		return
	}
	chatDiagnosticSink.CompareAndSwap(coord, nil)
}
