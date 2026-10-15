package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedule"
	"github.com/wwsheng009/ai-agent-runtime/internal/schedulehost"
)

// runDoctorCommand 自检（设计文档 scheduler-daemon-design.md §3.1）：
// store 可达 + 待触发数、单实例租约状态、runtime-server 连通性。
// 只读，不取租约、不启动调度。
func runDoctorCommand(configPath, flagStore, flagDataDir, runtimeServer string) error {
	cfg, err := loadScheduleConfig(configPath, runtimeServer)
	if err != nil {
		return err
	}
	path, err := resolveStorePath(flagStore, flagDataDir, cfg)
	if err != nil {
		return err
	}

	report := map[string]any{
		"store":          path,
		"mode":           cfg.Execution.Mode,
		"runtime_server": cfg.Execution.RuntimeServerURL,
	}

	if store, err := schedule.OpenStore(path); err != nil {
		report["store_ok"] = false
		report["store_error"] = err.Error()
	} else {
		report["store_ok"] = true
		now := schedule.SystemClock()
		if due, err := store.ListActiveDue(now); err != nil {
			report["due_error"] = err.Error()
		} else {
			report["due_count"] = len(due)
		}
		if tasks, err := store.List(); err != nil {
			report["task_error"] = err.Error()
		} else {
			active := 0
			for _, task := range tasks {
				if task.Status == schedule.StatusActive {
					active++
				}
			}
			report["task_count"] = len(tasks)
			report["active_count"] = active
		}
		_ = store.Close()
	}

	report["lease"] = schedulerLeaseStatus()
	report["runtime_server_ok"] = probeRuntimeServer(cfg.Execution.RuntimeServerURL)

	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, string(out))
	return nil
}

// schedulerLeaseStatus 只读观察调度租约（不取租约、不续租）。
func schedulerLeaseStatus() map[string]any {
	paths := mesh.ResolvePaths()
	if strings.TrimSpace(paths.Root) == "" {
		return map[string]any{"available": false, "reason": "mesh root unavailable"}
	}
	lease, ok := mesh.ReadLease(paths.LeasePath(schedulehost.LeasePurpose, schedulehost.LeaseKey))
	if !ok {
		return map[string]any{"available": true, "held": false}
	}
	return map[string]any{
		"available":     true,
		"held":          true,
		"owner_node_id": lease.OwnerNodeID,
		"owner_pid":     lease.OwnerPID,
		"epoch":         lease.Epoch,
		"stale":         lease.Stale(mesh.NowUTC()),
	}
}

// probeRuntimeServer 探测 runtime-server 是否可达（任何 HTTP 响应都算可达）。
func probeRuntimeServer(baseURL string) bool {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}
