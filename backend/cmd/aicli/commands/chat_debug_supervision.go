package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/uniqid"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// P2-12: the local (CLI) host now has a real acknowledge/defer/resolve entry
// for the durable supervision inbox. Before this, only the HTTP host could
// mutate notification decision state, so failure-class critical rows stayed
// `unresolved` forever in a local session and were re-injected every turn (N9).
// The commands below reuse the exact same store + CAS semantics as the HTTP
// handlers; no new protocol or service is involved.

// chatDebugSupervisionUsageText documents the local control entry.
func chatDebugSupervisionUsageText() string {
	return strings.TrimRight(strings.Join([]string{
		"/debug supervision list [--all] [--limit N] [--team <team_id>]",
		"    列出当前会话 root scope（以及活动团队）的监督通知与版本号。",
		"/debug supervision ack <notification_id> --note <text> [--expected-version N]",
		"    确认通知（必须带 --note 审计说明）；确认后该行不再计入 preflight 的 critical_unresolved。",
		"/debug supervision defer <notification_id> --until <30m|2h|RFC3339> [--reason <text>] [--expected-version N]",
		"    延后通知；未到期不再注入 preflight，到期自动恢复注入。",
		"/debug supervision resolve <notification_id> --state <closed|recovered|failed> [--expected-version N]",
		"    收敛通知的 resolution 状态；标的已消失的 critical 行可据此退出未决集合。",
		chatDebugSupervisorUsageText(),
	}, "\n"), "\n")
}

// isChatDebugSupervisionArgument reports whether /debug was invoked with the
// supervision subcommand. It is checked before the generic /debug parser
// because that parser rejects unknown top-level tokens.
func isChatDebugSupervisionArgument(argument string) bool {
	fields := splitChatCommandFields(argument)
	if len(fields) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(fields[0]), "supervision")
}

// chatDebugSupervisionRequest is the parsed CLI input.
type chatDebugSupervisionRequest struct {
	Subcommand      string
	NotificationID  string
	Note            string
	Reason          string
	Until           string
	State           string
	TeamID          string
	ExpectedVersion int64
	HasVersion      bool
	All             bool
	Limit           int
}

func parseChatDebugSupervisionRequest(argument string) (chatDebugSupervisionRequest, error) {
	req := chatDebugSupervisionRequest{}
	fields := splitChatCommandFields(argument)
	if len(fields) == 0 {
		return req, fmt.Errorf("缺少 supervision 子命令；用法：%s", chatDebugSupervisionUsageText())
	}
	// Accept both the full command text ("supervision ack n-1") and the
	// argument-only form ("ack n-1") so the same handler serves /debug,
	// tests and future scripted callers.
	start := 0
	if strings.EqualFold(strings.TrimSpace(fields[0]), "supervision") {
		start = 1
	}
	if start >= len(fields) {
		return req, fmt.Errorf("缺少 supervision 子命令；用法：%s", chatDebugSupervisionUsageText())
	}
	req.Subcommand = strings.ToLower(strings.TrimSpace(fields[start]))
	for index := start + 1; index < len(fields); index++ {
		token := strings.TrimSpace(fields[index])
		if token == "" {
			continue
		}
		lower := strings.ToLower(token)
		takeValue := func() (string, error) {
			if index+1 >= len(fields) {
				return "", fmt.Errorf("%s 需要指定值", token)
			}
			index++
			return strings.TrimSpace(fields[index]), nil
		}
		switch {
		case lower == "--note":
			value, err := takeValue()
			if err != nil {
				return req, err
			}
			req.Note = value
		case strings.HasPrefix(lower, "--note="):
			req.Note = strings.TrimSpace(token[len("--note="):])
		case lower == "--reason":
			value, err := takeValue()
			if err != nil {
				return req, err
			}
			req.Reason = value
		case strings.HasPrefix(lower, "--reason="):
			req.Reason = strings.TrimSpace(token[len("--reason="):])
		case lower == "--until":
			value, err := takeValue()
			if err != nil {
				return req, err
			}
			req.Until = value
		case strings.HasPrefix(lower, "--until="):
			req.Until = strings.TrimSpace(token[len("--until="):])
		case lower == "--state":
			value, err := takeValue()
			if err != nil {
				return req, err
			}
			req.State = strings.ToLower(value)
		case strings.HasPrefix(lower, "--state="):
			req.State = strings.ToLower(strings.TrimSpace(token[len("--state="):]))
		case lower == "--team":
			value, err := takeValue()
			if err != nil {
				return req, err
			}
			req.TeamID = value
		case strings.HasPrefix(lower, "--team="):
			req.TeamID = strings.TrimSpace(token[len("--team="):])
		case lower == "--expected-version":
			value, err := takeValue()
			if err != nil {
				return req, err
			}
			version, err := strconv.ParseInt(value, 10, 64)
			if err != nil || version < 0 {
				return req, fmt.Errorf("--expected-version 需要非负整数，收到 %q", value)
			}
			req.ExpectedVersion = version
			req.HasVersion = true
		case strings.HasPrefix(lower, "--expected-version="):
			value := strings.TrimSpace(token[len("--expected-version="):])
			version, err := strconv.ParseInt(value, 10, 64)
			if err != nil || version < 0 {
				return req, fmt.Errorf("--expected-version 需要非负整数，收到 %q", value)
			}
			req.ExpectedVersion = version
			req.HasVersion = true
		case lower == "--limit":
			value, err := takeValue()
			if err != nil {
				return req, err
			}
			limit, err := strconv.Atoi(value)
			if err != nil || limit <= 0 {
				return req, fmt.Errorf("--limit 需要正整数，收到 %q", value)
			}
			req.Limit = limit
		case strings.HasPrefix(lower, "--limit="):
			value := strings.TrimSpace(token[len("--limit="):])
			limit, err := strconv.Atoi(value)
			if err != nil || limit <= 0 {
				return req, fmt.Errorf("--limit 需要正整数，收到 %q", value)
			}
			req.Limit = limit
		case lower == "--all":
			req.All = true
		case strings.HasPrefix(token, "-"):
			return req, fmt.Errorf("未知 supervision 参数: %s", token)
		default:
			if req.NotificationID == "" {
				req.NotificationID = token
				continue
			}
			return req, fmt.Errorf("多余的参数: %s", token)
		}
	}
	return req, nil
}

// chatDebugSupervisionScopes returns the root scopes this session owns. The
// session itself is always the root scope; an active team adds the team scope
// consumed by the Team lead turn hook.
func chatDebugSupervisionScopes(session *ChatSession, teamID string) []string {
	scopes := make([]string, 0, 2)
	if sessionID := strings.TrimSpace(currentRuntimeSessionID(session)); sessionID != "" {
		scopes = append(scopes, sessionID)
	}
	if teamID == "" && session != nil && session.ActiveTeam != nil {
		teamID = strings.TrimSpace(session.ActiveTeam.TeamID)
	}
	if teamID != "" {
		scopes = append(scopes, teamID)
	}
	return scopes
}

func chatDebugSupervisionStore(session *ChatSession) (supervision.Store, error) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.Supervision == nil || session.LocalRuntimeHost.Supervision.Store == nil {
		return nil, fmt.Errorf("当前会话没有 supervision store：本地宿主未启用监督控制面")
	}
	return session.LocalRuntimeHost.Supervision.Store, nil
}

// notificationInScopes guards against converging a notification that belongs to
// another session's root scope. The CLI has exactly the same boundary as the
// HTTP action surface: only the owning parent/lead may mutate a row.
func notificationInScopes(n *supervision.Notification, scopes []string) bool {
	if n == nil {
		return false
	}
	candidates := []string{
		strings.TrimSpace(n.RootScopeID),
		strings.TrimSpace(n.TargetParentSessionID),
		strings.TrimSpace(n.TargetParentTeamID),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		for _, scope := range scopes {
			if scope != "" && candidate == scope {
				return true
			}
		}
	}
	return false
}

func parseChatDebugSupervisionUntil(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("defer 需要 --until（如 30m、2h 或 RFC3339 时间）")
	}
	if duration, err := time.ParseDuration(value); err == nil {
		if duration <= 0 {
			return time.Time{}, fmt.Errorf("--until 需要正数时长，收到 %q", value)
		}
		return time.Now().UTC().Add(duration), nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("--until 需要 30m/2h 形式的时长或 RFC3339 时间，收到 %q", value)
	}
	return parsed.UTC(), nil
}

func formatChatDebugSupervisionNotification(n supervision.Notification) string {
	decision := strings.TrimSpace(string(n.DecisionState))
	if decision == "" {
		decision = string(supervision.DecisionUnacknowledged)
	}
	resolution := strings.TrimSpace(string(n.ResolutionState))
	if resolution == "" {
		resolution = string(supervision.ResolutionUnresolved)
	}
	detail := fmt.Sprintf("%s severity=%s %s/%s state=%s decision=%s resolution=%s version=%d action_required=%t",
		n.NotificationID,
		n.Severity,
		n.SubjectKind,
		n.SubjectID,
		n.SupervisionState,
		decision,
		resolution,
		n.Version,
		n.ActionRequired(),
	)
	if n.DeferUntil != nil {
		detail += fmt.Sprintf(" defer_until=%s", n.DeferUntil.UTC().Format(time.RFC3339))
	}
	return detail
}

// handleChatDebugSupervisionCommand executes one `/debug supervision ...`
// invocation and returns user-facing text. It never acknowledges implicitly:
// every mutation requires an explicit subcommand, and ack additionally
// requires a --note audit rationale.
func handleChatDebugSupervisionCommand(session *ChatSession, argument string) (string, error) {
	req, err := parseChatDebugSupervisionRequest(argument)
	if err != nil {
		return "", err
	}
	// P0-4 周边：watchdog 是纯只读渲染，既不要求 supervision store 中的通知
	// 数据，也不触发看门狗惰性构建，因此先于 store 解析处理。
	if chatDebugSupervisorSubcommand(req.Subcommand) {
		return chatDebugExecutionSupervisorText(context.Background(), session), nil
	}
	store, err := chatDebugSupervisionStore(session)
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	scopes := chatDebugSupervisionScopes(session, req.TeamID)
	if len(scopes) == 0 {
		return "", fmt.Errorf("当前会话没有可用的 root scope（会话 ID 为空）")
	}

	switch req.Subcommand {
	case "list":
		text, err := runChatDebugSupervisionList(ctx, store, scopes, req)
		if err != nil {
			return text, err
		}
		return text + chatDebugSupervisionWakeBudgetText(ctx, session, scopes), nil
	case "ack":
		return runChatDebugSupervisionAck(ctx, store, scopes, req)
	case "defer":
		return runChatDebugSupervisionDefer(ctx, store, scopes, req)
	case "resolve":
		return runChatDebugSupervisionResolve(ctx, store, scopes, req)
	case "help", "--help", "-h":
		return chatDebugSupervisionUsageText(), nil
	default:
		return "", fmt.Errorf("未知 supervision 子命令 %q；用法：%s", req.Subcommand, chatDebugSupervisionUsageText())
	}
}

func runChatDebugSupervisionList(ctx context.Context, store supervision.Store, scopes []string, req chatDebugSupervisionRequest) (string, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	seen := map[string]bool{}
	lines := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		notifications, err := store.ListNotifications(ctx, supervision.NotificationFilter{
			RootScopeID:     scope,
			IncludeResolved: true,
			Limit:           limit,
		})
		if err != nil {
			return "", fmt.Errorf("读取 supervision 通知失败: %w", err)
		}
		for _, n := range notifications {
			if seen[n.NotificationID] {
				continue
			}
			if !req.All && n.DecisionState == supervision.DecisionAcknowledged {
				continue
			}
			seen[n.NotificationID] = true
			lines = append(lines, formatChatDebugSupervisionNotification(n))
		}
	}
	header := fmt.Sprintf("supervision 通知（root_scope=%s）：共 %d 条", strings.Join(scopes, ","), len(lines))
	if len(lines) == 0 {
		return header + "\n（无未决通知；--all 可包含已确认行）", nil
	}
	return header + "\n" + strings.Join(lines, "\n"), nil
}

// chatDebugSupervisionWakeBudgetText renders the auto-wake budget (plan P1-6)
// for every root scope this session owns. The scheduler is optional: hosts
// that only wire the supervision store simply skip the section instead of
// failing the command.
func chatDebugSupervisionWakeBudgetText(ctx context.Context, session *ChatSession, scopes []string) string {
	scheduler := chatDebugSupervisionWakeScheduler(session)
	if scheduler == nil {
		return ""
	}
	classes := []supervision.WakeBudgetClass{
		supervision.WakeBudgetClassApproval,
		supervision.WakeBudgetClassFailure,
		supervision.WakeBudgetClassOther,
	}
	lines := make([]string, 0, len(scopes))
	window := time.Duration(0)
	for _, scope := range scopes {
		parts := make([]string, 0, len(classes))
		for _, class := range classes {
			state := scheduler.BudgetState(ctx, scope, class)
			if state.Window > 0 {
				window = state.Window
			}
			if state.Unlimited {
				parts = append(parts, fmt.Sprintf("%s=unlimited(used=%d)", class, state.Used))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s=%d/%d", class, state.Used, state.Limit))
		}
		lines = append(lines, fmt.Sprintf("  %s: %s", scope, strings.Join(parts, " ")))
	}
	if len(lines) == 0 {
		return ""
	}
	title := "wake 预算"
	if window > 0 {
		title += fmt.Sprintf("（窗口 %s）", window)
	}
	return "\n" + title + "：\n" + strings.Join(lines, "\n")
}

// chatDebugSupervisionWakeScheduler resolves the scheduler from either the
// local control plane or the host's wake consumer, whichever is wired.
func chatDebugSupervisionWakeScheduler(session *ChatSession) *supervision.WakeScheduler {
	if session == nil || session.LocalRuntimeHost == nil {
		return nil
	}
	if plane := session.LocalRuntimeHost.Supervision; plane != nil && plane.Wakes != nil {
		return plane.Wakes
	}
	if consumer := session.LocalRuntimeHost.supervisionWake; consumer != nil {
		return consumer.Wakes
	}
	return nil
}

func loadChatDebugSupervisionNotification(ctx context.Context, store supervision.Store, scopes []string, notificationID string) (*supervision.Notification, error) {
	notificationID = strings.TrimSpace(notificationID)
	if notificationID == "" {
		return nil, fmt.Errorf("缺少 notification_id；用法：%s", chatDebugSupervisionUsageText())
	}
	record, err := store.GetNotification(ctx, notificationID)
	if err != nil {
		return nil, fmt.Errorf("读取通知 %s 失败: %w", notificationID, err)
	}
	if record == nil {
		return nil, fmt.Errorf("未找到通知 %s", notificationID)
	}
	if !notificationInScopes(record, scopes) {
		return nil, fmt.Errorf("通知 %s 不属于当前会话的 root scope，拒绝修改", notificationID)
	}
	return record, nil
}

func resolveChatDebugSupervisionVersion(record *supervision.Notification, req chatDebugSupervisionRequest) (int64, error) {
	if !req.HasVersion {
		return record.Version, nil
	}
	if record.Version != req.ExpectedVersion {
		return 0, fmt.Errorf("%w: 通知 %s 当前 version=%d，命令传入 expected=%d（请重新 list 后再试）",
			supervision.ErrActionConflict, record.NotificationID, record.Version, req.ExpectedVersion)
	}
	return req.ExpectedVersion, nil
}

func runChatDebugSupervisionAck(ctx context.Context, store supervision.Store, scopes []string, req chatDebugSupervisionRequest) (string, error) {
	record, err := loadChatDebugSupervisionNotification(ctx, store, scopes, req.NotificationID)
	if err != nil {
		return "", err
	}
	note := strings.TrimSpace(req.Note)
	if note == "" {
		return "", fmt.Errorf("ack 必须带 --note <审计说明>（确认即接受风险，需留下可审计理由）")
	}
	allowed := supervision.Evaluator{}.EvaluateAllowedActions(*record)
	if !supervision.AllowedAction(allowed, supervision.ActionAcknowledge) {
		return "", fmt.Errorf("%w: 通知 %s 当前不允许 acknowledge（allowed=[%s]）",
			supervision.ErrActionNotAllowed, record.NotificationID, strings.Join(allowed, ","))
	}
	version, err := resolveChatDebugSupervisionVersion(record, req)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	ok, err := store.AcknowledgeNotification(ctx, record.NotificationID, now, version)
	if err != nil {
		return "", fmt.Errorf("确认通知失败: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("%w: 通知 %s 的 version 已变化（expected=%d），请重新 list", supervision.ErrActionConflict, record.NotificationID, version)
	}
	auditNote, auditErr := recordChatDebugSupervisionAckAudit(ctx, store, scopes[0], record, note, version, now)
	return chatDebugSupervisionMutationSummary("acknowledged", record.NotificationID, auditNote, auditErr), nil
}

// recordChatDebugSupervisionAckAudit persists the --note rationale as a durable
// action row. The notification table has no note column by design (the durable
// lifecycle event is the source of truth), so the audit trail lives in the
// actions table where ListActions can read it back.
func recordChatDebugSupervisionAckAudit(ctx context.Context, store supervision.Store, rootScopeID string, record *supervision.Notification, note string, version int64, now time.Time) (string, error) {
	// action_id 是 durable 主键：同一 tick 内两次 ack 撞键会被 INSERT OR IGNORE
	// 静默丢弃（与该包 wake id 的历史故障同源）。
	actionID := "act_local_ack_" + uniqid.Token()
	_, err := store.CreateAction(ctx, supervision.ActionRecord{
		ActionID:        actionID,
		RootScopeID:     strings.TrimSpace(rootScopeID),
		RequestedByKind: "local_host",
		RequestedByID:   strings.TrimSpace(rootScopeID),
		TargetKind:      record.SubjectKind,
		TargetID:        record.SubjectID,
		Action:          supervision.ActionAcknowledge,
		Reason:          note,
		ExpectedVersion: record.Version,
		Status:          supervision.ActionCompleted,
		CreatedAt:       now,
		FinishedAt:      &now,
		Result:          "acknowledged via /debug supervision ack",
		Version:         1,
	})
	if err != nil {
		return "", err
	}
	return actionID, nil
}

func runChatDebugSupervisionDefer(ctx context.Context, store supervision.Store, scopes []string, req chatDebugSupervisionRequest) (string, error) {
	record, err := loadChatDebugSupervisionNotification(ctx, store, scopes, req.NotificationID)
	if err != nil {
		return "", err
	}
	until, err := parseChatDebugSupervisionUntil(req.Until)
	if err != nil {
		return "", err
	}
	allowed := supervision.Evaluator{}.EvaluateAllowedActions(*record)
	if !supervision.AllowedAction(allowed, supervision.ActionDefer) {
		return "", fmt.Errorf("%w: 通知 %s 当前不允许 defer（allowed=[%s]）",
			supervision.ErrActionNotAllowed, record.NotificationID, strings.Join(allowed, ","))
	}
	version, err := resolveChatDebugSupervisionVersion(record, req)
	if err != nil {
		return "", err
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "deferred via /debug supervision defer"
	}
	ok, err := store.DeferNotification(ctx, record.NotificationID, until, reason, version)
	if err != nil {
		return "", fmt.Errorf("延后通知失败: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("%w: 通知 %s 的 version 已变化（expected=%d），请重新 list", supervision.ErrActionConflict, record.NotificationID, version)
	}
	return fmt.Sprintf("deferred %s until %s（未到期不再进入 preflight，到期自动恢复注入）",
		record.NotificationID, until.Format(time.RFC3339)), nil
}

func runChatDebugSupervisionResolve(ctx context.Context, store supervision.Store, scopes []string, req chatDebugSupervisionRequest) (string, error) {
	record, err := loadChatDebugSupervisionNotification(ctx, store, scopes, req.NotificationID)
	if err != nil {
		return "", err
	}
	var resolution supervision.ResolutionState
	switch strings.ToLower(strings.TrimSpace(req.State)) {
	case string(supervision.ResolutionClosed):
		resolution = supervision.ResolutionClosed
	case string(supervision.ResolutionRecovered):
		resolution = supervision.ResolutionRecovered
	case string(supervision.ResolutionFailed):
		resolution = supervision.ResolutionFailed
	default:
		return "", fmt.Errorf("resolve 需要 --state closed|recovered|failed，收到 %q", req.State)
	}
	version, err := resolveChatDebugSupervisionVersion(record, req)
	if err != nil {
		return "", err
	}
	ok, err := store.ResolveNotification(ctx, record.NotificationID, resolution, time.Now().UTC(), version)
	if err != nil {
		return "", fmt.Errorf("收敛通知失败: %w", err)
	}
	if !ok {
		return "", fmt.Errorf("%w: 通知 %s 的 version 已变化（expected=%d），请重新 list", supervision.ErrActionConflict, record.NotificationID, version)
	}
	return fmt.Sprintf("resolved %s state=%s（该行退出未决集合）", record.NotificationID, resolution), nil
}

func chatDebugSupervisionMutationSummary(verb, notificationID, actionID string, auditErr error) string {
	summary := fmt.Sprintf("%s %s", verb, notificationID)
	if actionID != "" {
		summary += fmt.Sprintf("（审计记录 %s）", actionID)
	}
	if auditErr != nil {
		summary += fmt.Sprintf("；警告: 审计记录写入失败: %v", auditErr)
	}
	summary += "；下一轮 preflight 将不再计入 critical_unresolved"
	return summary
}
