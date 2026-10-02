package commands

// chat_startup_progress.go — 启动期耗时事件在动态栏上的展示通道。
//
// 背景：TUI 启动关键路径上原本有三段同步的「技能/工具发现」扫描
// （runtimebootstrap.NewManager / tools.NewDefaultManagerWithRuntimeConfig /
// ListTools），实测合计占首帧前的绝大部分时间。改为「先渲染 UI、再异步装载」
// 之后，这些成本不再阻塞首帧，但用户仍需要知道后台在做什么、以及已经等了多久
// ——否则启动后的十几秒里动态栏与首帧内容完全一样，看起来像卡死。
//
// 语义与恢复进度（chat_resume_progress.go）一致，但归属不同：
//   - resumeProgress 属于「进入会话之后」的历史/会话恢复；
//   - startupProgress 属于「进入会话之前」的后台能力装载（工具面 / skills /
//     runtime host / supervision 平面）。
//
// 两者共用同一条渲染路径（appendStatusHintsLocked → 整行替换动态栏）与同一套
// 秒表节拍（scheduleDynamicStatusTickLocked），因此不会产生第二套 tick 机制。

import (
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// chatStartupProgressPhase* 是启动后台装载的展示阶段短语。短语面向用户，不做
// 语义判断；与 chatResumeProgressPhase* 一样只在动态栏上出现。
const (
	chatStartupProgressPhaseTools   = "加载工具"
	chatStartupProgressPhaseSkills  = "加载 Skills"
	chatStartupProgressPhaseRuntime = "加载运行时"
)

// chatStartupProgressState 是 coordinator 持有的启动装载进度状态。
//
// started 在同一阶段的多次推进之间保持不变，让秒表连续走动；切阶段时重置为
// 该阶段起点，因此展示的秒表始终是「当前阶段已耗时」而不是「启动总耗时」。
type chatStartupProgressState struct {
	phase   string
	started time.Time
}

func (c *chatInteractionCoordinator) startupProgressActiveLocked() bool {
	return c != nil && c.startupProgress != nil
}

// ShowChatStartupProgress 更新动态栏上的启动装载进度（阶段短语 + 秒表）。
// 返回 false 表示当前没有可接收的交互式会话（非交互 / JSON / 已关闭）。
//
// 可被后台 goroutine 调用：内部只持 c.mu 更新缓存模型并投递 surface action，
// 不产生裸终端字节；连续更新在同一行上 latest-wins，不会堆叠。
func (c *chatInteractionCoordinator) ShowChatStartupProgress(phase string) bool {
	if c == nil {
		return false
	}
	phase = strings.Join(strings.Fields(phase), " ")
	if phase == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown || c.session == nil || c.session.NoInteractive || c.session.JSONOutput {
		return false
	}
	// 同一阶段重复推进时保留原 started，秒表不跳变；换阶段才重新起算。
	if c.startupProgress == nil || c.startupProgress.phase != phase {
		c.startupProgress = &chatStartupProgressState{phase: phase, started: time.Now()}
	}
	c.repaintStatusModelsLocked()
	return true
}

// advanceChatStartupProgress 在**已经在展示**启动进度的前提下切换阶段
// 短语，并把秒表从新阶段起点重新起算。
//
// 与 ShowChatStartupProgress 的关键区别：它不会在尚未展示时凭空创建
// 进度。发现阶段（discoverChatCapabilities）同时被两条路径调用——
// 异步路径（runChatCapabilitiesLoad 先 Show 再进发现）和同步路径
// （initializeChatCapabilities 直接进发现）。同步路径没有后台装载，
// 若这里无条件 Show 会在动态栏上冒出一条没人清的「加载 Skills」。
// 因此阶段推进只在异步路径已开的进度上生效。
//
// 可被后台 goroutine 调用：发现阶段在后台跑，内部只持 c.mu 更新
// 缓存模型并投递 surface action，不产生裸终端字节。
func (c *chatInteractionCoordinator) advanceChatStartupProgress(phase string) bool {
	if c == nil {
		return false
	}
	phase = strings.Join(strings.Fields(phase), " ")
	if phase == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.shutdown || c.session == nil || c.startupProgress == nil {
		return false
	}
	if c.startupProgress.phase == phase {
		return true
	}
	c.startupProgress.phase = phase
	c.startupProgress.started = time.Now()
	c.repaintStatusModelsLocked()
	return true
}

// ClearChatStartupProgress 结束启动装载展示：动态栏回到当前活动行/空闲态。
// 未在展示时是 no-op，因此调用方可以无条件清除（含错误路径与提前首帧的路径）。
func (c *chatInteractionCoordinator) ClearChatStartupProgress() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.startupProgress == nil || c.shutdown {
		return
	}
	c.startupProgress = nil
	c.repaintStatusModelsLocked()
	// 没有真实 run、也没有恢复进度时，动态栏的唯一 owner 就是启动装载进度；
	// 清除后必须停掉秒表 intent，否则空 tick 会每秒重绘一次空闲状态行。
	if c.dynamicStatusStarted.IsZero() && !c.resumeProgressActiveLocked() {
		c.stopDynamicStatusTickLocked()
	}
}

// showChatStartupProgress / clearChatStartupProgress 是非交互安全入口：
// plain / JSON 会话没有动态栏，静默跳过。
func showChatStartupProgress(session *ChatSession, phase string) {
	if session == nil || session.Interaction == nil {
		return
	}
	session.Interaction.ShowChatStartupProgress(phase)
}

func clearChatStartupProgress(session *ChatSession) {
	if session == nil || session.Interaction == nil {
		return
	}
	session.Interaction.ClearChatStartupProgress()
}

// advanceChatStartupProgress 是非交互安全入口：plain / JSON 会话
// 没有动态栏，静默跳过。
func advanceChatStartupProgress(session *ChatSession, phase string) {
	if session == nil || session.Interaction == nil {
		return
	}
	session.Interaction.advanceChatStartupProgress(phase)
}

// applyStartupProgressLocked 让启动装载进度在有前台活动时让位，否则整行替换为
// 进度文本。它挂在 appendStatusHintsLocked 链上，因此状态迁移重绘
// （repaintStatusModelsLocked）与每秒 tick（refreshDynamicStatusTick）共用同一条
// 构建路径，进度行不会在 tick 中闪回 idle。
//
// 让位规则与恢复进度一致：用户已经在跑 turn / 等待审批 / 输入面板接管时，启动
// 后台装载必须让出动态栏（见 resumeProgressYieldsToLiveActivityLocked）。
func (c *chatInteractionCoordinator) applyStartupProgressLocked(model *style.StatusLineModel) *style.StatusLineModel {
	if !c.startupProgressActiveLocked() {
		return model
	}
	if c.resumeProgressYieldsToLiveActivityLocked() {
		return model
	}
	progressModel := c.buildChatStartupProgressModelLocked(ui.GetTerminalWidth(), time.Now())
	if progressModel == nil {
		return model
	}
	return progressModel
}

// buildChatStartupProgressModelLocked 把启动装载状态渲染成动态栏模型：
// "◦ 加载 Skills (8s)"，宽度不足时压缩阶段文本。
func (c *chatInteractionCoordinator) buildChatStartupProgressModelLocked(width int, now time.Time) *style.StatusLineModel {
	state := c.startupProgress
	if state == nil {
		return nil
	}
	if width <= 0 {
		width = 80
	}
	body := state.phase + "…"
	elapsed := time.Duration(0)
	if !state.started.IsZero() && now.After(state.started) {
		elapsed = now.Sub(state.started)
	}
	suffix := " (" + formatChatDynamicStatusElapsed(elapsed) + ")"
	if budget := width - ui.DisplayWidth("◦ "+suffix); budget >= 4 {
		body = compactStatusValue(body, budget)
	}
	return &style.StatusLineModel{
		State:     chatStatusRunState(style.RoleProgress),
		StateText: "◦ " + body + suffix,
		StateRole: style.RoleProgress,
	}
}
