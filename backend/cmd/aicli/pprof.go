package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// pprofServerHandle 持有按需启动的 pprof HTTP 服务器。
// 默认监听 127.0.0.1 上的随机空闲端口；仅本机可访问，
// 避免把可触发 GC / 执行分析代码的端点暴露到网络上。
type pprofServerHandle struct {
	server *http.Server
	addr   string
}

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
	// frame 错误、backoff 是否 arm/触发——精确回答"executor 在重放什么、为什么
	// 没有收敛"。未设置 provider 时返回空快照。
	mux.HandleFunc("/debug/pprof/executor", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(ui.ExecutorDiagSnapshot())
	})

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
