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

// chatDebugPprofBaseURL 从 pprof 端点 URL 中提取基础地址（scheme + host:port），
// 例如 http://127.0.0.1:54321/debug/pprof/ → http://127.0.0.1:54321。
// 未启用时返回空串。
func chatDebugPprofBaseURL() string {
	url := chatDebugPprofEndpointURL()
	if url == "" {
		return ""
	}
	// 去掉末尾的 /debug/pprof/ 得到基础地址
	base := strings.TrimSuffix(url, "/debug/pprof/")
	if base == url {
		// 意外格式：直接去掉末尾的 /
		base = strings.TrimSuffix(url, "/")
	}
	return base
}

// chatDebugPprofDisplayURL 返回 /debug/chat/status 端点的完整 URL；
// 未启用 pprof 时返回空串。
func chatDebugPprofDisplayURL() string {
	base := chatDebugPprofBaseURL()
	if base == "" {
		return ""
	}
	return base + "/debug/chat/status"
}

// chatDebugPprofScreenURL 返回 /debug/chat/screen 端点的完整 URL；
// 未启用 pprof 时返回空串。
func chatDebugPprofScreenURL() string {
	base := chatDebugPprofBaseURL()
	if base == "" {
		return ""
	}
	return base + "/debug/chat/screen"
}

// chatDebugPprofEndpointsURL 返回 /debug/endpoints 端点的完整 URL；
// 未启用 pprof 时返回空串。
func chatDebugPprofEndpointsURL() string {
	base := chatDebugPprofBaseURL()
	if base == "" {
		return ""
	}
	return base + "/debug/endpoints"
}

// chatDebugPprofWebURL 返回 /web/ 微型 Web 客户端端点的完整 URL；
// 未启用 pprof 时返回空串。
func chatDebugPprofWebURL() string {
	base := chatDebugPprofBaseURL()
	if base == "" {
		return ""
	}
	return base + ChatWebPath
}

// appendChatDebugPprofLines 在 /debug display 中追加 pprof 诊断区块：
// 已启用时显示基础端点地址与常用 go tool pprof 用法，未启用时给出开启提示。
// 具体端点列表（/debug/pprof/executor、/debug/chat/status、/debug/chat/screen
// 等）统一在"HTTP 调试端点:"区块中列出，此处不重复。
func appendChatDebugPprofLines(builder *chatDebugDocumentBuilder) {
	builder.heading("pprof 诊断: (GET /debug/pprof/...)")
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
