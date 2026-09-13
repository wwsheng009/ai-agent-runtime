package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// P0-4 周边（可见性）：/debug supervision watchdog 输出本地执行看门狗状态。
//
// 该渲染是严格只读的：不会惰性构建 supervisor（不会启动后台巡检），也不会
// 修改 supervision store。它回答四个问题：
//  1. 本地是否具备 durable control plane（能否巡检）；
//  2. 看门狗是否已构建、循环是否在跑；
//  3. 生效的 mode 与阈值（与 API 同源）；
//  4. 最近一次扫描/决策/完成出件的结果。

// chatDebugSupervisorUsageText documents the watchdog subcommand.
func chatDebugSupervisorUsageText() string {
	return strings.Join([]string{
		"/debug supervision watchdog",
		"    打印本地执行看门狗（P0-4）状态：是否接线、循环是否运行、mode 与阈值、累计扫描/决策/强制动作数、最近一次决策与完成出件。只读，不启动巡检。",
	}, "\n")
}

// chatDebugSupervisorSubcommand reports whether the parsed subcommand selects
// the read-only watchdog snapshot.
func chatDebugSupervisorSubcommand(subcommand string) bool {
	switch strings.ToLower(strings.TrimSpace(subcommand)) {
	case "watchdog", "supervisor", "execution-supervisor":
		return true
	default:
		return false
	}
}

// chatDebugExecutionSupervisorText renders the local watchdog snapshot for the
// current session host.
func chatDebugExecutionSupervisorText(ctx context.Context, session *ChatSession) string {
	if session == nil || session.LocalRuntimeHost == nil {
		return "执行看门狗（P0-4）：未接线\n  当前会话没有本地运行时宿主，无法读取看门狗状态。"
	}
	host := session.LocalRuntimeHost
	if !host.localExecutionSupervisorAvailable() {
		return strings.Join([]string{
			"执行看门狗（P0-4）：未接线",
			"  原因：本地宿主没有 durable supervision control plane（supervision store 缺失或未实现 ExecutionRunStore）。",
			"  影响：本地 spawn 的子 agent 不受 deadline / progress / approval 巡检（与 P0-4 实施前一致）。",
			"  启用条件：为本地宿主配置 supervision store 后自动就绪，默认 mode=observe。",
			"  模式覆盖：AICLI_EXECUTION_SUPERVISOR_MODE=enforce（与 API 一致的 interrupt + cancel grace）。",
		}, "\n")
	}
	supervisor := host.peekLocalExecutionSupervisor()
	if supervisor == nil {
		cfg := localExecutionSupervisorConfig(host.supervisionConfig)
		return strings.Join([]string{
			"执行看门狗（P0-4）：已就绪（尚未启动）",
			"  说明：首次 spawn_agent 时惰性构建并启动后台巡检；本次渲染为只读，未启动。",
			"  " + chatDebugSupervisorConfigLine(cfg, localExecutionSupervisorMode()),
			"  模式覆盖：AICLI_EXECUTION_SUPERVISOR_MODE=enforce。",
		}, "\n")
	}
	stats := supervisor.Stats()
	lines := []string{
		"执行看门狗（P0-4）：" + chatDebugSupervisorLifecycleLabel(stats, supervisor),
		"  " + chatDebugSupervisorStatsLine(stats),
		"  " + chatDebugSupervisorConfigLine(supervisor.Config, stats.Mode),
		"  累计：scans=" + fmt.Sprintf("%d", stats.Scans) +
			" decisions=" + fmt.Sprintf("%d", stats.Decisions) +
			" enforced=" + fmt.Sprintf("%d", stats.Enforced),
		"  最近扫描：" + chatDebugSupervisorLastScanLine(stats),
	}
	for _, decision := range stats.LastScanDecisions {
		lines = append(lines, "    "+formatChatDebugSupervisorDecision(decision))
	}
	if !stats.LastDispatchAt.IsZero() {
		lines = append(lines, fmt.Sprintf("  最近完成出件：%s delivered=%d failed=%d",
			stats.LastDispatchAt.UTC().Format(time.RFC3339), stats.LastDispatchDelivered, stats.LastDispatchFailed))
	}
	if extra := chatDebugSupervisorStoreLine(ctx, host, session); extra != "" {
		lines = append(lines, extra)
	}
	return strings.Join(lines, "\n")
}

func chatDebugSupervisorLifecycleLabel(stats supervision.ExecutionSupervisorStats, supervisor *supervision.ExecutionSupervisor) string {
	if !supervisor.Config.Enabled {
		return "已构建（未启用）"
	}
	if stats.LoopRunning {
		return "运行中"
	}
	return "已构建（循环未运行）"
}

func chatDebugSupervisorStatsLine(stats supervision.ExecutionSupervisorStats) string {
	mode := strings.TrimSpace(stats.Mode)
	if mode == "" {
		mode = "enforce"
	}
	return fmt.Sprintf("循环=%s mode=%s", chatDebugSupervisorLoopLabel(stats), mode)
}

func chatDebugSupervisorLoopLabel(stats supervision.ExecutionSupervisorStats) string {
	if stats.LoopRunning {
		return "running"
	}
	return "idle"
}

func chatDebugSupervisorConfigLine(cfg supervision.ExecutionSupervisorConfig, mode string) string {
	effective := cfg
	defaults := supervision.DefaultExecutionSupervisorConfig()
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

func chatDebugSupervisorLastScanLine(stats supervision.ExecutionSupervisorStats) string {
	if stats.LastScanAt.IsZero() {
		return "尚未扫描"
	}
	detail := fmt.Sprintf("%s（%d 条决策）",
		stats.LastScanAt.UTC().Format(time.RFC3339), len(stats.LastScanDecisions))
	if strings.TrimSpace(stats.LastScanError) != "" {
		detail += " 错误=" + strings.TrimSpace(stats.LastScanError)
	}
	return detail
}

func formatChatDebugSupervisorDecision(decision supervision.RunDecision) string {
	detail := fmt.Sprintf("决策：run=%s session=%s status=%s decision=%s action=%s",
		decision.RunID, decision.SessionID, decision.Status, decision.Decision, decision.ActionTaken)
	if strings.TrimSpace(decision.Reason) != "" {
		detail += " reason=" + strings.TrimSpace(decision.Reason)
	}
	return detail
}

// chatDebugSupervisorStoreLine adds the durable view (active runs for this
// session and the undelivered completion outbox). Store errors degrade to an
// inline note instead of failing the whole command.
func chatDebugSupervisorStoreLine(ctx context.Context, host *localChatRuntimeHost, session *ChatSession) string {
	runStore, ok := host.Supervision.Store.(supervision.ExecutionRunStore)
	if !ok || runStore == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if sessionID := strings.TrimSpace(currentRuntimeSessionID(session)); sessionID != "" {
		runs, err := runStore.ListActiveExecutionRuns(ctx, 200)
		if err != nil {
			parts = append(parts, "未终态 run 读取失败: "+err.Error())
		} else {
			owned := 0
			for _, run := range runs {
				if strings.TrimSpace(run.ParentSessionID) == sessionID || strings.TrimSpace(run.SessionID) == sessionID {
					owned++
				}
			}
			parts = append(parts, fmt.Sprintf("未终态 run=%d（本会话 %d）", len(runs), owned))
		}
	}
	if pending, err := runStore.ListUndeliveredOutbox(ctx, 100); err != nil {
		parts = append(parts, "完成出件读取失败: "+err.Error())
	} else {
		parts = append(parts, fmt.Sprintf("未投递完成出件=%d", len(pending)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "  store：" + strings.Join(parts, "；")
}
