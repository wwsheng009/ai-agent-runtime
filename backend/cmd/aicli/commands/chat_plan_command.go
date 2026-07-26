package commands

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// handlePlanCommand implements the /plan slash command.
//
// Usage:
//
//	/plan                          show status
//	/plan status                   show status
//	/plan enter [path]             enter plan mode (optional plan path)
//	/plan on [path]                alias of enter
//	/plan exit <decision> [notes]  exit with approve|request_changes|quit
//	/plan approve [notes]          exit approve
//	/plan request_changes [notes]  exit request_changes (stay in plan)
//	/plan quit [notes]             exit quit
//	/plan off                      alias of exit quit
func handlePlanCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}

	arg := strings.TrimSpace(extractCommandArgument(command))
	if arg == "" || strings.EqualFold(firstToken(arg), "status") {
		printPlanModeStatus(session)
		return false
	}

	verb, rest := splitFirstToken(arg)
	switch strings.ToLower(verb) {
	case "enter", "on", "start":
		planPath := strings.TrimSpace(rest)
		if err := enterChatPlanMode(session, planPath); err != nil {
			fmt.Printf("错误: %v\n", err)
			return false
		}
		state := loadChatPlanMode(session)
		fmt.Printf("提示: 已进入 plan mode（plan=%s, previous=%s, writes=%s）\n",
			state.PlanPath,
			firstNonEmptyChatValue(state.PreviousMode, string(runtimepolicy.ModeDefault)),
			strings.Join(state.WriteAllowPaths, ", "),
		)
		return false

	case "exit":
		decisionToken, notes := splitFirstToken(rest)
		if strings.TrimSpace(decisionToken) == "" {
			fmt.Println("用法: /plan exit <approve|request_changes|quit> [notes]")
			return false
		}
		return exitChatPlanModeCommand(session, decisionToken, notes)

	case "approve", "approved", "yes", "y":
		return exitChatPlanModeCommand(session, "approve", rest)

	case "request_changes", "request-changes", "changes", "revise":
		return exitChatPlanModeCommand(session, "request_changes", rest)

	case "quit", "cancel", "abort", "off", "no", "n":
		return exitChatPlanModeCommand(session, "quit", rest)

	default:
		// Treat bare path as enter with that plan path.
		if looksLikePlanPath(verb) && rest == "" {
			if err := enterChatPlanMode(session, verb); err != nil {
				fmt.Printf("错误: %v\n", err)
				return false
			}
			state := loadChatPlanMode(session)
			fmt.Printf("提示: 已进入 plan mode（plan=%s, previous=%s）\n",
				state.PlanPath,
				firstNonEmptyChatValue(state.PreviousMode, string(runtimepolicy.ModeDefault)),
			)
			return false
		}
		fmt.Println("用法: /plan [status|enter [path]|exit <approve|request_changes|quit>]")
		return false
	}
}

func exitChatPlanModeCommand(session *ChatSession, decisionToken, notes string) bool {
	if err := exitChatPlanMode(session, decisionToken, notes); err != nil {
		fmt.Printf("错误: %v\n", err)
		return false
	}
	state := loadChatPlanMode(session)
	mode := string(session.PermissionMode)
	switch state.ExitDecision {
	case planmode.ExitApprove:
		fmt.Printf("提示: 已批准计划并退出 plan mode（permission-mode=%s）\n", mode)
	case planmode.ExitRequestChanges:
		fmt.Printf("提示: 已请求修改计划；保持 plan mode（permission-mode=%s, plan=%s）\n", mode, state.PlanPath)
	case planmode.ExitQuit:
		fmt.Printf("提示: 已退出 plan mode（permission-mode=%s）\n", mode)
	default:
		fmt.Printf("提示: plan mode 已更新（permission-mode=%s）\n", mode)
	}
	return false
}

func enterChatPlanMode(session *ChatSession, planPath string) error {
	if session == nil {
		return fmt.Errorf("当前没有活动会话")
	}
	ensureChatPlanModeRuntimeSession(session)

	current := loadChatPlanMode(session)
	previousMode := string(session.PermissionMode)
	if previousMode == "" {
		previousMode = string(runtimepolicy.ModeDefault)
	}
	// Nested enter while already active: keep original previous mode and refresh path.
	if planmode.IsActive(current) && strings.TrimSpace(current.PreviousMode) != "" {
		previousMode = current.PreviousMode
	}
	if strings.EqualFold(strings.TrimSpace(previousMode), string(runtimepolicy.ModePlan)) &&
		(!planmode.IsActive(current) || strings.TrimSpace(current.PreviousMode) == "") {
		// Entering from bare permission-mode=plan without plan state: restore default on exit.
		previousMode = string(runtimepolicy.ModeDefault)
	}

	state := planmode.Enter(previousMode, planPath)
	saveChatPlanMode(session, state)
	applyChatPlanPermissionMode(session, runtimepolicy.ModePlan)
	warnIfChatSessionSyncFails(session, "enter plan mode", syncRuntimeSessionFromChat(session))
	return nil
}

func exitChatPlanMode(session *ChatSession, decisionToken, notes string) error {
	if session == nil {
		return fmt.Errorf("当前没有活动会话")
	}
	ensureChatPlanModeRuntimeSession(session)

	current := loadChatPlanMode(session)
	if !planmode.IsActive(current) && current.Status != planmode.StatusExited {
		// Allow exit even if user only set permission-mode=plan via /mode.
		if session.PermissionMode != runtimepolicy.ModePlan {
			return fmt.Errorf("当前不在 plan mode；先执行 /plan enter")
		}
		current = planmode.Enter(string(runtimepolicy.ModeDefault), planmode.DefaultPlanPath)
	}

	exited, err := planmode.Exit(current, planmode.ExitDecision(decisionToken), notes)
	if err != nil {
		return err
	}

	resume := planmode.ResumeModeAfterExit(exited)
	mode, parseErr := parseChatPermissionMode(resume, false)
	if parseErr != nil {
		mode = runtimepolicy.ModeDefault
	}

	if exited.ExitDecision == planmode.ExitRequestChanges {
		// Stay active for another revision pass while recording the decision.
		exited.Status = planmode.StatusActive
		exited.PendingExitRequest = false
		saveChatPlanMode(session, exited)
		applyChatPlanPermissionMode(session, runtimepolicy.ModePlan)
	} else {
		saveChatPlanMode(session, exited)
		applyChatPlanPermissionMode(session, mode)
	}

	warnIfChatSessionSyncFails(session, "exit plan mode", syncRuntimeSessionFromChat(session))
	return nil
}

func applyChatPlanPermissionMode(session *ChatSession, mode runtimepolicy.Mode) {
	if session == nil {
		return
	}
	session.PermissionMode = mode
	session.RequestedPermissionMode = string(mode)
	session.EffectivePermissionMode = string(mode)
	if session.ActiveTeam != nil {
		session.ActiveTeam.PermissionMode = mode
	}
}

func loadChatPlanMode(session *ChatSession) planmode.State {
	if session == nil || session.RuntimeSession == nil {
		return planmode.State{Status: planmode.StatusInactive}
	}
	return planmode.Load(session.RuntimeSession)
}

func saveChatPlanMode(session *ChatSession, state planmode.State) {
	if session == nil {
		return
	}
	ensureChatPlanModeRuntimeSession(session)
	if session.RuntimeSession == nil {
		return
	}
	planmode.Save(session.RuntimeSession, state)
}

func ensureChatPlanModeRuntimeSession(session *ChatSession) {
	if session == nil {
		return
	}
	if session.RuntimeSession == nil {
		now := time.Now()
		session.RuntimeSession = &runtimechat.Session{
			ID:        "ephemeral-plan-mode",
			CreatedAt: now,
			UpdatedAt: now,
			Metadata: runtimechat.SessionMetadata{
				Context: map[string]interface{}{},
			},
		}
		return
	}
	if session.RuntimeSession.Metadata.Context == nil {
		session.RuntimeSession.Metadata.Context = make(map[string]interface{})
	}
}

func printPlanModeStatus(session *ChatSession) {
	state := loadChatPlanMode(session)
	mode := string(runtimepolicy.ModeDefault)
	if session != nil && strings.TrimSpace(string(session.PermissionMode)) != "" {
		mode = string(session.PermissionMode)
	}
	active := planmode.IsActive(state) || mode == string(runtimepolicy.ModePlan)
	fmt.Printf("plan mode: %s\n", map[bool]string{true: "active", false: "inactive"}[active])
	fmt.Printf("  permission-mode: %s\n", mode)
	if state.PlanPath != "" {
		fmt.Printf("  plan path: %s\n", state.PlanPath)
	} else if active {
		fmt.Printf("  plan path: %s\n", planmode.DefaultPlanPath)
	}
	if len(state.WriteAllowPaths) > 0 {
		fmt.Printf("  write allow: %s\n", strings.Join(state.WriteAllowPaths, ", "))
	} else if active {
		fmt.Printf("  write allow: %s\n", strings.Join(planmode.DefaultWriteAllowPaths(), ", "))
	}
	if state.PreviousMode != "" {
		fmt.Printf("  previous mode: %s\n", state.PreviousMode)
	}
	if state.EnteredAt != "" {
		fmt.Printf("  entered at: %s\n", state.EnteredAt)
	}
	if state.ExitDecision != "" {
		fmt.Printf("  last exit decision: %s\n", state.ExitDecision)
	}
	if state.Notes != "" {
		fmt.Printf("  notes: %s\n", state.Notes)
	}
	if state.PendingExitRequest {
		fmt.Println("  pending exit request: true")
	}
	fmt.Println("用法: /plan enter [path] | /plan exit <approve|request_changes|quit>")
}

func firstToken(text string) string {
	token, _ := splitFirstToken(text)
	return token
}

func splitFirstToken(text string) (string, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", ""
	}
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	// Preserve original spacing after first token for notes.
	idx := strings.Index(text, parts[0])
	rest := strings.TrimSpace(text[idx+len(parts[0]):])
	return parts[0], rest
}

func looksLikePlanPath(token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	lower := strings.ToLower(token)
	if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".txt") {
		return true
	}
	if strings.ContainsAny(token, `/\`) {
		return true
	}
	base := filepath.Base(token)
	return base != token
}
