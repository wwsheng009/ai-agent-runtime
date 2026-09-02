package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	runtimeobserve "github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// ============================================================================
// 调试端点清单（/debug/endpoints + /debug display "HTTP 调试端点:" 区块）
//
// 统一的"调试相关 HTTP 端点"列表，供两处消费：
//   - /debug display 面板：追加"HTTP 调试端点:"区块，按 loopback /
//     runtime-observe 两个分组列出当前环境可用的全部调试端点，每组带
//     基础地址，每个端点带 [enabled]/[disabled] 标记与用途说明；
//   - 独立的 /debug/endpoints HTTP 端点：返回同一清单的结构化 JSON（或
//     ?format=text 纯文本），便于脚本/工具一次性发现全部调试入口。
//
// 列表为只读快照：只读取 session/provider 上的配置与存在性，不做任何变更。
// ============================================================================

// chatDebugEndpointInfo 描述单个调试相关 HTTP 端点。
type chatDebugEndpointInfo struct {
	Method  string `json:"method"`
	Path    string `json:"path"`
	Scheme  string `json:"scheme"` // loopback | runtime-observe
	Enabled bool   `json:"enabled"`
	URL     string `json:"url,omitempty"` // 完整可访问 URL（base 已知时）
	Note    string `json:"note,omitempty"`
}

// chatDebugEndpointsSnapshot 是 /debug/endpoints 的 JSON 响应体。
type chatDebugEndpointsSnapshot struct {
	Available       bool                    `json:"available"`
	Reason          string                  `json:"reason,omitempty"`
	BaseURL         string                  `json:"base_url,omitempty"`          // loopback base URL (backwards compat)
	LoopbackBaseURL string                  `json:"loopback_base_url,omitempty"` // loopback 组基础地址
	ObserveBaseURL  string                  `json:"observe_base_url,omitempty"`  // runtime-observe 组基础地址
	Endpoints       []chatDebugEndpointInfo `json:"endpoints"`
}

// loopbackDebugEndpoints 列出 aicli 本机 loopback HTTP 服务器（--pprof 时启动）
// 上提供的调试端点（相对路径）。
var loopbackDebugEndpoints = []struct {
	Path string
	Note string
}{
	{Path: "/debug/pprof/", Note: "pprof 性能分析索引（含 heap/allocs/goroutine/block/mutex/trace 等）"},
	{Path: "/debug/pprof/executor", Note: "executor 恢复循环逐次诊断"},
	{Path: "/debug/chat/status", Note: "渲染/显示状态快照（JSON / ?format=text）"},
	{Path: "/debug/chat/screen", Note: "当前屏幕合成帧（JSON / ?format=text）"},
	{Path: "/debug/endpoints", Note: "调试端点清单（本端点）"},
}

// observeDebugEndpoints 列出 Runtime Observation Plane 的版本化端点
// （相对 RoutePrefix，默认 /api/runtime/observe/v1）。
var observeDebugEndpoints = []struct {
	Path string
	Note string
}{
	{Path: "/capabilities", Note: "观察平面能力声明"},
	{Path: "/snapshot", Note: "当前会话运行快照"},
	{Path: "/sessions/{session_id}", Note: "指定会话详情"},
	{Path: "/events", Note: "事件流（轮询观测）"},
}

// buildChatDebugEndpointList 构建当前会话环境下的全部调试相关端点清单。
// 每个端点带 enabled 状态：loopback 端点取决于 pprof HTTP 服务器是否在运行，
// runtime-observe 端点取决于 RuntimeConfig.Observe.Enabled。
// 无会话（nil）时返回 available=false 的轻量清单（endpoints 为空）。
func buildChatDebugEndpointList(session *ChatSession) *chatDebugEndpointsSnapshot {
	snap := &chatDebugEndpointsSnapshot{}
	if session == nil {
		snap.Available = false
		snap.Reason = "no active chat session"
		snap.Endpoints = []chatDebugEndpointInfo{}
		return snap
	}
	snap.Available = true

	// === Loopback 本机端点（aicli --pprof HTTP 服务器）===
	loopbackBase := chatDebugPprofBaseURL()
	loopbackActive := loopbackBase != ""
	if loopbackActive {
		snap.BaseURL = loopbackBase
		snap.LoopbackBaseURL = loopbackBase
	}
	for _, ep := range loopbackDebugEndpoints {
		info := chatDebugEndpointInfo{
			Method:  "GET",
			Path:    ep.Path,
			Scheme:  "loopback",
			Enabled: loopbackActive,
			Note:    ep.Note,
		}
		if loopbackActive {
			info.URL = loopbackBase + ep.Path
		}
		snap.Endpoints = append(snap.Endpoints, info)
	}

	// === Runtime Observation Plane 端点 ===
	observe, ok := chatSessionObserveConfig(session)
	prefix := runtimeobserve.DefaultConfig().RoutePrefix
	observeActive := false
	observeBase := ""
	if ok {
		if trimmed := strings.TrimSpace(observe.RoutePrefix); trimmed != "" {
			prefix = trimmed
		}
		observeBase = chatObserveBaseURL(session)
		// 本地 in-process 模式：本地 observe 服务真实可用（默认随 --pprof on
		// 开启）时，端点由本机 pprof loopback 服务器提供，base 使用 loopback
		// 地址，而不是 fallback 到不存在的 runtime-server 地址。
		localSvc := ensureLocalObserveService(session.LocalRuntimeHost)
		if localSvc != nil && localSvc.Enabled() {
			observeActive = true
			if loopbackActive {
				observeBase = loopbackBase
			}
		}
		if !observeActive {
			observeActive = observe.Enabled
		}
		// 当 observe 启用但既无本地服务、也未连接 runtime-server 时，
		// 使用默认 runtime-server 地址作为 base，使端点显示为完整地址。
		if observeBase == "" && observeActive {
			observeBase = defaultAICLIRuntimeServerURL
		}
	}
	if observeBase != "" {
		snap.ObserveBaseURL = observeBase + prefix
	}
	for _, ep := range observeDebugEndpoints {
		info := chatDebugEndpointInfo{
			Method:  "GET",
			Path:    prefix + ep.Path,
			Scheme:  "runtime-observe",
			Enabled: observeActive,
			Note:    ep.Note,
		}
		if observeBase != "" {
			info.URL = observeBase + prefix + ep.Path
		}
		snap.Endpoints = append(snap.Endpoints, info)
	}

	return snap
}

// BuildChatDebugEndpointsSnapshot 返回当前调试端点清单快照（无会话时轻量响应）。
func BuildChatDebugEndpointsSnapshot() *chatDebugEndpointsSnapshot {
	return buildChatDebugEndpointList(chatDebugDisplaySession())
}

// chatDebugEndpointFlag 返回端点的启用标记文本。
func chatDebugEndpointFlag(info chatDebugEndpointInfo) string {
	if info.Enabled {
		return "[enabled]"
	}
	return "[disabled]"
}

// chatDebugEndpointLine 渲染单个端点的单行文本。base 已知时附带完整 URL。
func chatDebugEndpointLine(info chatDebugEndpointInfo) string {
	path := info.Path
	if strings.TrimSpace(info.URL) != "" {
		path = info.URL
	}
	return "GET " + path + "  " + chatDebugEndpointFlag(info)
}

// BuildChatDebugEndpointsText 返回全部调试端点的纯文本摘要（?format=text）。
// 与 /debug display 面板的"HTTP 调试端点:"区块一致：按 loopback /
// runtime-observe 分组，每组带基础地址，每行格式：
//   GET <url|path>  [enabled|disabled]  <note>
func BuildChatDebugEndpointsText() string {
	snap := BuildChatDebugEndpointsSnapshot()
	var sb strings.Builder
	if !snap.Available {
		return "Debug Endpoints: " + snap.Reason + "\n"
	}
	for _, scheme := range []string{"loopback", "runtime-observe"} {
		sb.WriteString(chatDebugEndpointSchemeLabel(scheme))
		sb.WriteString("\n")
		var base string
		switch scheme {
		case "loopback":
			base = snap.LoopbackBaseURL
		case "runtime-observe":
			base = snap.ObserveBaseURL
		}
		if base != "" {
			fmt.Fprintf(&sb, "  Base: %s\n", base)
		} else if scheme == "runtime-observe" {
			sb.WriteString("  Base: <route-only>\n")
		}
		for _, info := range snap.Endpoints {
			if info.Scheme != scheme {
				continue
			}
			sb.WriteString("  ")
			sb.WriteString(chatDebugEndpointLine(info))
			if strings.TrimSpace(info.Note) != "" {
				sb.WriteString("  ")
				sb.WriteString(strings.TrimSpace(info.Note))
			}
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// MarshalChatDebugEndpointsJSON 返回缩进 JSON 字节，供 HTTP 端点直接写入。
func MarshalChatDebugEndpointsJSON() ([]byte, error) {
	return json.MarshalIndent(BuildChatDebugEndpointsSnapshot(), "", "  ")
}

// ============================================================================
// /debug display 面板渲染
// ============================================================================

// chatDebugEndpointSchemeLabel 返回端点的分组标签文本。
func chatDebugEndpointSchemeLabel(scheme string) string {
	switch scheme {
	case "loopback":
		return "loopback  (aicli --pprof 本机调试服务器)"
	case "runtime-observe":
		return "runtime-observe  (Runtime Observation Plane)"
	default:
		return scheme
	}
}

// appendChatDebugEndpointSubgroupLines 输出单个分组（loopback / runtime-observe）
// 的所有端点行。base 为该组的基础地址（可能为空，为空时只显示相对路径）。
func appendChatDebugEndpointSubgroupLines(builder *chatDebugDocumentBuilder, snap *chatDebugEndpointsSnapshot, scheme string) {
	if builder == nil || snap == nil {
		return
	}
	builder.plain("  " + chatDebugEndpointSchemeLabel(scheme))
	var base string
	switch scheme {
	case "loopback":
		base = snap.LoopbackBaseURL
	case "runtime-observe":
		base = snap.ObserveBaseURL
	}
	if base != "" {
		builder.meta("Base:", base)
	} else if scheme == "runtime-observe" {
		builder.meta("Base:", "<route-only>")
	}
	for _, info := range snap.Endpoints {
		if info.Scheme != scheme {
			continue
		}
		line := "  " + chatDebugEndpointLine(info)
		if strings.TrimSpace(info.Note) != "" {
			line += "  " + strings.TrimSpace(info.Note)
		}
		builder.plain(line)
	}
}

// appendChatDebugEndpointListLines 在 /debug display 中追加"HTTP 调试端点:"区块：
// 统一列出当前环境全部调试相关 HTTP 端点，按 loopback 与 runtime-observe 两个
// 分组展示，每组带基础地址与 [enabled]/[disabled] 标记。该区块是只读快照。
func appendChatDebugEndpointListLines(builder *chatDebugDocumentBuilder, session *ChatSession) {
	if builder == nil {
		return
	}
	builder.heading("HTTP 调试端点: (GET /debug/endpoints)")
	if session == nil {
		builder.meta("Status:", "<no session>")
		return
	}
	snap := buildChatDebugEndpointList(session)
	if !snap.Available {
		builder.meta("Status:", snap.Reason)
		return
	}
	appendChatDebugEndpointSubgroupLines(builder, snap, "loopback")
	appendChatDebugEndpointSubgroupLines(builder, snap, "runtime-observe")
}
