package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// runtimeServerControlCaller is the narrow HTTP surface needed by remote
// control commands. The concrete aicli executor implements it; tests can use
// the same executor against httptest.
type runtimeServerControlCaller interface {
	doRuntimeServerJSON(ctx context.Context, session *ChatSession, method, path, rawQuery string, body interface{}, target interface{}) (int, error)
}

func runtimeServerControl(session *ChatSession) (runtimeServerControlCaller, error) {
	if session == nil || session.ChatExecutor == nil {
		return nil, fmt.Errorf("runtime-server 执行器未配置")
	}
	caller, ok := session.ChatExecutor.(runtimeServerControlCaller)
	if !ok {
		return nil, fmt.Errorf("当前执行器不支持 runtime-server 控制命令")
	}
	return caller, nil
}

func runtimeServerSessionID(session *ChatSession) string {
	if session == nil || session.RuntimeSession == nil {
		return ""
	}
	return strings.TrimSpace(session.RuntimeSession.ID)
}

func runtimeServerSessionPath(session *ChatSession, suffix string) string {
	return "/api/runtime/sessions/" + url.PathEscape(runtimeServerSessionID(session)) + suffix
}

func remoteJSONText(value interface{}) string {
	if value == nil {
		return "（空响应）"
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

// tryExecuteRuntimeServerControlCommand handles commands whose server-side
// control APIs already exist. A matched command never falls through to a local
// store operation.
func tryExecuteRuntimeServerControlCommand(session *ChatSession, command string) (CommandResult, bool) {
	if !chatSessionUsesRuntimeServer(session) {
		return CommandResult{}, false
	}
	cmdLower := strings.ToLower(strings.TrimSpace(command))
	switch {
	case commandMatches(cmdLower, "/plan"):
		return executeRemotePlanCommand(session, command), true
	case commandMatches(cmdLower, "/plans"):
		return executeRemotePlansCommand(session, command), true
	case commandMatches(cmdLower, "/agents"), commandMatches(cmdLower, "/agent"):
		return executeRemoteAgentsCommand(session, command), true
	case commandMatches(cmdLower, "/memory"):
		return executeRemoteMemoryCommand(session, command), true
	case commandMatches(cmdLower, "/supervision"):
		return executeRemoteSupervisionCommand(session, command), true
	case commandMatches(cmdLower, "/debug") && remoteDebugSupervisionWatchdog(command):
		return executeRemoteSupervisionCommand(session, "/supervision watchdog"), true
	default:
		return CommandResult{}, false
	}
}

func remoteDebugSupervisionWatchdog(command string) bool {
	fields := strings.Fields(strings.ToLower(extractCommandArgument(command)))
	if len(fields) < 2 || fields[0] != "supervision" {
		return false
	}
	switch fields[1] {
	case "watchdog", "supervisor", "execution-supervisor":
		return true
	default:
		return false
	}
}

func executeRemotePlanCommand(session *ChatSession, command string) CommandResult {
	caller, err := runtimeServerControl(session)
	if err != nil {
		return commandErrorResult(err)
	}
	if strings.TrimSpace(runtimeServerSessionID(session)) == "" {
		return commandErrorResult(fmt.Errorf("当前会话没有 runtime session id"))
	}
	args := strings.Fields(extractCommandArgument(command))
	sub := "status"
	if len(args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
	}
	path := runtimeServerSessionPath(session, "/plan")
	switch sub {
	case "", "status", "review":
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, path, "", nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server plan 状态失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "enter", "on":
		body := map[string]interface{}{"action": "enter"}
		if len(args) > 1 {
			body["plan_path"] = args[1]
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", body, &response); err != nil {
			return commandErrorResult(fmt.Errorf("server plan enter 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "exit", "approve", "request_changes", "quit":
		decision := sub
		notesAt := 1
		if sub == "exit" {
			if len(args) < 2 {
				return commandErrorResult(fmt.Errorf("用法: /plan exit <approve|request_changes|quit> [notes]"))
			}
			decision = strings.ToLower(strings.TrimSpace(args[1]))
			notesAt = 2
		}
		body := map[string]interface{}{
			"action":   "exit",
			"decision": decision,
		}
		if len(args) > notesAt {
			body["notes"] = strings.Join(args[notesAt:], " ")
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", body, &response); err != nil {
			return commandErrorResult(fmt.Errorf("server plan exit 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	default:
		return commandErrorResult(fmt.Errorf("runtime-server 模式未接入 /plan %s；已显式拒绝，未访问本地 plan store", sub))
	}
}

func executeRemotePlansCommand(session *ChatSession, command string) CommandResult {
	caller, err := runtimeServerControl(session)
	if err != nil {
		return commandErrorResult(err)
	}
	args := strings.Fields(extractCommandArgument(command))
	if len(args) == 0 || strings.EqualFold(args[0], "list") {
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/plans", "", nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server plans 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "diff":
		if len(args) < 2 {
			return commandErrorResult(fmt.Errorf("用法: /plans diff <id> [vA [vB]]"))
		}
		values := url.Values{}
		values.Set("id", args[1])
		if len(args) > 2 {
			values.Set("from", strings.TrimPrefix(args[2], "v"))
		}
		if len(args) > 3 {
			values.Set("to", strings.TrimPrefix(args[3], "v"))
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/plans/diff", values.Encode(), nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server plan diff 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "reopen":
		if len(args) < 2 {
			return commandErrorResult(fmt.Errorf("用法: /plans reopen <id> [vN] [--force]"))
		}
		if strings.TrimSpace(runtimeServerSessionID(session)) == "" {
			return commandErrorResult(fmt.Errorf("当前会话没有 runtime session id"))
		}
		body := map[string]interface{}{"plan_id": args[1]}
		for _, arg := range args[2:] {
			switch {
			case strings.EqualFold(arg, "--force"):
				body["force"] = true
			case strings.HasPrefix(strings.ToLower(arg), "v"):
				if version, parseErr := strconv.Atoi(strings.TrimPrefix(arg, "v")); parseErr == nil {
					body["version"] = version
				}
			}
		}
		var response map[string]interface{}
		path := runtimeServerSessionPath(session, "/plan/reopen")
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", body, &response); err != nil {
			return commandErrorResult(fmt.Errorf("server plan reopen 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	default:
		values := url.Values{}
		values.Set("id", args[0])
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/plans", values.Encode(), nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server plan 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	}
}

func executeRemoteAgentsCommand(session *ChatSession, command string) CommandResult {
	caller, err := runtimeServerControl(session)
	if err != nil {
		return commandErrorResult(err)
	}
	args := strings.Fields(extractCommandArgument(command))
	if len(args) == 0 || strings.EqualFold(args[0], "list") {
		values := url.Values{}
		values.Set("include_closed", "true")
		values.Set("limit", "100")
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/agent-control/agents", values.Encode(), nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server agents 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "routing":
		sessionID := runtimeServerSessionID(session)
		if sessionID == "" {
			return commandErrorResult(fmt.Errorf("当前会话没有 runtime session id"))
		}
		var response map[string]interface{}
		path := runtimeServerSessionPath(session, "/routing")
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, path, "", nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server session routing 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "defs", "definitions":
		sessionID := runtimeServerSessionID(session)
		if sessionID == "" {
			return commandErrorResult(fmt.Errorf("当前会话没有 runtime session id"))
		}
		sub := ""
		name := ""
		if len(args) > 1 {
			sub = strings.ToLower(strings.TrimSpace(args[1]))
		}
		switch sub {
		case "", "list", "ls":
			sub = "list"
		case "show":
			if len(args) < 3 {
				return commandErrorResult(fmt.Errorf("用法: /agents defs show <name>"))
			}
			name = strings.TrimSpace(args[2])
		case "lint", "check":
			sub = "lint"
		default:
			return commandTextResult("用法: /agents defs [list|show <name>|lint]")
		}
		values := url.Values{}
		values.Set("action", sub)
		if name != "" {
			values.Set("name", name)
		}
		if workspace := strings.TrimSpace(session.WorkspaceAnchor); workspace != "" {
			values.Set("workspace_path", workspace)
		}
		if profileRoot := strings.TrimSpace(session.ProfileRoot); profileRoot != "" {
			values.Set("profile_root", profileRoot)
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/agent-definitions", values.Encode(), nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server agent definitions 失败: %w", err))
		}
		if text, _ := response["text"].(string); strings.TrimSpace(text) != "" {
			return commandTextResult(text)
		}
		return commandTextResult(remoteJSONText(response))
	case "cleanup", "prune":
		opts, parseErr := parseChatAgentCleanupOptions(strings.Join(args, " "))
		if parseErr != nil {
			return commandTextResult(chatAgentCleanupUsageText() + "\n错误: " + parseErr.Error())
		}
		sessionID := runtimeServerSessionID(session)
		if sessionID == "" {
			return commandErrorResult(fmt.Errorf("当前会话没有 runtime session id"))
		}
		payload := map[string]interface{}{
			"session_id": sessionID,
			"dry_run":    opts.DryRun,
			"idle_ms":    opts.Idle.Milliseconds(),
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, "/api/runtime/agent-control/cleanup", "", payload, &response); err != nil {
			return commandErrorResult(fmt.Errorf("执行 server AgentControl cleanup 失败: %w", err))
		}
		if text, _ := response["text"].(string); strings.TrimSpace(text) != "" {
			return commandTextResult(text)
		}
		return commandTextResult(remoteJSONText(response))
	}
	target := ""
	singular := commandMatches(strings.ToLower(strings.TrimSpace(command)), "/agent")
	if singular {
		sub = "view"
		target = strings.TrimSpace(args[0])
	} else if len(args) > 1 {
		target = strings.TrimSpace(args[1])
	}
	parentID := runtimeServerSessionID(session)
	if target == "" {
		return commandErrorResult(fmt.Errorf("用法: /agents status|view|events <id>"))
	}
	switch sub {
	case "status":
		var response map[string]interface{}
		path := runtimeServerSessionPath(session, "/agents/"+url.PathEscape(target))
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, path, "", nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server agent status 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "events", "view":
		values := url.Values{}
		values.Set("after_seq", "0")
		values.Set("limit", "50")
		var response map[string]interface{}
		path := runtimeServerSessionPath(session, "/agents/"+url.PathEscape(target)+"/events")
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, path, values.Encode(), nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server agent events 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "send", "followup", "input":
		if len(args) < 3 {
			return commandErrorResult(fmt.Errorf("用法: /agents %s <id> <message>", sub))
		}
		body := map[string]interface{}{"message": strings.Join(args[2:], " ")}
		if sub == "followup" {
			body["interrupt"] = false
		}
		var response map[string]interface{}
		path := runtimeServerSessionPath(session, "/agents/"+url.PathEscape(target)+"/input")
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", body, &response); err != nil {
			return commandErrorResult(fmt.Errorf("发送 server agent input 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "answer":
		_, questionID, answerText := parseChatAgentAnswerArgs(strings.Join(args, " "))
		if target == "" || questionID == "" {
			return commandErrorResult(fmt.Errorf("用法: /agents answer <target> <question_id> [text]"))
		}
		body := map[string]interface{}{
			"question_id": questionID,
			"answer":      answerText,
		}
		var response map[string]interface{}
		path := runtimeServerSessionPath(session, "/agents/"+url.PathEscape(target)+"/answer")
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", body, &response); err != nil {
			return commandErrorResult(fmt.Errorf("server agent answer 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "approve", "deny":
		_, requestID := parseChatAgentApprovalArgs(strings.Join(args, " "))
		if target == "" || requestID == "" {
			return commandErrorResult(fmt.Errorf("用法: /agents %s <target> <request_id>", sub))
		}
		body := map[string]interface{}{
			"request_id": requestID,
			"allow":      sub == "approve",
		}
		var response map[string]interface{}
		path := runtimeServerSessionPath(session, "/agents/"+url.PathEscape(target)+"/approve")
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", body, &response); err != nil {
			return commandErrorResult(fmt.Errorf("server agent %s 失败: %w", sub, err))
		}
		return commandTextResult(remoteJSONText(response))
	case "close", "resume":
		var response map[string]interface{}
		path := runtimeServerSessionPath(session, "/agents/"+url.PathEscape(target)+"/"+sub)
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("server agent %s 失败: %w", sub, err))
		}
		return commandTextResult(remoteJSONText(response))
	default:
		return commandErrorResult(fmt.Errorf("runtime-server 模式未接入 /agents %s（parent=%s）；已显式拒绝，未访问本地 registry", sub, parentID))
	}
}

func executeRemoteMemoryCommand(session *ChatSession, command string) CommandResult {
	caller, err := runtimeServerControl(session)
	if err != nil {
		return commandErrorResult(err)
	}
	args := strings.Fields(extractCommandArgument(command))
	sub := "status"
	if len(args) > 0 {
		sub = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch sub {
	case "", "status":
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/memory/status", "", nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server memory status 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "add", "note", "flush", "remember":
		text := strings.TrimSpace(strings.Join(args[1:], " "))
		if text == "" {
			return commandErrorResult(fmt.Errorf("用法: /memory add <text>"))
		}
		body := map[string]interface{}{
			"text":       text,
			"source":     "manual",
			"session_id": runtimeServerSessionID(session),
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, "/api/runtime/memory/records", "", body, &response); err != nil {
			return commandErrorResult(fmt.Errorf("写入 server memory 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "list", "ls", "recent", "records":
		rest, sourceKind, valid := parseChatMemorySourceKindOption(strings.Join(args[1:], " "))
		if !valid {
			return commandErrorResult(fmt.Errorf("用法: /memory list [n] [--source-kind <kind>]"))
		}
		limit := 20
		if token := strings.TrimSpace(firstToken(strings.Join(rest, " "))); token != "" {
			if n, parseErr := strconv.Atoi(token); parseErr == nil && n > 0 {
				limit = n
			}
		}
		if limit > 200 {
			limit = 200
		}
		values := url.Values{}
		values.Set("limit", strconv.Itoa(limit))
		if sourceKind != "" {
			values.Set("source_kind", sourceKind)
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/memory/records", values.Encode(), nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server memory 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "search", "find", "query":
		query, sourceKind, valid := parseChatMemorySearchArgs(strings.Join(args[1:], " "))
		if !valid {
			return commandErrorResult(fmt.Errorf("用法: /memory search <query> [--source-kind <kind>]"))
		}
		values := url.Values{}
		values.Set("q", query)
		values.Set("limit", "20")
		if sourceKind != "" {
			values.Set("source_kind", sourceKind)
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/memory/records", values.Encode(), nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("搜索 server memory 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "show":
		if len(args) < 2 {
			return commandErrorResult(fmt.Errorf("用法: /memory show <record-id>"))
		}
		var response map[string]interface{}
		path := "/api/runtime/memory/records/" + url.PathEscape(args[1])
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, path, "", nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server memory record 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "delete", "forget":
		if len(args) < 2 {
			return commandErrorResult(fmt.Errorf("用法: /memory delete <record-id>"))
		}
		var response map[string]interface{}
		path := "/api/runtime/memory/records/" + url.PathEscape(args[1])
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodDelete, path, "", nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("删除 server memory record 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "activate":
		if len(args) < 2 {
			return commandErrorResult(fmt.Errorf("用法: /memory activate <summary-id>"))
		}
		var response map[string]interface{}
		path := "/api/runtime/memory/summaries/" + url.PathEscape(args[1]) + "/activate"
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("激活 server memory summary 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "consolidate":
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, "/api/runtime/memory/consolidate", "", map[string]interface{}{}, &response); err != nil {
			return commandErrorResult(fmt.Errorf("server memory consolidate 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "jobs":
		return executeRemoteMemoryJobsCommand(session, caller, args[1:])
	case "rebuild", "rebuild-summaries":
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, "/api/runtime/memory/summaries/rebuild", "", map[string]interface{}{}, &response); err != nil {
			return commandErrorResult(fmt.Errorf("重建 server memory summaries 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "mode":
		if len(args) < 2 {
			return commandErrorResult(fmt.Errorf("用法: /memory mode <off|observe|...>"))
		}
		sessionID := runtimeServerSessionID(session)
		if sessionID == "" {
			return commandErrorResult(fmt.Errorf("当前会话没有 runtime session id"))
		}
		var response map[string]interface{}
		path := "/api/runtime/memory/sessions/" + url.PathEscape(sessionID) + "/mode"
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", map[string]interface{}{"mode": args[1]}, &response); err != nil {
			return commandErrorResult(fmt.Errorf("设置 server memory session mode 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "why-not":
		values := url.Values{}
		if len(args) > 1 {
			values.Set("query", strings.Join(args[1:], " "))
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/memory/why-not", values.Encode(), nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server memory why-not 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	default:
		return commandErrorResult(fmt.Errorf("runtime-server 模式未接入 /memory %s；已显式拒绝，未访问本地项目记忆", sub))
	}
}

func executeRemoteMemoryJobsCommand(
	session *ChatSession,
	caller runtimeServerControlCaller,
	args []string,
) CommandResult {
	if len(args) == 0 {
		return commandErrorResult(fmt.Errorf(
			"用法: /memory jobs replay-preview [limit] | replay <digest-id> --yes [--reason <text>] | deadline-repair [limit] [--apply --yes]",
		))
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "replay-preview", "replay_preview":
		limit := 100
		if len(args) > 1 {
			n, err := strconv.Atoi(strings.TrimSpace(args[1]))
			if err != nil || n <= 0 {
				return commandErrorResult(fmt.Errorf("用法: /memory jobs replay-preview [limit 1..500]"))
			}
			limit = n
		}
		if limit > 500 {
			limit = 500
		}
		values := url.Values{}
		values.Set("limit", strconv.Itoa(limit))
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(
			context.Background(), session, http.MethodGet,
			"/api/runtime/memory/digests/replay-preview", values.Encode(), nil, &response,
		); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server memory replay preview 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "replay":
		if len(args) < 2 {
			return commandErrorResult(fmt.Errorf("用法: /memory jobs replay <digest-id> --yes [--reason <text>]"))
		}
		id := strings.TrimSpace(args[1])
		options := strings.Join(args[2:], " ")
		reason, valid := parseMemoryJobOperatorOptions(options)
		if id == "" || !valid || !hasExactCommandFlag(options, "--yes") {
			return commandErrorResult(fmt.Errorf("用法: /memory jobs replay <digest-id> --yes [--reason <text>]"))
		}
		body := map[string]interface{}{"reason": reason}
		var response map[string]interface{}
		path := "/api/runtime/memory/digests/" + url.PathEscape(id) + "/replay"
		if _, err := caller.doRuntimeServerJSON(
			context.Background(), session, http.MethodPost, path, "", body, &response,
		); err != nil {
			return commandErrorResult(fmt.Errorf("server memory digest replay 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "deadline-repair", "deadline_repair":
		limit, apply, reason, valid := parseMemoryJobDeadlineRepairOptions(strings.Join(args[1:], " "))
		if !valid || (apply && !hasExactCommandFlag(strings.Join(args[1:], " "), "--yes")) {
			return commandErrorResult(fmt.Errorf(
				"用法: /memory jobs deadline-repair [limit 1..500] [--apply --yes] [--reason <text>]",
			))
		}
		body := map[string]interface{}{
			"limit": limit,
			"apply": apply,
		}
		if reason != "" {
			body["reason"] = reason
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(
			context.Background(), session, http.MethodPost,
			"/api/runtime/memory/jobs/deadline-repair", "", body, &response,
		); err != nil {
			return commandErrorResult(fmt.Errorf("server memory deadline repair 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	default:
		return commandErrorResult(fmt.Errorf(
			"runtime-server 模式未接入 /memory jobs %s；已显式拒绝，未访问本地项目记忆", sub,
		))
	}
}

func executeRemoteSupervisionCommand(session *ChatSession, command string) CommandResult {
	caller, err := runtimeServerControl(session)
	if err != nil {
		return commandErrorResult(err)
	}
	req, err := parseChatSupervisionRequest(extractCommandArgument(command))
	if err != nil {
		return commandErrorResult(err)
	}
	switch req.Subcommand {
	case "help":
		return commandTextResult(chatSupervisionUsageText())
	case "status", "audit":
		return executeRemoteSupervisionAudit(session, caller, req)
	case "wake":
		return executeRemoteSupervisionWake(session, caller, req)
	default:
		detail, err := parseChatDebugSupervisionRequest(req.Forward)
		if err != nil {
			return commandErrorResult(err)
		}
		return executeRemoteSupervisionDetail(session, caller, detail)
	}
}

func executeRemoteSupervisionAudit(session *ChatSession, caller runtimeServerControlCaller, req chatSupervisionRequest) CommandResult {
	sessionID := runtimeServerSessionID(session)
	if sessionID == "" {
		return commandErrorResult(fmt.Errorf("当前会话没有 runtime session id"))
	}
	values := url.Values{}
	values.Set("root_session_id", sessionID)
	if strings.TrimSpace(req.TeamID) != "" {
		values.Del("root_session_id")
		values.Set("root_team_id", strings.TrimSpace(req.TeamID))
	}
	if req.Limit > 0 {
		values.Set("limit", strconv.Itoa(req.Limit))
	}
	var response map[string]interface{}
	if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/supervision/audit", values.Encode(), nil, &response); err != nil {
		return commandErrorResult(fmt.Errorf("读取 server supervision audit 失败: %w", err))
	}
	return commandTextResult(remoteJSONText(response))
}

func executeRemoteSupervisionWake(session *ChatSession, caller runtimeServerControlCaller, req chatSupervisionRequest) CommandResult {
	if !req.Deliver {
		return executeRemoteSupervisionAudit(session, caller, req)
	}
	sessionID := runtimeServerSessionID(session)
	if sessionID == "" {
		return commandErrorResult(fmt.Errorf("当前会话没有 runtime session id"))
	}
	body := map[string]interface{}{
		"root_scope_id":            sessionID,
		"target_parent_session_id": sessionID,
	}
	if req.Limit > 0 {
		body["limit"] = req.Limit
	}
	var response map[string]interface{}
	if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, "/api/runtime/supervision/wake/drain", "", body, &response); err != nil {
		return commandErrorResult(fmt.Errorf("投递 server supervision wake 失败: %w", err))
	}
	return commandTextResult(remoteJSONText(response))
}

func executeRemoteSupervisionDetail(session *ChatSession, caller runtimeServerControlCaller, req chatDebugSupervisionRequest) CommandResult {
	sessionID := runtimeServerSessionID(session)
	if sessionID == "" {
		return commandErrorResult(fmt.Errorf("当前会话没有 runtime session id"))
	}
	switch strings.ToLower(strings.TrimSpace(req.Subcommand)) {
	case "list":
		values := url.Values{}
		values.Set("root_scope_id", sessionID)
		values.Set("include_resolved_since", "true")
		if req.Limit > 0 {
			values.Set("limit", strconv.Itoa(req.Limit))
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/supervision/digest", values.Encode(), nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server supervision digest 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "metrics":
		values := url.Values{}
		values.Set("root_session_id", sessionID)
		if req.Limit > 0 {
			values.Set("window_limit", strconv.Itoa(req.Limit))
		}
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/supervision/metrics", values.Encode(), nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server supervision metrics 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "ack":
		if strings.TrimSpace(req.Note) == "" {
			return commandErrorResult(fmt.Errorf("ack 必须带 --note <审计说明>"))
		}
		body := map[string]interface{}{
			"root_scope_id":        sessionID,
			"note":                 strings.TrimSpace(req.Note),
			"expected_version":     req.ExpectedVersion,
			"has_expected_version": req.HasVersion,
		}
		var response map[string]interface{}
		path := "/api/runtime/supervision/notifications/" + url.PathEscape(req.NotificationID) + "/ack"
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", body, &response); err != nil {
			return commandErrorResult(fmt.Errorf("server supervision ack 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "defer":
		until, err := parseChatDebugSupervisionUntil(req.Until)
		if err != nil {
			return commandErrorResult(err)
		}
		body := map[string]interface{}{
			"root_scope_id":        sessionID,
			"until":                until.UTC().Format(time.RFC3339),
			"reason":               strings.TrimSpace(req.Reason),
			"expected_version":     req.ExpectedVersion,
			"has_expected_version": req.HasVersion,
		}
		var response map[string]interface{}
		path := "/api/runtime/supervision/notifications/" + url.PathEscape(req.NotificationID) + "/defer"
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", body, &response); err != nil {
			return commandErrorResult(fmt.Errorf("server supervision defer 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "resolve":
		body := map[string]interface{}{
			"root_scope_id":        sessionID,
			"resolution":           strings.TrimSpace(req.State),
			"expected_version":     req.ExpectedVersion,
			"has_expected_version": req.HasVersion,
		}
		var response map[string]interface{}
		path := "/api/runtime/supervision/notifications/" + url.PathEscape(req.NotificationID) + "/resolve"
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", body, &response); err != nil {
			return commandErrorResult(fmt.Errorf("server supervision resolve 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "control":
		cascade := "target"
		if strings.EqualFold(strings.TrimSpace(req.Cascade), "descendants") {
			cascade = "descendants"
		}
		body := map[string]interface{}{
			"root_scope_id":        sessionID,
			"action":               strings.TrimSpace(req.Action),
			"reason":               strings.TrimSpace(req.Reason),
			"cascade_mode":         cascade,
			"expected_version":     req.ExpectedVersion,
			"has_expected_version": req.HasVersion,
		}
		var response map[string]interface{}
		path := "/api/runtime/supervision/notifications/" + url.PathEscape(req.NotificationID) + "/control"
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodPost, path, "", body, &response); err != nil {
			return commandErrorResult(fmt.Errorf("server supervision control 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "state":
		var response map[string]interface{}
		path := runtimeServerSessionPath(session, "/runtime")
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, path, "", nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server supervision state 失败: %w", err))
		}
		return commandTextResult(remoteJSONText(response))
	case "watchdog", "supervisor", "execution-supervisor":
		var response map[string]interface{}
		if _, err := caller.doRuntimeServerJSON(context.Background(), session, http.MethodGet, "/api/runtime/supervision/watchdog", "", nil, &response); err != nil {
			return commandErrorResult(fmt.Errorf("读取 server execution supervisor watchdog 失败: %w", err))
		}
		if text, _ := response["text"].(string); strings.TrimSpace(text) != "" {
			return commandTextResult(text)
		}
		return commandTextResult(remoteJSONText(response))
	default:
		return commandErrorResult(fmt.Errorf("runtime-server 模式未接入 /supervision %s", req.Subcommand))
	}
}
