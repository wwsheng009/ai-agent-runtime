package manager

// StderrDiagnosticsProvider 暴露 stdio 子进程诊断的可选能力（计划 §11.5）。
//
// 它不是 Manager 接口的一部分：主线 *manager 实现它；Win7 兼容构建的
// disabledManager 实现为空串；测试替身无需改动（可选能力按需探测）。
//
// 用法：
//
//	if provider, ok := mgr.(manager.StderrDiagnosticsProvider); ok {
//		tail := provider.StderrDiagnostics(name)
//	}
//
// 本文件不带构建标签，两种构建下都存在，保证调用方可编译。
type StderrDiagnosticsProvider interface {
	// StderrDiagnostics 返回名为 name 的 MCP 最近一次连接尝试的子进程
	// stderr 诊断；无可用诊断（非 stdio、未连接、无缓冲）时返回空串。
	StderrDiagnostics(name string) string
}
