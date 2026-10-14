// runtime-server 的 mesh 入网（P2 #8 后半，docs/bots/09 §7.5 切片 A/B）：
//
//	节点身份：NodeID = "runtime-server"（单进程部署即唯一实例，与 connectors
//	Deps.HostID 同源）；Kind = "runtime-server"；
//	Capabilities = ["mesh", "runner", "poller"]——runner（可作为派发目标执行
//	connector turn）+ poller（可持有通道轮询归属）。
//
//	生命周期：listener 建立后 Start（endpoint 可知），进程退出前 Stop——
//	mesh Host 心跳自续；降级纪律与 aicli 一致：mesh 不可用只 warn，
//	绝不影响 runtime-server 主功能（连接器派发/注意力扇出退化为单机模式）。
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// startRuntimeServerMeshHost 创建并启动 runtime-server 的 mesh 节点档案。
// baseURL 是本服务对外可达地址（listener addr 已解析）；失败只返回 host 并
// 记 warn（调用方继续启动；未写档时 peers 视图看不到本节点，连接器派发/
// 注意力扇出退化为单机模式——与 aicli 的 --mesh=false 降级同口径）。
func startRuntimeServerMeshHost(baseURL string, warn func(format string, args ...any)) *mesh.Host {
	if warn == nil {
		warn = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "runtime-server: mesh: "+format+"\n", args...)
		}
	}
	info := buildinfo.Backend()
	host := mesh.NewHost(mesh.HostConfig{
		Kind:          "runtime-server",
		Origin:        "cli",
		Version:       info.Version,
		BuildTime:     info.BuildTime,
		Exe:           runtimeServerExecutablePath(),
		WorkspacePath: runtimeServerWorkspacePath(),
		WorkspaceName: runtimeServerWorkspaceName(),
		// runner/poller 能力位（docs/bots/09 §7.5 切片 A）：本节点可作为
		// connector turn 执行体（派发目标）与轮询归属持有者。
		Capabilities: []string{"mesh", "runner", "poller"},
	})
	mesh.SetCurrent(host)
	if err := host.Start(); err != nil {
		warn("node record not written: %v", err)
		return host
	}
	if endpoint, ok := runtimeServerMeshEndpoint(baseURL); ok {
		host.SetEndpoint(endpoint, mesh.AuthInfo{})
	}
	return host
}

// stopRuntimeServerMeshHost 停止 mesh 心跳并移除节点档案（尽力，不阻塞退出；
// Target 用自身 node-id，graceful 默认）。
func stopRuntimeServerMeshHost(host *mesh.Host) {
	if host == nil {
		return
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	nodeID := host.NodeID()
	_ = host.Stop(stopCtx, mesh.StopRequest{Target: nodeID})
}

// runtimeServerMeshEndpoint 把 listener 地址转成 node record 的 endpoint 段；
// 通配监听取第一个 LAN 地址（与 aicli meshEndpointForListenAddr 同语义）。
func runtimeServerMeshEndpoint(baseURL string) (mesh.EndpointInfo, bool) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return mesh.EndpointInfo{}, false
	}
	scheme := "http"
	if strings.HasPrefix(baseURL, "https://") {
		scheme = "https"
	}
	addr := strings.TrimPrefix(strings.TrimPrefix(baseURL, "https://"), "http://")
	listenHost := "127.0.0.1"
	port := 0
	if idx := strings.LastIndex(addr, ":"); idx > 0 {
		if p, err := strconv.Atoi(addr[idx+1:]); err == nil {
			port = p
		}
		if h := addr[:idx]; h != "" && h != "0.0.0.0" && h != "::" {
			listenHost = h
		}
	}
	if port == 0 {
		return mesh.EndpointInfo{}, false
	}
	base := strings.TrimRight(scheme+"://"+listenHost+":"+strconv.Itoa(port), "/")
	return mesh.EndpointInfo{
		Scheme:     scheme,
		Host:       listenHost,
		Port:       port,
		Loopback:   listenHost == "127.0.0.1" || listenHost == "localhost",
		BaseURL:    base,
		WebBaseURL: base + "/web",
	}, true
}

// runtimeServerWorkspacePath/Name 与 aicli 同口径（进程 cwd）。
func runtimeServerWorkspacePath() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func runtimeServerWorkspaceName() string {
	wd := runtimeServerWorkspacePath()
	if wd == "" {
		return ""
	}
	idx := strings.LastIndexAny(wd, `/\`)
	if idx < 0 {
		return wd
	}
	return wd[idx+1:]
}

func runtimeServerExecutablePath() string {
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		return exe
	}
	if len(os.Args) > 0 {
		return os.Args[0]
	}
	return ""
}
