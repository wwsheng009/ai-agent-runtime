package commands

import (
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// P2-4b（方案 §3.7.1/§3.7.4）：S 档首批白名单与副屏执行原语。
//
// 首批只放行「只读 + 备用屏」且在注册表中已声明为 screen 的命令；
// picker/写入类 screen 命令（/model、/theme、/export、/routing panel…）
// 仍走 Deferred 入队，待各自确认流与快照依赖清单在 P2-5/P3 补齐。

// chatBusyScreenWaitBudget 是进入副屏前的租约等待预算（§3.7.1：≤2s）。
const chatBusyScreenWaitBudget = 2 * time.Second

// chatBusyScreenFirstBatchCommands 是首批白名单命令名（注册表 Command 键）。
var chatBusyScreenFirstBatchCommands = map[string]struct{}{
	"/todos":   {},
	"/history": {},
	"/usage":   {},
	"/debug":   {}, // 仅 "display" 变体是 screen（其余变体走 inline 路径）
	"/web":     {}, // 仅 "endpoints" 变体是 screen
}

// busyScreenCommandFirstBatch 判定命令是否属于首批 S 档白名单：
// 必须同时满足 screen 档 + 只读生效域 + 命令名在白名单内。
func busyScreenCommandFirstBatch(spec runtimeCommandSpec) bool {
	if spec.Mode != runtimeModeScreen || spec.Effect != runtimeEffectRead {
		return false
	}
	_, ok := chatBusyScreenFirstBatchCommands[spec.Command]
	return ok
}

// chatBusyScreenCapability 是副屏能力门（fail-closed）。抽成包级变量以便
// 单测注入：生产实现复用 /debug display 同款判定（统一渲染面 + 已启用的
// 统一 surface + 无在途租约/弹层 + 终端支持全屏列表）。
var chatBusyScreenCapability = func(session *ChatSession) bool {
	if session == nil || session.NoInteractive || session.JSONOutput ||
		session.Interaction == nil || session.Surface == nil {
		return false
	}
	if !unifiedDirectInteractiveOutput(session) {
		return false
	}
	if !session.Surface.Enabled() || !session.Surface.OwnedViewport() ||
		session.Surface.LeaseActive() || session.Surface.HasActivePopup() {
		return false
	}
	return ui.CanUseFullScreenList(resumeFullScreenTerminal(session))
}

// chatBusyScreenDispatchOverride 仅供测试注入替身（nil = 走主分派器）。
// 不能把主分派器写进包级变量初始化式：dispatchChatCommand → … →
// runBusyScreenCommand 会构成初始化环。
var chatBusyScreenDispatchOverride func(session *ChatSession, line string) bool

// chatBusyScreenRunDispatch 是副屏执行入口（生产 = 主分派器，含命令渲染与
// Phase B 屏幕开启）。
func chatBusyScreenRunDispatch(session *ChatSession, line string) bool {
	if chatBusyScreenDispatchOverride != nil {
		return chatBusyScreenDispatchOverride(session, line)
	}
	return dispatchChatCommand(session, line, false)
}

// runBusyScreenCommand 是 screen 模式的执行原语（P2-4b）。返回 true 表示命令
// 已在副屏通道占有并完成；false 表示未占有（调用方必须回退入队）。
//
// 门禁（全部 fail-closed）：
//  1. 副屏能力（统一渲染面 + surface 就绪 + 终端支持全屏），见 chatBusyScreenCapability；
//  2. L0 仲裁（INV-7）：模态/priority prompt 活跃时不放行；
//  3. Phase A TryLock：与 inline 相同的命令互斥；
//
// 执行期间：登记 chatInputOwnerModal（§3.7.1 第 3 步：副屏拥有 stdin 与 ESC，
// 其他通道的请求降级），并把 surface 的租约等待预算设为 2s，使 handler 内部
// 的 AcquireAlternateScreen 在撞上在途租约时等待而非直接失败。
func runBusyScreenCommand(session *ChatSession, line string) bool {
	if session == nil || session.Interaction == nil {
		return false
	}
	if !chatBusyScreenCapability(session) {
		return false
	}
	if !chatBusyCommandArbitrationAllows(session) {
		return false
	}
	if !session.commandMu.TryLock() {
		session.Interaction.RenderLocalSupplement("[input] 命令通道正忙，该命令已排队，将在当前回合结束后执行。")
		return false
	}
	defer session.commandMu.Unlock()

	releaseModal := beginChatInputShadowLevel(session, chatInputOwnerModal)
	defer releaseModal()
	if session.Surface != nil {
		session.Surface.SetAlternateScreenWaitBudget(chatBusyScreenWaitBudget)
		defer session.Surface.SetAlternateScreenWaitBudget(0)
	}

	if !chatBusyScreenRunDispatch(session, line) {
		return false
	}
	recordChatPromptHistory(session, line)
	return true
}
