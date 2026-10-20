package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
	"github.com/wwsheng009/ai-agent-runtime/internal/plugins"
)

// hooksCommandOptions carries the shared inputs for `aicli hooks *`.
type hooksCommandOptions struct {
	Workspace  string
	ConfigFile string
}

// hooksTrustInfo explains whether project-scope files were allowed to load.
// Without it an untrusted workspace looks identical to an empty one.
type hooksTrustInfo struct {
	FeatureEnabled bool   `json:"feature_enabled"`
	Trusted        bool   `json:"trusted"`
	Source         string `json:"source,omitempty"`
}

type hooksListEntry struct {
	ID            string `json:"id"`
	Event         string `json:"event"`
	HandlerType   string `json:"handler_type"`
	ExecutionMode string `json:"execution_mode"`
	Matcher       string `json:"matcher,omitempty"`
	Command       string `json:"command,omitempty"`
	URL           string `json:"url,omitempty"`
	Server        string `json:"server,omitempty"`
	Tool          string `json:"tool,omitempty"`
	TimeoutSec    int    `json:"timeout_seconds,omitempty"`
	StatusMessage string `json:"status_message,omitempty"`
	Source        string `json:"source,omitempty"`
	SourcePath    string `json:"source_path,omitempty"`
	Enabled       bool   `json:"enabled"`
}

type hooksListResult struct {
	Workspace string           `json:"workspace"`
	Hooks     []hooksListEntry `json:"hooks"`
	Sources   []string         `json:"sources,omitempty"`
	Warnings  []string         `json:"warnings,omitempty"`
	Count     int              `json:"count"`
	Trust     hooksTrustInfo   `json:"project_trust"`
}

type hooksCheckResult struct {
	Workspace string         `json:"workspace"`
	OK        bool           `json:"ok"`
	Sources   []string       `json:"sources,omitempty"`
	Warnings  []string       `json:"warnings,omitempty"`
	Count     int            `json:"count"`
	Trust     hooksTrustInfo `json:"project_trust"`
}

type hooksTestResult struct {
	Event        string                 `json:"event"`
	Action       string                 `json:"action"`
	Message      string                 `json:"message,omitempty"`
	Matched      int                    `json:"matched"`
	MatchedHooks []string               `json:"matched_hooks,omitempty"`
	FailedHooks  []string               `json:"failed_hooks,omitempty"`
	ExtraContext map[string]string      `json:"extra_context,omitempty"`
	Patched      json.RawMessage        `json:"patched_payload,omitempty"`
	Warnings     []string               `json:"warnings,omitempty"`
	SourceCount  int                    `json:"source_count"`
	Error        string                 `json:"error,omitempty"`
	Payload      map[string]interface{} `json:"payload,omitempty"`
	DryRun       bool                   `json:"dry_run"`
	Trust        hooksTrustInfo         `json:"project_trust"`
}

// NewHooksCommand exposes read-only hooks diagnostics on the CLI.
//
// Before this command the only way to see why a hooks file did not load was the
// runtime-server control plane; CLI users had no feedback at all for a typo'd
// event name, a missing file, or an untrusted project scope.
func NewHooksCommand() *cobra.Command {
	opts := &hooksCommandOptions{}
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "查看 hooks 配置、诊断加载问题、试跑单个 hook",
		Long: `hooks 子命令用于本地诊断（不执行 Agent 回合）。

  aicli hooks list     列出当前生效的 handler（含来源与告警）
  aicli hooks check    只做加载检查：文件是否存在、能否解析、是否有告警
  aicli hooks test     用给定 payload 试跑匹配的 handler，打印决策

作用域与优先级：runtime.yaml -> managed -> user -> project(.aicli)
-> project(根目录) -> plugin。项目作用域受 folder trust 约束。`,
	}
	cmd.PersistentFlags().StringVar(&opts.Workspace, "workspace", "", "工作区根目录（默认当前目录）")
	cmd.PersistentFlags().StringVarP(&opts.ConfigFile, "config-file", "f", "", "runtime.yaml 路径（默认按 .aicli 层解析）")

	cmd.AddCommand(newHooksListCommand(opts))
	cmd.AddCommand(newHooksCheckCommand(opts))
	cmd.AddCommand(newHooksTestCommand(opts))
	return cmd
}

func newHooksListCommand(opts *hooksCommandOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "列出当前生效的 hooks handler",
		Example: `  aicli hooks list
  aicli hooks list --workspace ./my-repo
  aicli hooks list --output json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("hooks list", "text", err, nil)
			}
			executeStructuredCommand("hooks list", outputOptions,
				func() (hooksListResult, map[string]interface{}, error) {
					result := collectHooksList(opts)
					return result, map[string]interface{}{
						"workspace":  result.Workspace,
						"hook_count": result.Count,
					}, nil
				},
				nil,
				renderHooksList,
			)
		},
	}
	addStructuredOutputFlags(cmd)
	return cmd
}

func newHooksCheckCommand(opts *hooksCommandOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check",
		Short: "检查 hooks 配置能否正常加载",
		Long: `只做加载检查，不执行任何 handler。

有告警时以非零退出码结束，便于 CI 使用。`,
		Example: `  aicli hooks check
  aicli hooks check --output json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("hooks check", "text", err, nil)
			}
			result := collectHooksCheck(opts)
			if isJSONOutputFormat(outputOptions.Format) {
				printCommandJSONOutput("hooks check", outputOptions.Envelope, result)
			} else {
				renderHooksCheck(result)
			}
			if !result.OK {
				os.Exit(1)
			}
		},
	}
	addStructuredOutputFlags(cmd)
	return cmd
}

func newHooksTestCommand(opts *hooksCommandOptions) *cobra.Command {
	var (
		event       string
		payloadFile string
		payloadJSON string
		dryRun      bool
	)
	cmd := &cobra.Command{
		Use:   "test",
		Short: "用给定 payload 试跑匹配的 handler",
		Long: `在本地执行匹配到该事件的 handler，并打印合并后的决策。

这是真实执行：handler 会以同样方式启动进程/发 HTTP/调 MCP。
必须显式给 --event；payload 可通过 --payload-file 或 --payload 提供。

加 --dry-run 只做匹配与 payload 归一化，不执行任何 handler（不启动进程、
不发 HTTP、不调 MCP），适合先确认“到底哪些 handler 会命中”。

payload 里的 hook_event_name 会按 --event 自动补齐，无需手写。`,
		Example: `  aicli hooks test --event PreToolUse --payload "{\"tool_name\":\"shell\"}"
  aicli hooks test --event PreToolUse --payload "{\"tool_name\":\"shell\"}" --dry-run
  aicli hooks test --event PreToolUse --payload-file ./payload.json
  aicli hooks test --event SessionStart --output json`,
		Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
			if err != nil {
				exitCommandError("hooks test", "text", err, nil)
			}
			result, details, runErr := runHooksTest(opts, event, payloadFile, payloadJSON, dryRun)
			if runErr != nil {
				exitCommandError("hooks test", outputOptions.Format, runErr, details)
			}
			if isJSONOutputFormat(outputOptions.Format) {
				printCommandJSONOutput("hooks test", outputOptions.Envelope, result)
			} else {
				renderHooksTest(result)
			}
			// A handler that crashed is a diagnostic failure even when the
			// decision looks like a harmless "continue" (fail_open).
			if result.Error != "" {
				os.Exit(1)
			}
		},
	}
	cmd.Flags().StringVar(&event, "event", "", "要触发的事件名（PreToolUse / pre_tool_use 均可）")
	cmd.Flags().StringVar(&payloadFile, "payload-file", "", "payload JSON 文件路径")
	cmd.Flags().StringVar(&payloadJSON, "payload", "", "payload JSON 字符串")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "只匹配并规范化 payload，不执行 handler")
	_ = cmd.MarkFlagRequired("event")
	addStructuredOutputFlags(cmd)
	return cmd
}

// addStructuredOutputFlags mirrors the flag pair other aicli commands expose.
func addStructuredOutputFlags(cmd *cobra.Command) {
	cmd.Flags().String("output", "", "输出格式（text|json）")
	cmd.Flags().BoolP("json", "j", false, "以 JSON 格式输出")
}

// resolveHooksHostSources gathers the same inputs the hosts collect, then lets
// internal/hooks own ordering/precedence/managed-only. Duplicating that logic
// here would let the CLI disagree with the runtime about what is loaded.
func resolveHooksHostSources(opts *hooksCommandOptions) (runtimehooks.HostSources, hooksTrustInfo) {
	workspace := strings.TrimSpace(opts.Workspace)
	if workspace == "" {
		if wd, err := os.Getwd(); err == nil {
			workspace = wd
		}
	}
	if abs, err := filepath.Abs(workspace); err == nil {
		workspace = abs
	}

	var base []runtimehooks.HookConfig
	configFile := strings.TrimSpace(opts.ConfigFile)
	if configFile == "" {
		configFile = agentconfig.ResolveConfigPath(agentconfig.DefaultConfigSearchPaths())
	}
	if configFile != "" {
		manager := runtimecfg.NewRuntimeManager(configFile)
		if err := manager.Load(); err == nil {
			base = manager.Get().Hooks
		}
	}

	trust := resolveHooksWorkspaceTrust(workspace)
	trusted := trust.Trusted
	sources := runtimehooks.HostSources{
		Base:        base,
		ProjectRoot: workspace,
		SkipProject: !trusted,
	}
	if !runtimehooks.AllowManagedHooksOnlyFromEnv() {
		if catalog, err := plugins.Discover(plugins.DiscoverOptions{
			ProjectRoot:     workspace,
			SkipProjectRoot: !trusted,
			State:           plugins.NewStateStore(""),
		}); err == nil && catalog != nil {
			for _, file := range catalog.ActiveHooksFiles() {
				sources.PluginFiles = append(sources.PluginFiles, runtimehooks.PluginHookFile{
					Path:     file.Path,
					PluginID: file.PluginID,
				})
			}
			sources.LegacyPluginHooks = catalog.ActiveHooks()
		}
	}
	return sources, trust
}

// resolveHooksWorkspaceTrust resolves folder trust for the workspace being
// inspected, not for the process cwd.
//
// `--workspace` used to change only where hook files were looked up while the
// trust decision stayed tied to cwd. Pointing the command at another checkout
// therefore either skipped its project hooks (cwd untrusted) or loaded them
// (cwd trusted) — a diagnostic that disagreed with the host it was describing.
//
// Diagnostics never prompt: an unresolved workspace is reported as untrusted
// instead of blocking the command on stdin.
func resolveHooksWorkspaceTrust(workspace string) hooksTrustInfo {
	res := foldertrust.Resolve(foldertrust.ResolveOptions{
		CWD:         workspace,
		Interactive: boolPtr(false),
		SkipPrompt:  true,
	})
	return hooksTrustInfo{
		FeatureEnabled: res.FeatureEnabled,
		Trusted:        res.Trusted,
		Source:         res.Source,
	}
}

func boolPtr(value bool) *bool { return &value }

func collectHooksList(opts *hooksCommandOptions) hooksListResult {
	sources, trust := resolveHooksHostSources(opts)
	merged, discovered := runtimehooks.ResolveForHostFromEnv(sources)
	result := hooksListResult{
		Workspace: sources.ProjectRoot,
		Sources:   discovered.Sources,
		Warnings:  discovered.Warnings,
		Count:     len(merged),
		Trust:     trust,
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Event != merged[j].Event {
			return merged[i].Event < merged[j].Event
		}
		return merged[i].ID < merged[j].ID
	})
	for _, hook := range merged {
		mode := "sync"
		if hook.Async {
			mode = "async"
		}
		handlerType := strings.ToLower(strings.TrimSpace(hook.Exec.Type))
		if handlerType == "" {
			handlerType = "unknown"
		}
		result.Hooks = append(result.Hooks, hooksListEntry{
			ID:            hook.ID,
			Event:         string(hook.Event),
			HandlerType:   handlerType,
			ExecutionMode: mode,
			Matcher:       hook.Matcher,
			Command:       hook.Exec.Command,
			URL:           hook.Exec.URL,
			Server:        hook.Exec.Server,
			Tool:          hook.Exec.Tool,
			// Report the timeout that will really be applied, not the raw
			// field: an unset timeout means 10s (command) / 5s (http).
			TimeoutSec:    int(hook.EffectiveTimeout().Seconds()),
			StatusMessage: hook.StatusMessage,
			Source:        hook.Source,
			SourcePath:    hook.SourcePath,
			Enabled:       hook.Enabled == nil || *hook.Enabled,
		})
	}
	return result
}

func collectHooksCheck(opts *hooksCommandOptions) hooksCheckResult {
	list := collectHooksList(opts)
	return hooksCheckResult{
		Workspace: list.Workspace,
		OK:        len(list.Warnings) == 0,
		Sources:   list.Sources,
		Warnings:  list.Warnings,
		Count:     list.Count,
		Trust:     list.Trust,
	}
}

func runHooksTest(opts *hooksCommandOptions, event, payloadFile, payloadJSON string, dryRun bool) (hooksTestResult, map[string]interface{}, error) {
	parsedEvent, ok := runtimehooks.ParseEventName(event)
	if !ok {
		return hooksTestResult{}, map[string]interface{}{"event": event},
			fmt.Errorf("未知事件: %s（支持 Codex 12 类事件与 snake_case 内部事件）", event)
	}

	payload := map[string]interface{}{}
	if strings.TrimSpace(payloadFile) != "" {
		data, err := os.ReadFile(payloadFile)
		if err != nil {
			return hooksTestResult{}, nil, fmt.Errorf("读取 payload 文件失败: %w", err)
		}
		if err := json.Unmarshal(data, &payload); err != nil {
			return hooksTestResult{}, nil, fmt.Errorf("payload 文件不是合法 JSON 对象: %w", err)
		}
	} else if strings.TrimSpace(payloadJSON) != "" {
		if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			return hooksTestResult{}, nil, fmt.Errorf("payload 不是合法 JSON 对象: %w", err)
		}
	}

	sources, trust := resolveHooksHostSources(opts)
	merged, discovered := runtimehooks.ResolveForHostFromEnv(sources)
	manager := runtimehooks.NewManager(merged)

	matchedHooks := manager.MatchingHooks(parsedEvent, payload)
	matchedIDs := make([]string, 0, len(matchedHooks))
	for _, hook := range matchedHooks {
		matchedIDs = append(matchedIDs, hook.ID)
	}

	var (
		decision    runtimehooks.Decision
		dispatchErr error
		failedHooks []string
	)
	if dryRun {
		// Dry-run stops after matching: no process, no HTTP, no MCP call.
		decision = runtimehooks.Decision{Action: runtimehooks.DecisionContinue}
	} else {
		manager.SetMCPToolCaller(mcpToolCallerForHooks())
		// Dispatch folds handler failures into fail_open/fail_closed, so a
		// crashed or timed-out handler would otherwise be indistinguishable
		// from "nothing matched". The status observer keeps the failure visible.
		manager.SetStatusObserver(func(hook runtimehooks.HookConfig, phase string) {
			if phase == "failed" {
				failedHooks = append(failedHooks, hook.ID)
			}
		})
		decision, dispatchErr = manager.Dispatch(context.Background(), parsedEvent, payload)
	}
	result := hooksTestResult{
		Event:        string(parsedEvent),
		Action:       string(decision.Action),
		Message:      decision.Message,
		Matched:      len(matchedHooks),
		MatchedHooks: matchedIDs,
		FailedHooks:  failedHooks,
		Warnings:     discovered.Warnings,
		SourceCount:  len(discovered.Sources),
		ExtraContext: decision.ExtraContext,
		Payload:      runtimehooks.PrepareEventPayload(parsedEvent, payload),
		DryRun:       dryRun,
		Trust:        trust,
	}
	if len(decision.PatchedPayload) > 0 {
		result.Patched = decision.PatchedPayload
	}
	if dispatchErr != nil {
		result.Error = dispatchErr.Error()
	} else if len(failedHooks) > 0 {
		result.Error = fmt.Sprintf("%d 个 handler 执行失败: %s", len(failedHooks), strings.Join(failedHooks, ", "))
	}
	return result, map[string]interface{}{
		"event":   string(parsedEvent),
		"matched": result.Matched,
		"action":  result.Action,
	}, nil
}

// mcpToolCallerForHooks bridges mcp_tool handlers to the process MCP manager.
//
// `hooks test` is diagnostics and may run before any chat session initialized
// MCP, so the manager is started lazily here. When startup fails the caller gets
// a nil bridge: the MCP executor then reports "caller is not configured", which
// is the honest answer instead of silently returning "no decision".
func mcpToolCallerForHooks() runtimehooks.MCPToolCaller {
	if err := ensureMCPManager(); err != nil {
		fmt.Fprintf(os.Stderr, "警告: MCP 管理器未就绪（mcp_tool handler 将无法执行）: %v\n", err)
		return nil
	}
	if MCPManager == nil {
		return nil
	}
	return func(ctx context.Context, server, tool string, input map[string]interface{}) (interface{}, error) {
		return MCPManager.CallTool(ctx, server, tool, input)
	}
}

// renderHooksTrust prints one line describing whether project scope was allowed.
// "no files found" and "files skipped because the workspace is untrusted" look
// identical otherwise, and they need completely different fixes.
func renderHooksTrust(trust hooksTrustInfo) {
	switch {
	case !trust.FeatureEnabled:
		fmt.Println("project scope: enabled（folder trust 功能未开启）")
	case trust.Trusted:
		fmt.Printf("project scope: trusted（%s）\n", trust.Source)
	default:
		fmt.Printf("project scope: SKIPPED（未信任该工作区，source=%s）\n", trust.Source)
		fmt.Println("  提示：项目级 hooks 已整体跳过；在宿主里对该目录执行信任后重试。")
	}
}

func renderHooksList(result hooksListResult) {
	fmt.Printf("工作区: %s\n", result.Workspace)
	renderHooksTrust(result.Trust)
	if len(result.Sources) == 0 {
		if result.Trust.FeatureEnabled && !result.Trust.Trusted {
			// Do not claim the files are missing: they may exist and have been
			// skipped by the trust gate, which needs a different fix.
			fmt.Println("项目级 hooks 因未信任该工作区而未加载（这不代表文件不存在）。")
		} else {
			fmt.Println("未发现任何 hooks 文件。")
			fmt.Println("提示：项目级文件需放在 <workspace>/.aicli/hooks.json 或 hooks.json。")
		}
	} else {
		fmt.Printf("来源文件 (%d):\n", len(result.Sources))
		for _, source := range result.Sources {
			fmt.Printf("  - %s\n", source)
		}
	}
	fmt.Printf("handler (%d):\n", result.Count)
	for _, hook := range result.Hooks {
		fmt.Printf("  [%s] %s  %s/%s", hook.Event, hook.ID, hook.Source, hook.HandlerType)
		if hook.ExecutionMode == "async" {
			fmt.Print(" async")
		}
		if hook.Matcher != "" {
			fmt.Printf(" matcher=%q", hook.Matcher)
		}
		if !hook.Enabled {
			fmt.Print(" (disabled)")
		}
		fmt.Println()
		if hook.Command != "" {
			fmt.Printf("      command: %s\n", hook.Command)
		}
		if hook.URL != "" {
			fmt.Printf("      url: %s\n", hook.URL)
		}
		if hook.Server != "" {
			fmt.Printf("      mcp: %s/%s\n", hook.Server, hook.Tool)
		}
	}
	printHooksWarnings(result.Warnings)
}

func renderHooksCheck(result hooksCheckResult) {
	fmt.Printf("工作区: %s\n", result.Workspace)
	renderHooksTrust(result.Trust)
	fmt.Printf("来源文件: %d, handler: %d, 告警: %d\n", len(result.Sources), result.Count, len(result.Warnings))
	for _, source := range result.Sources {
		fmt.Printf("  ok  %s\n", source)
	}
	printHooksWarnings(result.Warnings)
	if result.OK {
		fmt.Println("\n检查通过。")
	}
}

func renderHooksTest(result hooksTestResult) {
	mode := ""
	if result.DryRun {
		mode = " dry-run=true（未执行 handler）"
	}
	fmt.Printf("event=%s matched=%d action=%s%s\n", result.Event, result.Matched, result.Action, mode)
	if result.DryRun {
		fmt.Println("action 为占位值 continue：dry-run 不做执行，因此没有真实决策。")
	}
	renderHooksTrust(result.Trust)
	if len(result.MatchedHooks) > 0 {
		fmt.Printf("matched hooks: %s\n", strings.Join(result.MatchedHooks, ", "))
	}
	if result.Message != "" {
		fmt.Printf("message: %s\n", result.Message)
	}
	if text := strings.TrimSpace(result.ExtraContext["additional_context"]); text != "" {
		fmt.Printf("additional_context: %s\n", text)
	}
	if len(result.Patched) > 0 {
		fmt.Printf("patched_payload: %s\n", string(result.Patched))
	}
	if result.Error != "" {
		fmt.Fprintf(os.Stderr, "dispatch error: %s\n", result.Error)
	}
	printHooksWarnings(result.Warnings)
}

func printHooksWarnings(warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Printf("\n告警 (%d):\n", len(warnings))
	for _, warning := range warnings {
		fmt.Printf("  ! %s\n", warning)
	}
}
