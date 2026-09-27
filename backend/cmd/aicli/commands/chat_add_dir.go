package commands

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// 本文件实现 §4.5 的 CLI 面：/add-dir 与 --add-dir。
//
// 语义：工作区外的目录默认会触发 policy 的外部目录门（external_dir:admit）。
// 用户批准一次即把目录加入会话根集合；/add-dir 提供预先准入（pre-admit），
// 让已知的相邻仓库/共享目录不必逐个调用审批。集合随会话元数据持久化，并在
// 变更后重建本地 runtime actor，使 agent options 中的 allowed_roots 立即生效。

const chatAllowedRootsContextKey = "allowed_roots"

// chatAllowedRoots returns the session's admitted external directories.
// The session field is authoritative; resumed sessions fall back to the
// persisted metadata context.
func chatAllowedRoots(session *ChatSession) []string {
	if session == nil {
		return nil
	}
	if len(session.AllowedRoots) > 0 {
		return append([]string(nil), session.AllowedRoots...)
	}
	if session.RuntimeSession == nil || session.RuntimeSession.Metadata.Context == nil {
		return nil
	}
	raw := session.RuntimeSession.Metadata.Context[chatAllowedRootsContextKey]
	return normalizePersistedAllowedRoots(raw)
}

func normalizePersistedAllowedRoots(raw interface{}) []string {
	var values []string
	switch typed := raw.(type) {
	case nil:
		return nil
	case []string:
		values = append(values, typed...)
	case []interface{}:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				values = append(values, text)
			}
		}
	case string:
		values = strings.FieldsFunc(typed, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n'
		})
	default:
		return nil
	}
	return dedupeChatPaths(values)
}

func dedupeChatPaths(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// setChatAllowedRoots stores the set on the live session and in the durable
// session metadata (best effort: an unpersisted session keeps the live set).
func setChatAllowedRoots(session *ChatSession, roots []string) {
	if session == nil {
		return
	}
	roots = dedupeChatPaths(roots)
	session.AllowedRoots = roots
	if session.RuntimeSession == nil {
		return
	}
	if session.RuntimeSession.Metadata.Context == nil {
		session.RuntimeSession.Metadata.Context = map[string]interface{}{}
	}
	session.RuntimeSession.Metadata.Context[chatAllowedRootsContextKey] = roots
	if session.SessionManager != nil && strings.TrimSpace(session.RuntimeSession.ID) != "" {
		_ = session.SessionManager.UpdateContext(context.Background(), session.RuntimeSession.ID, chatAllowedRootsContextKey, roots)
	}
}

// normalizeChatAllowedRootEntry canonicalizes one admitted directory: ~ expand,
// absolute path, symlinks resolved when possible, and the path must exist as a
// directory (fail loudly instead of admitting a typo).
func normalizeChatAllowedRootEntry(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", fmt.Errorf("空路径")
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") || strings.HasPrefix(trimmed, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil || strings.TrimSpace(home) == "" {
			return "", fmt.Errorf("无法解析用户主目录: %s", trimmed)
		}
		if trimmed == "~" {
			trimmed = home
		} else {
			trimmed = filepath.Join(home, trimmed[2:])
		}
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", fmt.Errorf("无法解析路径 %s: %w", trimmed, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("目录不存在: %s", abs)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("不是目录: %s", abs)
	}
	return filepath.Clean(abs), nil
}

// splitChatPathArguments splits the /add-dir argument list, honoring double
// quotes so Windows paths with spaces can be admitted.
func splitChatPathArguments(argument string) []string {
	argument = strings.TrimSpace(argument)
	if argument == "" {
		return nil
	}
	var (
		parts   []string
		current strings.Builder
		quoted  bool
	)
	flush := func() {
		if value := strings.TrimSpace(current.String()); value != "" {
			parts = append(parts, value)
		}
		current.Reset()
	}
	for _, r := range argument {
		switch {
		case r == '"':
			if quoted {
				quoted = false
				flush()
				continue
			}
			quoted = true
		case !quoted && (r == ' ' || r == '\t'):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return parts
}

// addChatAllowedRoots admits directories, skipping ones that are already
// inside the workspace or already admitted.
func addChatAllowedRoots(session *ChatSession, paths []string) (added []string, notes []string) {
	workspaceRoot := chatPlanWorkspacePath(session)
	existing := chatAllowedRoots(session)
	for _, path := range paths {
		normalized, err := normalizeChatAllowedRootEntry(path)
		if err != nil {
			notes = append(notes, err.Error())
			continue
		}
		if insideChatWorkspace(workspaceRoot, normalized) {
			notes = append(notes, fmt.Sprintf("已在工作区内，无需准入: %s", normalized))
			continue
		}
		duplicate := false
		for _, root := range existing {
			if strings.EqualFold(root, normalized) {
				duplicate = true
				break
			}
		}
		if duplicate {
			notes = append(notes, fmt.Sprintf("已准入: %s", normalized))
			continue
		}
		existing = append(existing, normalized)
		added = append(added, normalized)
	}
	if len(added) > 0 {
		setChatAllowedRoots(session, existing)
	}
	return added, notes
}

// removeChatAllowedRoots revokes previously admitted directories.
func removeChatAllowedRoots(session *ChatSession, paths []string) (removed []string, notes []string) {
	existing := chatAllowedRoots(session)
	kept := make([]string, 0, len(existing))
	for _, root := range existing {
		revoked := false
		for _, path := range paths {
			normalized, err := normalizeChatAllowedRootEntry(path)
			if err != nil {
				normalized = strings.TrimSpace(path)
			}
			if normalized != "" && strings.EqualFold(root, normalized) {
				revoked = true
				break
			}
		}
		if revoked {
			removed = append(removed, root)
			continue
		}
		kept = append(kept, root)
	}
	if len(removed) > 0 {
		setChatAllowedRoots(session, kept)
	} else {
		notes = append(notes, "没有匹配的已准入目录")
	}
	return removed, notes
}

func insideChatWorkspace(workspaceRoot, path string) bool {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	path = strings.TrimSpace(path)
	if workspaceRoot == "" || path == "" {
		return false
	}
	if strings.EqualFold(filepath.Clean(workspaceRoot), filepath.Clean(path)) {
		return true
	}
	rel, err := filepath.Rel(workspaceRoot, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// refreshChatRuntimeForAllowedRoots rebuilds the local runtime actor so the new
// allowed_roots reach the next tool call instead of waiting for a restart.
// 有在途 turn 时整包刷新延迟到回合入口（绝不打断本轮）。
func refreshChatRuntimeForAllowedRoots(session *ChatSession) error {
	if session == nil {
		return nil
	}
	warnIfChatSessionSyncFails(session, "sync allowed roots", syncRuntimeSessionFromChat(session))
	return refreshLocalRuntimeAfterSelection(session, true, chatActorRebuildReasonWorkspaceWrite)
}

// handleAddDirCommand implements /add-dir [list|remove <路径>|<路径> ...].
func handleAddDirCommand(session *ChatSession, command string) bool {
	if unifiedDirectInteractiveOutput(session) {
		_ = renderChatCommandResult(session, executeStructuredAddDirCommand(session, command), false)
		return false
	}
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	fmt.Println(addDirCommandText(session, command))
	return false
}

// executeStructuredAddDirCommand exposes the same /add-dir semantics through
// the unified command pipeline.
func executeStructuredAddDirCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	return commandTextResult(addDirCommandText(session, command))
}

func addDirCommandText(session *ChatSession, command string) string {
	argument := strings.TrimSpace(extractCommandArgument(command))
	fields := splitChatPathArguments(argument)
	action := ""
	if len(fields) > 0 {
		action = strings.ToLower(fields[0])
	}
	switch {
	case argument == "" || action == "list" || action == "ls":
		return chatAllowedRootsText(session)
	case action == "remove" || action == "rm" || action == "delete":
		paths := fields[1:]
		if len(paths) == 0 {
			return "用法: /add-dir remove <路径>\n" + chatAllowedRootsText(session)
		}
		removed, notes := removeChatAllowedRoots(session, paths)
		lines := make([]string, 0, 4)
		for _, root := range removed {
			lines = append(lines, "已移除外部目录: "+root)
		}
		lines = append(lines, notes...)
		if len(removed) > 0 {
			if err := refreshChatRuntimeForAllowedRoots(session); err != nil {
				lines = append(lines, "警告: 刷新本地 runtime 失败: "+err.Error())
			}
		}
		lines = append(lines, chatAllowedRootsText(session))
		return strings.Join(lines, "\n")
	default:
		added, notes := addChatAllowedRoots(session, fields)
		lines := make([]string, 0, 4)
		for _, root := range added {
			lines = append(lines, "已添加外部目录: "+root+"（工作区外的读写不再重复询问）")
		}
		lines = append(lines, notes...)
		if len(added) > 0 {
			if err := refreshChatRuntimeForAllowedRoots(session); err != nil {
				lines = append(lines, "警告: 刷新本地 runtime 失败: "+err.Error()+"（重启会话后生效）")
			}
		}
		lines = append(lines, chatAllowedRootsText(session))
		return strings.Join(lines, "\n")
	}
}

func chatAllowedRootsText(session *ChatSession) string {
	roots := chatAllowedRoots(session)
	lines := []string{"外部目录（工作区外准入）:"}
	if len(roots) == 0 {
		lines = append(lines, "  （无）工作区外的读写会在执行前请求一次准入")
	} else {
		for _, root := range roots {
			lines = append(lines, "  "+root)
		}
	}
	lines = append(lines, "用法: /add-dir <路径> [更多路径] | /add-dir remove <路径> | /add-dir list")
	return strings.Join(lines, "\n")
}

// applyChatAddDirFlagArgs validates --add-dir values at startup so a typo fails
// loudly instead of silently admitting nothing.
func applyChatAddDirFlagArgs(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	roots := make([]string, 0, len(paths))
	for _, path := range paths {
		normalized, err := normalizeChatAllowedRootEntry(path)
		if err != nil {
			return nil, fmt.Errorf("--add-dir: %w", err)
		}
		roots = append(roots, normalized)
	}
	return dedupeChatPaths(roots), nil
}

// chatAllowedRootsPolicyAdmitter wires an approved external_dir:admit decision
// back into the session set (used by hosts that own the policy engine).
func chatAllowedRootsPolicyAdmitter(session *ChatSession) func(dirs []string) {
	if session == nil {
		return nil
	}
	return func(dirs []string) {
		if len(dirs) == 0 {
			return
		}
		added, _ := addChatAllowedRoots(session, dirs)
		if len(added) > 0 {
			_ = refreshChatRuntimeForAllowedRoots(session)
		}
	}
}

// applyACPAdditionalDirectories admits the directories an ACP client sent with
// session/new|load|resume (§4.5). Invalid entries are skipped with a warning:
// a stale client-supplied path must not fail session establishment, and the
// external-directory gate still asks before any of them is touched.
func applyACPAdditionalDirectories(session *ChatSession, dirs []string) []string {
	if session == nil || len(dirs) == 0 {
		return nil
	}
	added, notes := addChatAllowedRoots(session, dirs)
	for _, note := range notes {
		logpkg.Warnf("acp additionalDirectories: %s", note)
	}
	return added
}
