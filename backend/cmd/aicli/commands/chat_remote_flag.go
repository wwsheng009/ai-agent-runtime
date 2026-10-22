package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
	"github.com/wwsheng009/ai-agent-runtime/internal/remotetargets"
)

// remoteFlagTarget is what `--remote <spec>` contributes to one chat session:
// a single in-memory MCP server entry plus its per-target capability grant.
//
// Design notes
//
//  1. In-memory, never written to disk. `--remote` is a session-scoped decision;
//     silently rewriting the user's mcp.yaml would change the behaviour of every
//     later unrelated session.
//
//  2. Additive, not exclusive. The synthesized server is MERGED with whatever
//     mcp.yaml already provides, so other MCP tools stay reachable. Isolation is a
//     different feature and a different flag.
//
//  3. The user supplies only the endpoint. Tool prefix, transport type, execution
//     scope and the capability grant are all derived from the spec, so there is no
//     second configuration file to keep in sync.
type remoteFlagTarget struct {
	// ServerName is the mcpServers key for this session only.
	ServerName string
	// Config is the synthesized MCP entry.
	Config mcpconfig.MCPConfig
	// Prefix is the FALLBACK tool-name prefix, derived from the host.
	//
	// The effective prefix comes from the target's own `toolPrefix` (carried in
	// Config.Args for ssh-stdio and resolved server-side for HTTP). This value only
	// applies when the remote server was started without one, and it exists so that
	// even that case cannot collide with a local builtin — which the collision
	// guard would refuse.
	Prefix string
	// Depth is the requested capability ladder (readonly|standard|ops).
	Depth remotetargets.SpecDepth
	// Warnings are the expansion's non-fatal notices.
	//
	// `#depth` only takes effect as a server-side PROFILE: ssh-stdio starts the
	// server with it, while an http target is already running and cannot be
	// narrowed from here. That difference is reported by Expand, and dropping the
	// warning would leave `--remote http://host#standard` looking like it applied a
	// restriction it did not apply - the exact silent-lie shape this feature has
	// already been fixed for twice.
	Warnings []string
}

// buildRemoteFlagTarget parses `--remote` and synthesizes the MCP entry.
//
// Returns (nil, nil) for an empty spec so callers can treat "flag absent" as a
// no-op without a separate check.
func buildRemoteFlagTarget(spec string) (*remoteFlagTarget, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := remotetargets.ParseSpec(trimmed)
	if err != nil {
		return nil, fmt.Errorf("--remote %q: %w", trimmed, err)
	}
	expanded, err := parsed.Expand()
	if err != nil {
		return nil, fmt.Errorf("--remote %q: %w", trimmed, err)
	}

	// Expand produces exactly one server. Read it without relying on map order,
	// and fail loudly if that invariant ever changes.
	var serverName string
	for name := range expanded.Servers {
		if serverName != "" {
			return nil, fmt.Errorf("--remote %q expanded to multiple servers (%v); this is a bug", trimmed, keysOf(expanded.Servers))
		}
		serverName = name
	}
	if serverName == "" {
		return nil, fmt.Errorf("--remote %q produced no MCP server entry", trimmed)
	}

	return &remoteFlagTarget{
		ServerName: serverName,
		Config:     expanded.Servers[serverName],
		Prefix:     "remote_" + serverName,
		Depth:      parsed.Depth,
		Warnings:   append([]string(nil), expanded.Warnings...),
	}, nil
}

// applyRemoteFlagTarget merges the synthesized server into a config snapshot.
//
// The return value reports what happened so the caller can surface it:
//
//	added    – the server was new and is now part of the snapshot
//	replaced – a same-named entry existed (a real mcp.yaml target with the same
//	           host); the flag wins because the user typed it on this command line
//
// A nil target (no flag) returns the input untouched.
func applyRemoteFlagTarget(cfg *mcpconfig.Config, target *remoteFlagTarget) (added, replaced bool) {
	if target == nil {
		return false, false
	}
	if cfg == nil {
		return false, false
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]mcpconfig.MCPConfig{}
	}
	_, existed := cfg.MCPServers[target.ServerName]
	cfg.MCPServers[target.ServerName] = target.Config
	return !existed, existed
}

func keysOf(input map[string]mcpconfig.MCPConfig) []string {
	names := make([]string, 0, len(input))
	for name := range input {
		names = append(names, name)
	}
	return names
}

// remoteFlagMCPLoader is implemented by managers that can accept a fully
// materialized config snapshot.
//
// It exists so `--remote` does not need a config FILE: the caller loads whatever
// mcp.yaml provides (possibly nothing), merges the synthesized server, and hands
// the result over in memory.
type remoteFlagMCPLoader interface {
	LoadConfigFromConfig(cfg *mcpconfig.Config) error
}

// loadMCPConfigWithRemoteFlag loads the ordinary MCP configuration, then merges
// the `--remote` server into it.
//
// The two-step shape matters: the file chain (user → project → upward) is still
// honoured, so `--remote` composes with existing servers instead of replacing the
// whole MCP surface. When no config file exists anywhere, the base is simply
// empty and the flag is the only server — which is the "no local mcp.yaml needed"
// case the flag is meant to cover.
//
// The `remoteTargets` declaration contributes its generated servers on the same
// snapshot, so one file can both declare a target and connect it. Before that,
// `Expanded.Servers` had no production consumer: the declaration was read only
// for its authorization table, and every target still needed a duplicate
// mcp.yaml entry (gap G7).
func loadMCPConfigWithRemoteFlag(mgr manager.Manager, configPath, explicitOverride, baseDir string, selection runtimeprofileinput.ResolvedMCPSelection, target *remoteFlagTarget, declaration *remoteTargetsDeclaration) error {
	if target == nil && len(declarationServerNames(declaration)) == 0 {
		// Nothing is contributed in memory: keep the existing behaviour byte for byte.
		return loadMCPConfigForSelectionFrom(mgr, configPath, explicitOverride, baseDir, selection)
	}

	scoped, ok := mgr.(remoteFlagMCPLoader)
	if !ok {
		return fmt.Errorf("MCP 管理器不支持内存配置快照，无法应用 %s", remoteContributionSource(target, declaration))
	}

	// Materialize the base config: the layered file chain when a file exists,
	// otherwise an empty shell that the in-memory contributions populate.
	base, dropped, err := resolveMCPConfigBaseForRemote(configPath, explicitOverride, baseDir, selection)
	if err != nil {
		return err
	}
	// Declaration servers go in first: a hand-written mcp.yaml entry with the same
	// name is a conflict the user must resolve, not something to shadow.
	if err := applyRemoteTargetsServers(base, declaration); err != nil {
		return err
	}
	added, replaced := applyRemoteFlagTarget(base, target)
	if err := scoped.LoadConfigFromConfig(base); err != nil {
		return fmt.Errorf("加载 MCP 配置失败: %w", err)
	}
	emitMCPSelectionNotice(dropped)
	emitRemoteTargetWarnings(declarationWarnings(declaration))
	emitRemoteFlagNotice(target, added, replaced)
	return nil
}

// applyRemoteTargetsServers merges the servers generated from the
// `remoteTargets` declaration into a config snapshot.
//
// Unlike the `--remote` flag (which wins over a same-named file entry because the
// user typed it on this command line), a declaration-vs-mcp.yaml collision is an
// error: both are files, neither is more authoritative, and silently picking one
// would leave the other looking effective while being dead.
func applyRemoteTargetsServers(cfg *mcpconfig.Config, declaration *remoteTargetsDeclaration) error {
	if cfg == nil || declaration == nil || len(declaration.Servers) == 0 {
		return nil
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]mcpconfig.MCPConfig{}
	}
	names := declarationServerNames(declaration)
	for _, name := range names {
		if _, exists := cfg.MCPServers[name]; exists {
			return fmt.Errorf("remoteTargets 中的目标 %q 与 mcp.yaml 中的同名服务器冲突：请只保留一份声明（remoteTargets 会自己连接它声明的目标，无需再写 mcp.yaml）", name)
		}
		cfg.MCPServers[name] = declaration.Servers[name]
	}
	return nil
}

// declarationServerNames returns the sorted server names a declaration generates.
func declarationServerNames(declaration *remoteTargetsDeclaration) []string {
	if declaration == nil || len(declaration.Servers) == 0 {
		return nil
	}
	names := make([]string, 0, len(declaration.Servers))
	for name := range declaration.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func declarationWarnings(declaration *remoteTargetsDeclaration) []string {
	if declaration == nil {
		return nil
	}
	return declaration.Warnings
}

// remoteContributionSource names what is being applied, for the error message a
// manager without snapshot support produces.
func remoteContributionSource(target *remoteFlagTarget, declaration *remoteTargetsDeclaration) string {
	var parts []string
	if len(declarationServerNames(declaration)) > 0 {
		parts = append(parts, "remoteTargets 声明的服务器")
	}
	if target != nil {
		parts = append(parts, "--remote")
	}
	if len(parts) == 0 {
		return "远程目标"
	}
	return strings.Join(parts, " 与 ")
}

// emitRemoteTargetWarnings reports a lossy or ignored declaration.
//
// Warnings were previously returned by the loader and then dropped on the floor
// by every caller (`targets, _ := ...`), which is the same silent-failure shape as
// the `--remote` connect check: a file the user wrote had no visible effect.
func emitRemoteTargetWarnings(warnings []string) {
	for _, warning := range remoteTargetWarnings(warnings) {
		// Routed through the shared diagnostic channel, never straight to stderr:
		// in an interactive session the bare write lands on the fixed bottom
		// surface and overwrites the status bar with half a line (reported live,
		// 2026-10-22). Without a registered interactive session the helper falls
		// back to stderr, which is the correct pre-TUI / non-interactive behaviour.
		//
		// The dynamic row is transient and width-clamped, so the full text also goes
		// to the log: a configuration warning must remain retrievable after the
		// six-second notice expires.
		logpkg.Warnf("remoteTargets: %s", warning)
		aicliDiagf("警告: remoteTargets: %s\n", warning)
	}
}

// resolveMCPConfigBaseForRemote materializes the base config the `--remote` server
// will be merged into.
//
// Three cases:
//
//	file(s) found            → layered load, then apply the profile selection
//	no file at all           → empty config (the flag is the whole surface)
//	selection given + file    → snapshot with the selection applied
//
// The empty-config case is the point of the feature: without it the loader returns
// early on "no config file" and the flag would have nothing to attach to.
func resolveMCPConfigBaseForRemote(configPath, explicitOverride, baseDir string, selection runtimeprofileinput.ResolvedMCPSelection) (*mcpconfig.Config, []string, error) {
	// Layered load (honours user → project → upward ordering).
	if strings.TrimSpace(baseDir) != "" {
		if layered, err := mcpconfig.LoadEffectiveFrom(baseDir, explicitOverride); err == nil && layered != nil {
			return applySelectionToConfig(layered.Config, selection)
		}
	}
	if layered, err := mcpconfig.LoadEffective(explicitOverride); err == nil && layered != nil {
		return applySelectionToConfig(layered.Config, selection)
	}

	// A single explicit file that the layered loader did not pick up.
	if trimmed := strings.TrimSpace(configPath); trimmed != "" {
		if loaded, err := mcpconfig.NewLoader(trimmed).Load(); err == nil && loaded != nil {
			return applySelectionToConfig(loaded, selection)
		}
	}

	// Nothing on disk: start from an empty config so --remote still works.
	return &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{}}, nil, nil
}

// applySelectionToConfig narrows the base config by the profile's MCP selection.
func applySelectionToConfig(cfg *mcpconfig.Config, selection runtimeprofileinput.ResolvedMCPSelection) (*mcpconfig.Config, []string, error) {
	if cfg == nil {
		return &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{}}, nil, nil
	}
	if mcpSelectionKey(selection) == "" {
		return cfg, nil, nil
	}
	filtered, dropped := runtimeprofileinput.ApplyMCPSelection(cfg, selection)
	return filtered, dropped, nil
}

// emitRemoteFlagNotice reports what the flag contributed.
//
// Printed to stderr rather than stdout so it never corrupts --output json, and
// kept short because this is a routine notice, not a warning.
//
// It deliberately does NOT print a tool prefix. The prefix is decided by the
// TARGET's own config (`toolPrefix`), which the local side cannot know before the
// handshake; the synthesized `remote_<host>` value is only a fallback used when
// the remote server has none. Printing the local guess was actively misleading —
// a live run showed "remote_127-0-0-1__" while the tools really appeared as
// "remote_wsl__".
func emitRemoteFlagNotice(target *remoteFlagTarget, added, replaced bool) {
	if target == nil {
		return
	}
	verb := "added"
	if replaced {
		verb = "replaced a same-named entry in"
	}
	aicliDiagf(
		"remote: %s server %q (transport=%s, requested profile=%s); tool names come from the target's own toolPrefix\n",
		verb, target.ServerName, target.Config.Type, target.Depth)
	// Expansion warnings are part of the truthful notice: an http target cannot be
	// narrowed by `#depth`, and saying nothing would leave the flag looking like it
	// applied a restriction. Emitted here because this is the one place the user is
	// already reading about the target.
	emitRemoteTargetWarnings(target.Warnings)
}

// initMCPManagerWithRemoteFlag mirrors initMCPManagerWithSelectionFrom but lets
// the caller contribute an in-memory server on top of the file chain.
//
// It is a separate entry point rather than a parameter on the existing one
// because the process-global manager cache is keyed by (configPath, selection,
// baseDir): a `--remote` session must not reuse a cached manager that was built
// without the flag, and must not poison the cache for a later plain session.
func initMCPManagerWithRemoteFlag(configPath, explicitOverride, baseDir string, selection runtimeprofileinput.ResolvedMCPSelection, target *remoteFlagTarget, declaration *remoteTargetsDeclaration, async bool) error {
	configPath = strings.TrimSpace(configPath)
	baseDir = strings.TrimSpace(baseDir)

	// Always rebuild: the cached instance cannot know about this session's flag.
	if MCPManagerInstance != nil {
		_ = MCPManagerInstance.Stop()
		MCPManagerInstance = nil
		mcpManagerConfigPath = ""
		mcpManagerSelectionKey = ""
		mcpManagerLayerBase = ""
	}

	managerInstance := manager.NewManager()
	MCPManagerInstance = managerInstance
	// Deliberately leave mcpManagerConfigPath empty: this instance carries a
	// session-specific server and must never be reused from the cache.
	mcpManagerConfigPath = ""
	mcpManagerSelectionKey = ""
	mcpManagerLayerBase = ""

	if err := loadMCPConfigWithRemoteFlag(managerInstance, configPath, explicitOverride, baseDir, selection, target, declaration); err != nil {
		return err
	}
	if err := startMCPManager(managerInstance, context.Background(), async); err != nil {
		return fmt.Errorf("启动 MCP 失败: %w", err)
	}
	wireChatMCPToolSurfaceInvalidation(managerInstance)
	// The flag named a specific endpoint, so a connection failure is NOT a
	// "configured but unused server" situation — it is the whole point of the
	// command. Report it now, while the user still associates the run with the
	// flag, instead of letting the session silently proceed with no remote tools.
	if err := verifyRemoteFlagConnected(managerInstance, target); err != nil {
		return err
	}
	// A declaration-provided server carries the same promise: the user wrote the
	// target down on purpose, so an unreachable one must not be mistaken for a
	// configured-but-unused mcp.yaml entry.
	if err := verifyDeclaredTargetsConnected(managerInstance, declaration); err != nil {
		return err
	}
	return nil
}

// verifyDeclaredTargetsConnected fails fast when a target generated from the
// `remoteTargets` declaration cannot be reached.
//
// Only declared targets are checked. Ordinary mcp.yaml servers keep the lenient
// "warn and continue" behaviour: a shared config file lists servers a given session
// may never need, while a declaration is a deliberate statement about the targets
// this workspace works against.
func verifyDeclaredTargetsConnected(mgr manager.Manager, declaration *remoteTargetsDeclaration) error {
	if mgr == nil || declaration == nil {
		return nil
	}
	names := declarationServerNames(declaration)
	if len(names) == 0 {
		return nil
	}
	const probeBudget = 8 * time.Second
	deadline := time.Now().Add(probeBudget)
	pending := make(map[string]bool, len(names))
	for _, name := range names {
		pending[name] = true
	}
	for time.Now().Before(deadline) {
		for name := range pending {
			status, err := mgr.GetMCPStatus(name)
			if err != nil || status == nil {
				continue
			}
			if status.Connected {
				delete(pending, name)
				continue
			}
			if msg := strings.TrimSpace(status.LastError); msg != "" {
				return fmt.Errorf("remoteTargets 目标 %q 无法连接: %s", name, msg)
			}
		}
		if len(pending) == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Anything still pending gets one last look: a connect that landed just after
	// the deadline is a success, not a failure.
	unreachable := make([]string, 0, len(pending))
	for _, name := range names {
		if !pending[name] {
			continue
		}
		if status, err := mgr.GetMCPStatus(name); err == nil && status != nil && status.Connected {
			continue
		}
		unreachable = append(unreachable, name)
	}
	if len(unreachable) == 0 {
		return nil
	}
	return fmt.Errorf("remoteTargets 目标 %s 在 %s 内未建立连接；请检查地址、token 与目标侧是否在监听", strings.Join(unreachable, ", "), probeBudget)
}

// verifyRemoteFlagConnected waits briefly for the target's first connect and turns
// a failure into a startup error.
//
// Why this is a hard error rather than a warning:
//
// Before this check, `aicli chat --remote http://127.0.0.1:18765` against a closed
// port printed the "added server" notice, connected to nothing, and let the model
// answer normally. The user saw a perfectly healthy reply and had no way to know
// the remote surface was empty. A flag that names an endpoint must fail loudly when
// that endpoint is unreachable.
//
// The wait is bounded: async start is still async, and a slow-but-alive server must
// not block the UI. Whatever the probe learns within the budget is reported; a
// still-pending connect is left to the normal async path.
func verifyRemoteFlagConnected(mgr manager.Manager, target *remoteFlagTarget) error {
	if target == nil || mgr == nil {
		return nil
	}
	const probeBudget = 8 * time.Second
	deadline := time.Now().Add(probeBudget)
	for time.Now().Before(deadline) {
		status, err := mgr.GetMCPStatus(target.ServerName)
		if err == nil && status != nil {
			if status.Connected {
				return nil
			}
			if msg := strings.TrimSpace(status.LastError); msg != "" {
				return fmt.Errorf("--remote %s: cannot connect to the remote tool server: %s", target.ServerName, msg)
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	// Nothing conclusive within the budget: report what we know instead of
	// pretending success, and let the session continue (the server may still come
	// up; the tool list will populate on the turn boundary refresh).
	status, err := mgr.GetMCPStatus(target.ServerName)
	if err == nil && status != nil && status.Connected {
		return nil
	}
	detail := "connection did not complete"
	if err == nil && status != nil {
		if msg := strings.TrimSpace(status.LastError); msg != "" {
			detail = msg
		}
	}
	return fmt.Errorf("--remote %s: %s after %s; check the address, the token and that the target is listening", target.ServerName, detail, probeBudget)
}
