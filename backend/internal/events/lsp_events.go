package events

// LSP 观测事件类型（live-only）：由 aicli chat 的 LSP observer 发布——池事件
// 在 LSP 热路径上产生，这里只保留标量（trigger/outcome/duration_ms/diag_count/
// appended_bytes）与池内名称，源码正文、诊断文本与路径不进入事件载荷。
//
// 契约登记见 contract.go（B 通道 live-only）；观测白名单与字段投影见
// internal/runtimeobserve；方案见
// docs/plan/lsp-observability-and-analysis-plan-20260929.md §3。
const (
	// EventLSPRequestFinished 每次后写请求（编辑内联 / lsp_diagnostics 工具）
	// 完成时发布一次；是覆盖率、降级率与等待延迟读数的唯一事实源。
	EventLSPRequestFinished = "lsp.request.finished"
	// EventLSPServerState 池成员生命周期状态变化（starting/ready/...）。
	EventLSPServerState = "lsp.server.state"
	// EventLSPDiagnosticsUpdated 成员为被追踪文档发布诊断（采样/turn 聚合）。
	EventLSPDiagnosticsUpdated = "lsp.diagnostics.updated"
)
