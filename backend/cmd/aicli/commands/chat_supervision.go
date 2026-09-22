package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 2026-09-22 手动核查（Manual Audit）入口。
//
// 方案：docs/plan/supervision-manual-audit-plan-20260922.md
//
// 背景：turn 结束的自动 drain + digest-only self-check 会占用业务回合槽、在
// turn 结束路径内联阻塞，而且缺少"用户静默期"闸门（用户正在打字时仍会起一个
// 监督 turn）。调整后自动核查默认关闭（supervision.turn_end_check，nil/false
// 等价），改由用户显式发起核查：
//
//	/supervision status  —— 只读：开关状态 + digest 计数 + wake 预算
//	/supervision audit   —— 只读：digest 明细 + descendants 矩阵 + pending wake
//	/supervision wake    —— pending wake 明细 + 预算；--deliver 显式投递
//
// ack/defer/resolve/control/list/watchdog 直接委托 /debug supervision 的既有实现
// （同一 store + CAS 语义 + 同一审计行），/debug supervision 保持原样，既有脚本
// 与文档不受影响；/supervision 只是它的上层入口。

// chatSupervisionUsageText documents the manual audit entry.
func chatSupervisionUsageText() string {
	return strings.TrimRight(strings.Join([]string{
		"/supervision status [--team <team_id>] [--limit N] [--json]",
		"    只读：自动核查开关（supervision.turn_end_check）、root scope、lifecycle digest 计数与 wake 预算。",
		"/supervision audit [--team <team_id>] [--limit N] [--json]",
		"    只读核查：digest 明细 + descendants 矩阵 + 未领取 durable wake（不投递、不改任何状态）。",
		"/supervision wake [--deliver] [--team <team_id>] [--limit N] [--json]",
		"    查看积压 durable wake 与预算；--deliver 在父会话可运行时显式投递一次监督 turn（父会话忙/预算耗尽时 wake 保持 pending）。",
		"/supervision list|ack|defer|resolve|control|watchdog ...",
		"    委托 /debug supervision 的既有动作子命令（同一实现与审计语义）：",
		chatDebugSupervisionUsageText(),
	}, "\n"), "\n")
}

// chatSupervisionRequest is the parsed CLI input of /supervision.
type chatSupervisionRequest struct {
	Subcommand string
	TeamID     string
	Limit      int
	JSON       bool
	Deliver    bool
	// Forward is the verbatim delegated argument ("supervision <sub> ...") for
	// the ack/defer/resolve/control/list/watchdog family.
	Forward string
}

// parseChatSupervisionRequest parses /supervision arguments. Bare /supervision
// degrades to the read-only `status` report, mirroring bare /debug.
func parseChatSupervisionRequest(argument string) (chatSupervisionRequest, error) {
	req := chatSupervisionRequest{}
	fields := splitChatCommandFields(argument)
	if len(fields) > 0 && strings.EqualFold(strings.TrimSpace(fields[0]), "supervision") {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		req.Subcommand = "status"
		return req, nil
	}
	req.Subcommand = strings.ToLower(strings.TrimSpace(fields[0]))
	switch req.Subcommand {
	case "help", "-h", "--help":
		req.Subcommand = "help"
		return req, nil
	case "list", "ack", "defer", "resolve", "control", "watchdog", "supervisor", "execution-supervisor":
		// 动作/明细子命令原样转发给 /debug supervision：解析、CAS 版本校验与
		// 审计写入都只有一份实现，避免两条入口的语义漂移。
		req.Forward = "supervision " + strings.Join(fields, " ")
		return req, nil
	case "status", "audit", "wake":
	default:
		return req, fmt.Errorf("未知 supervision 子命令 %q\n用法：%s", req.Subcommand, chatSupervisionUsageText())
	}
	for index := 1; index < len(fields); index++ {
		token := strings.TrimSpace(fields[index])
		if token == "" {
			continue
		}
		name, value, hasValue := strings.Cut(token, "=")
		switch strings.ToLower(name) {
		case "--json":
			req.JSON = true
		case "--deliver", "--apply":
			req.Deliver = true
		case "--dry-run":
			// 默认就是预览（dry-run）；显式写出来等价，仅便于脚本自解释。
			req.Deliver = false
		case "--team":
			if !hasValue {
				index++
				if index >= len(fields) {
					return req, fmt.Errorf("--team 缺少取值\n用法：%s", chatSupervisionUsageText())
				}
				value = fields[index]
			}
			req.TeamID = strings.TrimSpace(value)
		case "--limit":
			if !hasValue {
				index++
				if index >= len(fields) {
					return req, fmt.Errorf("--limit 缺少取值\n用法：%s", chatSupervisionUsageText())
				}
				value = fields[index]
			}
			limit, err := strconv.Atoi(strings.TrimSpace(value))
			if err != nil || limit <= 0 {
				return req, fmt.Errorf("--limit 需要正整数，收到 %q", value)
			}
			req.Limit = limit
		default:
			return req, fmt.Errorf("未知参数 %q\n用法：%s", token, chatSupervisionUsageText())
		}
	}
	if req.Subcommand != "wake" && req.Deliver {
		return req, fmt.Errorf("--deliver 仅适用于 /supervision wake")
	}
	return req, nil
}

// handleSupervisionCommand is the /supervision entry used by the legacy
// dispatcher. Unified terminals go through the structured command cell so no
// variant can revive the legacy terminal writer.
func handleSupervisionCommand(session *ChatSession, command string) bool {
	if unifiedDirectInteractiveOutput(session) {
		if result, handled := tryExecuteStructuredSupervisionCommand(session, command); handled {
			_ = renderChatCommandResult(session, result, false)
			return false
		}
	}
	text, err := handleChatSupervisionCommand(session, extractCommandArgument(command))
	if err != nil {
		printChatCommandOutput(session, "错误: "+err.Error())
		return false
	}
	printChatCommandOutput(session, text)
	return false
}

// tryExecuteStructuredSupervisionCommand renders /supervision as one finite
// command cell. It always claims the command (unknown subcommands become an
// explicit usage error instead of a silent fall-through).
func tryExecuteStructuredSupervisionCommand(session *ChatSession, command string) (CommandResult, bool) {
	text, err := handleChatSupervisionCommand(session, extractCommandArgument(command))
	if err != nil {
		return commandTextResult("错误: " + err.Error()), true
	}
	return commandTextResult(text), true
}

// handleChatSupervisionCommand runs one manual audit command and returns the
// finite text report (or a delegated /debug supervision result).
func handleChatSupervisionCommand(session *ChatSession, argument string) (string, error) {
	req, err := parseChatSupervisionRequest(argument)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	switch req.Subcommand {
	case "help":
		return chatSupervisionUsageText(), nil
	case "status":
		return runChatSupervisionAudit(ctx, session, req, false)
	case "audit":
		return runChatSupervisionAudit(ctx, session, req, true)
	case "wake":
		return runChatSupervisionWake(ctx, session, req)
	default:
		if req.Forward != "" {
			return handleChatDebugSupervisionCommand(session, req.Forward)
		}
		return "", fmt.Errorf("未知 supervision 子命令 %q\n用法：%s", req.Subcommand, chatSupervisionUsageText())
	}
}

// chatSupervisionReport is the read-only manual audit payload. It is the same
// object for the text and --json projections, so scripts and humans never see
// divergent numbers.
type chatSupervisionReport struct {
	GeneratedAt      string                        `json:"generated_at"`
	Subcommand       string                        `json:"subcommand"`
	AutoAuditEnabled bool                          `json:"auto_audit_enabled"`
	TurnEndCheck     bool                          `json:"turn_end_check"`
	Scopes           []string                      `json:"scopes,omitempty"`
	Digest           *supervision.Digest           `json:"digest,omitempty"`
	Snapshot         *supervision.Snapshot         `json:"snapshot,omitempty"`
	PendingWakes     []supervision.WakePending     `json:"pending_wakes,omitempty"`
	PendingWakeCount int                           `json:"pending_wake_count"`
	WakeBudget       []supervision.WakeBudgetState `json:"wake_budget,omitempty"`
	Delivery         string                        `json:"delivery,omitempty"`
	Warnings         []string                      `json:"warnings,omitempty"`
}

func runChatSupervisionAudit(ctx context.Context, session *ChatSession, req chatSupervisionRequest, detailed bool) (string, error) {
	report, err := chatSupervisionBuildReport(ctx, session, req, detailed)
	if err != nil {
		return "", err
	}
	if req.JSON {
		return chatSupervisionJSONText(report)
	}
	return chatSupervisionReportText(report, detailed), nil
}

func runChatSupervisionWake(ctx context.Context, session *ChatSession, req chatSupervisionRequest) (string, error) {
	report, err := chatSupervisionBuildReport(ctx, session, req, false)
	if err != nil {
		return "", err
	}
	if req.Deliver {
		delivery, err := chatSupervisionDeliverWake(ctx, session, req)
		if err != nil {
			return "", err
		}
		report.Delivery = delivery
	}
	if req.JSON {
		return chatSupervisionJSONText(report)
	}
	return chatSupervisionWakeText(report, req.Deliver), nil
}

// chatSupervisionBuildReport collects the read-only picture. detailed adds the
// digest rows and the descendants matrix (audit); status keeps counters only.
func chatSupervisionBuildReport(ctx context.Context, session *ChatSession, req chatSupervisionRequest, detailed bool) (chatSupervisionReport, error) {
	report := chatSupervisionReport{
		GeneratedAt:      time.Now().UTC().Format(time.RFC3339),
		Subcommand:       req.Subcommand,
		AutoAuditEnabled: chatSupervisionAutoAuditEnabled(session),
		TurnEndCheck:     chatSupervisionAutoAuditEnabled(session),
	}
	report.Scopes = chatDebugSupervisionScopes(session, req.TeamID)
	if len(report.Scopes) == 0 {
		return report, fmt.Errorf("supervision scope is required：当前会话缺少 runtime session id")
	}
	controller := chatSupervisionController(session)
	if controller == nil {
		return report, fmt.Errorf("当前会话没有 supervision 控制面：本地宿主未启用监督控制面（durable store 未接线）")
	}
	digest, err := controller.SupervisionSnapshot(ctx, "", toolbroker.SupervisionSnapshotArgs{Limit: req.Limit})
	if err != nil {
		return report, fmt.Errorf("读取 lifecycle digest 失败：%w", err)
	}
	report.Digest = digest
	if detailed {
		snapshot, err := controller.SupervisionDescendants(ctx, "", toolbroker.SupervisionDescendantsArgs{
			Mode:            "descendants",
			Health:          "any",
			IncludeTerminal: false,
			Limit:           req.Limit,
		})
		if err != nil {
			return report, fmt.Errorf("读取 descendants 矩阵失败：%w", err)
		}
		report.Snapshot = snapshot
	}
	wakes, warnings := chatSupervisionPendingWakes(ctx, session, report.Scopes, req.Limit)
	report.PendingWakes = wakes
	report.PendingWakeCount = len(wakes)
	report.WakeBudget = chatSupervisionBudgetStates(ctx, session, report.Scopes)
	report.Warnings = warnings
	return report, nil
}

// chatSupervisionController resolves the local control-plane reader, which
// already enforces the same scope rules as the model-facing tools (a session
// can never widen its own scope).
func chatSupervisionController(session *ChatSession) *localSupervisionToolController {
	if session == nil || session.LocalRuntimeHost == nil {
		return nil
	}
	return newLocalSupervisionToolController(session.LocalRuntimeHost, session)
}

// chatSupervisionAutoAuditEnabled reports the effective supervision.turn_end_check
// value of this session's host (nil/false = manual audit only).
func chatSupervisionAutoAuditEnabled(session *ChatSession) bool {
	if session == nil || session.LocalRuntimeHost == nil {
		return false
	}
	return session.LocalRuntimeHost.supervisionConfig.TurnEndCheckEnabled()
}

// chatSupervisionPendingWakes lists unclaimed durable wakes for every root
// scope this session owns. Missing rows are normal (nothing pending); a store
// error is reported as a warning instead of failing the whole audit.
func chatSupervisionPendingWakes(ctx context.Context, session *ChatSession, scopes []string, limit int) ([]supervision.WakePending, []string) {
	store, err := chatDebugSupervisionStore(session)
	if err != nil {
		return nil, []string{err.Error()}
	}
	if limit <= 0 {
		limit = 20
	}
	seen := make(map[string]bool, len(scopes))
	rows := make([]supervision.WakePending, 0, len(scopes))
	warnings := make([]string, 0, 1)
	for _, scope := range scopes {
		pending, err := store.ListWakePending(ctx, supervision.WakeFilter{
			RootScopeID:   scope,
			UnclaimedOnly: true,
			Limit:         limit,
		})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("读取 root scope %s 的 pending wake 失败：%v", scope, err))
			continue
		}
		for _, row := range pending {
			if seen[row.WakeID] {
				continue
			}
			seen[row.WakeID] = true
			rows = append(rows, row)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	return rows, warnings
}

// chatSupervisionBudgetStates reads the four independent auto-wake budgets per
// root scope (doc 6.5 rule 4). A host without a scheduler simply reports no
// budget section.
func chatSupervisionBudgetStates(ctx context.Context, session *ChatSession, scopes []string) []supervision.WakeBudgetState {
	scheduler := chatDebugSupervisionWakeScheduler(session)
	if scheduler == nil {
		return nil
	}
	classes := []supervision.WakeBudgetClass{
		supervision.WakeBudgetClassApproval,
		supervision.WakeBudgetClassFailure,
		supervision.WakeBudgetClassOther,
		supervision.WakeBudgetClassProgress,
	}
	states := make([]supervision.WakeBudgetState, 0, len(scopes)*len(classes))
	for _, scope := range scopes {
		for _, class := range classes {
			states = append(states, scheduler.BudgetState(ctx, scope, class))
		}
	}
	return states
}

// chatSupervisionDeliverWake is the explicit manual delivery. It never claims a
// wake when the parent is busy or the class budget is exhausted: the durable row
// stays pending for the next natural turn preflight (2026-09-22 调整：手动核查
// 不等于"绕过 runnable/预算闸门"）。
func chatSupervisionDeliverWake(ctx context.Context, session *ChatSession, req chatSupervisionRequest) (string, error) {
	if session == nil || session.LocalRuntimeHost == nil {
		return "", fmt.Errorf("当前会话没有本地宿主，无法投递 durable wake")
	}
	consumer := session.LocalRuntimeHost.supervisionWake
	if consumer == nil {
		return "宿主未装配 wake consumer（supervisionWake=nil）：wake 保持 pending，下一次自然 turn 的 preflight digest 仍会注入。", nil
	}
	parentSessionID := strings.TrimSpace(currentRuntimeSessionID(session))
	if parentSessionID == "" {
		return "", fmt.Errorf("supervision scope is required：当前会话缺少 runtime session id")
	}
	delivered := make([]string, 0, 2)
	busy := false
	limited := false
	for _, scope := range chatDebugSupervisionScopes(session, req.TeamID) {
		teamID := ""
		if scope != parentSessionID {
			// team scope：父会话仍是本会话（team lead），团队只作为 root scope。
			teamID = scope
		}
		err := consumer.MaybeWakeParent(ctx, parentSessionID, teamID, scope)
		switch {
		case err == nil:
			delivered = append(delivered, scope)
		case errors.Is(err, supervision.ErrWakeParentBusy):
			busy = true
		case errors.Is(err, supervision.ErrWakeRateLimited):
			limited = true
		default:
			return "", fmt.Errorf("投递 durable wake 失败（scope=%s）：%w", scope, err)
		}
	}
	switch {
	case len(delivered) > 0:
		return fmt.Sprintf("已请求投递（scope=%s）：父会话可运行且预算允许时已转为一次监督 turn；否则 wake 保持 pending。", strings.Join(delivered, ", ")), nil
	case busy:
		return "父会话当前不可运行（running / waiting approval / waiting input / rewinding）：wake 保持 pending，下一次自然 turn 的 preflight digest 仍会注入。", nil
	case limited:
		return "wake 预算已耗尽（窗口内该 class 已达上限）：wake 保持 pending，窗口滚动后可再投递；关键行也可先 ack/defer 处理。", nil
	default:
		return "没有匹配的未领取 durable wake：无需投递。", nil
	}
}

func chatSupervisionJSONText(report chatSupervisionReport) (string, error) {
	payload, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return "", fmt.Errorf("序列化 supervision 报告失败：%w", err)
	}
	return string(payload), nil
}

func chatSupervisionReportText(report chatSupervisionReport, detailed bool) string {
	title := "supervision 状态（manual audit / status）"
	if detailed {
		title = "supervision 手动核查（manual audit / audit）"
	}
	lines := []string{title}
	lines = append(lines, fmt.Sprintf("  自动核查（turn 结束 drain + digest-only self-check）：%s", chatSupervisionAutoAuditStateText(report.AutoAuditEnabled)))
	lines = append(lines, fmt.Sprintf("  根 scope：%s", strings.Join(report.Scopes, ", ")))
	lines = append(lines, fmt.Sprintf("  生成时间：%s", report.GeneratedAt))
	lines = append(lines, chatSupervisionDigestText(report.Digest, detailed)...)
	if detailed {
		lines = append(lines, chatSupervisionSnapshotText(report.Snapshot)...)
	}
	lines = append(lines, chatSupervisionPendingWakeText(report.PendingWakes)...)
	if budget := chatSupervisionBudgetText(report.WakeBudget); budget != "" {
		lines = append(lines, budget)
	}
	for _, warning := range report.Warnings {
		lines = append(lines, "  警告："+warning)
	}
	if detailed {
		lines = append(lines, "  提示：/supervision list 查看通知明细；ack/defer/resolve/control 见 /supervision help；显式投递用 /supervision wake --deliver。")
	} else {
		lines = append(lines, "  提示：/supervision audit 查看明细；/supervision wake 查看并投递积压 wake；/supervision help 查看全部子命令。")
	}
	return strings.Join(lines, "\n")
}

func chatSupervisionWakeText(report chatSupervisionReport, deliverRequested bool) string {
	lines := []string{"supervision wake（手动投递）"}
	lines = append(lines, fmt.Sprintf("  自动核查（turn 结束 drain + digest-only self-check）：%s", chatSupervisionAutoAuditStateText(report.AutoAuditEnabled)))
	lines = append(lines, fmt.Sprintf("  根 scope：%s", strings.Join(report.Scopes, ", ")))
	lines = append(lines, chatSupervisionPendingWakeText(report.PendingWakes)...)
	if budget := chatSupervisionBudgetText(report.WakeBudget); budget != "" {
		lines = append(lines, budget)
	}
	for _, warning := range report.Warnings {
		lines = append(lines, "  警告："+warning)
	}
	if report.Delivery != "" {
		lines = append(lines, "  投递结果："+report.Delivery)
	} else if deliverRequested {
		lines = append(lines, "  投递结果：未执行（宿主未装配 wake consumer）。")
	} else {
		lines = append(lines, "  提示：以上仅为预览（不 claim、不 resolve）；需要立即尝试投递时执行 /supervision wake --deliver。")
	}
	return strings.Join(lines, "\n")
}

func chatSupervisionAutoAuditStateText(enabled bool) string {
	if enabled {
		return "开启（supervision.turn_end_check=true，恢复 2026-09-16 §6.5 规则 2 的 turn 结束自动闭合语义）"
	}
	return "关闭（默认；turn 结束不 drain、不起 digest-only self-check，改用 /supervision audit|wake 手动核查）"
}

func chatSupervisionDigestText(digest *supervision.Digest, detailed bool) []string {
	if digest == nil {
		return []string{"  lifecycle digest：不可用（宿主未接线或读取失败）"}
	}
	lines := []string{fmt.Sprintf(
		"  lifecycle digest：critical_unresolved=%d action_required=%d auto_actions_in_progress=%d resolved_since_last_turn=%d stale=%d snapshot_seq=%d next_seq=%d",
		digest.CriticalUnresolved,
		digest.ActionRequired,
		digest.AutoActionsInProgress,
		digest.ResolvedSinceLastTurn,
		digest.StaleSubjects,
		digest.SnapshotSeq,
		digest.NextSeq,
	)}
	if digest.Truncated {
		lines = append(lines, "  （digest 已截断：用 /supervision audit --limit N 提高上限，或先处理关键行）")
	}
	if !detailed {
		return lines
	}
	if len(digest.Items) == 0 {
		lines = append(lines, "    未决行：无")
		return lines
	}
	for _, item := range digest.Items {
		line := fmt.Sprintf("    - %s:%s state=%s", item.SubjectKind, item.SubjectID, item.SupervisionState)
		if item.Reason != "" {
			line += " reason=" + item.Reason
		}
		if item.RecommendedAction != "" {
			line += " recommended=" + item.RecommendedAction
		}
		if len(item.AllowedActions) > 0 {
			line += " allowed=" + strings.Join(item.AllowedActions, ",")
		}
		if item.ActionRequired {
			line += " action_required=true"
		}
		if item.Stale {
			line += " stale=true"
		}
		if item.NotificationID != "" {
			line += " notification=" + item.NotificationID
		}
		lines = append(lines, line)
	}
	return lines
}

func chatSupervisionSnapshotText(snapshot *supervision.Snapshot) []string {
	if snapshot == nil {
		return []string{"  descendants 矩阵：不可用"}
	}
	lines := []string{fmt.Sprintf(
		"  descendants 矩阵：rows=%d action_required=%d stalled=%d timed_out=%d orphaned=%d invalid=%d canceling=%d terminal_unacknowledged=%d",
		len(snapshot.Descendants),
		snapshot.Summary.ActionRequired,
		snapshot.Summary.Stalled,
		snapshot.Summary.TimedOut,
		snapshot.Summary.Orphaned,
		snapshot.Summary.Invalid,
		snapshot.Summary.Canceling,
		snapshot.Summary.TerminalUnacknowledged,
	)}
	if snapshot.Truncated {
		lines = append(lines, "  （矩阵已截断：用 /supervision audit --limit N 提高上限）")
	}
	for _, row := range snapshot.Descendants {
		line := fmt.Sprintf("    - %s:%s execution=%s supervision=%s", row.Kind, row.ID, chatSupervisionDash(row.ExecutionStatus), row.SupervisionState)
		if row.HeartbeatAgeMs > 0 {
			line += fmt.Sprintf(" heartbeat_age=%s", (time.Duration(row.HeartbeatAgeMs) * time.Millisecond).Truncate(time.Second))
		}
		if row.ProgressAgeMs > 0 {
			line += fmt.Sprintf(" progress_age=%s", (time.Duration(row.ProgressAgeMs) * time.Millisecond).Truncate(time.Second))
		}
		if row.ExecutionDeadlineAt != nil {
			line += " deadline=" + row.ExecutionDeadlineAt.UTC().Format(time.RFC3339)
		}
		lines = append(lines, line)
	}
	return lines
}

func chatSupervisionPendingWakeText(wakes []supervision.WakePending) []string {
	lines := []string{fmt.Sprintf("  未领取 durable wake：%d", len(wakes))}
	for _, wake := range wakes {
		line := fmt.Sprintf("    - %s scope=%s parent=%s reason=%s seq=%d", wake.WakeID, wake.RootScopeID, chatSupervisionDash(wake.TargetParentSessionID), chatSupervisionDash(wake.WakeReason), wake.NotificationSeq)
		if wake.TargetParentTeamID != "" {
			line += " team=" + wake.TargetParentTeamID
		}
		if !wake.CreatedAt.IsZero() {
			line += " created=" + wake.CreatedAt.UTC().Format(time.RFC3339)
		}
		lines = append(lines, line)
	}
	return lines
}

func chatSupervisionBudgetText(states []supervision.WakeBudgetState) string {
	if len(states) == 0 {
		return ""
	}
	window := time.Duration(0)
	order := make([]string, 0, 2)
	byScope := make(map[string][]string, 2)
	for _, state := range states {
		if state.Window > 0 {
			window = state.Window
		}
		if _, ok := byScope[state.RootScopeID]; !ok {
			order = append(order, state.RootScopeID)
		}
		if state.Unlimited {
			byScope[state.RootScopeID] = append(byScope[state.RootScopeID], fmt.Sprintf("%s=unlimited(used=%d)", state.BudgetClass, state.Used))
			continue
		}
		byScope[state.RootScopeID] = append(byScope[state.RootScopeID], fmt.Sprintf("%s=%d/%d", state.BudgetClass, state.Used, state.Limit))
	}
	title := "  wake 预算"
	if window > 0 {
		title += fmt.Sprintf("（窗口 %s）", window)
	}
	lines := []string{title + "："}
	for _, scope := range order {
		lines = append(lines, fmt.Sprintf("    %s: %s", scope, strings.Join(byScope[scope], " ")))
	}
	return strings.Join(lines, "\n")
}

func chatSupervisionDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// chatSupervisionSubcommandQueueSafe reports whether a /supervision subcommand
// is read-only and therefore safe to park in the busy input queue. Actions
// (wake/ack/defer/resolve/control) are rejected while the agent is busy so a
// deferred mutation can never change supervision state at an invisible moment.
func chatSupervisionSubcommandQueueSafe(args []string) bool {
	if len(args) == 0 {
		// 裸 /supervision = status，只读。
		return true
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "status", "audit", "help", "-h", "--help", "list", "watchdog", "supervisor", "execution-supervisor":
		return true
	default:
		return false
	}
}
