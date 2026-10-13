package commands

import (
	"sync"
	"time"
)

// 忙时副屏（S 档）准入与副屏执行原语。
//
// 准入策略（P2-6）：**只读默认放行 + 写入显式放行 + 例外黑名单**，声明收敛到
// 注册表变体 spec（chat_runtime_command_registry.go 的 rtBusyAllow / rtBusyBlock）：
//   - 只读 screen（Effect=read）：默认放行；
//   - 写入/交互 screen（Effect≠read）：fail-closed，必须显式 rtBusyAllow() 才放行；
//   - 只读中的例外：用 rtBusyBlock() 排除（核验确认不能与在途 turn 并发者）。
// 声明在**变体 spec** 上（而非命令名级），因此能区分同命令的只读/写入变体
// （/agents bare 放行、pick|panel 排除；/theme status 放行、select|set 排除）。
//
// 默认放行的依据（§3.8.1 注册表不变式，T28 静态断言 + runtimeHostEffectReachable
// 宿主守卫）：screen 档的生效域只能是 read/live/next-turn/next-call——session/process
// 只允许声明为 queue/block；且执行链路自带三重安全网：
//  1. 选择/写入只在副屏租约释放后应用（I3）；
//  2. 宿主侧运行时刷新永不打断在途 turn（refreshLocalRuntimeAfterSelection
//     把 actor 重建登记为延迟重建，下一回合生效）；
//  3. 能力不足/租约忙/嵌套时框架 fail-closed 降级为主屏内联（D-F/I6/I9）。
//
// 黑名单（BusyBlock）收录经逐命令核验确认「不能与在途 turn 并发」的变体
// （2026-10-13 核验：handler 在 Phase A 内阻塞/写全局态、在租约释放后仍走副作用
// 路径、或与在途 turn 争锁）：
//   - /theme select|set、/agents pick|panel：live 生效域，Phase A 内改全局/会话
//     选中态并戳 presenter（假设 actor 空闲）；
//   - /profile pick、/agents pick：Phase A（持 commandMu）内阻塞读 priority prompt；
//   - /routing panel：handler 内同步开全屏并取 LockSessionWrite，可能与在途 turn 争锁；
//   - /export、/skills select、/mcp select：gate 在 run 活跃时硬拒绝，随后回退到
//     「立即导出在途会话 / 改配置热刷新函数面」等副作用路径。
//
// 写入类放行（rtBusyAllow）：/model、/provider、/reasoning_effort——next-turn 安全
// 且结果在 Phase B（租约释放后）应用。新增写入 screen 变体默认拒绝（fail-closed），
// 核验通过后再在注册表加 rtBusyAllow()。

// chatBusyScreenWaitBudget 是进入副屏前的租约等待预算（§3.7.1：≤2s）。
const chatBusyScreenWaitBudget = 2 * time.Second

// busyScreenCommandAllowed 是忙时副屏的统一判据（I7 单一实现）：screen 档 +
// 显式声明输出类别 + 未声明 BusyBlock。策略映射（chatBusyPolicyFromRuntimeSpec）
// 与宿主准入（runtimeCommandHost.submit）都必须经本函数，不得各自维护一份。
func busyScreenCommandAllowed(spec runtimeCommandSpec) bool {
	if spec.Mode != runtimeModeScreen {
		return false
	}
	// screen 档必须显式声明输出类别（T11 守卫）；未声明视为非法声明，fail-closed。
	if !spec.Output.declared() {
		return false
	}
	if spec.BusyBlock {
		return false
	}
	// fail-closed：只读 screen 默认放行（只读中的例外用 BusyBlock 排除，如
	// /export、/skills select）；写入/交互类（Effect≠read）必须显式声明 BusyAllow
	// 才放行——新增写入 screen 变体默认拒绝，须先核验再声明。
	if spec.Effect == runtimeEffectRead {
		return true
	}
	return spec.BusyAllow
}

// markChatBusyInteractiveScreenActive 登记一个忙时副屏交互窗口，返回幂等释放函数。
// 窗口存续期间 chatPickerSurfaceReady 允许在 run 活跃时打开选择器（见
// chat_picker_common.go）。nil 会话安全。
func markChatBusyInteractiveScreenActive(session *ChatSession) func() {
	if session == nil {
		return func() {}
	}
	session.busyInteractiveScreen.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() { session.busyInteractiveScreen.Add(-1) })
	}
}

// chatBusyInteractiveScreenActive 报告当前是否处在忙时副屏交互窗口内。
func chatBusyInteractiveScreenActive(session *ChatSession) bool {
	return session != nil && session.busyInteractiveScreen.Load() > 0
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
	// Phase B 效应必须在锁外应用。screen 命令不可能请求退出会话，quit 返回值
	// 仅服务于主分派器契约，此处忽略。
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
	// 忙时副屏交互窗口：Phase A/B 期间允许交互选择器在 run 活跃时开屏。
	releaseInteractive := markChatBusyInteractiveScreenActive(session)
	defer releaseInteractive()

	if !chatBusyScreenRunDispatch(session, line) {
		return false
	}
	recordChatPromptHistory(session, line)
	return true
}
