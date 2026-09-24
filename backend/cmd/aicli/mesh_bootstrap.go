package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/commands"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// 本文件是 aicli 进程加入 mesh（多进程网格）的启动/退出接线，对应实施方案
// S2。三条硬规则：
//
//  1. 网格初始化必须早于 loopback 服务器：进程一启动就写节点档案，endpoint
//     在监听成功后补写（architecture §4.1）；
//  2. --mesh=false 完全短路：不写档案、不注册 mesh 端点、不订阅事件；
//  3. 网格不可用（无法解析网格根目录、档案写失败）只降级为 stderr warning，
//     绝不影响 chat 本身（MN1 / §4.7 降级矩阵）。

// meshEnvSpawnedBy is set by a spawning node (S9) so the child can record its
// parent in the node record.
const meshEnvSpawnedBy = "AICLI_MESH_SPAWNED_BY"

// meshNodeCommands are the long-lived interactive commands that join the mesh.
// One-shot commands (version / stats / mcp ...) stay invisible on purpose:
// a node record is only useful for processes another node can talk to.
var meshNodeCommands = map[string]bool{
	"chat":   true,
	"resume": true,
}

// shouldJoinMesh reports whether the resolved command is a mesh participant.
func shouldJoinMesh(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	return meshNodeCommands[strings.TrimSpace(cmd.Name())]
}

// startMeshHost builds and starts this process's mesh host. It never fails:
// degraded setups produce warnings and an inert host.
func startMeshHost(cmd *cobra.Command) *mesh.Host {
	host := mesh.NewHost(mesh.HostConfig{
		Kind:          "chat",
		Origin:        meshOriginForCommand(cmd),
		SpawnedBy:     strings.TrimSpace(os.Getenv(meshEnvSpawnedBy)),
		Version:       version,
		BuildTime:     buildTime,
		Exe:           meshExecutablePath(),
		WorkspacePath: meshWorkspacePath(),
		WorkspaceName: meshWorkspaceName(),
		// 治理开关（§5.7）：--mesh-allow-stop=true 时档案声明 CapabilityStop，
		// 让不经本进程 HTTP 层的调用方（aicli-mesh stop 本地编排）也能 fail
		// closed 地判定「谁能停我」。main.go 在 startMeshHost 之前已 apply 过
		// 治理开关，此处读到的一定是本次启动的取值。
		StopAllowed: commands.ChatWebMeshAllowStop(),
		Warn:        meshWarn,
	})
	mesh.SetCurrent(host)
	if err := host.Start(); err != nil {
		meshWarn("node record not written: %v", err)
	}
	// S7：控制面就绪即启动 peer 订阅循环与本进程事件的扇入转发（§6.2 / §6.3）。
	// 两者都只增强实时性：mesh 不可用（--mesh=false / 无网格根目录）时静默跳过，
	// 任何 peer 故障都只降级为 warning（MN1 / §4.7）。
	if fanin := host.Fanin(); fanin != nil && fanin.Enabled() {
		host.StartPeerSync(mesh.PeerSyncConfig{Warn: meshWarn})
		commands.StartChatWebMeshLocalRelay(host)
	}
	return host
}

// meshWarn 是网格降级路径的统一 warning 出口。
//
// 交互式会话期间投递给 TUI（语义补充 cell，由 TerminalSession 渲染）；
// 其余情况（启动阶段 / 非交互 / JSON 模式）回退 stderr。直接写 stderr 会
// 落在 FixedBottomSurface 的底部保留区上，把状态栏覆盖成半截文本。
func meshWarn(format string, args ...any) {
	line := "Warning: mesh: " + fmt.Sprintf(format, args...)
	if commands.NotifyChatDiagnostic(line) {
		return
	}
	fmt.Fprintln(os.Stderr, line)
}

// meshOriginForCommand answers "who started this process" (cli / resume / mesh).
func meshOriginForCommand(cmd *cobra.Command) string {
	if strings.TrimSpace(os.Getenv(meshEnvSpawnedBy)) != "" {
		return "mesh"
	}
	if cmd != nil && strings.TrimSpace(cmd.Name()) == "resume" {
		return "resume"
	}
	return "cli"
}

// meshEndpointForListenAddr builds the endpoint section of the node record for
// the loopback server that just started listening. A wildcard listen address is
// replaced by the first LAN address: other nodes must be able to dial it.
func meshEndpointForListenAddr(listenHost string, port int) mesh.EndpointInfo {
	scheme := "http"
	advertised := strings.TrimSpace(listenHost)
	if isWildcardListenHost(advertised) {
		if addrs := commands.ChatWebLocalAddresses(); len(addrs) > 0 {
			advertised = strings.TrimSpace(addrs[0])
		}
	}
	if advertised == "" {
		advertised = "127.0.0.1"
	}
	baseURL := fmt.Sprintf("%s://%s:%d", scheme, advertised, port)
	return mesh.EndpointInfo{
		Scheme:      scheme,
		Host:        advertised,
		Port:        port,
		Loopback:    commands.ChatWebHostIsLoopback(advertised),
		BaseURL:     baseURL,
		WebBaseURL:  baseURL + "/web",
		ManifestURL: baseURL + "/debug/endpoints",
	}
}

// meshAuthForListenAddr mirrors the write-token policy of the loopback server
// (architecture §3.1: loopback-dev / loopback-strict / lan).
func meshAuthForListenAddr(listenHost string) mesh.AuthInfo {
	loopback := commands.ChatWebHostIsLoopback(strings.TrimSpace(listenHost))
	devMode := commands.IsChatWebDevMode()
	mode := "loopback-strict"
	switch {
	case !loopback:
		mode = "lan"
	case devMode:
		mode = "loopback-dev"
	}
	return mesh.AuthInfo{
		Mode:     mode,
		Required: !(loopback && devMode),
		Token:    commands.EnsureChatWebAuthToken(),
	}
}

func isWildcardListenHost(host string) bool {
	switch strings.TrimSpace(host) {
	case "0.0.0.0", "::", "[::]", "":
		return true
	default:
		return false
	}
}

func meshWorkspacePath() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func meshWorkspaceName() string {
	wd := meshWorkspacePath()
	if wd == "" {
		return ""
	}
	return filepath.Base(wd)
}

func meshExecutablePath() string {
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		return exe
	}
	if len(os.Args) > 0 {
		return os.Args[0]
	}
	return ""
}
