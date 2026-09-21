package commands

import (
	"net"
	"strings"
)

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
// 去除查询参数（非回环模式下 URL 可能带 ?token=...）；未启用时返回空串。
func chatDebugPprofBaseURL() string {
	url := chatDebugPprofEndpointURL()
	if url == "" {
		return ""
	}
	// 先去除查询参数，确保 TrimSuffix 能正确匹配路径尾缀。
	if idx := strings.Index(url, "?"); idx >= 0 {
		url = url[:idx]
	}
	// 去掉末尾的 /debug/pprof/ 得到基础地址
	base := strings.TrimSuffix(url, "/debug/pprof/")
	if base == url {
		// 意外格式：直接去掉末尾的 /
		base = strings.TrimSuffix(url, "/")
	}
	return base
}

// chatDebugListenAddr 返回 pprof 服务器的监听地址（host:port，不含 scheme）。
// 未启用 pprof 时返回空串。用于非回环模式下显示实际绑定地址（IPv4 优先于IPv6格式）。
func chatDebugListenAddr() string {
	base := chatDebugPprofBaseURL()
	if base == "" {
		return ""
	}
	// base 形如 http://0.0.0.0:54321，去掉 http:// 得到 host:port。
	return strings.TrimPrefix(base, "http://")
}

// chatDebugListenPort 从监听地址中提取端口号字符串（不含前导冒号）。
// 未启用 pprof 或地址不含端口时返回空串。供非回环模式下构造 LAN 访问 URL 使用。
func chatDebugListenPort() string {
	addr := chatDebugListenAddr()
	if addr == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	return port
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
		builder.plain("  启动时加 --pprof / --debug（随机空闲端口），或用 --web-port <端口>、")
		builder.plain("  --web-host 0.0.0.0（局域网访问）、AICLI_PPROF=127.0.0.1:<端口> 固定地址；")
		builder.plain("  Web 客户端与 /debug 端点共用该端口")
		return
	}
	base := strings.TrimSuffix(url, "/")
	// 非回环模式下，pprof 读端点也是外部可访问的，因此需附加 token。
	// token 作为查询参数放在 URL 末尾，需要处理已有查询参数（如 ?gc=1）的情况。
	tokenVal := ""
	if !IsChatWebLoopbackMode() {
		// 显示实际监听地址（tcp4 → 0.0.0.0:port；tcp6 → [::]:port）
		addr := chatDebugListenAddr()
		builder.meta("Listen:", addr+" (all interfaces, all requests require token)")
		tokenVal = ChatWebAuthToken()
	}
	// appendToken 构造 pprof 子路径的完整访问 URL，正确处理查询参数分隔。
	appendToken := func(path string) string {
		if tokenVal == "" {
			return base + path
		}
		sep := "&"
		if !strings.Contains(path, "?") {
			sep = "?"
		}
		return base + path + sep + "token=" + tokenVal
	}
	builder.meta("Endpoint:", url)
	builder.plain("  Heap:  go tool pprof \"" + appendToken("/heap?gc=1") + "\"")
	builder.plain("  Alloc: go tool pprof \"" + appendToken("/allocs") + "\"")
	builder.plain("  CPU:   go tool pprof \"" + appendToken("/profile?seconds=30") + "\"")
	builder.plain("  Trace: go tool pprof \"" + appendToken("/trace?seconds=5") + "\"")
}
