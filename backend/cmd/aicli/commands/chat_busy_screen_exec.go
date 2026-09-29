package commands

import (
	"time"
)

// P2-4b（方案 §3.7.1/§3.7.4）：S 档首批白名单与副屏执行原语。
//
// 首批只放行「只读 + 备用屏」且在注册表中已声明为 screen 的命令；
// picker/写入类 screen 命令（/model、/theme、/export、/routing panel…）
// 仍走 Deferred 入队，待各自确认流与快照依赖清单在 P2-5/P3 补齐。

// chatBusyScreenWaitBudget 是进入副屏前的租约等待预算（§3.7.1：≤2s）。
const chatBusyScreenWaitBudget = 2 * time.Second

// chatBusyScreenDocumentCommands 是忙时 S 档白名单（批次 4：从首批 5 条扩展
// 到全部「只读 + 副屏文档」命令，方案 §6 批次 4 / T10）。
//
// 判据（三重，全部 fail-closed）：注册表声明 screen 档 + read 生效域 +
// 显式声明 `screen-document` 输出类别。命令名进白名单只表示「其只读变体
// 允许忙时开屏」：picker/确认流变体（/model、/provider、/theme select、
// /skills select、/mcp select、/routing panel、/profile pick）因
// Effect≠read 或输出类别为 screen-interactive 被判据本身排除，live 写入类
// （/agents panel、/export）同理。批次 5 合并单一事实源后，本名单由注册表
// 的「输出类别」声明直接派生。
var chatBusyScreenDocumentCommands = map[string]struct{}{
	"/todos":     {},
	"/history":   {},
	"/usage":     {},
	"/debug":     {}, // 仅 "display" 变体是 screen（其余变体走 inline 路径）
	"/web":       {}, // 仅 "endpoints" 变体是 screen
	"/account":   {},
	"/accounts":  {},
	"/help":      {},
	"/status":    {},
	"/sessions":  {},
	"/functions": {},
	"/plans":     {}, // 仅 "detail" 变体是 screen（列表/对比仍是短内联）
	"/timeline":  {},
	"/collab":    {},
	"/hotkeys":   {},
	// 批次 3 尾批：只读 list/status 变体迁入副屏后同批纳入忙时通道
	// （/model status、/provider status、/theme status|list|preview、
	// /skills list、/mcp list|status、/profile status|list|show|diff）。
	"/model":    {},
	"/provider": {},
	"/theme":    {},
	"/skills":   {},
	"/mcp":      {},
	"/profile":  {},
}

// busyScreenCommandReadOnlyDocument 判定命令是否可走忙时副屏通道（S 档）：
// 必须同时满足 screen 档 + 只读生效域 + 显式声明只读文档输出类别 + 属于
// 白名单。输出类别判据保证同名命令的交互变体（如 /skills select）不会
// 因命令名命中白名单而被误放行。
func busyScreenCommandReadOnlyDocument(spec runtimeCommandSpec) bool {
	if spec.Mode != runtimeModeScreen || spec.Effect != runtimeEffectRead {
		return false
	}
	if spec.Output != chatOutputScreenDocument {
		return false
	}
	_, ok := chatBusyScreenDocumentCommands[spec.Command]
	return ok
}

// chatBusyScreenCapability 是副屏能力门（fail-closed）。抽成包级变量以便
// 单测注入；生产实现就是框架的 chatScreenCapability（批次 0：I7 单一实现，
// 忙时与空闲路径共用同一个 gate，不再各自维护一份）。
var chatBusyScreenCapability = func(session *ChatSession) bool {
	return chatScreenCapability(session)
}

// chatBusyScreenDispatchOverride 仅供测试注入替身（nil = 走主分派器）。
var chatBusyScreenDispatchOverride func(session *ChatSession, line string) bool

// chatBusyScreenRunDispatch 是副屏执行入口，**调用方必须已持有 commandMu**：
// 本函数在锁内执行 Phase A（解析 + 会话/配置变更 + 渲染），随后立即释放锁，
// 并在锁外应用 Phase B 效应（副屏开启/交互、send 等，§3.3 两阶段契约）。
// 返回 true 表示命令已被本通道占有并完成。
//
// 禁止退化为直接调用 dispatchChatCommand：主分派器会再次获取同一把**非可
// 重入** commandMu（lockChatCommandPhaseA → command.go:119 一带），使同一
// goroutine 自我死锁——修复前 S 档白名单 15 条命令在能力满足时全部不可用，
// 且 capture goroutine 永久持锁。回归测试见
// TestRuntimeCommandHostRunsWhitelistedScreenProductionDispatch。
//
// 测试替身替换整个执行步骤（含锁的释放），保证替身被调用时锁已释放、
// modal 登记与租约预算均已生效。
func chatBusyScreenRunDispatch(session *ChatSession, line string) bool {
	if chatBusyScreenDispatchOverride != nil {
		// 替身路径：TryLock 只作 fail-fast 探测，执行期间不持锁。
		session.commandMu.Unlock()
		return chatBusyScreenDispatchOverride(session, line)
	}
	result, handled, renderErr := chatCommandPhaseALocked(session, line, false)
	session.commandMu.Unlock()
	if !handled || renderErr != nil {
		// 未命中结构化命令或结果单元未提交：不认领输入，调用方回退入队。
		return false
	}
	// Phase B 效应必须在锁外应用。白名单内的只读 screen 命令不可能请求
	// 退出会话，quit 返回值仅服务于主分派器契约，此处忽略。
	_ = chatCommandPhaseB(session, result, renderErr, false)
	return true
}

// runBusyScreenCommand 是 screen 模式的执行原语（P2-4b）。返回 true 表示命令
// 已在副屏通道占有并完成；false 表示未占有（调用方必须回退入队）。
//
// 门禁（全部 fail-closed）：
//  1. 副屏能力（统一渲染面 + surface 就绪 + 终端支持全屏），见 chatBusyScreenCapability；
//  2. L0 仲裁（INV-7）：模态/priority prompt 活跃时不放行；
//  3. Phase A TryLock：与 inline 相同的命令互斥（Phase A 在锁内执行，
//     解锁后才应用 Phase B 效应）；
//
// 执行期间：登记 chatInputOwnerModal（§3.7.1 第 3 步：副屏拥有 stdin 与 ESC，
// 其他通道的请求降级），并把 surface 的租约等待预算设为 2s，使 handler 内部
// 的 AcquireAlternateScreen 在撞上在途租约时等待而非直接失败。
func runBusyScreenCommand(session *ChatSession, line string) bool {
	if session == nil || session.Interaction == nil {
		return false
	}
	if !chatBusyScreenCapability(session) {
		notifyBusyCommandDegraded(session, line, "当前终端/渲染面不支持忙时副屏")
		return false
	}
	if !chatBusyCommandArbitrationAllows(session) {
		return false
	}
	if !session.commandMu.TryLock() {
		session.Interaction.RenderLocalSupplement("[input] 命令通道正忙，该命令已排队，将在当前回合结束后执行。")
		return false
	}
	// 锁由 chatBusyScreenRunDispatch 在 Phase A 之后释放：不得在此 defer
	// Unlock，否则 Phase B 仍会在持锁状态下执行。

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
