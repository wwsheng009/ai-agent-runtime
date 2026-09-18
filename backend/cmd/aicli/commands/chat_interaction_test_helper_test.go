package commands

import "testing"

// newTestChatInteractionCoordinator builds a coordinator for tests and stops it
// (including its lazily started UI actor) before the test completes.
//
// 背景：UI actor 启动后是进程级资产，其 timer 驱动的重绘会经
// ui.TerminalOutput() 写进程级 writer；测试会临时把它换成 stdout 捕获管道。
// 如果某个测试创建的 coordinator 在测试结束后仍在运行，它的重绘会污染后续
// 测试的捕获（例如 captureStdout 抓到的 JSON 混入 ANSI 帧），后台 goroutine
// 在测试完成后调用 t.Logf 还会直接 panic 整个测试进程。测试必须经本助手构造
// coordinator，不要再直接调用 newChatInteractionCoordinator。
func newTestChatInteractionCoordinator(tb testing.TB, session *ChatSession) *chatInteractionCoordinator {
	tb.Helper()
	coord := newChatInteractionCoordinator(session)
	tb.Cleanup(coord.Shutdown)
	return coord
}
