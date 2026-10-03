package commands

import (
	"fmt"
	"os"
	"strings"
)

// aicliDiagf / aicliDiagln 是 chat 交互模式的 [aicli-diag] 诊断输出钩子。
//
// 输出策略（§8.5 stderr/stdout 契约）：
//
//  1. 优先尝试通过 NotifyChatDiagnostic 路由到交互式会话的动态状态栏
//     ——单行、临时、整行覆盖活动行、自动过期清除、不进入 transcript 历史、
//     不产生裸终端字节。这样避免了直接写 stderr 在 TUI 活动时字节落在
//     FixedBottomSurface 底部保留区（状态栏 / 输入行）上、把状态栏覆盖成
//     半截文本的问题。
//
//  2. 多行消息（如 goroutine dump）无法在单行动态栏上完整展示，因此绕过
//     状态栏直接写入 stderr，保留完整内容。
//
//  3. 当没有可用的交互式会话时（启动阶段 / 非交互 / JSON 模式 / 兼容模式），
//     NotifyChatDiagnostic 返回 false，回退到直接写入 os.Stderr，保持
//     原有行为。
//
// 定义为变量以便测试或宿主嵌入方静默/重定向。
//
// 初始化时只引用普通函数值，不能在这里放一个直接调用
// NotifyChatDiagnostic 的闭包：NotifyChatDiagnostic 的渲染链最终会回到
// paintScheduledPromptFrame，而该函数本身也使用 aicliDiag*，会被 Go 判定为
// package initialization cycle。
var aicliDiagf func(format string, args ...any)
var aicliDiagln func(args ...any)

func init() {
	// 放在 init 中绑定，避免变量初始化依赖沿 NotifyChatDiagnostic 的渲染
	// 调用链回到本文件而形成 package initialization cycle。
	aicliDiagf = writeAICLIDiagf
	aicliDiagln = writeAICLIDiagln
}

// aicliDiagRawf/aicliDiagRawln 是渲染器内部的逃生通道。它们只应在已经
// 持有 coordinator 锁、或正在执行 UI reducer 的路径中使用；这些路径不能
// 再调用 NotifyChatDiagnostic，否则会递归进入同一个 reducer 并死锁。
var aicliDiagRawf = func(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format, args...)
}

var aicliDiagRawln = func(args ...any) {
	_, _ = fmt.Fprintln(os.Stderr, args...)
}

func writeAICLIDiagf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	// fmt.Fprintf 的常规诊断格式通常带一个末尾换行；去掉这个结构性
	// 换行后再尝试动态栏，真正包含中间换行的内容仍会走 stderr。
	if routeAICLIDiagnostic(strings.TrimSuffix(msg, "\n")) {
		return
	}
	_, _ = fmt.Fprint(os.Stderr, msg)
}

func writeAICLIDiagln(args ...any) {
	// 先保留 Fprintln 的原有格式语义（多个参数之间插入空格），再去掉
	// 末尾换行交给动态栏；无法压成单行时仍按原样写 stderr。
	msg := fmt.Sprintln(args...)
	if routeAICLIDiagnostic(strings.TrimSuffix(msg, "\n")) {
		return
	}
	_, _ = fmt.Fprint(os.Stderr, msg)
}

func routeAICLIDiagnostic(msg string) bool {
	if msg == "" || strings.ContainsRune(msg, '\n') {
		// 动态栏是单行临时提示。多行内容（例如 goroutine dump）必须保留
		// 完整文本，不能静默截断后再丢失原始诊断。
		return false
	}
	return NotifyChatDiagnostic(msg)
}
