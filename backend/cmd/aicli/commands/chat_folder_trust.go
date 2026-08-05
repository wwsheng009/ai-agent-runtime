package commands

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
)

// processFolderTrust holds the folder-trust decision for the current CLI process.
// Resolved early (before profile / plugin discovery) so project-scope gates apply
// consistently to plugins, hooks, and MCP loaders.
var (
	processFolderTrustMu    sync.RWMutex
	processFolderTrust      foldertrust.Resolution
	processFolderTrustReady bool
)

// ensureProcessFolderTrust resolves folder trust once for this process (idempotent).
// trustGrant mirrors CLI --trust. interactive=false forces headless decide semantics.
func ensureProcessFolderTrust(trustGrant bool, interactive bool) foldertrust.Resolution {
	processFolderTrustMu.Lock()
	defer processFolderTrustMu.Unlock()
	if processFolderTrustReady {
		return processFolderTrust
	}
	cwd := ""
	if abs, err := os.Getwd(); err == nil {
		cwd = abs
	}
	interactiveCopy := interactive
	processFolderTrust = foldertrust.Resolve(foldertrust.ResolveOptions{
		CWD:         cwd,
		TrustGrant:  trustGrant,
		Interactive: &interactiveCopy,
	})
	processFolderTrustReady = true
	return processFolderTrust
}

// setProcessFolderTrust replaces the process-level resolution (tests / /trust grant).
func setProcessFolderTrust(res foldertrust.Resolution) {
	processFolderTrustMu.Lock()
	defer processFolderTrustMu.Unlock()
	processFolderTrust = res
	processFolderTrustReady = true
}

// resetProcessFolderTrust clears the process cache (tests).
func resetProcessFolderTrust() {
	processFolderTrustMu.Lock()
	defer processFolderTrustMu.Unlock()
	processFolderTrust = foldertrust.Resolution{}
	processFolderTrustReady = false
}

// currentFolderTrust returns the process resolution, resolving with feature-default
// (no grant, interactive from TTY) when never initialized.
func currentFolderTrust() foldertrust.Resolution {
	processFolderTrustMu.RLock()
	ready := processFolderTrustReady
	res := processFolderTrust
	processFolderTrustMu.RUnlock()
	if ready {
		return res
	}
	// Lazy resolve for code paths that never went through HandleChat/exec
	// (plugin CLI helpers, unit tests). No --trust grant; interactive from TTY.
	return ensureProcessFolderTrust(false, folderTrustDefaultInteractive())
}

func folderTrustDefaultInteractive() bool {
	// Mirror foldertrust's TTY check without importing private helper:
	// headless when NoInteractive would be set is handled by callers.
	stdinStat, errIn := os.Stdin.Stat()
	stderrStat, errErr := os.Stderr.Stat()
	if errIn != nil || errErr != nil {
		return false
	}
	stdinTTY := (stdinStat.Mode() & os.ModeCharDevice) != 0
	stderrTTY := (stderrStat.Mode() & os.ModeCharDevice) != 0
	return stdinTTY && stderrTTY
}

// projectScopeAllowed reports whether project-scope plugins/hooks/MCP may load.
// Feature-off → true (preserve prior behavior). Unresolved → resolve lazily.
func projectScopeAllowed() bool {
	res := currentFolderTrust()
	if !res.FeatureEnabled {
		return true
	}
	return res.Trusted
}

// sessionProjectScopeAllowed uses session resolution when present, else process.
func sessionProjectScopeAllowed(session *ChatSession) bool {
	if session != nil && session.FolderTrust.WorkspaceKey != "" {
		if !session.FolderTrust.FeatureEnabled {
			return true
		}
		return session.FolderTrust.Trusted
	}
	return projectScopeAllowed()
}

// folderTrustProjectRoot returns the project root used for project-scoped path checks.
func folderTrustProjectRoot(session *ChatSession) string {
	if session != nil {
		if root := strings.TrimSpace(session.FolderTrust.ProjectRoot); root != "" {
			return root
		}
	}
	res := currentFolderTrust()
	if root := strings.TrimSpace(res.ProjectRoot); root != "" {
		return root
	}
	if cwd, err := os.Getwd(); err == nil {
		return cwd
	}
	return ""
}

// agentdefDiscoverOptions builds agentdef discovery options with folder-trust applied.
// Untrusted workspaces skip project agent roots; builtins, user-home agents, profile,
// and ExtraDirs (e.g. trusted plugin agent dirs) remain available.
func agentdefDiscoverOptions(projectRoot, profileRoot string, extraDirs []string) agentdef.DiscoverOptions {
	opts := agentdef.DiscoverOptions{
		ProfileRoot: strings.TrimSpace(profileRoot),
		ExtraDirs:   extraDirs,
	}
	if projectScopeAllowed() {
		opts.ProjectRoot = strings.TrimSpace(projectRoot)
	} else {
		opts.SkipProjectRoot = true
	}
	return opts
}

// applyChatFolderTrust attaches a resolved trust decision to the session.
func applyChatFolderTrust(session *ChatSession, res foldertrust.Resolution) {
	if session == nil {
		return
	}
	session.FolderTrust = res
	setProcessFolderTrust(res)
}

// handleTrustCommand implements /trust [status].
func handleTrustCommand(session *ChatSession, command string) bool {
	if unifiedDirectInteractiveOutput(session) {
		_ = renderChatCommandResult(session, executeStructuredTrustCommand(session, command), false)
		return false
	}
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}
	arg := strings.ToLower(strings.TrimSpace(extractCommandArgument(command)))
	switch arg {
	case "", "status":
		printFolderTrustStatus(session)
		return false
	case "grant", "yes", "y":
		// Fall through to grant.
	default:
		fmt.Printf("用法: /trust [status|grant]\n")
		printFolderTrustStatus(session)
		return false
	}

	if !foldertrust.FeatureEnabled() {
		fmt.Println("folder trust 功能未启用（设置 AICLI_FOLDER_TRUST=1 后生效）")
		printFolderTrustStatus(session)
		return false
	}

	cwd := folderTrustProjectRoot(session)
	key, err := foldertrust.GrantTrust(cwd)
	if err != nil {
		fmt.Printf("错误: 无法写入信任记录: %v\n", err)
		return false
	}
	// Re-resolve so session + process gates flip to trusted without restart.
	interactive := !session.NoInteractive
	res := foldertrust.Resolve(foldertrust.ResolveOptions{
		CWD:         cwd,
		TrustGrant:  false, // already granted above
		Interactive: &interactive,
	})
	// Ensure source reflects explicit grant when store already trusted.
	if res.Trusted && res.Source == "store" {
		res.Source = "grant"
	}
	if key != "" && res.WorkspaceKey == "" {
		res.WorkspaceKey = key
	}
	applyChatFolderTrust(session, res)
	fmt.Printf("已信任工作区: %s\n", res.WorkspaceKey)
	printFolderTrustStatus(session)
	return false
}

// executeStructuredTrustCommand exposes the read-only trust state through the
// unified command pipeline. Granting trust writes a durable security decision,
// so it remains unavailable until a typed confirmation effect can express the
// user's intent without reviving the legacy terminal prompt path.
func executeStructuredTrustCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	arg := strings.ToLower(strings.TrimSpace(extractCommandArgument(command)))
	switch arg {
	case "", "status":
		return commandTextResult(folderTrustStatusText(session))
	case "grant", "yes", "y":
		return commandTextResult("错误: /trust grant 需要确认交互，尚未迁移到统一渲染命令通道。\n" + folderTrustStatusText(session))
	default:
		return commandTextResult("用法: /trust [status|grant]\n" + folderTrustStatusText(session))
	}
}

func printFolderTrustStatus(session *ChatSession) {
	fmt.Println(folderTrustStatusText(session))
}

func folderTrustStatusText(session *ChatSession) string {
	var res foldertrust.Resolution
	if session != nil && (session.FolderTrust.WorkspaceKey != "" || session.FolderTrust.FeatureEnabled || session.FolderTrust.Source != "") {
		res = session.FolderTrust
	} else {
		res = currentFolderTrust()
	}
	lines := []string{"Folder trust: " + foldertrust.FormatSummary(res)}
	if res.StorePath != "" {
		lines = append(lines, "  Store: "+res.StorePath)
	}
	if !res.FeatureEnabled {
		lines = append(lines, "  Hint: set AICLI_FOLDER_TRUST=1 to enable project plugin/hooks/MCP gating")
	} else if !res.Trusted {
		lines = append(lines, "  Hint: run /trust grant or start with --trust to allow project-scope configs")
	}
	return strings.Join(lines, "\n")
}
