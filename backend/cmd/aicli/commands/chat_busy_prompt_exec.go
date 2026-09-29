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

// chatBusyPromptFirstBatchCommands 是首批 prompt 档白名单（命令名前缀，已归一）。
// 首批只放行 `/queue clear`：用户排队过多时最需要它，且语义完全落在 InputQueue
// 自身，不触碰 actor/终端所有权。
var chatBusyPromptFirstBatchCommands = []string{"/queue clear"}

// busyPromptCommandWhitelisted 判定输入是否属于首批 prompt 档白名单。
func busyPromptCommandWhitelisted(text string) bool {
	// 折叠内部空白：`/queue   clear` 与 `/queue clear` 是同一个命令。
	normalized := strings.ToLower(strings.Join(strings.Fields(normalizeQueuedInputLine(text)), " "))
	if normalized == "" {
		return false
	}
	for _, prefix := range chatBusyPromptFirstBatchCommands {
		if normalized == prefix {
			return true
		}
	}
	return false
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
	if !chatBusyScreenRunDispatch(session, line) {
		return false, false
	}
	recordChatPromptHistory(session, line)
	return true, true
}
