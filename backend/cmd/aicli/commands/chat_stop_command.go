package commands

// /stop 是「可键入的回合中断入口」（2026-10-18 入口审计 R3）。
//
// 动机：Esc 在部分载体上不可达——Windows console 下依赖 KeyHandler/composer 的
// 分工、PTY 管道（winpty/SSH）只有裸 Esc 探测、Web 微客户端 Esc 依赖焦点。
// /stop 提供与 Esc 完全等价的键入式入口：内部只调用会话级唯一决策点
// handleChatEscapeInterruptRequest，不新增任何中断语义（保留草稿与排队输入、
// 重复请求节流提示、stalled cleanup 重试都沿用同一实现）。
//
// 忙时路由由注册表声明（chat_runtime_command_registry.go 的 "/stop" 条目，
// runtimeModeInline）驱动：capture/主循环忙时通道经 runtimeCommandHost 立即执行。

// stopCommandText 生成 /stop 的用户可见回执；有在途回合时执行中断并返回。
func stopCommandText(session *ChatSession) string {
	if session == nil {
		return "错误: 当前没有活动会话"
	}
	if !chatStopCommandHasTarget(session) {
		return "当前没有在途回合，/stop 未触发中断；空闲时 Esc 用于用户回合回溯。"
	}
	handleChatEscapeInterruptRequest(session)
	return "已请求停止当前回合（与 Esc 等价：保留草稿与排队输入）。"
}

// executeStructuredStopCommand 是统一渲染面的命令单元入口。
func executeStructuredStopCommand(session *ChatSession) CommandResult {
	return commandTextResult(stopCommandText(session))
}

// chatStopCommandHasTarget 报告 /stop 是否存在可中断的在途回合。判定顺序与
// “esc to interrupt” 承诺同源（消费者的真实存在性），并对远程/actor 回合与
// 无 UI 宿主保留兜底，避免把“正在运行的远程回合”误判为空闲：
//   - 回合级 ESC 消费者（本地 TUI 回合 / wake / parked）→ 有目标；
//   - 控制台 busy capture 活跃（consoleInputCaptureActive，不含 Web/远程粘滞
//     标记）→ 有目标；
//   - actor 不接单（chatActorAcceptingWork=false）→ 有在途回合；
//   - 无 Interaction 的宿主（runtime-server、程序化调用）→ 交给中断链路判定。
func chatStopCommandHasTarget(session *ChatSession) bool {
	if session == nil {
		return false
	}
	if chatEscapeInterruptConsumerActive(session) {
		return true
	}
	if session.InputQueue != nil && session.InputQueue.consoleInputCaptureActive() {
		return true
	}
	if !chatActorAcceptingWork(session) {
		return true
	}
	if session.Interaction == nil {
		return true
	}
	return !session.Interaction.IsReady()
}
