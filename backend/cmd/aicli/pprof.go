package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// chatWebH2CIdleTimeout 是 h2c 连接的空闲上限：与 HTTP/1.1 侧的 IdleTimeout 语义
// 对齐，长时间没有流的 h2 连接不长期占位。
const chatWebH2CIdleTimeout = 120 * time.Second

// newChatWebH2CHandler 把调试/Web 端点 handler 包成支持 h2c（明文 HTTP/2）的
// handler（§4.2.1 连接预算）。
//
// 动机：本服务只监听回环明文，但页面要维持常驻 SSE。HTTP/1.1 下浏览器对单 host
// 的并发连接上限约 6 条，多开两个标签页就占满连接池，之后所有 /web/api/* 请求在
// 浏览器侧永久排队——页面既不报错也不恢复。h2c 让支持明文 HTTP/2 的客户端（Go
// 客户端、aicli 自身、脚本、代理）把多条流复用到一个 TCP 连接；不支持 h2c 的
// 客户端（浏览器对明文 HTTP/2 尚不启用）继续走 HTTP/1.1，行为与包装前逐字一致
// ——纯增量能力，不是「必须协商成功」的依赖。浏览器侧的连接预算由
// /web/api/events?mesh=1 单连接合并解决（见 commands.HandleChatWebAPIEvents）。
//
// 单独成函数是为了让测试能直接验证协商结果（见 pprof_h2c_test.go）。
func newChatWebH2CHandler(handler http.Handler) http.Handler {
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	return h2c.NewHandler(handler, &http2.Server{IdleTimeout: chatWebH2CIdleTimeout})
}

// pprofServerHandle 持有按需启动的 pprof HTTP 服务器。
// 默认监听 127.0.0.1 上的随机空闲端口；仅本机可访问，
// 避免把可触发 GC / 执行分析代码的端点暴露到网络上。
// 当 --web-host 指定非回环地址（如 0.0.0.0）时，监听所有接口并
// 启用非回环鉴权模式（所有请求需令牌）。
type pprofServerHandle struct {
	server *http.Server
	addr   string
	// tokenQueryParam 是非回环模式下的 token 查询参数后缀（?token=xxxxx），
	// 回环模式下为空串。用于构造带令牌的访问 URL。
	tokenQueryParam string
}

// chatDisplayPath 是会话渲染/显示状态快照端点的独立路径。
// 它刻意不挂在 /debug/pprof/ 下：pprof 是 Go 标准 profiling 命名空间，
// 应用自定义的诊断端点应使用自己的路径，避免与标准端点混淆。
const chatDisplayPath = "/debug/chat/status"

// chatScreenPath 是当前屏幕合成帧（用户实际看到的屏幕内容）端点的独立路径。
// 与 chatDisplayPath 同族：/debug/chat/status 看渲染器内部状态，
// /debug/chat/screen 看合成帧的最终文本内容。
const chatScreenPath = "/debug/chat/screen"

// chatEndpointsPath 是调试端点清单的独立路径：返回当前环境全部调试相关
// HTTP 端点（loopback pprof/chat 端点 + Runtime Observation Plane 端点），
// 每个端点带 [enabled]/[disabled] 标记。默认返回 JSON；?format=text 返回
// 纯文本清单。该端点服务于"一次性发现全部调试入口"的场景。
const chatEndpointsPath = "/debug/endpoints"

// Addr 返回服务器的监听地址（host:port）。
// 例如 0.0.0.0:54321（tcp4）或 [::]:54321（tcp6）。
func (h *pprofServerHandle) Addr() string {
	if h == nil {
		return ""
	}
	return h.addr
}

// WebPort 返回服务器的监听端口号（字符串形式）。
func (h *pprofServerHandle) WebPort() string {
	if h == nil || h.addr == "" {
		return ""
	}
	// Addr 形如 "0.0.0.0:54321" 或 "[::]:54321"，提取端口部分。
	_, portStr, err := net.SplitHostPort(h.addr)
	if err != nil {
		// 没有端口号的兑换失败情况
		return ""
	}
	return portStr
}

// URL 返回 pprof 索引页面的完整 URL（不含 token 查询参数；
// 非回环模式下 URL 构造处会附加 ChatWebTokenQueryParam()）。
func (h *pprofServerHandle) URL() string {
	addr := h.Addr()
	if addr == "" {
		return ""
	}
	return "http://" + addr + "/debug/pprof/"
}

// DisplayURL 返回会话渲染/显示状态快照端点的完整 URL。
func (h *pprofServerHandle) DisplayURL() string {
	addr := h.Addr()
	if addr == "" {
		return ""
	}
	return "http://" + addr + chatDisplayPath
}

// ScreenURL 返回当前屏幕合成帧端点的完整 URL。
func (h *pprofServerHandle) ScreenURL() string {
	addr := h.Addr()
	if addr == "" {
		return ""
	}
	return "http://" + addr + chatScreenPath
}

// EndpointsURL 返回调试端点清单端点的完整 URL。
func (h *pprofServerHandle) EndpointsURL() string {
	addr := h.Addr()
	if addr == "" {
		return ""
	}
	return "http://" + addr + chatEndpointsPath
}

// WebURL 返回微型 Web 客户端页面（/web/）的完整 URL。
// 非回环模式下附加 ?token= 便于直接从局域网访问。
func (h *pprofServerHandle) WebURL() string {
	addr := h.Addr()
	if addr == "" {
		return ""
	}
	return "http://" + addr + commands.ChatWebPath + h.tokenQueryParam
}

// InvokeURL 返回同步远程调用端点（POST /web/api/invoke）的完整 URL。
func (h *pprofServerHandle) InvokeURL() string {
	addr := h.Addr()
	if addr == "" {
		return ""
	}
	return "http://" + addr + commands.ChatWebAPIInvokePath + h.tokenQueryParam
}

// TokenQueryParam 返回非回环模式下的 token 查询参数后缀。
func (h *pprofServerHandle) TokenQueryParam() string {
	return h.tokenQueryParam
}

// Close 关闭服务器并释放监听端口。
func (h *pprofServerHandle) Close() error {
	if h == nil || h.server == nil {
		return nil
	}
	return h.server.Close()
}

// resolveLoopbackServerAddr 解析 loopback 服务器（Web 客户端 / /debug 端点）的监听地址。
// 优先级（高 → 低）：
//  1. --web-port <port>：显式端口，展开为 <webHost>:<port>；端口越界直接报错，
//     避免"手滑写成 70000"这类笔误静默退化成随机端口；
//  2. AICLI_PPROF 环境变量：非空即启用，并按原样作为地址（可含自定义 host，保持既有语义）；
//  3. --pprof / --debug：<webHost>:0，随机空闲端口；
//  4. 都未设置：返回空串，不启动服务器。
//
// webHost 默认为 127.0.0.1；--web-host 0.0.0.0 或 AICLI_WEB_HOST=0.0.0.0 时监听
// 所有接口，进入非回环鉴权模式（所有请求需 token）。--web-host :: 使用 IPv6 双栈。
func resolveLoopbackServerAddr(pprofFlag, debugFlag bool, webPort int, webPortSet bool, webHost, pprofEnv string) (string, error) {
	if webPortSet {
		if webPort < 1 || webPort > 65535 {
			return "", fmt.Errorf("invalid --web-port %d: must be between 1 and 65535", webPort)
		}
		return webHost + ":" + strconv.Itoa(webPort), nil
	}
	if env := strings.TrimSpace(pprofEnv); env != "" {
		return env, nil
	}
	if pprofFlag || debugFlag {
		return webHost + ":0", nil
	}
	return "", nil
}

// resolveLoopbackServerNetwork 根据监听地址确定网络类型。
// 显式 IPv4 地址（如 0.0.0.0）→ "tcp4"，确保 Addr().String() 显示为 IPv4 格式；
// 显式 IPv6 地址（如 ::）→ "tcp6"；回环/其他 → "tcp"（Go 自动选择）。
func resolveLoopbackServerNetwork(addr string) string {
	if addr == "" {
		return "tcp"
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	// IPv6 地址可能被 [:] 包裹
	if len(host) >= 2 && host[0] == '[' && host[len(host)-1] == ']' {
		host = host[1 : len(host)-1]
	}
	if ip := net.ParseIP(host); ip != nil {
		// 显式 IPv4 地址 → 强制 tcp4，避免 dual-stack 返回 [::]
		if ip.To4() != nil {
			return "tcp4"
		}
		// IPv6 地址 → tcp6
		return "tcp6"
	}
	// hostname（如 localhost）→ 交由 Go 自动解析
	return "tcp"
}

// startPprofServer 启动 pprof HTTP 服务器。
// addr 为空时使用 127.0.0.1:0（随机空闲端口）；传入其他地址时按原样监听。
// 当 addr 绑定在非回环地址（如 0.0.0.0）时，自动开启非回环鉴权模式
// （所有请求需携带写令牌），并在 URL 方法中附加 ?token= 供局域网访问。
func startPprofServer(addr string) (*pprofServerHandle, error) {
	if strings.TrimSpace(addr) == "" {
		addr = "127.0.0.1:0"
	}
	// 根据地址确定网络类型：显式 IPv4（如 0.0.0.0）用 tcp4 避免 dual-stack
	// 返回 [::] 格式，显式 IPv6（如 ::）用 tcp6。
	network := resolveLoopbackServerNetwork(addr)
	ln, err := net.Listen(network, addr)
	if err != nil {
		return nil, fmt.Errorf("pprof listen on %s: %w", addr, err)
	}

	// 确保写令牌已初始化（用于非回环模式的 URL 构造与鉴权）。
	commands.EnsureChatWebAuthToken()

	// 非回环地址 → 开启非回环鉴权模式（所有请求需令牌）。
	// tokenQueryParam 由鉴权模式决定，而非监听地址：
	// 非回环模式下 URL 需附加 ?token=（浏览器无法通过 Header 访问）。
	if !commands.ChatWebHostIsLoopback(ln.Addr().String()) {
		commands.SetChatWebLoopbackMode(false)
	} else {
		commands.SetChatWebLoopbackMode(true)
	}
	tokenQueryParam := ""
	if !commands.IsChatWebLoopbackMode() {
		tokenQueryParam = commands.ChatWebTokenQueryParam()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	mux.Handle("/debug/pprof/allocs", pprof.Handler("allocs"))
	mux.Handle("/debug/pprof/block", pprof.Handler("block"))
	mux.Handle("/debug/pprof/goroutine", pprof.Handler("goroutine"))
	mux.Handle("/debug/pprof/heap", pprof.Handler("heap"))
	mux.Handle("/debug/pprof/mutex", pprof.Handler("mutex"))
	mux.Handle("/debug/pprof/threadcreate", pprof.Handler("threadcreate"))
	// /debug/pprof/executor 暴露 TerminalSessionExecutor 的 recovery-loop 逐次
	// 诊断（环形缓冲 + 计数器）。这是 CPU/goroutine profile 之外的观测手段：
	// 它显示每次 recovery flush 的 revision 前后值、generation、epoch、
	// ProjectionUnknown/ReconciliationRequired、FullRepaint/ScrollbackReset、
	// frame 错误、backoff 是否 arm/触发，并给出派生的循环健康诊断
	// （WindowDiagnosis 为当前窗口判决、Diagnosis 为 since_start 历史判决；
	// 取值 idle / healthy / backoff_engaged / backoff_engaged_handing_off / dead_guard）——
	// 精确回答"executor 在重放什么、为什么没有收敛、backoff 是否真的在工作"。
	// ?format=text 时返回人类可读摘要（便于 curl 直接观测，无需解析 JSON）；
	// 未设置 provider 时返回空快照。
	mux.HandleFunc("/debug/pprof/executor", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "text" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(ui.ExecutorDiagTextSummary()))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(ui.ExecutorDiagSnapshot())
	})

	// /debug/chat/status 暴露当前会话的渲染/显示状态快照（Unified Render
	// Encoder / Scene / Render Output / AppState / Paint Trace），等价于在会话内
	// 手动执行 /debug display 的 JSON 化版本。该端点使用独立路径，不混入标准
	// pprof 命名空间，是 --debug / --pprof 模式下在线连续采样渲染状态的主要
	// 端点：curl 周期性请求即可观察 Encode/Append/Commit 计数器是否停滞，
	// 定位"统一渲染器只更新 active band 而不提交"。
	//   - 默认返回 JSON；?format=text 返回 /debug display 纯文本摘要。
	//   - 无活动会话时返回 available=false（HTTP 200），便于轮询探测。
	//   - 默认受跨区块预算约束（超预算的重区块跳过并登记在 skipped_sections）；
	//     ?fast=1 直接跳过重区块（files/storage/scene_layout/plan_layout），
	//     供风暴期高频采样使用。
	mux.HandleFunc(chatDisplayPath, func(w http.ResponseWriter, r *http.Request) {
		opts := commands.ChatDebugDisplayBoundedOptions()
		if r.URL.Query().Get("fast") == "1" {
			opts = commands.ChatDebugDisplayFastOptions()
		}
		if r.URL.Query().Get("format") == "text" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(commands.BuildChatDebugDisplayTextWithOptions(opts)))
			return
		}
		body, err := commands.MarshalChatDebugDisplayJSONWithOptions(opts)
		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(body)
	})

	// /debug/chat/screen 暴露当前屏幕合成帧（用户实际看到的文本内容），
	// 等价于从外部观察 TerminalSession 的最终输出。该端点与 /debug/chat/status
	// 互补：status 看渲染器内部状态，screen 看合成帧的最终文本。
	//   - 默认返回 JSON（含 width/height/lines/text）；
	//   - ?format=text 返回纯文本屏幕内容（每行以 \n 分隔，末尾有 \n）；
	//   - 无会话 / 无 surface / 空帧时返回 available=false（HTTP 200）。
	mux.HandleFunc(chatScreenPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "text" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(commands.BuildChatDebugScreenText()))
			return
		}
		body, err := commands.MarshalChatDebugScreenJSON()
		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(body)
	})

	// /debug/endpoints 暴露当前环境全部调试相关 HTTP 端点清单：
	// loopback 本机端点（pprof 索引/executor/chat status/screen/本端点）
	// 与 Runtime Observation Plane 端点（capabilities/snapshot/sessions/events），
	// 每个端点带 [enabled]/[disabled] 标记。默认返回 JSON；?format=text 返回
	// 纯文本清单。该端点服务于"一次性发现全部调试入口"的场景，便于脚本
	// 或 curl 直接枚举可用的调试端点。
	mux.HandleFunc(chatEndpointsPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "text" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(commands.BuildChatDebugEndpointsText()))
			return
		}
		body, err := commands.MarshalChatDebugEndpointsJSON()
		if err != nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = w.Write(body)
	})

	// /api/runtime/observe/v1/* 暴露本地 Runtime Observation Plane 端点
	// （capabilities/snapshot/sessions/{id}/events）。aicli 本地 in-process
	// 模式没有独立的 runtime-server，这些端点由本机 loopback 服务器直接提供，
	// 数据源是当前会话的本地 runtimeobserve.Service（复用 host.EventBus +
	// SessionHub，默认随 --pprof on 开启）。响应与 runtime-server 版本保持同一
	// envelope/错误码契约，便于同一套客户端/脚本无缝切换。
	observePrefix := strings.TrimRight(commands.ChatDebugObservePrefix(), "/")
	if observePrefix != "" {
		mux.HandleFunc(observePrefix+"/", commands.HandleChatDebugObserveRequest)
	}

	// /web/* 微型 Web 客户端端点族（同源，无 CORS）。
	// 页面由本机 loopback 服务器直接提供；EventSource 事件流 / 屏幕快照 /
	// 状态快照 / 输入注入全部复用 commands 包内现有会话与 EventBus。
	mux.HandleFunc(commands.ChatWebPath, commands.HandleChatWebPage)
	// /web/api/health 网格存活探针（架构 §5.2）：极轻量，无会话也返回 200，
	// 供网格探活 / 外部脚本就绪等待 / aicli-mesh doctor 使用。
	mux.HandleFunc(commands.ChatWebAPIHealthPath, commands.HandleChatWebAPIHealth)
	// /web/api/mesh/self|peers 网格控制面只读端点（架构 §5.3 / §5.4）：
	// 自述与聚合视图，和 `aicli-mesh ls` 同源。总开关 --mesh=false 时
	// 进程不参与网格，这两条路由**不注册**（§9.7）——此时请求落到 404，
	// 比「注册后回 available=false」更诚实地表达「本进程不在网格里」。
	if mesh.Current() != nil {
		mux.HandleFunc(commands.ChatWebAPIMeshSelfPath, commands.HandleChatWebAPIMeshSelf)
		mux.HandleFunc(commands.ChatWebAPIMeshPeersPath, commands.HandleChatWebAPIMeshPeers)
		// SSE 扇入（S7）：路由走统一鉴权（§5.8），写路径不存在——这条端点只读。
		mux.HandleFunc(commands.ChatWebAPIMeshEventsPath, commands.HandleChatWebAPIMeshEvents)
		// 网格调用（S8，架构 §5.6）：读 op 走统一鉴权；写 op 还要求
		// allow_write=true，且调用者必须是回环（§5.8）。
		mux.HandleFunc(commands.ChatWebAPIMeshCallPath, commands.HandleChatWebAPIMeshCall)
		// 网格拉起（S9，架构 §5.7）：复用该会话的活节点，或在它的工作区里
		// 拉起新进程，并返回 §7.3 的窗口 URL（含令牌）。仅回环；
		// --mesh-allow-spawn=false 时回 refused + mesh_spawn_not_allowed。
		mux.HandleFunc(commands.ChatWebAPIMeshSpawnPath, commands.HandleChatWebAPIMeshSpawn)
		// 网格停止（S16，架构 §5.7）：graceful = 投 /exit 让目标自己收尾，
		// force = 终止进程。治理动作，**默认关闭**：--mesh-allow-stop=false
		// 时回 refused + mesh_stop_not_allowed（端点仍注册，让调用方读得到
		// 原因码，而不是 404）。
		mux.HandleFunc(commands.ChatWebAPIMeshStopPath, commands.HandleChatWebAPIMeshStop)
	}
	mux.HandleFunc(commands.ChatWebAPIScreenPath, commands.HandleChatWebAPIScreen)
	mux.HandleFunc(commands.ChatWebAPIStatusPath, commands.HandleChatWebAPIStatus)
	mux.HandleFunc(commands.ChatWebAPIStatusBarPath, commands.HandleChatWebAPIStatusLine)
	mux.HandleFunc(commands.ChatWebAPIRuntimePath, commands.HandleChatWebAPIRuntime)
	mux.HandleFunc(commands.ChatWebAPIEventsPath, commands.HandleChatWebAPIEvents)
	mux.HandleFunc(commands.ChatWebAPIInputPath, commands.HandleChatWebAPIInput)
	// /web/api/invoke 同步远程调用：一次请求内完成"注入 prompt → 等待 turn
	// 结束 → 返回最终状态与 TUI 渲染"，供脚本/外部 Agent 远程控制会话。
	mux.HandleFunc(commands.ChatWebAPIInvokePath, commands.HandleChatWebAPIInvoke)
	// /web/api/turn turn 后验查询：配合异步 input 拿终态/耗时/token 用量。
	mux.HandleFunc(commands.ChatWebAPITurnPath, commands.HandleChatWebAPITurn)
	// /web/api/token 写令牌读取端点：本机回环 + 同源可读（GET，无需令牌自举），
	// 供脚本/外部 Agent 免解析启动行获取 X-AICLI-Token。
	mux.HandleFunc(commands.ChatWebAPITokenPath, commands.HandleChatWebAPIToken)
	mux.HandleFunc(commands.ChatWebAPISchemaPath, commands.HandleChatWebAPIEventsSchema)
	mux.HandleFunc(commands.ChatWebAPISessionsPath, commands.HandleChatWebAPISessions)
	mux.HandleFunc(commands.ChatWebAPISessionsNewPath, commands.HandleChatWebAPISessionsNew)
	mux.HandleFunc(commands.ChatWebAPISessionsResumePath, commands.HandleChatWebAPISessionsResume)
	mux.HandleFunc(commands.ChatWebAPISessionsDeletePath, commands.HandleChatWebAPISessionsDelete)
	mux.HandleFunc(commands.ChatWebAPISessionsRenamePath, commands.HandleChatWebAPISessionsRename)
	mux.HandleFunc(commands.ChatWebAPIConfigPath, commands.HandleChatWebAPIConfig)
	mux.HandleFunc(commands.ChatWebAPIConfigProvidersPath, commands.HandleChatWebAPIConfigProviders)
	mux.HandleFunc(commands.ChatWebAPIConfigProvidersDeletePath, commands.HandleChatWebAPIConfigProvidersDelete)
	mux.HandleFunc(commands.ChatWebAPIConfigProvidersEnabledPath, commands.HandleChatWebAPIConfigProvidersEnabled)
	mux.HandleFunc(commands.ChatWebAPIConfigProvidersModelsPath, commands.HandleChatWebAPIConfigProvidersFetchModels)
	mux.HandleFunc(commands.ChatWebAPIConfigProvidersProbeModelsPath, commands.HandleChatWebAPIConfigProvidersProbeModels)
	mux.HandleFunc(commands.ChatWebAPIConfigProvidersAutoImportPath, commands.HandleChatWebAPIConfigProvidersAutoImport)
	mux.HandleFunc(commands.ChatWebAPIConfigChatPath, commands.HandleChatWebAPIConfigChat)
	// /web/api/export 会话导出下载：内容与 /export、`aicli export` 同源
	//（同一份格式归一化与写出实现），供顶部菜单栏「文件 → 导出会话」直接下载。
	mux.HandleFunc(commands.ChatWebAPIExportPath, commands.HandleChatWebAPIExport)
	// /web/api/cache/* LLM 缓存分析端点族（cache.analytics.v1）：
	// overview / requests / messages/{id}/trace，数据源为当前会话的本地
	// cacheanalytics.Service（复用 host.EventBus，与 TUI /usage 共用）。
	mux.HandleFunc(commands.ChatWebAPICachePath, commands.HandleChatWebAPICache)
	mux.HandleFunc(commands.ChatWebAPICachePath+"/", commands.HandleChatWebAPICache)
	// /web/api/analysis/* 用量分析端点族（runtime.analytics.v1「分析」页签）：
	// status / tools / subagents / errors，数据源与 TUI /usage tools|subagents|errors
	// 同源（进程内 usageanalytics.Service，同一 usage_analytics.sqlite）。
	mux.HandleFunc(commands.ChatWebAPIAnalysisPath, commands.HandleChatWebAPIAnalysis)
	mux.HandleFunc(commands.ChatWebAPIAnalysisPath+"/", commands.HandleChatWebAPIAnalysis)
	// /web/api/skills[/{name}] 当前会话的 skill catalog（与 TUI /skills 同源：
	// session.FunctionCatalog 的 skill 描述符），供「技能」页签的列表与详情面板。
	mux.HandleFunc(commands.ChatWebAPISkillsPath, commands.HandleChatWebAPISkills)
	mux.HandleFunc(commands.ChatWebAPISkillsPath+"/", commands.HandleChatWebAPISkills)
	// /web/api/mcps[/...] MCP 管理（MCP 页签）：列表 / 新增 / 编辑 / 删除 /
	// 启停 / 热重载；配置读写与 CLI、runtime-server 共用 internal/mcp/admin。
	mux.HandleFunc(commands.ChatWebAPIMCPsPath, commands.HandleChatWebAPIMCPs)
	mux.HandleFunc(commands.ChatWebAPIMCPsPath+"/", commands.HandleChatWebAPIMCP)
	// /web/api/fs/* 文件浏览器（「文件」页签）：roots / list / stat / preview /
	// download / search，作用域根为当前 aicli 会话的工作目录；只读端点。
	mux.HandleFunc(commands.ChatWebAPIFsPath, commands.HandleChatWebAPIFs)
	mux.HandleFunc(commands.ChatWebAPIFsPath+"/", commands.HandleChatWebAPIFs)
	// /web/api/git/* git 浏览（「GIT」页签）：status / diff / commits 只读，
	// stage 为写操作（stage|unstage）；与文件页签共用同一作用域根解析。
	mux.HandleFunc(commands.ChatWebAPIGitPath, commands.HandleChatWebAPIGit)
	mux.HandleFunc(commands.ChatWebAPIGitPath+"/", commands.HandleChatWebAPIGit)
	// style.css / app.js / js/*.js 等静态资源由 HandleChatWebPage 统一伺服
	// （go:embed 嵌入 web/ 目录，按文件名 + 扩展名 Content-Type 返回）。

	// 写令牌：本机 Web/调试端点统一鉴权（Host/Origin 校验 + 写操作令牌）。
	// 服务器开始对外服务：Handler 已通过 EnsureChatWebAuthToken() 在
	// startPprofServer 开始时同步初始化过，此处无需重复。
	// 根路径 "/" 重定向到 /debug/endpoints?format=text，便于浏览器粘贴
	// 包含 token 的基础 URL 直接看到调试信息。
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		target := chatEndpointsPath + "?format=text"
		if r.URL.Query().Get("token") != "" {
			target += "&token=" + url.QueryEscape(r.URL.Query().Get("token"))
		} else if tokenQueryParam != "" {
			target += tokenQueryParam
		}
		http.Redirect(w, r, target, http.StatusSeeOther)
	})
	server := &http.Server{
		// h2c（HTTP/2 cleartext，§4.2.1 连接预算）：
		//
		// 本服务只监听回环明文，但页面要维持两条常驻 SSE（/web/api/events 与
		// /web/api/mesh/events）。HTTP/1.1 下浏览器对单 host 的并发连接上限约
		// 6 条，多开两个标签页就占满连接池，之后所有 /web/api/* 请求在浏览器侧
		// 永久排队——页面既不报错也不恢复（表现为「打不开 / 卡死」）。
		//
		// h2c 让支持明文 HTTP/2 的客户端（Go 客户端、aicli 自身、脚本、代理）
		// 把多条流复用到一个 TCP 连接；不支持 h2c 的客户端（浏览器对明文 HTTP/2
		// 尚不启用）继续走 HTTP/1.1，行为与之前逐字一致——因此这里是纯增量能力，
		// 不是「必须协商成功」的依赖。浏览器侧的连接预算由
		// /web/api/events?mesh=1 单连接合并解决（见 HandleChatWebAPIEvents）。
		Handler: newChatWebH2CHandler(commands.ChatWebAuthGuard(mux.ServeHTTP)),
		// 本地诊断端点：读请求头超时收紧，避免残留连接占用；
		// profile 下载期属于 body 读取，不受此限制影响。
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = server.Serve(ln)
	}()

	return &pprofServerHandle{server: server, addr: ln.Addr().String(), tokenQueryParam: tokenQueryParam}, nil
}
