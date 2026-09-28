package commands

import (
	"strings"
	"time"
)

// P2-3（方案 §3.8.4）：统一运行时交互宿主。忙时 capture 通道的唯一入口，
// 串起「注册表解析 → 三级开关 → 安全门 → 模式分发 → 生效域守卫 → 审计事件」。
//
// 本步骤落地的模式：inline（复用 runBusyInlineCommand）、queue（降级入队）、
// block（拒绝 + 提示）；screen/prompt 的载体属 P2-4，落地前一律降级 queue
//（T6：裸 `/model` 等仍 deferred）。生效域 session/process 在宿主层不可达（T32）。

type runtimeHostOutcome struct {
	Occupied   bool // true = 输入已被占有（执行/拒绝），调用方不得入队
	Registered bool
	Command    string
	Mode       runtimeInteractionMode
	Effect     runtimeEffectScope
	Result     string // executed | rejected | queued | degraded
	Duration   time.Duration
}

type runtimeCommandHost struct {
	session *ChatSession
}

func runtimeCommandHostFor(session *ChatSession) *runtimeCommandHost {
	return &runtimeCommandHost{session: session}
}

// SubmitBusy 是忙时 capture 循环的宿主入口。返回 true 表示输入已被占有
// （已执行或已拒绝并给出提示）；false 表示调用方必须把输入回退入队（不丢输入）。
func (h *runtimeCommandHost) SubmitBusy(line string) bool {
	if h == nil || h.session == nil || h.session.Interaction == nil {
		return false
	}
	start := time.Now()
	outcome := h.submit(line)
	outcome.Duration = time.Since(start)
	h.publishAudit(outcome)
	return outcome.Occupied
}

func (h *runtimeCommandHost) submit(line string) runtimeHostOutcome {
	spec, registered := resolveRuntimeCommandSpec(line)
	outcome := runtimeHostOutcome{
		Registered: registered,
		Command:    spec.Command,
		Mode:       spec.Mode,
		Effect:     spec.Effect,
	}
	if !registered {
		// INV-6：未登记命令 / 未知子命令 → queue（不丢输入）。
		outcome.Mode = runtimeModeQueue
		outcome.Result = "queued"
		return outcome
	}

	// 三级开关（§3.8.3）；P1 灰度未开启时一律排队（总闸优先，保证现行为等价）。
	effective := runtimeCommandWithSwitch(spec, runtimeSwitchTableFromEnv())
	if !chatBusyCommandEnabled() {
		effective.Mode = runtimeModeQueue
	}
	outcome.Mode = effective.Mode
	outcome.Effect = effective.Effect

	// T32：session/process 生效域在宿主层不可达（静态断言之外的第二道防线）。
	// 唯一的合法组合是 block+process（`/exit`），它只做拒绝、不执行生效域动作。
	if effective.Mode != runtimeModeBlock && !runtimeHostEffectReachable(effective.Effect) {
		outcome.Result = "degraded"
		return outcome
	}

	switch effective.Mode {
	case runtimeModeBlock:
		h.renderBlockNotice(effective)
		outcome.Result = "rejected"
		outcome.Occupied = true
		return outcome
	case runtimeModeInline:
		if !runBusyInlineCommand(h.session, line) {
			outcome.Result = "degraded"
			return outcome
		}
		// T32：非 read 生效域的执行必须伴随生效提示（Notice 模板）。
		if effective.Effect != runtimeEffectRead {
			if notice := strings.TrimSpace(effective.Notice); notice != "" {
				h.session.Interaction.RenderLocalSupplement(notice)
			}
		}
		outcome.Result = "executed"
		outcome.Occupied = true
		return outcome
	case runtimeModeScreen:
		// P2-4b：首批白名单 S 档走副屏通道；其余 screen 档（picker/写入类）
		// 仍降级入队，待各自确认流在 P2-5/P3 落地。降级必须显式提示（INV-10）。
		if !busyScreenCommandFirstBatch(effective) {
			notifyBusyCommandDegraded(h.session, line, "该命令尚未开通忙时副屏通道")
			outcome.Result = "degraded"
			return outcome
		}
		if !runBusyScreenCommand(h.session, line) {
			outcome.Result = "degraded"
			return outcome
		}
		outcome.Result = "executed"
		outcome.Occupied = true
		return outcome
	case runtimeModePrompt:
		// P2-4b-3：首批 prompt 档经忙时确认门执行；未确认/通道不可用按契约
		// 消费或降级。非首批 prompt 命令仍 deferred（显式提示，INV-10）。
		if !busyPromptCommandWhitelisted(line) {
			notifyBusyCommandDegraded(h.session, line, "该命令尚未开通忙时确认通道")
			outcome.Result = "degraded"
			return outcome
		}
		occupied, executed := runBusyPromptCommand(h.session, line)
		if !occupied {
			outcome.Result = "degraded"
			return outcome
		}
		outcome.Occupied = true
		if !executed {
			outcome.Result = "rejected"
			return outcome
		}
		outcome.Result = "executed"
		return outcome
	default:
		// 其余 screen/prompt 命令与 queue 档见后续增量；queue 本就要入队。
		outcome.Result = "degraded"
		return outcome
	}
}

// runtimeHostEffectReachable 判定生效域是否允许在运行时执行：session/process 拒绝。
func runtimeHostEffectReachable(effect runtimeEffectScope) bool {
	return effect != runtimeEffectSession && effect != runtimeEffectProcess
}

func (h *runtimeCommandHost) renderBlockNotice(spec runtimeCommandSpec) {
	notice := strings.TrimSpace(spec.Notice)
	if notice == "" {
		notice = "该命令在忙碌回合中不可执行，请等待回合结束后重试。"
	}
	if h.session != nil && h.session.Interaction != nil {
		h.session.Interaction.RenderLocalSupplement(notice)
	}
}

// publishAudit 发布统一审计事件（T33）：命令/模式/生效域/结果/耗时。
func (h *runtimeCommandHost) publishAudit(outcome runtimeHostOutcome) {
	if h == nil || h.session == nil {
		return
	}
	publishLocalChatDiagnosticEvent(h.session, chatEventRuntimeInteraction, map[string]interface{}{
		"command":     outcome.Command,
		"registered":  outcome.Registered,
		"mode":        outcome.Mode.String(),
		"effect":      outcome.Effect.String(),
		"result":      outcome.Result,
		"occupied":    outcome.Occupied,
		"duration_ms": outcome.Duration.Milliseconds(),
	})
}
