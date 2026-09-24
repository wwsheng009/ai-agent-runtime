package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
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

// chatDebugProcessStartedAt 记录包初始化时刻（≈ 进程启动），供 /debug/endpoints
// 输出 StartedAt/UptimeSec：脚本据此识别"远端进程是不是旧构建/没重启"。
var chatDebugProcessStartedAt = time.Now()

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
	Available       bool   `json:"available"`
	Reason          string `json:"reason,omitempty"`
	BaseURL         string `json:"base_url,omitempty"`          // loopback base URL (backwards compat)
	LoopbackBaseURL string `json:"loopback_base_url,omitempty"` // loopback 组基础地址
	WebBaseURL      string `json:"web_base_url,omitempty"`      // web 远程调用端点组基础地址
	ObserveBaseURL  string `json:"observe_base_url,omitempty"`  // runtime-observe 组基础地址
	// ListenMode 描述鉴权模式："loopback"（Host/Origin + 写令牌）或"non-loopback"
	// （所有请求需令牌，Host/Origin 放宽）。供脚本判断是否需要附加 token。
	ListenMode string `json:"listen_mode,omitempty"`
	// Version / BuildTime 与 aicli version、/status 同源（ldflags 注入）；
	// StartedAt / UptimeSec 标识**本进程实例**——脚本据此判断"远端进程是不是
	// 旧构建/未重启"，避免按新契约调用旧实例。
	Version   string `json:"version,omitempty"`
	BuildTime string `json:"build_time,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	UptimeSec int64  `json:"uptime_sec"`
	// WriteAuthHeader / WriteAuthHint 描述 Web API 状态变更请求的鉴权要求
	// （Host/Origin 校验之外的第二层；令牌来自进程启动行或页面 meta 注入）。
	WriteAuthHeader string `json:"write_auth_header,omitempty"`
	WriteAuthHint   string `json:"write_auth_hint,omitempty"`
	// WriteAuthToken 是令牌原文，**仅供 /debug display（TUI）渲染**，刻意
	// json:"-" 排除在 /debug/endpoints 响应之外：清单常被脚本转发或贴进
	// issue，不应连带泄露令牌；需要令牌的脚本走 GET /web/api/token。
	WriteAuthToken string                  `json:"-"`
	Endpoints      []chatDebugEndpointInfo `json:"endpoints"`
}

// loopbackDebugEndpoints 列出 aicli 本机 loopback HTTP 服务器（--pprof 时启动）
// 上提供的调试端点（相对路径）。
var loopbackDebugEndpoints = []struct {
	Method string
	Path   string
	Note   string
}{
	{Method: "GET", Path: "/debug/pprof/", Note: "pprof 性能分析索引（含 heap/allocs/goroutine/block/mutex/trace 等）"},
	{Method: "GET", Path: "/debug/pprof/executor", Note: "executor 恢复循环逐次诊断"},
	{Method: "GET", Path: "/debug/chat/status", Note: "渲染/显示状态快照（JSON / ?format=text）"},
	{Method: "GET", Path: "/debug/chat/screen", Note: "当前屏幕合成帧（JSON / ?format=text）"},
	{Method: "GET", Path: "/debug/endpoints", Note: "调试端点清单（本端点）"},
}

// webDebugEndpoints 列出 /web/* 远程调用端点族（与调试端点同端口，同源）：
// 屏幕/状态快照读取、SSE 实时事件、异步输入注入与同步 invoke 调用。
// 该组是"远程控制 aicli chat TUI"的稳定入口，脚本/外部 Agent 可仅依赖
// /debug/endpoints 返回的 URL 发现并调用。
var webDebugEndpoints = []struct {
	Method string
	Path   string
	Note   string
}{
	{Method: "GET", Path: "/web/", Note: "微型 Web 客户端页面（浏览器交互入口）"},
	{Method: "GET", Path: "/web/api/screen", Note: "当前渲染快照（默认完整 transcript；?view=tui TUI 合成帧；?format=json 结构化；?msg_limit=N&msg_before=M 消息窗口）"},
	{Method: "GET", Path: "/web/api/status", Note: "渲染/显示状态快照（JSON / ?format=text）"},
	{Method: "GET", Path: "/web/api/statusbar", Note: "底部状态栏快照（JSON；balance/context used/directory/git branch/window 等段），与 TUI 底部状态行同源"},
	{Method: "GET", Path: "/web/api/runtime", Note: "运行时元数据（provider/model/reasoning 权威值）"},
	{Method: "GET", Path: "/web/api/events", Note: "SSE 事件流（实时 turn 事件，可续传）"},
	{Method: "POST", Path: "/web/api/input", Note: "异步注入 prompt / 审批决议 / 提问回答 / interrupt（立即返回 queued）"},
	{Method: "POST", Path: "/web/api/invoke", Note: "同步远程调用：注入 prompt 并等待 turn 结束。参数 wait_only（只等待，不注入；会话已空闲时立即返回 settled 不空等）/timeout_ms/session_id/client_request_id（幂等回放 duplicate=true）；Accept: text/event-stream 时以 SSE 返回 start/delta/result 帧；响应含 status/elapsed_ms/screen，turn 结束附带 assistant/usage"},
	{Method: "GET", Path: "/web/api/turn", Note: "turn 后验查询：?id={turn_id} 返回单条记录，缺省返回 current（busy/turn_id/pending_inputs）+ recent（最多 30m，含 status/started_at/finished_at/duration_ms/steps/error/usage+usage_scope[turn=本轮增量，优先取 turn 结束事件载荷；session=累计参考]+usage_source、assistant_preview/assistant_chars）"},
	{Method: "GET", Path: "/web/api/token", Note: "读取本进程 Web 写令牌（X-AICLI-Token，或 ?token=）；仅回环 + 同源可读，响应含 source（random=每进程随机、重启轮换；--web-token/AICLI_WEB_TOKEN=显式指定、重启不轮换）与 hint"},
	{Method: "GET", Path: "/web/api/events/schema", Note: "SSE 事件 schema"},
	{Method: "GET", Path: "/web/api/sessions", Note: "会话列表（current_session_id + 候选会话）"},
	{Method: "POST", Path: "/web/api/sessions/new", Note: "新建会话"},
	{Method: "POST", Path: "/web/api/sessions/resume", Note: "恢复指定会话为当前会话"},
	{Method: "POST", Path: "/web/api/sessions/rename", Note: "重命名会话"},
	{Method: "POST", Path: "/web/api/sessions/delete", Note: "删除会话（当前活动会话不可删除）"},
	{Method: "GET", Path: "/web/api/config", Note: "配置快照（providers/chat/config_path）"},
	{Method: "GET/POST", Path: "/web/api/config/providers", Note: "provider 详情读取（GET）/ 保存（POST）"},
	{Method: "POST", Path: "/web/api/config/providers/delete", Note: "删除 provider（连带分组/默认值/auth 引用清理）"},
	{Method: "POST", Path: "/web/api/config/providers/enabled", Note: "启用/禁用 provider"},
	{Method: "POST", Path: "/web/api/config/providers/fetch-models", Note: "拉取 provider 模型列表"},
	{Method: "POST", Path: "/web/api/config/providers/probe-models", Note: "探测 provider 模型可用性"},
	{Method: "POST", Path: "/web/api/config/providers/auto-import", Note: "从本地客户端配置自动导入 provider"},
	{Method: "POST", Path: "/web/api/config/chat", Note: "保存 chat 配置（默认 provider/model 等）"},
	{Method: "GET", Path: "/web/api/export", Note: "会话导出下载（?format=full|body|tools|trace，缺省 full；?session_id=<id> 缺省当前会话）。内容与 /export、`aicli export` 同源，响应为 attachment（Content-Disposition 带文件名，X-AICLI-Export-Messages 给出消息条数）"},
	{Method: "GET", Path: "/web/api/skills", Note: "技能目录（/{name} 拉取单个技能详情）"},
	{Method: "GET/POST", Path: "/web/api/mcps", Note: "MCP 列表（config+status）/ 新增（写 mcp.yaml 并热重载）"},
	{Method: "GET/PUT/DELETE", Path: "/web/api/mcps/{name}", Note: "查看 / 更新 / 删除单个 MCP"},
	{Method: "POST", Path: "/web/api/mcps/{name}/enable|disable", Note: "启用/停用 MCP（持久化 + 重连，刷新会话工具）"},
	{Method: "POST", Path: "/web/api/mcps/reload", Note: "热重载 MCP 配置并重连（MCP 页签）"},
	{Method: "GET", Path: "/web/api/analysis", Note: "用量分析（/status|/tools|/subagents|/errors|/routing|/routing/events）"},
	{Method: "GET", Path: "/web/api/cache", Note: "LLM 缓存分析（/overview|/requests|/messages/{id}/trace）"},
}

// meshDebugEndpoints 列出多进程网格控制面端点（架构 §5.1，P0 只读三件套）：
// 存活探针 / 本节点自述 / 网格聚合视图。与 loopback / web 同服务器同源，
// 但语义上属于「网格」——`--mesh=false` 时不注册（§9.7），故单列一组，
// 让「只认清单」的脚本能按 enabled 标记判断本进程是否参与网格。
var meshDebugEndpoints = []struct {
	Method string
	Path   string
	Note   string
}{
	{Method: "GET", Path: "/web/api/health", Note: "网格存活探针（极轻量；无会话也 200，供探活/就绪等待/aicli-mesh doctor）"},
	{Method: "GET", Path: "/web/api/mesh/self", Note: "本节点自述（档案 + derived 实时段 + mesh 根目录；auth.token 默认脱敏，回环 ?reveal_token=1 给原文）"},
	{Method: "GET", Path: "/web/api/mesh/peers", Note: "网格聚合视图（默认跨工作区全量；?scope=self|all&workspace=&state=all|live&probe=1&redact_token=1）"},
	{Method: "GET", Path: "/web/api/mesh/events", Note: "网格实时事件流（SSE 扇入；?since_seq=<n> 续传游标、?peers=auto|none 订阅拓扑；只连本进程即可见全网格）"},
	{Method: "POST", Path: "/web/api/mesh/call", Note: "网格调用（op 白名单：node.info/status/screen/turn/sessions.list 只读；invoke/input/cancel/sessions.resume 写操作需 allow_write=true；仅回环）"},
	{Method: "POST", Path: "/web/api/mesh/spawn", Note: "网格拉起（在会话工作区复用活节点或拉起新进程，返回含令牌的窗口 URL；仅回环，--mesh-allow-spawn=false 时 refused）"},
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
	snap.Version = strings.TrimSpace(chatStatusVersion)
	snap.BuildTime = strings.TrimSpace(chatStatusBuildTime)
	if !chatDebugProcessStartedAt.IsZero() {
		snap.StartedAt = chatDebugProcessStartedAt.UTC().Format(time.RFC3339)
		snap.UptimeSec = int64(time.Since(chatDebugProcessStartedAt).Round(time.Second) / time.Second)
	}
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
	// 非回环模式下，端点 URL 也需要 token，但为避免在 JSON /debug/endpoints
	// 响应中泄露令牌，token 不写入 info.URL；而是在 TUI 显示区单独展示。
	if loopbackActive {
		snap.BaseURL = loopbackBase
		snap.LoopbackBaseURL = loopbackBase
	}
	for _, ep := range loopbackDebugEndpoints {
		info := chatDebugEndpointInfo{
			Method:  ep.Method,
			Path:    ep.Path,
			Scheme:  "loopback",
			Enabled: loopbackActive,
			Note:    ep.Note,
		}
		if loopbackActive {
			// JSON 响应中不带 token；TUI 文字渲染时补充。
			info.URL = loopbackBase + ep.Path
		}
		snap.Endpoints = append(snap.Endpoints, info)
	}

	// 监听模式
	if IsChatWebLoopbackMode() {
		snap.ListenMode = "loopback"
	} else {
		snap.ListenMode = "non-loopback"
	}
	// === Web 远程调用端点（/web/*，与 loopback 同服务器同源）===
	if loopbackActive {
		snap.WebBaseURL = loopbackBase + "/web"
		snap.WriteAuthHeader = ChatWebAuthTokenHeader
		if IsChatWebLoopbackMode() {
			snap.WriteAuthHint = "POST 请求需携带 " + ChatWebAuthTokenHeader +
				"（或 ?token=）；令牌可由 GET /web/api/token 读取（或见 aicli 启动行 web write token），内置页面自动注入"
		} else {
			snap.WriteAuthHint = "ALL 请求（含 GET/SSE）需携带 " + ChatWebAuthTokenHeader +
				"（或 ?token=）；令牌可由 aicli 启动行读取，内置页面自动注入"
		}
		// 终端内显示：本机交互式输出，便于人工复制（不进入 HTTP 响应）。
		snap.WriteAuthToken = ChatWebAuthToken()
	}
	for _, ep := range webDebugEndpoints {
		info := chatDebugEndpointInfo{
			Method:  ep.Method,
			Path:    ep.Path,
			Scheme:  "web",
			Enabled: loopbackActive,
			Note:    ep.Note,
		}
		if loopbackActive {
			// JSON 响应中不带 token；TUI 文字渲染时补充。
			info.URL = loopbackBase + ep.Path
		}
		snap.Endpoints = append(snap.Endpoints, info)
	}

	// === 网格控制面端点（/web/api/health + /web/api/mesh/*）===
	// 与 loopback / web 同服务器同源，但语义独立：--mesh=false 时进程不写档案、
	// 不订阅、也不注册 mesh/* 路由（§9.7），此时清单里对应行显示为 [disabled]——
	// 「只认清单」的脚本据此即可判断本进程是否参与网格，不必先探一次。
	meshActive := loopbackActive && mesh.Current() != nil
	for _, ep := range meshDebugEndpoints {
		info := chatDebugEndpointInfo{
			Method:  ep.Method,
			Path:    ep.Path,
			Scheme:  "mesh",
			Enabled: meshActive,
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
	method := strings.TrimSpace(info.Method)
	if method == "" {
		method = "GET"
	}
	return method + " " + path + "  " + chatDebugEndpointFlag(info)
}

// BuildChatDebugEndpointsText 返回全部调试端点的纯文本摘要（?format=text）。
// 与 /debug display 面板的"HTTP 调试端点:"区块一致：按 loopback /
// runtime-observe 分组，每组带基础地址，每行格式：
//
//	GET <url|path>  [enabled|disabled]  <note>
func BuildChatDebugEndpointsText() string {
	snap := BuildChatDebugEndpointsSnapshot()
	var sb strings.Builder
	if !snap.Available {
		return "Debug Endpoints: " + snap.Reason + "\n" + chatDebugUsageGuideText()
	}
	// 实例身份（与 JSON 同源）：脚本据此判断远端进程是否为旧构建/未重启。
	if snap.Version != "" || snap.UptimeSec > 0 {
		build := strings.TrimSpace(snap.Version)
		if snap.BuildTime != "" {
			build += " (" + snap.BuildTime + ")"
		}
		fmt.Fprintf(&sb, "  Build: %s\n", build)
		fmt.Fprintf(&sb, "  Uptime: %s\n", (time.Duration(snap.UptimeSec) * time.Second).String())
	}
	// 非回环模式下：显示局域网 IP 列表（0.0.0.0 不可直接浏览器访问）。
	if snap.ListenMode == "non-loopback" && snap.WebBaseURL != "" {
		port := chatDebugListenPort()
		lanAddrs := ChatWebLocalAddresses()
		if len(lanAddrs) > 0 {
			// 安全红线（E2E-DEBUG-02 实测发现）：本函数同时是 HTTP
			// /debug/endpoints?format=text 的响应体，会被脚本转发、写进日志、
			// 贴进 issue，因此**绝不回显令牌原文**（与 JSON 侧 WriteAuthToken
			// json:"-" 的既有约定一致）。这里只给 <token> 占位符，真实令牌走
			// aicli 启动行或回环 GET /web/api/token。
			fmt.Fprintf(&sb, "  LAN access (paste in browser; 用启动行令牌替换 <token>):\n")
			for _, ip := range lanAddrs {
				// 构造局域网 IP:port + /debug/endpoints?format=text&token=<token>
				fmt.Fprintf(&sb, "    http://%s:%s/debug/endpoints?format=text&token=<token>\n", ip, port)
			}
		}
	}
	for _, scheme := range []string{"loopback", "web", "mesh", "runtime-observe"} {
		schemeLabel := chatDebugEndpointSchemeLabel(scheme)
		if scheme == "loopback" && snap.ListenMode == "non-loopback" {
			schemeLabel += " (non-loopback)"
		}
		sb.WriteString(schemeLabel)
		sb.WriteString("\n")
		var base string
		switch scheme {
		case "loopback":
			base = snap.LoopbackBaseURL
		case "web":
			base = snap.WebBaseURL
		case "mesh":
			// 网格端点与 loopback/web 同服务器：base 复用 loopback 地址。
			base = snap.LoopbackBaseURL
		case "runtime-observe":
			base = snap.ObserveBaseURL
		}
		if base != "" {
			fmt.Fprintf(&sb, "  Base: %s\n", base)
		} else if scheme == "runtime-observe" {
			sb.WriteString("  Base: <route-only>\n")
		}
		if scheme == "web" && strings.TrimSpace(snap.WriteAuthHint) != "" {
			fmt.Fprintf(&sb, "  Auth: %s\n", strings.TrimSpace(snap.WriteAuthHint))
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
	sb.WriteString(chatDebugUsageGuideText())
	return sb.String()
}

// chatDebugUsageGuideText 返回「Debug 使用说明」速览块（自带前置空行）。
// 排查入口 / 脚本驱动 / 鉴权 / 文档指针四行；与 Web 客户端「关于」页的
// 「调试速览」保持同一口径（改一处请同步另一处）。
func chatDebugUsageGuideText() string {
	return "\nDebug 使用说明:\n" +
		"  排查: GET /debug/chat/status（渲染状态）· GET /web/api/screen?view=tui&tail=N（屏幕内容）· GET /debug/pprof/（性能）\n" +
		"  驱动: POST /web/api/invoke（注入 prompt 并等 turn 结束；只等不注入加 wait_only=true）\n" +
		"  鉴权: 写操作需 X-AICLI-Token（见上方 Auth 行，或 GET /web/api/token）；只读端点无需令牌\n" +
		"  文档: docs/aicli/web-remote-api.md · docs/user-guide/aicli-tui-remote.md\n"
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
	case "web":
		return "web  (aicli 微型 Web 客户端 / 远程调用 API)"
	case "mesh":
		return "mesh  (aicli 多进程网格控制面；--mesh=false 时不注册)"
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
	case "web":
		base = snap.WebBaseURL
	case "runtime-observe":
		base = snap.ObserveBaseURL
	}
	if base != "" {
		builder.meta("Base:", base)
	} else if scheme == "runtime-observe" {
		builder.meta("Base:", "<route-only>")
	}
	// 令牌提示
	hint := strings.TrimSpace(snap.WriteAuthHint)
	if hint != "" && (scheme == "web" || (scheme == "loopback" && !IsChatWebLoopbackMode())) {
		builder.meta("Auth:", hint)
	}
	// 令牌原文只在 TUI（/debug display）输出：与 Auth 提示相邻便于复制；
	// HTTP 侧 /debug/endpoints 用 WriteAuthToken(json:"-") 排除，改走
	// GET /web/api/token，避免清单被转发时连带泄露。
	token := strings.TrimSpace(snap.WriteAuthToken)
	if token != "" && (scheme == "web" || (scheme == "loopback" && !IsChatWebLoopbackMode())) {
		builder.meta("Token:", token+"  (GET /web/api/token)")
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
	// 实例身份：version/build_time 与 aicli version 同源，uptime 用于识别
	// "这个进程是不是旧构建/未重启"（按新契约调用旧实例是最常见的错配）。
	if snap.Version != "" || snap.UptimeSec > 0 {
		build := strings.TrimSpace(snap.Version)
		if snap.BuildTime != "" {
			build += " (" + snap.BuildTime + ")"
		}
		builder.meta("Build:", build)
		builder.meta("Uptime:", (time.Duration(snap.UptimeSec) * time.Second).String())
	}
	appendChatDebugEndpointSubgroupLines(builder, snap, "loopback")
	appendChatDebugEndpointSubgroupLines(builder, snap, "web")
	appendChatDebugEndpointSubgroupLines(builder, snap, "runtime-observe")
}
