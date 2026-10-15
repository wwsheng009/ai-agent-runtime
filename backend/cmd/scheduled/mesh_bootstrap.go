package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

// startDaemonMeshHost 让 scheduled 以"无 endpoint 的 mesh 节点"入网（S1 / M4）：
// 节点档案负责在岗可见性（kind=scheduled + capabilities 含 scheduler），但
// **不注册任何 HTTP 端点**——daemon 只被看见、不被 mesh.Call 调用。mesh 不可用
// 只降级为 warning（与 aicli / runtime-server 同纪律，MN1）。
func startDaemonMeshHost() *mesh.Host {
	info := buildinfo.Backend()
	host := mesh.NewHost(mesh.HostConfig{
		Kind:          "scheduled",
		Origin:        "cli",
		Version:       info.Version,
		BuildTime:     info.BuildTime,
		Exe:           daemonExecutablePath(),
		WorkspacePath: daemonWorkspacePath(),
		WorkspaceName: daemonWorkspaceName(),
		// S3（§4.2）：除能力位外携带 store 兼容档——aicli-mesh 与在岗检查
		// 据此判定"在岗者能否跑当前 store"。
		Capabilities: []string{
			"mesh",
			schedulehost.CapabilityScheduler,
			schedulehost.SchedulerCapability(schedule.SupportedSchemaVersion()),
		},
		Warn: func(format string, args ...any) {
			logf("scheduled: mesh: "+format, args...)
		},
	})
	if err := host.Start(); err != nil {
		logf("scheduled: warning: mesh node record not written (%v); this scheduler stays invisible to aicli-mesh views", err)
	}
	return host
}

// stopDaemonMeshHost 退出时移档（尽力，不阻塞退出）。
func stopDaemonMeshHost(host *mesh.Host) {
	if host == nil {
		return
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = host.Stop(stopCtx, mesh.StopRequest{Target: host.NodeID()})
}

func daemonExecutablePath() string {
	if exe, err := os.Executable(); err == nil && strings.TrimSpace(exe) != "" {
		return exe
	}
	if len(os.Args) > 0 {
		return os.Args[0]
	}
	return ""
}

func daemonWorkspacePath() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func daemonWorkspaceName() string {
	wd := daemonWorkspacePath()
	if wd == "" {
		return ""
	}
	return filepath.Base(wd)
}
