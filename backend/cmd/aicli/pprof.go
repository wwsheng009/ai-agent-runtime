package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// pprofServerHandle 持有按需启动的 pprof HTTP 服务器。
// 默认监听 127.0.0.1 上的随机空闲端口；仅本机可访问，
// 避免把可触发 GC / 执行分析代码的端点暴露到网络上。
type pprofServerHandle struct {
	server *http.Server
	addr   string
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

// Addr 返回实际绑定地址（host:port）。
func (h *pprofServerHandle) Addr() string {
	if h == nil {
		return ""
	}
	return h.addr
}

// URL 返回 pprof 索引页面的完整 URL。
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

// Close 关闭服务器并释放监听端口。
func (h *pprofServerHandle) Close() error {
	if h == nil || h.server == nil {
		return nil
	}
	return h.server.Close()
}

// startPprofServer 启动 pprof HTTP 服务器。
// addr 为空时使用 127.0.0.1:0（随机空闲端口）；传入其他地址时按原样监听，
// 但调用方应保证其绑定在 loopback 上。
func startPprofServer(addr string) (*pprofServerHandle, error) {
	if strings.TrimSpace(addr) == "" {
		addr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("pprof listen on %s: %w", addr, err)
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
	// （Diagnosis：idle / healthy / backoff_engaged / dead_guard）——
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
	mux.HandleFunc(chatDisplayPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "text" {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(commands.BuildChatDebugDisplayText()))
			return
		}
		body, err := commands.MarshalChatDebugDisplayJSON()
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
	mux.HandleFunc(commands.ChatWebAPIScreenPath, commands.HandleChatWebAPIScreen)
	mux.HandleFunc(commands.ChatWebAPIStatusPath, commands.HandleChatWebAPIStatus)
	mux.HandleFunc(commands.ChatWebAPIEventsPath, commands.HandleChatWebAPIEvents)
	mux.HandleFunc(commands.ChatWebAPIInputPath, commands.HandleChatWebAPIInput)
	mux.HandleFunc(commands.ChatWebAPISchemaPath, commands.HandleChatWebAPIEventsSchema)
	mux.HandleFunc(commands.ChatWebAPISessionsPath, commands.HandleChatWebAPISessions)
	mux.HandleFunc(commands.ChatWebAPISessionsResumePath, commands.HandleChatWebAPISessionsResume)

	server := &http.Server{
		Handler: mux,
		// 本地诊断端点：读请求头超时收紧，避免残留连接占用；
		// profile 下载期属于 body 读取，不受此限制影响。
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = server.Serve(ln)
	}()

	return &pprofServerHandle{server: server, addr: ln.Addr().String()}, nil
}
