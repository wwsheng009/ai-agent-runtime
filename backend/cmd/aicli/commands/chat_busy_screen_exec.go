package commands

import (
	"sync"
	"time"
)

// 忙时副屏（S 档）准入与副屏执行原语。
//
// 准入保持**显式白名单**（fail-closed），不做「screen 档默认放行」的反转：
// 2026-10-13 对候选命令逐一核验（handler 是否在 Phase A 内阻塞/写全局态、
// 是否在租约释放后应用、是否持有与在途 turn 争用的锁）发现——`/theme select|set`、
// `/profile pick`、`/mcp select`、`/routing panel`、`/agents pick|panel`、
// `/export`、`/skills select` 全部**不能**与在途 turn 并发：
//   - `/theme set`、`/agents pick|panel` 是 live 生效域，会在 Phase A 内改全局/
//     会话选中态并戳 presenter（假设 actor 空闲）；
//   - `/profile pick`、`/agents pick` 在 Phase A（持 commandMu）内阻塞读 priority
//     prompt；`/routing panel` 在 handler 内同步开全屏并取 LockSessionWrite，
//     可能与在途 turn 争锁；
//   - `/export`、`/skills select`、`/mcp select` 的 gate 在 run 活跃时硬拒绝，
//     随后回退到「立即导出在途会话 / 改配置热刷新函数面」等副作用路径。
//
// 因此只放行经核验的命令：只读文档 + 只读交互列表（/agents）+ next-turn 安全且
// 在 Phase B（租约释放后）应用的三个写入选择器（/model、/provider、
// /reasoning_effort）。新增 screen 命令必须先过同样的核验再进白名单。

// chatBusyScreenWaitBudget 是进入副屏前的租约等待预算（§3.7.1：≤2s）。
const chatBusyScreenWaitBudget = 2 * time.Second

// chatBusyScreenDocumentCommands 是忙时 S 档白名单（批次 4：从首批 5 条扩展
// 到全部「只读 + 副屏文档」命令，方案 §6 批次 4 / T10）。
//
// 判据（三重，全部 fail-closed）：注册表声明 screen 档 + read 生效域 +
// 显式声明 `screen-document` 输出类别。命令名进白名单只表示「其只读变体
// 允许忙时开屏」：picker/确认流变体（/theme select、/skills select、/mcp select、
// /routing panel、/profile pick）因 Effect≠read 或输出类别为 screen-interactive
// 被判据本身排除。
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
	// 批次 3 尾批扩展：/lsp 只读长文档（status/list/servers/diagnostics）
	// 迁入副屏后同批纳入忙时通道；restart/start 与 help 不在此列。
	"/lsp": {},
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

// chatBusyScreenInteractiveCommands 是忙时 S 档的「只读交互副屏」白名单
// （A 族列表）：声明 screen+read+screen-interactive，交互只做只读快照与
// 选择后打开只读视图，不写会话/配置状态、不发消息、不跨回合。
//
// /agents（bare）是本表当前唯一成员：列表数据是 durable registry 快照，
// 选中后仅打开只读 transcript。写变体（panel/pick/approve/deny/send/followup/
// cleanup）因 Effect≠read 或未列入本表继续排队；/agents status|list 保持
// inline+read（I 档）。
var chatBusyScreenInteractiveCommands = map[string]struct{}{
	"/agents": {},
}

// busyScreenCommandReadOnlyInteractive 判定只读交互副屏（列表）是否可以
// 走忙时通道：screen 档 + 只读生效域 + screen-interactive 输出类别 + 白名单。
func busyScreenCommandReadOnlyInteractive(spec runtimeCommandSpec) bool {
	if spec.Mode != runtimeModeScreen || spec.Effect != runtimeEffectRead {
		return false
	}
	if spec.Output != chatOutputScreenInteractive {
		return false
	}
	_, ok := chatBusyScreenInteractiveCommands[spec.Command]
	return ok
}

// chatBusyScreenInteractiveWriteCommands 是忙时 S 档「可写交互副屏」白名单
// （P2-5 生效域适配）：选择器自身在副屏内完成选择，会话/配置写入只在租约释放后
// 应用（I3），且宿主侧运行时刷新永不打断在途 turn（refreshLocalRuntimeAfterSelection
// 把 actor 重建登记为延迟重建，下一回合生效），因此与 next-turn 生效域一致、
// 可安全地在回合运行中执行。
//
// 与只读白名单同样 fail-closed：未列入本表的 screen-interactive 写入类命令
// 继续按 Deferred 入队（逐命令核验结论见文件头）。
var chatBusyScreenInteractiveWriteCommands = map[string]struct{}{
	"/model":            {},
	"/provider":         {},
	"/reasoning_effort": {},
}

// busyScreenCommandInteractiveWrite 判定「可写交互副屏」是否可以走忙时通道：
// screen 档 + next-turn 生效域 + screen-interactive 输出类别 + 白名单。
func busyScreenCommandInteractiveWrite(spec runtimeCommandSpec) bool {
	if spec.Mode != runtimeModeScreen || spec.Effect != runtimeEffectNextTurn {
		return false
	}
	if spec.Output != chatOutputScreenInteractive {
		return false
	}
	_, ok := chatBusyScreenInteractiveWriteCommands[spec.Command]
	return ok
}

// busyScreenCommandAllowed 是忙时副屏的统一判据（I7 单一实现）：只读文档白名单
// 或只读交互列表白名单或可写交互副屏白名单。策略映射
// （chatBusyPolicyFromRuntimeSpec）与宿主准入（runtimeCommandHost.submit）都必须
// 经本函数，不得各自维护一份。
func busyScreenCommandAllowed(spec runtimeCommandSpec) bool {
	return busyScreenCommandReadOnlyDocument(spec) ||
		busyScreenCommandReadOnlyInteractive(spec) ||
		busyScreenCommandInteractiveWrite(spec)
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
	// 忙时副屏交互窗口：Phase A/B 期间允许交互选择器在 run 活跃时开屏。
	releaseInteractive := markChatBusyInteractiveScreenActive(session)
	defer releaseInteractive()

	if !chatBusyScreenRunDispatch(session, line) {
		return false
	}
	recordChatPromptHistory(session, line)
	return true
}
