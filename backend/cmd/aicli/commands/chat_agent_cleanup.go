package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// chatAgentCleanupOptions is the parsed /agents cleanup request (plan §P2-9 方案
// 3: a manual, one-shot eviction entry so an operator can release thread quota
// without waiting for the next reconcile pass or for a spawn to trip the limit).
type chatAgentCleanupOptions struct {
	// DryRun previews the reclaim set without closing anything.
	DryRun bool
	// Idle enables the opt-in idle eviction for children idle at least this
	// long. Zero keeps the conservative default: only containers that are
	// provably gone (session_missing) or terminal (session_terminal).
	Idle time.Duration
}

func chatAgentCleanupUsageText() string {
	return strings.Join([]string{
		"用法: /agents cleanup [--dry-run] [--idle <时长>]",
		"  回收当前会话 root scope 内可安全关闭的子 agent 行，释放 agents.maxThreads 配额。",
		"  --dry-run      只预览可回收对象，不执行回收。",
		"  --idle <时长>  额外回收空闲超过该时长的子 agent（如 30m；默认关闭）。",
		"  运行中 / 等审批 / 等输入 / 有后台任务的子 agent 永不回收。",
	}, "\n")
}

func parseChatAgentCleanupOptions(argument string) (chatAgentCleanupOptions, error) {
	opts := chatAgentCleanupOptions{}
	fields := splitChatCommandFields(argument)
	for index := 0; index < len(fields); index++ {
		field := strings.ToLower(strings.TrimSpace(fields[index]))
		switch field {
		case "", "cleanup", "prune", "gc":
			// verb / alias
		case "--dry-run", "dry-run", "dryrun", "preview", "-n":
			opts.DryRun = true
		case "--idle", "idle":
			if index+1 >= len(fields) {
				return opts, fmt.Errorf("--idle 需要时长参数（如 30m）")
			}
			index++
			duration, err := time.ParseDuration(strings.TrimSpace(fields[index]))
			if err != nil || duration <= 0 {
				return opts, fmt.Errorf("无效的 --idle 时长: %s", fields[index])
			}
			opts.Idle = duration
		default:
			return opts, fmt.Errorf("未知参数: %s", fields[index])
		}
	}
	return opts, nil
}

// executeStructuredAgentCleanupCommand is the unified-pipeline entry.
func executeStructuredAgentCleanupCommand(session *ChatSession, argument string) CommandResult {
	text, err := runChatAgentCleanupCommand(session, argument)
	if err != nil {
		return commandErrorResult(err)
	}
	return commandTextResult(text)
}

// handleChatAgentCleanupCommand is the legacy plain-terminal entry. It must
// print through printChatCommandOutput (never fmt.Print* directly) so the
// direct-writer inventory and the unified output boundary stay intact.
func handleChatAgentCleanupCommand(session *ChatSession, argument string) {
	text, err := runChatAgentCleanupCommand(session, argument)
	if err != nil {
		printfChatCommandOutput(session, "错误: %v", err)
		return
	}
	printChatCommandOutput(session, text)
}

// runChatAgentCleanupCommand evaluates the root-scope reclaim set through the
// exact same decision path as the spawn gate (QuotaChildren ->
// observeLocalQuotaChildren -> SelectReclaimable -> ReclaimAgentQuota), so a
// manual cleanup can never close something a spawn would refuse to evict, and
// every eviction is written with the reclaimed:<reason> event kind.
func runChatAgentCleanupCommand(session *ChatSession, argument string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}
	opts, err := parseChatAgentCleanupOptions(argument)
	if err != nil {
		return chatAgentCleanupUsageText() + "\n错误: " + err.Error(), nil
	}
	host := session.LocalRuntimeHost
	if host == nil || host.ActorRegistry == nil || host.ActorRegistry.localAgentRegistryStore() == nil {
		return chatAgentCleanupUsageText() + "\nreclaim=unavailable（宿主未装配 durable agent registry，无需清理）", nil
	}
	registry := host.ActorRegistry
	store := registry.localAgentRegistryStore()
	reclaimStore, ok := store.(agentcontrol.AgentReclaimStore)
	if !ok || reclaimStore == nil {
		return chatAgentCleanupUsageText() + "\nreclaim=unavailable（store 不支持按回收原因关闭）", nil
	}
	parentSessionID := ""
	if session.RuntimeSession != nil {
		parentSessionID = strings.TrimSpace(session.RuntimeSession.ID)
	}
	if parentSessionID == "" {
		return "", fmt.Errorf("当前会话缺少 session id，无法定位 root scope")
	}
	ctx := context.Background()
	rootSessionID, _, err := registry.localAgentRegistryRootAndPath(ctx, parentSessionID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(rootSessionID) == "" {
		rootSessionID = parentSessionID
	}
	// Refresh the projection first: the cleanup must judge the same snapshot a
	// spawn would, never a stale row left behind by a host that skipped its
	// last materialize pass.
	_ = registry.materializeLocalAgentRegistry(ctx)
	records, err := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{RootSessionID: rootSessionID})
	if err != nil {
		return "", err
	}
	children := agentcontrol.QuotaChildren(records)
	now := time.Now().UTC()
	observations := registry.observeLocalQuotaChildren(ctx, children)
	policy := agentcontrol.ReclaimPolicy{IdleTimeout: opts.Idle, Now: now}
	decisions := agentcontrol.SelectReclaimable(observations, policy)

	lines := []string{"Agent Registry Cleanup:"}
	lines = append(lines, fmt.Sprintf("  root_session=%s quota_children=%d reclaimable=%d idle_policy=%s",
		rootSessionID, len(children), len(decisions), chatAgentCleanupIdlePolicyText(opts.Idle)))
	if len(decisions) == 0 {
		lines = append(lines, "  没有可安全回收的子 agent（running / waiting_approval / waiting_input 永不回收）")
	}
	if opts.DryRun {
		for _, decision := range decisions {
			lines = append(lines, chatAgentCleanupDecisionLine("would_reclaim", decision))
		}
		lines = append(lines, "  dry_run=true（未执行任何回收）")
		return strings.Join(lines, "\n"), nil
	}

	outcome, reclaimErr := agentcontrol.ReclaimAgentQuota(ctx, reclaimStore, rootSessionID, observations, policy)
	if reclaimErr != nil {
		return "", reclaimErr
	}
	if outcome.Reclaimed() > 0 {
		// P2-8 方案 4：手工清理与 spawn 闸门共用同一产品事件口径，
		// 只是 source 不同（manual_cleanup），便于 /debug 区分“谁回收的”。
		registry.recordLocalAgentReclaim(ctx, parentSessionID, rootSessionID, agentcontrol.ReclaimSourceManualCleanup, outcome)
	}
	summary := outcome.Summary()
	if summary == "" {
		summary = "reclaimed=0"
	}
	lines = append(lines, "  "+summary)
	reclaimed := make(map[string]string, len(outcome.Decisions))
	for _, decision := range outcome.Decisions {
		reclaimed[decision.AgentPath] = decision.Reason
	}
	for _, decision := range decisions {
		if reason, closed := reclaimed[decision.AgentPath]; closed {
			lines = append(lines, chatAgentCleanupDecisionLine("reclaimed", agentcontrol.ReclaimDecision{
				AgentPath: decision.AgentPath,
				SessionID: decision.SessionID,
				Reason:    reason,
			}))
			continue
		}
		lines = append(lines, chatAgentCleanupDecisionLine("reclaim_failed", decision))
	}
	if records, listErr := store.ListAgentControlAgents(ctx, agentcontrol.AgentFilter{RootSessionID: rootSessionID}); listErr == nil {
		lines = append(lines, fmt.Sprintf("  remaining_quota_children=%d", len(agentcontrol.QuotaChildren(records))))
	}
	if outcome.Failed > 0 && strings.TrimSpace(outcome.FirstError) != "" {
		lines = append(lines, "  first_error="+strings.TrimSpace(outcome.FirstError))
	}
	lines = append(lines, "  提示: /agents cleanup --dry-run 预览；--idle 30m 额外回收长期空闲对象")
	return strings.Join(lines, "\n"), nil
}

func chatAgentCleanupDecisionLine(prefix string, decision agentcontrol.ReclaimDecision) string {
	fields := []string{"  " + prefix}
	if path := strings.TrimSpace(decision.AgentPath); path != "" {
		fields = append(fields, "path="+path)
	}
	if sessionID := strings.TrimSpace(decision.SessionID); sessionID != "" {
		fields = append(fields, "session="+sessionID)
	}
	if reason := strings.TrimSpace(decision.Reason); reason != "" {
		fields = append(fields, "reason="+reason)
	}
	return strings.Join(fields, " ")
}

func chatAgentCleanupIdlePolicyText(idle time.Duration) string {
	if idle <= 0 {
		return "disabled"
	}
	return idle.String()
}
