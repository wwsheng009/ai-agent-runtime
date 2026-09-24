package commands

import (
	"strings"
	"sync/atomic"
)

// 进程级诊断消息出口（mesh 等后台子系统的 warning 通道）。
//
// 背景：网格订阅循环这类长期运行的 goroutine 会在 active session 期间发警告。
// 若它们直接写 os.Stderr，字节就落在 FixedBottomSurface 的底部保留区
// （状态栏 / 输入行）上，把状态栏覆盖成半截文本——渲染字节只能由
// TerminalSession 产生（§8.5 stderr/stdout 契约）。
//
// 因此交互式会话在构造 coordinator 时把自己登记为唯一出口：诊断消息走
// 语义补充 cell，由渲染层负责占位与滚动。没有登记（启动阶段 / 非交互 /
// JSON 模式）时返回 false，调用方保留 stderr 兜底。
var chatDiagnosticSink atomic.Pointer[chatInteractionCoordinator]

// NotifyChatDiagnostic 把一条诊断消息作为语义补充 cell 投递给当前交互式
// 会话。返回 false 表示当前没有可接收的交互式会话，调用方应回退到 stderr。
//
// 该方法可被后台 goroutine 调用：RenderLocalSupplement 内部自行加锁并把
// 提交结果发布为 Scene snapshot，不产生裸终端字节。
func NotifyChatDiagnostic(line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	coord := chatDiagnosticSink.Load()
	if coord == nil {
		return false
	}
	coord.RenderLocalSupplement(line)
	return true
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
