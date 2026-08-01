package commands

import "strings"

// chatDebugPprofProvider 提供当前进程 pprof 端点信息（完整 URL；未启用返回空串）。
// pprof 服务器由 cmd/aicli main 包启动，commands 包无法反向引用 main，
// 因此通过 RegisterChatDebugPprofProvider 在启动时注册取值函数。
var chatDebugPprofProvider = func() string { return "" }

// RegisterChatDebugPprofProvider 注册 pprof 端点信息提供者。
// provider 返回完整端点 URL（如 http://127.0.0.1:54321/debug/pprof/），
// 未启用 pprof 时应返回空串。
func RegisterChatDebugPprofProvider(provider func() string) {
	if provider != nil {
		chatDebugPprofProvider = provider
	}
}

// chatDebugPprofEndpointURL 返回当前 pprof 端点 URL；未启用时返回空串。
func chatDebugPprofEndpointURL() string {
	if chatDebugPprofProvider == nil {
		return ""
	}
	return chatDebugPprofProvider()
}

// appendChatDebugPprofLines 在 /debug display 中追加 pprof 诊断区块：
// 已启用时显示端点地址与常用 go tool pprof 用法，未启用时给出开启提示。
func appendChatDebugPprofLines(builder *chatDebugDocumentBuilder) {
	builder.heading("pprof 诊断:")
	url := chatDebugPprofEndpointURL()
	if url == "" {
		builder.meta("Status:", "未启用")
		builder.plain("  启动时加 --pprof（或设 AICLI_PPROF=127.0.0.1:<端口>）即可开启")
		return
	}
	base := strings.TrimSuffix(url, "/")
	builder.meta("Endpoint:", url)
	builder.plain("  Heap:  go tool pprof \"" + base + "/heap?gc=1\"")
	builder.plain("  Alloc: go tool pprof \"" + base + "/allocs\"")
	builder.plain("  CPU:   go tool pprof \"" + base + "/profile?seconds=30\"")
	builder.plain("  Trace: go tool pprof \"" + base + "/trace?seconds=5\"")
}
