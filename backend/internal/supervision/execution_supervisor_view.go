package supervision

import (
	"fmt"
	"strings"
	"time"
)

// ExecutionSupervisorSnapshot is the host-neutral read-only watchdog view.
// Available/Built distinguish "no durable control plane" from "control plane
// exists but the loop has not been lazily constructed yet".
type ExecutionSupervisorSnapshot struct {
	Available bool
	Built     bool
	Config    ExecutionSupervisorConfig
	Stats     ExecutionSupervisorStats
}

// Render produces the operator-facing snapshot shared by CLI and runtime-server.
func (s ExecutionSupervisorSnapshot) Render() string {
	if !s.Available {
		return strings.Join([]string{
			"执行看门狗（P0-4）：未接线",
			"  原因：宿主没有 durable supervision control plane（supervision store 缺失或未实现 ExecutionRunStore）。",
			"  影响：spawn 的子 agent 不受 deadline / progress / approval 巡检。",
			"  启用条件：配置 supervision store 后自动就绪，默认 mode=enforce。",
		}, "\n")
	}
	if !s.Built {
		cfg := s.Config
		if !cfg.Enabled {
			cfg = DefaultExecutionSupervisorConfig()
			cfg.Enabled = true
		}
		return strings.Join([]string{
			"执行看门狗（P0-4）：已就绪（尚未启动）",
			"  说明：首次 subagent_spawn 时惰性构建并启动后台巡检。",
			"  " + supervisorConfigLine(cfg, cfg.Mode),
		}, "\n")
	}
	stats := s.Stats
	lines := []string{
		"执行看门狗（P0-4）：" + supervisorLifecycleLabel(s.Config, stats),
		"  " + supervisorStatsLine(stats),
		"  " + supervisorConfigLine(s.Config, stats.Mode),
		fmt.Sprintf("  累计：scans=%d decisions=%d enforced=%d", stats.Scans, stats.Decisions, stats.Enforced),
		"  最近扫描：" + supervisorLastScanLine(stats),
	}
	for _, decision := range stats.LastScanDecisions {
		lines = append(lines, "    "+formatSupervisorDecision(decision))
	}
	if !stats.LastDispatchAt.IsZero() {
		lines = append(lines, fmt.Sprintf("  最近完成出件：%s delivered=%d failed=%d",
			stats.LastDispatchAt.UTC().Format(time.RFC3339), stats.LastDispatchDelivered, stats.LastDispatchFailed))
	}
	return strings.Join(lines, "\n")
}

func supervisorLifecycleLabel(cfg ExecutionSupervisorConfig, stats ExecutionSupervisorStats) string {
	if !cfg.Enabled {
		return "已构建（未启用）"
	}
	if stats.LoopRunning {
		return "运行中"
	}
	return "已构建（循环未运行）"
}

func supervisorStatsLine(stats ExecutionSupervisorStats) string {
	mode := strings.TrimSpace(stats.Mode)
	if mode == "" {
		mode = "enforce"
	}
	loop := "idle"
	if stats.LoopRunning {
		loop = "running"
	}
	return fmt.Sprintf("循环=%s mode=%s", loop, mode)
}

func supervisorConfigLine(cfg ExecutionSupervisorConfig, mode string) string {
	effective := cfg
	defaults := DefaultExecutionSupervisorConfig()
	if effective.ScanInterval <= 0 {
		effective.ScanInterval = defaults.ScanInterval
	}
	if effective.DefaultExecutionTimeout <= 0 {
		effective.DefaultExecutionTimeout = defaults.DefaultExecutionTimeout
	}
	if effective.DefaultProgressTimeout <= 0 {
		effective.DefaultProgressTimeout = defaults.DefaultProgressTimeout
	}
	if effective.DefaultApprovalTimeout <= 0 {
		effective.DefaultApprovalTimeout = defaults.DefaultApprovalTimeout
	}
	if effective.DefaultCancelGrace <= 0 {
		effective.DefaultCancelGrace = defaults.DefaultCancelGrace
	}
	if effective.StoreOutageGrace <= 0 {
		effective.StoreOutageGrace = defaults.StoreOutageGrace
	}
	if strings.TrimSpace(mode) == "" {
		mode = effective.Mode
	}
	if strings.TrimSpace(mode) == "" {
		mode = defaults.Mode
	}
	return fmt.Sprintf("mode=%s scan=%s execution_deadline=%s progress_deadline=%s approval_deadline=%s cancel_grace=%s store_outage_grace=%s",
		mode,
		effective.ScanInterval,
		effective.DefaultExecutionTimeout,
		effective.DefaultProgressTimeout,
		effective.DefaultApprovalTimeout,
		effective.DefaultCancelGrace,
		effective.StoreOutageGrace,
	)
}

func supervisorLastScanLine(stats ExecutionSupervisorStats) string {
	if stats.LastScanAt.IsZero() {
		return "尚未扫描"
	}
	detail := fmt.Sprintf("%s（%d 条决策）", stats.LastScanAt.UTC().Format(time.RFC3339), len(stats.LastScanDecisions))
	if strings.TrimSpace(stats.LastScanError) != "" {
		detail += " 错误=" + strings.TrimSpace(stats.LastScanError)
	}
	return detail
}

func formatSupervisorDecision(decision RunDecision) string {
	detail := fmt.Sprintf("决策：run=%s session=%s status=%s decision=%s action=%s",
		decision.RunID, decision.SessionID, decision.Status, decision.Decision, decision.ActionTaken)
	if strings.TrimSpace(decision.Reason) != "" {
		detail += " reason=" + strings.TrimSpace(decision.Reason)
	}
	return detail
}
