package commands

import (
	"context"
	"strings"
)

// P2-4b-3（方案 §3.7）：prompt 档的忙时确认载体。
//
// 复用既有 priority prompt 通道（与 bypass_permissions 确认同一条读取路径）：
// 忙时收到 prompt 档命令 → 先经该通道请用户确认 → 确认则 Phase A 执行，
// 拒绝则消费该行并给出拒绝提示，通道不可用/被中断则未占有（调用方回退入队）。

// chatBusyPromptAllowed 是忙时 prompt 档（确认门）允许清单：命令名 → 允许的
// 子命令 token 集合（"" 表示裸命令）。仅收录「确认后无二次交互、且生效域不触碰
// 安全/控制面」的命令：
//   - /queue clear：用户排队过多时最需要它，语义完全落在 InputQueue 自身；
//   - /stream|/fast|/reasoning on|off、/s、/normal：纯显示/外观开关（live）；
//   - /reasoning_effort set：显示档位写入（next-turn），值随命令给出、不再追问。
//   - 组 D（next-turn 会话/目标/工作区写入，核验确认无二次追问输入）：
//     /debug on|off、/hotkeys reload、/goal clear|pause|resume|complete、
//     /add-dir add|remove、/memory add|note|flush。
//
// 权限/安全类（/permission-mode set、/yolo、/trust grant、/grants revoke、
// /approval-reuse set）与控制面类（/supervision ack|resolve|control、
// /agents approve|deny|answer|send|followup|cleanup）保持 Deferred——不在运行中
// 改安全策略/控制面。next-call 类（/plan enter|exit|approve、/attach add|clear|remove）
// 会影响当前 turn 的下一次调用，live 类（/skills enable|disable）会热刷新函数面，
// 均不纳入。/routing on|off|reset|save 保持 Deferred：会话层写入虽复用
// refreshLocalRuntimeAfterSelection（next-turn 安全），但同一子命令可经
// `--to workspace|config` 指向持久层，而确认门按「命令名 + 首 token」匹配无法区分
// 层（`/routing on` 与 `/routing on --to config` 同 token），会误放行持久写入，故
// 整体保守拒绝（§4.5）。/title 需要标题参数（无参报错），不纳入。
//
// 结构化匹配（命令名 + 首个子命令 token）取代此前的裸字符串精确匹配：既支持
// 带参命令（/reasoning_effort set high），又避免裸前缀歧义（/s 不会命中 /status）。
var chatBusyPromptAllowed = map[string]map[string]struct{}{
	"/queue":            {"clear": {}},
	"/stream":           {"on": {}, "off": {}},
	"/fast":             {"on": {}, "off": {}},
	"/reasoning":        {"on": {}, "off": {}},
	"/reasoning_effort": {"set": {}},
	"/s":                {"": {}},
	"/normal":           {"": {}},
	// 组 D（next-turn，无二次追问输入）
	"/debug":   {"on": {}, "off": {}},
	"/hotkeys": {"reload": {}},
	"/goal":    {"clear": {}, "pause": {}, "resume": {}, "complete": {}},
	"/add-dir": {"add": {}, "remove": {}},
	"/memory":  {"add": {}, "note": {}, "flush": {}},
}

// busyPromptCommandWhitelisted 判定输入是否属于忙时 prompt 确认门允许清单。
// 调用方（chatBusyPolicyFromRuntimeSpec / runtimeCommandHost.submit）在解析出
// prompt 档 spec 之后调用，因此本函数只需做「命令名 + 子命令 token」匹配。
func busyPromptCommandWhitelisted(text string) bool {
	name, args, ok := chatBusyCommandNameAndArgs(normalizeQueuedInputLine(text))
	if !ok {
		return false
	}
	subs, ok := chatBusyPromptAllowed[canonicalRuntimeCommandName(name)]
	if !ok {
		return false
	}
	token := ""
	if len(args) > 0 {
		token = strings.ToLower(strings.TrimSpace(args[0]))
	}
	_, ok = subs[token]
	return ok
}

// chatBusyPromptConfirmOverride 仅测试注入（nil = 走真实 priority prompt 通道）。
var chatBusyPromptConfirmOverride func(session *ChatSession, question string, details []string) (confirmed bool, answered bool)

// chatBusyPromptChannelAvailable 是 prompt 载体能力门（fail-closed）：需要统一
// 渲染面 + 交互式会话，否则确认提示无处渲染，必须降级入队。抽成包级变量以便单测注入。
var chatBusyPromptChannelAvailable = func(session *ChatSession) bool {
	if session == nil || session.Interaction == nil {
		return false
	}
	if session.NoInteractive || session.JSONOutput {
		return false
	}
	return unifiedDirectInteractiveOutput(session)
}

// chatBusyPromptConfirm 经 priority prompt 通道读取一次确认。
//   - answered=false：通道不可用/读取被中断 → 调用方必须降级（不消费输入）；
//   - answered=true, confirmed=false：用户显式拒绝（非本地来源同样视为拒绝）。
func chatBusyPromptConfirm(session *ChatSession, question string, details []string) (bool, bool) {
	if chatBusyPromptConfirmOverride != nil {
		return chatBusyPromptConfirmOverride(session, question, details)
	}
	if session == nil || session.InputQueue == nil {
		return false, false
	}
	restoreInputMode := pushChatComposerInputMode(session, chatInputModeConfirmation)
	defer restoreInputMode()
	readPrompt, cleanupPrompt, transientPrompt := showChatRuntimePriorityPrompt(session, details, question)
	item, err := func() (chatQueuedInput, error) {
		endAction := beginChatTitleAction(session, "Busy Command Confirmation Required")
		defer endAction()
		return chatInteractiveReadPriorityItemWithPrompt(session, context.Background(), readPrompt)
	}()
	cleanupPrompt()
	if err != nil {
		return false, false
	}
	text := strings.TrimSpace(normalizeQueuedInputLine(item.Text))
	if transientPrompt {
		renderChatRuntimePriorityPromptTranscript(session, details, question, text)
	}
	if !chatInputSourceIsLocalTerminal(item.Source) {
		// INV-8 同款：确认手势必须来自终端前的人，Web/注入行不能充当确认。
		return false, true
	}
	return chatBusyPromptAffirmative(text), true
}

// chatBusyPromptAffirmative 解析确认输入；只有明确肯定词才算确认（默认拒绝）。
func chatBusyPromptAffirmative(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "y", "yes", "是", "确认", "ok", "确定":
		return true
	}
	return false
}

// runBusyPromptCommand 是 prompt 档的执行原语，返回 (occupied, executed)：
//   - (false,false)：未占有（通道不可用/中断/执行入口拒绝）→ 调用方回退入队；
//   - (true,false)：已消费但未执行（用户显式拒绝）→ 审计 rejected，不再入队；
//   - (true,true)：已执行 → 审计 executed。
//
// 锁边界：确认门与 Phase A（解析 + 渲染）在 commandMu 内；执行步骤解锁后
// 才应用 Phase B 效应（chatBusyScreenRunDispatch 承担解锁）。
func runBusyPromptCommand(session *ChatSession, line string) (occupied bool, executed bool) {
	if session == nil || session.Interaction == nil {
		return false, false
	}
	if !chatBusyPromptChannelAvailable(session) {
		notifyBusyCommandDegraded(session, line, "当前终端不支持忙时确认通道")
		return false, false
	}
	if !chatBusyCommandArbitrationAllows(session) {
		return false, false
	}
	if !session.commandMu.TryLock() {
		session.Interaction.RenderLocalSupplement("[input] 命令通道正忙，该命令已排队，将在当前回合结束后执行。")
		return false, false
	}
	// 从这里起锁由各出口手工释放：执行步骤 chatBusyScreenRunDispatch 会在
	// Phase A 之后解锁，以便在锁外应用 Phase B 效应；此处不得 defer Unlock。

	releaseModal := beginChatInputShadowLevel(session, chatInputOwnerModal)
	defer releaseModal()

	question := "[确认] 忙时执行 " + line + "？(y/N)"
	details := []string{
		"[确认] 命令将在当前回合继续运行的同时执行（Phase A，无 Phase B 效应）。",
		"[确认] 输入 y/yes/确认 执行；其他输入取消该行。",
	}
	confirmed, answered := chatBusyPromptConfirm(session, question, details)
	if !answered {
		// 通道不可用或读取被中断：不消费，回退入队（不丢输入）。
		session.commandMu.Unlock()
		return false, false
	}
	if !confirmed {
		// 用户显式拒绝：消费该行并给出提示，避免反复弹确认。
		if session.Interaction != nil {
			session.Interaction.RenderLocalSupplement("[input] 已取消忙时执行：" + line)
		}
		session.commandMu.Unlock()
		return true, false
	}
	// 忙时副屏交互窗口：Phase A/B 期间允许交互选择器在 run 活跃时开屏。
	releaseInteractive := markChatBusyInteractiveScreenActive(session)
	defer releaseInteractive()
	if !chatBusyScreenRunDispatch(session, line) {
		return false, false
	}
	recordChatPromptHistory(session, line)
	return true, true
}
