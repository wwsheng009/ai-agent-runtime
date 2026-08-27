package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strings"
	"time"
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

// resolveRuntimeServerPprofAddr 解析 pprof 监听地址（与 aicli 行为一致）：
//  1. AICLI_PPROF 环境变量非空时直接启用，并将其作为指定地址；
//  2. 否则 --pprof 开启时默认 127.0.0.1:0（随机空闲端口）；
//  3. 两者都未设置时返回空串（不启动 pprof 服务器）。
func resolveRuntimeServerPprofAddr(pprofFlag bool) string {
	if env := strings.TrimSpace(os.Getenv("AICLI_PPROF")); env != "" {
		return env
	}
	if pprofFlag {
		return "127.0.0.1:0"
	}
	return ""
}

// isLoopbackAddr 判断 addr（host:port）是否绑定在本机回环地址上。
// 用于对显式指定的非 loopback pprof 地址发出告警。
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
