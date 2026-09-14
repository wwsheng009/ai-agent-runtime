package commands

// turn 级工具执行计数。
//
// turn 级自动重跑（maybeAutoRetryDegenerateTurn）会重放整条用户消息，因此只有
// 「本轮尚未真正执行过任何工具」时才是无副作用的。此前这条前提只写在注释里：
// 判据是错误类别，而一次多步 turn 完全可能在第 1 步执行成功、第 2 步才退化失败，
// 此时重放会重复执行第 1 步的工具调用（如 shell 命令、git 提交）。
//
// CLI 自己就是工具执行方（aicliToolExecutor），所以这里用一手计数把「无副作用」
// 从断言变成构造性保证：
//   - resetChatTurnToolExecutions：每个用户 turn 开始时清零（自动重跑不经过
//     该入口，因此重跑期间计数会累积，上一轮已执行工具会立即阻止后续重跑）；
//   - recordChatTurnToolExecution：参数预检通过、工具即将真正执行时记账；
//   - chatTurnToolExecutions：判据读取。
//
// 计数只增不减，语义是「本轮是否已经碰过工具」，而不是「工具是否成功」。

// resetChatTurnToolExecutions 在用户 turn 开始时清零工具执行计数。
func resetChatTurnToolExecutions(session *ChatSession) {
	if session == nil {
		return
	}
	session.turnToolExecutionMu.Lock()
	session.turnToolExecutions = 0
	session.turnToolExecutionMu.Unlock()
}

// recordChatTurnToolExecution 记录一次即将真正执行的工具调用。
func recordChatTurnToolExecution(session *ChatSession) {
	if session == nil {
		return
	}
	session.turnToolExecutionMu.Lock()
	session.turnToolExecutions++
	session.turnToolExecutionMu.Unlock()
}

// chatTurnToolExecutions 返回当前 turn 已经真正执行过的工具调用数。
func chatTurnToolExecutions(session *ChatSession) int {
	if session == nil {
		return 0
	}
	session.turnToolExecutionMu.Lock()
	defer session.turnToolExecutionMu.Unlock()
	return session.turnToolExecutions
}
