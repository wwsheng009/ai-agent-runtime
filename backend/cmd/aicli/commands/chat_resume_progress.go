package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// 恢复进度在 composer 动态栏上的展示通道。
//
// 背景：会话恢复（启动 `aicli resume` / `chat --resume` / `--session` 的较早页
// 后台补齐、会话内 `/resume`、`/load`、选择器恢复）在首帧之后仍可能持续数秒到
// 数分钟。恢复期间不能写 transcript（历史信息流只承载会话语义，进度行会污染
// 正文），也不能写裸终端字节（会覆盖底部保留区，见 §8.5 stderr/stdout 契约）。
// 因此统一走动态栏：单行、临时、整行覆盖活动行、结束即清除——与
// ShowDiagnosticNotice 同一条渲染路径（repaintStatusModelsLocked）。
const (
	chatResumeProgressPhaseLoad    = "加载会话"
	chatResumeProgressPhaseRestore = "恢复历史会话"
	// 装载收尾段：backfill 完成后的全量 seed + 原生 scrollback 重投递是异步的，
	// renderFinal 只登记重投递，真正的字节要在此后几十个批次里写向终端，这段
	// 时间用户仍处于「界面在装载」状态。
	chatResumeProgressPhaseSettle = "装载历史"
)

// ChatResumeProgress 描述一次会话恢复的进度快照。
//
//   - Phase 是展示用短语（"加载会话" / "恢复历史会话"），不做语义判断；
//   - Done/Total 是已加载 / 预计的 canonical 消息条数；Total<=0 表示规模未知
//     （只显示已加载条数），Done<=0 且 Total<=0 表示未知进度（只显示阶段名）。
//
// 进度行只存在于动态栏，绝不进入 transcript。
type ChatResumeProgress struct {
	Phase string
	Done  int
	Total int
}

// chatResumeProgressState 是 coordinator 持有的进度状态。恢复阶段 started 在
// 多次进度更新之间保持不变，让秒表连续走动；装载收尾段没有秒表（见
// buildChatResumeProgressModelLocked），started 只作为有界兜底窗口的起点。
type chatResumeProgressState struct {
	phase string
	done  int
	total int
	// started 是当前阶段的窗口起点：装载收尾段的有界兜底窗口从这里算。
	started    time.Time
	loadSettle bool
}

func (c *chatInteractionCoordinator) resumeProgressActiveLocked() bool {
	return c != nil && c.resumeProgress != nil
}

// resumeProgressYieldsToLiveActivityLocked 报告是否有前台活动必须优先于恢复
// 进度。恢复进度是后台任务，不能覆盖正在进行的用户回合状态行（Analyzing /
// Running tool / Retrying …）。注意这里刻意不把 actor-not-ready 的兜底 Waiting
// 算作前台活动：启动恢复阶段 actor 可能尚未就绪，而恢复进度正是要在那个窗口
// 可见；真实前台 Waiting 由 waitingActive 标志覆盖。
func (c *chatInteractionCoordinator) resumeProgressYieldsToLiveActivityLocked() bool {
	if c == nil {
		return false
	}
	if c.streamingActive || c.thinkingActive || c.reasoningActive || c.waitingActive {
		return true
	}
	// 任何输入面接管（审批 / 回答 / 选择 / 确认 / 密钥 / 面板）都必须优先于
	// 后台恢复进度：这些模式的行由 chatDynamicStatusAction 单独渲染。
	if normalizeChatInputMode(c.inputMode) != chatInputModeChat {
		return true
	}
	switch c.surfaceStatus.kind {
	case chatSurfaceStatusStreaming, chatSurfaceStatusThinking, chatSurfaceStatusRetrying,
		chatSurfaceStatusTool, chatSurfaceStatusPlanning,
		chatSurfaceStatusApproval, chatSurfaceStatusAnswer, chatSurfaceStatusStopping:
		return true
	default:
		return false
	}
}

// ShowChatResumeProgress 更新动态栏上的恢复进度。返回 false 表示当前没有可
// 接收的交互式会话（非交互 / JSON / 已关闭），调用方无需特殊处理。
//
// 可被后台 goroutine 调用：内部只持 c.mu 更新缓存模型并投递 surface action，
// 不产生裸终端字节；连续更新在同一行上 latest-wins，不会堆叠。
func (c *chatInteractionCoordinator) ShowChatResumeProgress(progress ChatResumeProgress) bool {
	if c == nil {
		return false
	}
	phase := strings.Join(strings.Fields(progress.Phase), " ")
	if phase == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return false
	}
	state := c.resumeProgress
	if state == nil {
		state = &chatResumeProgressState{started: time.Now()}
		c.resumeProgress = state
	}
	state.phase = phase
	state.done = progress.Done
	state.total = progress.Total
	c.repaintStatusModelsLocked()
	return true
}

// ClearChatResumeProgress 结束进度展示：动态栏回到当前活动行/空闲态。未在
// 展示时是 no-op，因此调用方可以无条件清除（含错误路径）。
func (c *chatInteractionCoordinator) ClearChatResumeProgress() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.resumeProgress == nil || c.shutdown {
		return
	}
	c.resumeProgress = nil
	c.repaintStatusModelsLocked()
	// 没有真实 run 时，动态栏的唯一 owner 是恢复进度；清除后停止秒表 intent，
	// 否则空 tick 会每秒重绘一次空闲状态行。
	if c.dynamicStatusStarted.IsZero() {
		c.stopDynamicStatusTickLocked()
	}
}

// applyResumeProgressLocked 让恢复进度在有前台活动时让位，否则整行替换为进度
// 文本。它挂在 appendStatusHintsLocked 链上，因此状态迁移重绘（
// repaintStatusModelsLocked）与每秒 tick（refreshDynamicStatusTick）共用同一条
// 构建路径，进度行不会在 tick 中闪回 idle。
func (c *chatInteractionCoordinator) applyResumeProgressLocked(model *style.StatusLineModel) *style.StatusLineModel {
	if !c.resumeProgressActiveLocked() {
		return model
	}
	if c.resumeProgressYieldsToLiveActivityLocked() {
		return model
	}
	// 装载收尾段（backfill 完成后的全量 seed/重投递）自带一个有界窗口：它只属于
	// 启动装载，绝不能跨越用户第一次真实活动或无限期停留。到期后就地撤掉进度
	// 状态（此处持有 c.mu，直接清字段而不是调用 ClearChatResumeProgress，避免
	// 重入死锁），让动态行回到常规活动行/空闲态。
	if c.resumeProgress != nil && c.resumeProgress.loadSettle {
		if time.Since(c.resumeProgress.started) > chatHistoryLoadSettleLimit {
			c.resumeProgress = nil
			if c.dynamicStatusStarted.IsZero() {
				c.stopDynamicStatusTickLocked()
			}
			return model
		}
	}
	progressModel := c.buildChatResumeProgressModelLocked(ui.GetTerminalWidth(), time.Now())
	if progressModel == nil {
		return model
	}
	return progressModel
}

// chatHistoryLoadSettleLimit 是「装载历史」行的最长滞留时间。收尾全量 seed 是
// ready 之后最贵的一段（大会话可达数十 MB 的 wire 字节），进度行必须覆盖它；
// 同时这里保持有界，避免投递失败时状态行永久停在装载阶段。
const chatHistoryLoadSettleLimit = 30 * time.Second

// MarkChatHistoryLoadPending 把动态行交给「装载历史」。backfill 完成后收尾的
// 全量 seed + 原生 scrollback 重投递是异步的：renderFinal 只是登记重投递，真正
// 的字节要在此后几十个批次里才写向终端。此前收尾序列立刻清除进度，用户看到
// 的正是「进度行在最长的阶段消失、动态行整段空白」。
func (c *chatInteractionCoordinator) MarkChatHistoryLoadPending() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown || c.resumeProgressYieldsToLiveActivityLocked() {
		return
	}
	c.resumeProgress = &chatResumeProgressState{
		phase:      chatResumeProgressPhaseSettle,
		started:    time.Now(),
		loadSettle: true,
	}
	c.repaintStatusModelsLocked()
}

// markChatHistoryLoadPending 是非交互安全入口（plain/JSON 会话静默跳过）。
func markChatHistoryLoadPending(session *ChatSession) {
	if coord := chatResumeProgressTarget(session); coord != nil {
		coord.MarkChatHistoryLoadPending()
	}
}

// buildChatResumeProgressModelLocked 把进度状态渲染成动态栏模型：
// "◦ 恢复历史会话 320/1200 (12s)"，宽度不足时压缩阶段文本。
func (c *chatInteractionCoordinator) buildChatResumeProgressModelLocked(width int, now time.Time) *style.StatusLineModel {
	state := c.resumeProgress
	if state == nil {
		return nil
	}
	if width <= 0 {
		width = 80
	}
	body := state.phase
	switch {
	case state.total > 0:
		done := state.done
		if done < 0 {
			done = 0
		}
		body = fmt.Sprintf("%s %d/%d", state.phase, done, state.total)
	case state.done > 0:
		body = fmt.Sprintf("%s 已加载 %d", state.phase, state.done)
	default:
		body = state.phase + "…"
	}
	// 装载收尾段不显示秒表：它起于加载完成之后（"加载完成才开始计时"没有
	// 意义），且重投递期间动态栏 tick 会被投递压力抑制，数字只会冻在某个
	// 值上误导用户。收尾段的进度语义由"何时撤行"表达（落地即撤，见
	// settleChatHistoryLoadWhenDelivered），而不是由一个不会走的秒表表达。
	suffix := ""
	if !state.loadSettle {
		elapsed := time.Duration(0)
		if !state.started.IsZero() && now.After(state.started) {
			elapsed = now.Sub(state.started)
		}
		suffix = " (" + formatChatDynamicStatusElapsed(elapsed) + ")"
	}
	if budget := width - ui.DisplayWidth("◦ "+suffix); budget >= 4 {
		body = compactStatusValue(body, budget)
	}
	return &style.StatusLineModel{
		State:     chatStatusRunState(style.RoleProgress),
		StateText: "◦ " + body + suffix,
		StateRole: style.RoleProgress,
	}
}

// chatResumeProgressTarget 返回可以接收恢复进度的交互式 coordinator。非交互 /
// JSON 会话返回 nil：这些平面的输出契约不允许出现瞬时状态行。
func chatResumeProgressTarget(session *ChatSession) *chatInteractionCoordinator {
	if session == nil || session.Interaction == nil || session.NoInteractive || session.JSONOutput {
		return nil
	}
	return session.Interaction
}

// showChatResumeProgress 是恢复路径的统一入口（best-effort：没有交互式动态栏
// 时静默跳过，plain / JSON 输出保持不变）。
func showChatResumeProgress(session *ChatSession, phase string, done, total int) {
	if coord := chatResumeProgressTarget(session); coord != nil {
		coord.ShowChatResumeProgress(ChatResumeProgress{Phase: phase, Done: done, Total: total})
	}
}

// clearChatResumeProgress 结束恢复进度展示（best-effort，错误路径同样适用）。
func clearChatResumeProgress(session *ChatSession) {
	if coord := chatResumeProgressTarget(session); coord != nil {
		coord.ClearChatResumeProgress()
	}
}

// chatHistoryLoadSettlePoll / chatHistoryLoadSettleQuiet 定义「装载历史」收尾段的
// 落地探测节奏：账本必须连续多次采样都为空才判定重投递完成。批次之间会有瞬时
// 间隙（上一批已 ack、下一批尚未入账），单次空采样不足以判定落地。
const (
	chatHistoryLoadSettlePoll  = 400 * time.Millisecond
	chatHistoryLoadSettleQuiet = 2
)

// historyReplayDeliverySettled 报告收尾全量重投递是否已真正落地：scrollback
// 授权已被消费、投影已知、队列未冻结且没有未完成提交。只读 controller 的廉价
// 诊断投影（不克隆 ledger）；调用方不得持有 coordinator.mu，本函数会取 controller
// 的锁，与 actor 内回调互锁。
func (c *chatInteractionCoordinator) historyReplayDeliverySettled() bool {
	if c == nil || c.uiActor == nil {
		return true
	}
	diagnostics := c.uiActor.HistoryEffectDiagnostics()
	if diagnostics.ScrollbackReplayArmed || diagnostics.ProjectionUnknown || diagnostics.Frozen {
		return false
	}
	return diagnostics.Summary.Pending == 0 && diagnostics.Summary.InFlight == 0
}

// resumeProgressLoadSettleActive 报告「装载历史」收尾行是否仍需要落地探测。
func (c *chatInteractionCoordinator) resumeProgressLoadSettleActive() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resumeProgress != nil && c.resumeProgress.loadSettle
}

// settleChatHistoryLoadWhenDelivered 在收尾重投递真正落地后立刻撤掉「装载历史」
// 行。该行只是启动装载的过渡提示：字节投递一旦完成就必须消失，而不是留到 30s
// 有界窗口（chatHistoryLoadSettleLimit 仍然保留为投递失败/挂起时的最终兜底）。
// 探测 goroutine 有界（≤兜底窗口），收尾行提前被前台活动或兜底窗口清掉时自行退出。
func settleChatHistoryLoadWhenDelivered(session *ChatSession) {
	coordinator := chatResumeProgressTarget(session)
	if coordinator == nil || coordinator.uiActor == nil {
		return
	}
	if !coordinator.resumeProgressLoadSettleActive() {
		return
	}
	go func() {
		deadline := time.Now().Add(chatHistoryLoadSettleLimit)
		quiet := 0
		for time.Now().Before(deadline) {
			if !coordinator.resumeProgressLoadSettleActive() {
				return
			}
			if coordinator.historyReplayDeliverySettled() {
				quiet++
				if quiet >= chatHistoryLoadSettleQuiet {
					coordinator.ClearChatResumeProgress()
					return
				}
			} else {
				quiet = 0
			}
			time.Sleep(chatHistoryLoadSettlePoll)
		}
	}()
}

// resumeSummaryNoticeTTL 是恢复「结束」事件在动态栏上的驻留时长。比后台诊断
// 略长：它是本轮恢复的结论行（轮数/条数/整段耗时），需要让用户来得及看到；
// 到期后由同一条瞬时通道清除，动态栏回到常规活动行/空闲态。
var resumeSummaryNoticeTTL = 10 * time.Second

// formatResumeSummaryNoticeLine 把恢复汇总压成动态栏可用的单行文本：折叠换行、
// 按终端宽度紧凑截断。与后台诊断不同，这里不加 "⚠ " 前缀——恢复完成不是告警。
func formatResumeSummaryNoticeLine(line string, width int) string {
	flat := strings.Join(strings.Fields(line), " ")
	if flat == "" {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	return compactStatusValue(flat, width)
}

// queueResumeSummaryNotice 把恢复汇总排队为动态栏上的「结束」事件。
//
// 恢复在动态栏上的事件顺序：开始（恢复历史会话… (0s)）→ 进度（N/M）→ 装载
// 历史（收尾重投递）→ 结束（本汇总）。汇总通常与恢复流程同时产生（历史装载
// 刚起步），此时进度行仍在动态栏上，因此必须等进度行全部退场后再投递：进度
// 行与瞬时提示都会整行替换动态栏，抢跑只会让汇总被随后的进度重绘覆盖。
// 没有任何进度行的快速路径则立即显现。
//
// 返回 false 表示当前没有可用的动态栏（plain/JSON/非交互/未挂 surface），
// 调用方应回退到原有的直写通道。
func (c *chatInteractionCoordinator) queueResumeSummaryNotice(line string) bool {
	if c == nil {
		return false
	}
	text := formatResumeSummaryNoticeLine(line, ui.GetTerminalWidth())
	if text == "" {
		return false
	}
	c.mu.Lock()
	if c.shutdown || c.session == nil || c.session.NoInteractive || c.session.JSONOutput || c.surface == nil {
		c.mu.Unlock()
		return false
	}
	// 耗时从整段恢复的起点算起：进度状态（若在）的 started 就是这次恢复的
	// 秒表起点；取不到则退化为排队时刻，绝不从"加载结束"重新起算。
	started := time.Now()
	if c.resumeProgress != nil && !c.resumeProgress.started.IsZero() {
		started = c.resumeProgress.started
	}
	c.resumeSummaryNotice = text
	c.resumeSummaryStartedAt = started
	c.mu.Unlock()
	// latest-wins：连续恢复只保留最后一份汇总，旧 watcher 与新 watcher 都只
	// 等待"进度行退场"这一条件，投递时读取队列里的最新文本。
	go c.showResumeSummaryNoticeWhenProgressSettled()
	return true
}

// showResumeSummaryNoticeWhenProgressSettled 等待进度行退场后投递恢复汇总。
// 有界且自清理：协调器关闭或队列被消费后立即退出；单次恢复最多一个 watcher
// （latest-wins 允许多余 watcher 无害退出）。
func (c *chatInteractionCoordinator) showResumeSummaryNoticeWhenProgressSettled() {
	for {
		c.mu.Lock()
		if c.shutdown || c.resumeSummaryNotice == "" {
			c.mu.Unlock()
			return
		}
		active := c.resumeProgress != nil
		c.mu.Unlock()
		if !active {
			break
		}
		time.Sleep(chatHistoryLoadSettlePoll)
	}
	c.mu.Lock()
	text := c.resumeSummaryNotice
	started := c.resumeSummaryStartedAt
	if text == "" {
		c.mu.Unlock()
		return
	}
	c.resumeSummaryNotice = ""
	c.resumeSummaryStartedAt = time.Time{}
	c.mu.Unlock()
	if !started.IsZero() {
		text += " · 耗时" + formatChatDynamicStatusElapsed(time.Since(started))
	}
	c.ShowResumeSummaryNotice(text)
}
