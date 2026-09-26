package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 会话级 Agent 路由的 TUI 命令面（方案 §5.2/§5.3.1/§5.4）。
//
// 渲染形态（v3.1）：仅「打开面板」一族命令走独立全屏；`show`/`doctor`/写入类
// 子命令一律行内文本。本文件实现行内文本一族 + 三层写入（session / 会话绑定
// workspace / config，§5.5：workspace 路径以会话绑定为准，不随 cwd 漂移；config
// 层需 --yes 二次确认）；全屏面板（Level → 字段 → 值三级导航，§5.3）见
// chat_routing_panel.go，Tab 逐段补全（I-9）见 chat_routing_completion.go。
//
// 单一投影纪律（§6.1）：本文件的展示字段一律来自 chatRoutingStatusProjection /
// chatRoutingLevelSummaries，禁止自行拼装。

const (
	chatRoutingScopeMain = "main"
	chatRoutingScopeSub  = "sub"

	chatRoutingLayerSession   = "session"
	chatRoutingLayerWorkspace = "workspace"
	chatRoutingLayerConfig    = "config"

	// chatRoutingChildSessionReadOnlyNote 是子会话（非主会话）的唯一拒绝/提示文案
	// （§5.6/I-8/INV-A3，M16）：写入不生效就必须明说，不能「写成功但不生效」。
	chatRoutingChildSessionReadOnlyNote = "子会话只读：子 Agent 路由由父会话在 spawn 时解析，本会话的写入不会生效（M16/INV-A3）；请在父会话用 /routing sub 修改"
)

// chatRoutingSessionIsChildAgent 判定当前会话是否为子 Agent 会话（M16/INV-A3）。
// 判据与宿主接线同源（chat_actor_host.go:1379-1403 与
// internal/api/runtimeapi/session_runtime_support.go:3938-3942 的 child 标记）：
// agent_type / depth>0 / read_only 任一命中即为子会话——这类会话可能被
// `aicli chat --session <id>`、`/load`、`/resume` 或 ACP 载入到 TUI。
func chatRoutingSessionIsChildAgent(session *ChatSession) bool {
	if session == nil || session.RuntimeSession == nil {
		return false
	}
	runtimeSession := session.RuntimeSession
	if agentType := strings.TrimSpace(agentcontrol.ContextString(runtimeSession, toolbroker.AgentSessionContextAgentType)); agentType != "" {
		return true
	}
	if localAgentSessionDepth(runtimeSession) > 0 {
		return true
	}
	if readOnly, ok := runtimeSessionContextBool(runtimeSession, toolbroker.AgentSessionContextReadOnly); ok && readOnly {
		return true
	}
	return false
}

// tryExecuteStructuredRoutingCommand 是 `/routing` 的结构化命令入口（§5.2）。
func tryExecuteStructuredRoutingCommand(session *ChatSession, command string) (CommandResult, bool) {
	// §8.3 紧急开关（U-6）：仅隐藏 UI 入口；解析器、宿主接线与 API 保持生效。
	if chatRoutingUIDisabled() {
		return commandTextResult("路由 UI 已被 AICLI_DISABLE_ROUTING_UI 关闭（仅隐藏 UI 入口；解析器与 /api/runtime/sessions/{id}/routing API 仍生效）"), true
	}
	arg := strings.TrimSpace(extractCommandArgument(command))
	tokens := strings.Fields(arg)

	// 取出 --json / --to <layer> / --yes 三个修饰参数，其余按位置解析。
	jsonOut := false
	confirm := false
	layer := chatRoutingLayerSession
	positional := make([]string, 0, len(tokens))
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		switch {
		case token == "--json":
			jsonOut = true
		case token == "--yes" || token == "-y":
			confirm = true
		case token == "--to":
			if index+1 >= len(tokens) {
				return commandTextResult("错误: --to 需要一个层名（session|workspace|config）"), true
			}
			index++
			layer = strings.ToLower(strings.TrimSpace(tokens[index]))
		case strings.HasPrefix(token, "--to="):
			layer = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(token, "--to=")))
		default:
			positional = append(positional, token)
		}
	}
	switch layer {
	case chatRoutingLayerSession, chatRoutingLayerWorkspace, chatRoutingLayerConfig:
	default:
		return commandTextResult("错误: 未知写入层 " + layer + "（可用 session|workspace|config）"), true
	}

	if len(positional) == 0 {
		return commandTextResult(chatRoutingPanelEntry(session, chatRoutingScopeMain, "", "")), true
	}

	switch strings.ToLower(positional[0]) {
	case "show":
		scope, err := chatRoutingScopeFromArgs(positional[1:])
		if err != nil {
			return commandTextResult("错误: " + err.Error()), true
		}
		text, err := chatRoutingShowText(session, scope, jsonOut)
		if err != nil {
			return commandTextResult("错误: " + err.Error()), true
		}
		return commandTextResult(text), true
	case "doctor":
		scope, err := chatRoutingScopeFromArgs(positional[1:])
		if err != nil {
			return commandTextResult("错误: " + err.Error()), true
		}
		return commandTextResult(chatRoutingDoctorText(session, scope)), true
	case "on", "off":
		scope, err := chatRoutingScopeFromArgs(positional[1:])
		if err != nil {
			return commandTextResult("错误: " + err.Error()), true
		}
		text, err := chatRoutingWriteKey(session, scope, "enabled", strconv.FormatBool(positional[0] == "on"), layer, confirm)
		if err != nil {
			return commandTextResult("错误: " + err.Error()), true
		}
		return commandTextResult(text), true
	case "reset":
		return commandTextResult(chatRoutingResetFromArgs(session, positional[1:], layer, confirm)), true
	case "save":
		return commandTextResult(chatRoutingSaveFromArgs(session, positional[1:], layer, confirm)), true
	case "main", "sub":
		scope := strings.ToLower(positional[0])
		rest := positional[1:]
		switch {
		case len(rest) == 0:
			return commandTextResult(chatRoutingPanelEntry(session, scope, "", "")), true
		case strings.EqualFold(rest[0], "level"):
			if len(rest) < 4 {
				return commandTextResult("错误: 用法 /routing " + scope + " level <level> <field> <value>"), true
			}
			key := chatRoutingLevelKeyPrefix(scope) + rest[1] + "." + rest[2]
			text, err := chatRoutingWriteKey(session, scope, key, strings.Join(rest[3:], " "), layer, confirm)
			if err != nil {
				return commandTextResult("错误: " + err.Error()), true
			}
			return commandTextResult(text), true
		case len(rest) == 1:
			// 直达入口（§5.3）：`<level>` 打开该级字段列表；`profiles.<level>.<field>`
			// / `levels.<level>.<field>` 键路径直达值选择器。
			return commandTextResult(chatRoutingPanelEntry(session, scope, rest[0], "")), true
		default:
			text, err := chatRoutingWriteKey(session, scope, rest[0], strings.Join(rest[1:], " "), layer, confirm)
			if err != nil {
				return commandTextResult("错误: " + err.Error()), true
			}
			return commandTextResult(text), true
		}
	default:
		return commandTextResult(chatRoutingUsageText()), true
	}
}

// chatRoutingCommandText 提取 `/routing` 的纯文本结果（legacy/JSON 会话入口共用）。
func chatRoutingCommandText(session *ChatSession, command string) (string, bool) {
	result, handled := tryExecuteStructuredRoutingCommand(session, command)
	if !handled {
		return "", false
	}
	lines := make([]string, 0, len(result.Blocks))
	for _, block := range result.Blocks {
		if text := strings.TrimSpace(block.Document.PlainText()); text != "" {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, "\n"), true
}

// handleChatRoutingCommand 是 legacy / JSON 输出会话的 `/routing` 入口（§5.2）：
// 复用结构化实现，把纯文本结果落到普通命令输出。
func handleChatRoutingCommand(session *ChatSession, command string) {
	text, handled := chatRoutingCommandText(session, command)
	if !handled || strings.TrimSpace(text) == "" {
		return
	}
	printChatCommandOutput(session, text)
}

// chatRoutingSubcommandQueueSafe 判定 `/routing` 子命令在 Agent 忙碌时能否排队
// （§5.2/§5.3.1）：只读与「打开面板」一族可排队（面板本身受 surface 就绪检查
// 约束，忙时退化为只读摘要）；写入类忙时拒绝，避免与进行中 turn 的路由快照交错。
func chatRoutingSubcommandQueueSafe(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(args[0])) {
	case "show", "doctor":
		return true
	case "main", "sub":
		// 打开面板一族：`main|sub`、`main|sub <level>`、`main|sub <key-path>`。
		return len(args[1:]) <= 1
	default:
		return false
	}
}

func chatRoutingScopeFromArgs(args []string) (string, error) {
	if len(args) == 0 {
		return chatRoutingScopeMain, nil
	}
	scope := strings.ToLower(strings.TrimSpace(args[0]))
	switch scope {
	case chatRoutingScopeMain, chatRoutingScopeSub:
		return scope, nil
	default:
		return "", fmt.Errorf("未知作用域 %q（可用 main|sub）", args[0])
	}
}

func chatRoutingLevelKeyPrefix(scope string) string {
	if scope == chatRoutingScopeSub {
		return "levels."
	}
	return "profiles."
}

func chatRoutingNormalizeScope(scope string) string {
	if strings.EqualFold(strings.TrimSpace(scope), chatRoutingScopeSub) {
		return chatRoutingScopeSub
	}
	return chatRoutingScopeMain
}

// chatRoutingProjectionForScope 按作用域取同一投影（§6.1）。
func chatRoutingProjectionForScope(session *ChatSession, scope string) agentconfig.RoutingStatusProjection {
	res := chatRoutingResolutionForSession(session)
	revision := chatRoutingRevision(session)
	if chatRoutingNormalizeScope(scope) == chatRoutingScopeSub {
		return agentconfig.ProjectSubAgentRoutingStatus(res, "", revision)
	}
	return agentconfig.ProjectRoutingStatus(res, "", revision)
}

// chatRoutingShowText 是 `/routing show [main|sub] [--json]` 的文本/JSON 投影（§6.3）。
func chatRoutingShowText(session *ChatSession, scope string, jsonOut bool) (string, error) {
	scope = chatRoutingNormalizeScope(scope)
	projection := chatRoutingProjectionForScope(session, scope)
	levels := chatRoutingLevelSummaries(session, scope)

	if jsonOut {
		payload := struct {
			Scope   string                              `json:"scope"`
			Routing agentconfig.RoutingStatusProjection `json:"routing"`
			Levels  []agentconfig.RoutingLevelSummary   `json:"levels,omitempty"`
		}{Scope: scope, Routing: projection, Levels: levels}
		raw, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}

	lines := make([]string, 0, len(levels)+6)
	if projection.Enabled {
		lines = append(lines, fmt.Sprintf("路由（%s）· 启用 · 下一 turn 生效", scope))
	} else {
		lines = append(lines, fmt.Sprintf("路由（%s）· 关闭（route:off）· 下一 turn 生效", scope))
	}
	if baseline := chatRoutingBaselineLine(session); baseline != "" {
		lines = append(lines, "  "+baseline)
	}
	if len(levels) == 0 {
		lines = append(lines, "  （未配置档位）")
	}
	for _, row := range levels {
		lines = append(lines, chatRoutingLevelRowText(row))
	}
	if scope == chatRoutingScopeSub {
		for _, note := range chatRoutingSubReadOnlyNotes(session) {
			lines = append(lines, "  "+note)
		}
	}
	if projection.Enabled {
		current := "当前档位: " + projection.Level
		if projection.Model != "" {
			current += " → " + projection.Model
		}
		if projection.Reasoning != "" {
			current += " · " + projection.Reasoning
		}
		if projection.Source != "" {
			current += " (" + projection.Source + chatRoutingSourceSuffix(projection.Source) + ")"
		}
		lines = append(lines, current)
	}
	if revision := strings.TrimSpace(projection.Revision); revision != "" {
		lines = append(lines, "revision: "+revision)
	}
	for _, warning := range projection.Warnings {
		lines = append(lines, "warning: "+warning)
	}
	return strings.Join(lines, "\n"), nil
}

// chatRoutingDoctorText 是 `/routing doctor [main|sub]` 的诊断视图（§5.2/M14）：
// 逐字段来源 + warnings + 回退阶梯记录，自建渲染，不复用 /debug routing。
func chatRoutingDoctorText(session *ChatSession, scope string) string {
	scope = chatRoutingNormalizeScope(scope)
	res := chatRoutingResolutionForSession(session)
	projection := chatRoutingProjectionForScope(session, scope)

	lines := []string{fmt.Sprintf("路由诊断（%s）", scope)}
	lines = append(lines, fmt.Sprintf("  enabled=%t level=%q model=%q effort=%q source=%s effective_from=%s",
		projection.Enabled, projection.Level, projection.Model, projection.Reasoning, projection.Source, projection.EffectiveFrom))

	sources := res.Sources
	prefix := "main_agent."
	if scope == chatRoutingScopeSub {
		sources = res.SubSources
		prefix = "sub_agent."
	}
	keys := make([]string, 0, len(sources))
	for key := range sources {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		lines = append(lines, "  来源: （无显式字段，全部继承）")
	}
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("  来源: %s = %s", strings.TrimPrefix(key, prefix), sources[key]))
	}
	if len(projection.Warnings) == 0 {
		lines = append(lines, "  回退阶梯: （无回退）")
	}
	for _, warning := range projection.Warnings {
		lines = append(lines, "  回退: "+warning)
	}
	if revision := strings.TrimSpace(projection.Revision); revision != "" {
		lines = append(lines, "  revision: "+revision)
	}
	if scope == chatRoutingScopeSub {
		for _, note := range chatRoutingSubReadOnlyNotes(session) {
			lines = append(lines, "  "+note)
		}
	}
	lines = append(lines, "  提示: 写入后下一 turn 生效；进行中的 turn 保持原 actor（§4.5）")
	return strings.Join(lines, "\n")
}

func chatRoutingBaselineLine(session *ChatSession) string {
	if session == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if model := strings.TrimSpace(session.Model); model != "" {
		parts = append(parts, model)
	}
	if provider := strings.TrimSpace(session.ProviderName); provider != "" {
		parts = append(parts, provider)
	}
	if effort := strings.TrimSpace(session.ReasoningEffort); effort != "" {
		parts = append(parts, effort)
	}
	if len(parts) == 0 {
		return ""
	}
	return "baseline: " + strings.Join(parts, " · ")
}

func chatRoutingLevelRowText(row agentconfig.RoutingLevelSummary) string {
	model := strings.TrimSpace(row.Model)
	if model == "" {
		model = "—（未配置，继承 baseline）"
	}
	cells := []string{row.Level, model}
	if provider := strings.TrimSpace(row.Provider); provider != "" {
		cells = append(cells, provider)
	}
	if effort := strings.TrimSpace(row.Reasoning); effort != "" {
		cells = append(cells, effort)
	}
	line := "  " + strings.Join(cells, " · ")
	if row.Expensive {
		line += " [expensive]"
	}
	if source := strings.TrimSpace(row.Source); source != "" {
		line += " (" + source + chatRoutingSourceSuffix(source) + ")"
	}
	return line
}

// chatRoutingPanelEntryText 是面板入口无法使用全屏时的退化输出（§5.3.1 边界）：
// 管道/JSON 会话、备用屏被占用或 turn 运行中给出只读摘要 + 行内操作提示。
func chatRoutingPanelEntryText(session *ChatSession, scope, level string) string {
	scope = chatRoutingNormalizeScope(scope)
	text, err := chatRoutingShowText(session, scope, false)
	if err != nil {
		return "错误: " + err.Error()
	}
	if strings.TrimSpace(level) != "" {
		text += "\n（当前会话/终端无法开启全屏面板，显示只读摘要；面板可直达 " + level + " 级）"
	} else {
		text += "\n（当前会话/终端无法开启全屏面板，显示只读摘要；用 /routing show 或 /routing main <key> <value> 操作）"
	}
	return text
}

func chatRoutingUsageText() string {
	return strings.Join([]string{
		"用法:",
		"  /routing show [main|sub] [--json]",
		"  /routing doctor [main|sub]",
		"  /routing on|off [main|sub] [--to session|workspace|config] [--yes]",
		"  /routing main|sub <key> <value> [--to session|workspace|config] [--yes]",
		"  /routing main|sub level <level> <field> <value> [--to session|workspace|config] [--yes]",
		"  /routing reset [main|sub] [<level>] [--to session|workspace|config] [--yes]",
		"  /routing save [session|workspace|config] [--yes]",
		"键空间: enabled | levels | default_difficulty | profiles.<level>.<field>（main）",
		"        enabled | default_difficulty | levels.<level>.<field>（sub）",
		"写入层: session（默认，仅本会话）| workspace（会话绑定工作区偏好，N9）| config（全局，影响所有会话，需 --yes）",
		"提示: 写入后下一 turn 生效；/routing doctor 查看逐字段来源与回退",
	}, "\n")
}

// chatRoutingWriteKey 是写入类子命令的统一入口（§5.2/§5.4）：
// 解析 → 校验（§3.5）→ 落盘 → 失效（§4.5）→ 状态栏刷新（§6）。
// layer=workspace/config 走 chat_routing_layers.go 的同源写入路径（I-2），
// config 层需 --yes 二次确认。
func chatRoutingWriteKey(session *ChatSession, scope, key, value string, layer string, confirm bool) (string, error) {
	if session == nil || session.RuntimeSession == nil {
		return "", fmt.Errorf("当前会话不可写路由覆盖")
	}
	// M16/INV-A3（§5.6）：子会话写入一律拒绝——宿主接线只把主 Agent 路由接主会话
	// （chat_actor_host.go 的 isBaseSession 分支），这里的写入不会生效；workspace/
	// config 层更会产生真实副作用，必须挡在落盘之前。
	if chatRoutingSessionIsChildAgent(session) {
		return "", fmt.Errorf("%s", chatRoutingChildSessionReadOnlyNote)
	}
	scope = chatRoutingNormalizeScope(scope)
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return "", fmt.Errorf("缺少键名")
	}
	switch layer {
	case chatRoutingLayerSession:
	case chatRoutingLayerWorkspace:
		text, err := chatRoutingWriteWorkspaceLayer(session, scope, key, value)
		return chatRoutingWithRuntimeRefresh(session, text, err)
	case chatRoutingLayerConfig:
		text, err := chatRoutingWriteConfigLayer(session, scope, key, value, confirm)
		return chatRoutingWithRuntimeRefresh(session, text, err)
	default:
		return "", fmt.Errorf("未知写入层 %s（可用 session|workspace|config）", layer)
	}

	// §3.4/M11：会话级「读-改-写」与 API handler（PATCH /routing）共用同一把
	// 进程内会话锁，避免两个写入端各自基于旧快照落盘（后写覆盖前者字段）。
	if err := chatRoutingWithSessionWriteLock(session, func() error {
		current := chatSessionRoutingOverride(session)
		candidate := chatRoutingCloneOverride(current)
		if err := chatRoutingApplyKey(candidate, scope, key, value); err != nil {
			return chatRoutingFieldEcho(key, err)
		}
		if err := chatRoutingValidateCandidate(session, candidate, scope, key); err != nil {
			return err
		}
		return chatRoutingCommitOverride(session, candidate)
	}); err != nil {
		return "", err
	}
	projection := chatRoutingProjectionForScope(session, scope)
	text := fmt.Sprintf("已写入会话路由覆盖: %s.%s = %s（下一 turn 生效）", scope, key, value)
	if projection.Enabled {
		text += fmt.Sprintf("\n当前: %s", chatRoutingCompactSummary(projection))
	}
	return chatRoutingWithRuntimeRefresh(session, text, nil)
}

// chatRoutingWithRuntimeRefresh 让路由写入在「下一 turn」真正生效（§4.5/M6）：
// 主 Agent 路由在 actor 构建期被克隆进 loop 配置（`internal/agent/loop.go` 的
// cloneMainAgentRoutingConfig），旧 actor 不会感知新配置；复用 /model /provider
// /reasoning 一族的重建路径（停 actor + 重新预热）才能让新配置进入下一次 run。
// 失败只追加提示、不回滚已落盘的写入；无本地 runtime host 时静默 no-op（测试与
// 结构化调用不因此产生副作用）。
func chatRoutingWithRuntimeRefresh(session *ChatSession, text string, err error) (string, error) {
	if err != nil {
		return text, err
	}
	return chatRoutingWithRuntimeRefreshText(session, text), nil
}

// chatRoutingWithRuntimeRefreshText 是 reset/save 一族（只返回文本）的同款包装。
func chatRoutingWithRuntimeRefreshText(session *ChatSession, text string) string {
	if warning := chatRoutingRefreshRuntimeAfterWrite(session); warning != "" {
		text += "\n" + warning
	}
	return text
}

// chatRoutingRefreshRuntimeAfterWrite 复用模型切换的本地 runtime 刷新路径
// （`refreshLocalRuntimeAfterModelSelection`）：停掉缓存 actor、重载 provider
// 配置并重新预热，使下一次 turn 用新路由配置构建 loop。返回非空即警告文案。
func chatRoutingRefreshRuntimeAfterWrite(session *ChatSession) string {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return ""
	}
	if err := refreshLocalRuntimeAfterModelSelection(session); err != nil {
		return "警告: 路由写入已落盘，但刷新本地 runtime 失败（旧 actor 可能仍用旧配置）: " + err.Error()
	}
	return ""
}

func chatRoutingCompactSummary(projection agentconfig.RoutingStatusProjection) string {
	parts := []string{"route:" + projection.Level}
	if model := strings.TrimSpace(projection.Model); model != "" {
		parts = append(parts, model)
	}
	if effort := strings.TrimSpace(projection.Reasoning); effort != "" {
		parts = append(parts, effort)
	}
	if source := strings.TrimSpace(projection.Source); source != "" {
		parts = append(parts, source+chatRoutingSourceSuffix(source))
	}
	return strings.Join(parts, " · ")
}

// chatRoutingValidateCandidate 按 §3.5.2 校验候选覆盖：
// 解析器合并后仍非法（或本次写入的字段被阶梯回退丢弃）则拒绝，不落盘（U-3 同口径）。
func chatRoutingValidateCandidate(session *ChatSession, candidate *agentconfig.AICLISessionRoutingOverride, scope, key string) error {
	workspace := chatRoutingWorkspacePreferences(session)
	if scope == chatRoutingScopeSub {
		res := agentconfig.ResolveSubagentRouting(session.Config, candidate, workspace, nil)
		if res.EffectiveSub == nil {
			// §3.5.2 阶梯回退可能把 enabled 一并丢回下层（写入前是开启的）：此时真正的
			// 拒绝原因是该字段让合并结果非法，按字段回显 + 建议动作（I-4）。
			if chatRoutingProjectionForScope(session, scope).Enabled {
				if reason := chatRoutingRollbackReason(res.Warnings, scope, key); reason != "" {
					return chatRoutingWithValidationSuggestion(scope, key, errors.New(reason))
				}
			}
			return fmt.Errorf("子 Agent 路由未启用（先用 /routing on sub）")
		}
		// 子 Agent 的 `levels.<level>.<field>` 本身就是新增档位的方式，无档位成员检查。
		if _, err := agentconfig.ValidateSubagentRoutingConfig("aicli.subagents.routing", res.EffectiveSub); err != nil {
			return chatRoutingWithValidationSuggestion(scope, key, err)
		}
		if reason := chatRoutingDroppedFieldReason(res.Warnings, "sub_agent."+key, key); reason != "" {
			return fmt.Errorf("字段 %s 被回退丢弃: %s", key, reason)
		}
		return nil
	}
	res := agentconfig.ResolveMainAgentRouting(session.Config, candidate, workspace, nil)
	if res.Effective == nil || !res.Effective.Enabled {
		// 同上：回退阶梯把 enabled 丢回下层时，不误报「未启用」，而是按字段回显真实原因。
		// 档位成员判定用「写入前」的生效 levels（与面板/`/routing show` 同源；本命令不改
		// levels，故与候选解析结果一致），避免回退后 levels 视图失真造成误判。
		if chatRoutingProjectionForScope(session, scope).Enabled {
			if note := chatRoutingLevelMembershipNote(agentconfig.BuildRoutingLevelSummaries(chatRoutingResolutionForSession(session), scope), scope, key); note != "" {
				return fmt.Errorf("字段 %s 未写入: %s", key, note)
			}
			if reason := chatRoutingRollbackReason(res.Warnings, scope, key); reason != "" {
				return chatRoutingWithValidationSuggestion(scope, key, errors.New(reason))
			}
		}
		return fmt.Errorf("主 Agent 路由未启用（先用 /routing on）")
	}
	if note := chatRoutingLevelMembershipNote(agentconfig.BuildRoutingLevelSummaries(res, scope), scope, key); note != "" {
		return fmt.Errorf("字段 %s 未写入: %s", key, note)
	}
	if _, err := agentconfig.ValidateMainAgentRoutingConfig(res.Effective); err != nil {
		return chatRoutingWithValidationSuggestion(scope, key, err)
	}
	if reason := chatRoutingDroppedFieldReason(res.Warnings, "main_agent."+key, key); reason != "" {
		return fmt.Errorf("字段 %s 被回退丢弃: %s", key, reason)
	}
	return nil
}

// chatRoutingDroppedFieldReason 在 warnings 中查找本次写入字段的回退记录。
func chatRoutingDroppedFieldReason(warnings []agentconfig.RoutingWarning, keys ...string) string {
	for _, warning := range warnings {
		field := strings.TrimSpace(warning.Field)
		for _, key := range keys {
			if field == "" || key == "" || !strings.EqualFold(field, key) {
				continue
			}
			reason := strings.TrimSpace(warning.Reason)
			if reason == "" {
				reason = "被下层覆盖值回退"
			}
			return reason
		}
	}
	return ""
}

// chatRoutingLevelMembershipNote 判定「档位不在生效 levels 中」的无效写入（§10.2 I-4）。
// 主 Agent 的 `profiles.<level>.<field>` 只有在生效 levels 含该档位时才参与解析：
// `agentconfig.ValidateMainAgentRoutingConfig` 会以英文原文拒绝，这里提前给出
// 「按字段回显 + 建议动作」的中文拒绝文案，避免用户面对 `profiles.expert is not
// listed in levels` 这类不带字段与下一步的报错。子 Agent 的 `levels.<level>.<field>`
// 本身就是新增档位的方式，不做此检查。
// 判定依据是**解析后的生效 levels**（与面板/`/routing show` 同源；回退分支传入
// 「写入前」的投影，因为本命令只写一个字段、levels 不会随之变化），因此配置层已含
// 该档位时会放行，不产生误拒。
func chatRoutingLevelMembershipNote(rows []agentconfig.RoutingLevelSummary, scope, key string) string {
	if scope != chatRoutingScopeMain {
		return ""
	}
	level, _, ok := chatRoutingSplitLevelKey(key, chatRoutingLevelKeyPrefix(scope))
	if !ok {
		return ""
	}
	available := make([]string, 0, len(rows))
	for _, row := range rows {
		name := strings.TrimSpace(row.Level)
		if name == "" {
			continue
		}
		if strings.EqualFold(name, level) {
			return ""
		}
		available = append(available, name)
	}
	next := append(append([]string{}, available...), level)
	suggestion := fmt.Sprintf("/routing %s levels %s", scope, strings.Join(next, ","))
	if strings.EqualFold(level, "expert") {
		suggestion += fmt.Sprintf(" 且 /routing %s allow_expert on", scope)
	}
	current := strings.Join(available, ",")
	if current == "" {
		current = "（空）"
	}
	return fmt.Sprintf("档位 %s 不在生效 levels 中（当前 levels: %s）；先用 %s 使该字段生效", level, current, suggestion)
}

// chatRoutingFieldEcho 保证 §10.2 I-4 的「按字段回显」：写入路径上的错误若尚未带上
// 用户所写的字段名，则补 `字段 <key> 未写入: ` 前缀（如 `需要整数: "abc"`）；已含
// 字段名的错误（如「未知键 %q；可用键: …」）保持原文，避免重复回显。仍以 %w 包装，
// 不改变错误判定与拒绝语义。
func chatRoutingFieldEcho(key string, err error) error {
	if err == nil {
		return nil
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.Contains(err.Error(), key) {
		return err
	}
	return fmt.Errorf("字段 %s 未写入: %w", key, err)
}

// chatRoutingRollbackReason 在候选解析被 §3.5.2 回退阶梯打回（enabled 被丢回下层）时
// 还原真实原因：优先本字段自己的回退记录（解析器逐轮回退都会写 {字段, 原因, 最终来源}）；
// 否则取首条能映射到建议模板的原因——例如 enabled 因
// `max_consecutive_expensive_steps must be a finite value` 被丢弃时，原因即该硬校验原文。
// 只读取解析器产出，不另建判定规则。
func chatRoutingRollbackReason(warnings []agentconfig.RoutingWarning, scope, key string) string {
	if reason := chatRoutingDroppedFieldReason(warnings, chatRoutingScopeFieldPrefix(scope)+key, key); reason != "" {
		return reason
	}
	for _, warning := range warnings {
		if !strings.HasPrefix(strings.TrimSpace(warning.Field), chatRoutingScopeFieldPrefix(scope)) {
			continue
		}
		reason := strings.TrimSpace(warning.Reason)
		if reason == "" || chatRoutingValidationSuggestion(scope, reason) == "" {
			continue
		}
		return reason
	}
	return ""
}

// chatRoutingScopeFieldPrefix 返回解析器 warning 字段的层前缀（§3.5.1 键名表）。
func chatRoutingScopeFieldPrefix(scope string) string {
	if chatRoutingNormalizeScope(scope) == chatRoutingScopeSub {
		return "sub_agent."
	}
	return "main_agent."
}

// chatRoutingWithValidationSuggestion 为 §3.5.2 校验失败补「按字段回显 + 建议动作」
// （§10.2 I-4）。只追加文案，不改变校验结论与拒绝语义（§3.5 同源）：错误仍以 %w
// 包装，errors.Is/errors.As 判定不受影响。
func chatRoutingWithValidationSuggestion(scope, key string, err error) error {
	if err == nil {
		return nil
	}
	suggestion := chatRoutingValidationSuggestion(scope, err.Error())
	if suggestion == "" {
		return fmt.Errorf("字段 %s 未写入: %w", key, err)
	}
	return fmt.Errorf("字段 %s 未写入: %w；建议: %s", key, err, suggestion)
}

// chatRoutingValidationSuggestion 按校验错误文案给出下一步命令（§3.5.3 建议模板）。
// 未命中已知文案时返回空串，保持原始报错。
func chatRoutingValidationSuggestion(scope, message string) string {
	scope = chatRoutingNormalizeScope(scope)
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "max_consecutive_expensive_steps must be a finite value"):
		return fmt.Sprintf("/routing %s max_consecutive_expensive_steps <N>（>0），再 /routing %s allow_expert on", scope, scope)
	case strings.Contains(lower, "allow_expert"):
		return fmt.Sprintf("/routing %s allow_expert on（或从 levels 移除 expert）", scope)
	case strings.Contains(lower, "not listed in levels"):
		return fmt.Sprintf("先用 /routing %s levels <level,...> 把该档位纳入 levels", scope)
	case strings.Contains(lower, "max_tokens cannot be negative"), strings.Contains(lower, "timeout cannot be negative"):
		// 数值档位字段越界时解析器会把整层 enabled 丢回下层，警告里只有硬校验原文；
		// 这条模板既让 chatRoutingRollbackReason 认领该原因（不再误报「未启用」），
		// 也让面板/命令的拒绝文案带上字段与下一步（§10.2 I-4）。
		return "把该字段改为非负值（>=0），例如 /routing " + scope + " level <level> <field> <value>"
	case strings.Contains(lower, "must list the allowed difficulties"):
		return fmt.Sprintf("/routing %s levels easy,normal,hard", scope)
	case strings.Contains(lower, "levels entry"), strings.Contains(lower, "profiles key"):
		return "levels/profiles 档位只接受 easy|normal|hard|expert"
	}
	return ""
}

// chatRoutingCommitOverride 落盘会话覆盖（§3.4）+ 失效 + 状态栏刷新（§4.5/§6）。
func chatRoutingCommitOverride(session *ChatSession, override *agentconfig.AICLISessionRoutingOverride) error {
	if session == nil || session.RuntimeSession == nil {
		return fmt.Errorf("当前会话不可写路由覆盖")
	}
	if chatRoutingOverrideIsEmpty(override) {
		override = nil
	}
	if override != nil {
		override.UpdatedAt = time.Now()
		override.UpdatedBy = "aicli-tui"
	}
	encoded, err := agentconfig.EncodeSessionRoutingOverride(override)
	if err != nil {
		return err
	}
	ctx := session.RuntimeSession.Metadata.Context
	if strings.TrimSpace(encoded) == "" {
		sessionmeta.Delete(ctx, sessionmeta.LegacyAICLIRoutingOverride, agentconfig.SessionRoutingOverrideContextKey)
	} else {
		sessionmeta.Set(ctx, sessionmeta.LegacyAICLIRoutingOverride, encoded, agentconfig.SessionRoutingOverrideContextKey)
	}
	if err := syncRuntimeSessionFromChat(session); err != nil {
		return fmt.Errorf("路由已写入会话内存但持久化失败: %w", err)
	}
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
	return nil
}

// chatRoutingLockSessionWrite 取会话级写锁（§3.4/M11）：与 API handler 的
// runtimechat.LockSessionWrite 是同一把进程内锁；返回幂等释放函数。
func chatRoutingLockSessionWrite(session *ChatSession) func() {
	if session == nil || session.RuntimeSession == nil {
		return func() {}
	}
	return runtimechat.LockSessionWrite(session.RuntimeSession.ID)
}

// chatRoutingWithSessionWriteLock 在会话级写锁内执行「读-改-写」（§3.4/M11）。
func chatRoutingWithSessionWriteLock(session *ChatSession, fn func() error) error {
	unlock := chatRoutingLockSessionWrite(session)
	defer unlock()
	return fn()
}

// ---------- 覆盖读写与键空间（§3.2/§5.4） ----------

// chatSessionRoutingOverride 解码会话覆盖；无覆盖/解码失败按 nil 处理（B5）。
func chatSessionRoutingOverride(session *ChatSession) *agentconfig.AICLISessionRoutingOverride {
	override, err := agentconfig.DecodeSessionRoutingOverride(chatSessionRoutingOverrideRaw(session))
	if err != nil {
		return nil
	}
	return override
}

// chatRoutingWorkspacePreferences 读取会话绑定 workspace 的 routing 偏好（N9）：
// 有绑定路径时以该路径为准，避免随 shell cwd 漂移；无绑定时回退进程 cwd。
func chatRoutingWorkspacePreferences(session *ChatSession) *agentconfig.AICLIWorkspaceRoutingPreferences {
	if path := chatSessionRoutingWorkspacePath(session); path != "" {
		prefs, err := agentconfig.LoadWorkspaceRoutingPreferencesForPath(path)
		if err != nil || prefs == nil || !prefs.HasRoutingFields() {
			return nil
		}
		return prefs
	}
	return localChatWorkspaceRoutingPreferences()
}

// chatRoutingCloneOverride 深拷贝会话覆盖，保证校验/写入不原地改写会话状态（INV-A5）。
func chatRoutingCloneOverride(in *agentconfig.AICLISessionRoutingOverride) *agentconfig.AICLISessionRoutingOverride {
	out := &agentconfig.AICLISessionRoutingOverride{}
	if in == nil {
		return out
	}
	out.UpdatedAt = in.UpdatedAt
	out.UpdatedBy = in.UpdatedBy
	if in.MainAgent != nil {
		cloned := *in.MainAgent
		out.MainAgent = &cloned
		if len(in.MainAgent.Profiles) > 0 {
			out.MainAgent.Profiles = make(map[string]agentconfig.AICLISessionRouteProfileOverride, len(in.MainAgent.Profiles))
			for level, profile := range in.MainAgent.Profiles {
				out.MainAgent.Profiles[level] = chatRoutingCloneProfileOverride(profile)
			}
		}
	}
	if in.SubAgent != nil {
		cloned := *in.SubAgent
		out.SubAgent = &cloned
		if len(in.SubAgent.Levels) > 0 {
			out.SubAgent.Levels = make(map[string]agentconfig.AICLISessionRouteProfileOverride, len(in.SubAgent.Levels))
			for level, profile := range in.SubAgent.Levels {
				out.SubAgent.Levels[level] = chatRoutingCloneProfileOverride(profile)
			}
		}
	}
	return out
}

func chatRoutingCloneProfileOverride(in agentconfig.AICLISessionRouteProfileOverride) agentconfig.AICLISessionRouteProfileOverride {
	out := in
	if in.Candidates != nil {
		candidates := make([]agentconfig.AICLISubagentRouteCandidate, len(*in.Candidates))
		copy(candidates, *in.Candidates)
		out.Candidates = &candidates
	}
	return out
}

// chatRoutingOverrideIsEmpty 判定覆盖是否已无生效字段（用于回收 context 键）。
func chatRoutingOverrideIsEmpty(override *agentconfig.AICLISessionRoutingOverride) bool {
	if override == nil {
		return true
	}
	return !override.HasMainAgentFields() && !override.HasSubAgentFields()
}

// chatRoutingApplyKey 把 `<key> <value>` 写入覆盖副本（§5.4 键空间）。
func chatRoutingApplyKey(override *agentconfig.AICLISessionRoutingOverride, scope, key, value string) error {
	value = strings.TrimSpace(value)
	if chatRoutingNormalizeScope(scope) == chatRoutingScopeSub {
		if override.SubAgent == nil {
			override.SubAgent = &agentconfig.AICLISessionSubAgentRoutingOverride{}
		}
		return chatRoutingApplySubKey(override.SubAgent, key, value)
	}
	if override.MainAgent == nil {
		override.MainAgent = &agentconfig.AICLISessionMainAgentRoutingOverride{}
	}
	return chatRoutingApplyMainKey(override.MainAgent, key, value)
}

func chatRoutingApplyMainKey(main *agentconfig.AICLISessionMainAgentRoutingOverride, key, value string) error {
	switch key {
	case "enabled":
		parsed, err := chatRoutingParseBool(value)
		if err != nil {
			return err
		}
		main.Enabled = &parsed
	case "allow_expert":
		parsed, err := chatRoutingParseBool(value)
		if err != nil {
			return err
		}
		main.AllowExpert = &parsed
	case "respect_provider_health":
		parsed, err := chatRoutingParseBool(value)
		if err != nil {
			return err
		}
		main.RespectProviderHealth = &parsed
	case "levels":
		parsed, err := chatRoutingParseList(value)
		if err != nil {
			return err
		}
		main.Levels = &parsed
	case "expensive_levels":
		parsed, err := chatRoutingParseList(value)
		if err != nil {
			return err
		}
		main.ExpensiveLevels = &parsed
	case "default_difficulty":
		parsed, err := chatRoutingNonEmpty(value)
		if err != nil {
			return err
		}
		parsed = strings.ToLower(parsed)
		main.DefaultDifficulty = &parsed
	case "cost_guard_mode":
		parsed := strings.ToLower(strings.TrimSpace(value))
		if parsed != "soft" && parsed != "hard" {
			return fmt.Errorf("cost_guard_mode 仅支持 soft|hard（M1）: %q", value)
		}
		main.CostGuardMode = &parsed
	case "max_consecutive_expensive_steps":
		parsed, err := chatRoutingParseInt(value)
		if err != nil {
			return err
		}
		main.MaxConsecutiveExpensiveSteps = &parsed
	case "max_invalid_reports_per_turn":
		parsed, err := chatRoutingParseInt(value)
		if err != nil {
			return err
		}
		main.MaxInvalidReportsPerTurn = &parsed
	case "downgrade_confirm_steps":
		parsed, err := chatRoutingParseInt(value)
		if err != nil {
			return err
		}
		main.DowngradeConfirmSteps = &parsed
	case "min_dwell_steps":
		parsed, err := chatRoutingParseInt(value)
		if err != nil {
			return err
		}
		main.MinDwellSteps = &parsed
	default:
		level, field, ok := chatRoutingSplitLevelKey(key, "profiles.")
		if !ok {
			return fmt.Errorf("未知键 %q；%s", key, chatRoutingKeySpaceHint(chatRoutingScopeMain))
		}
		if main.Profiles == nil {
			main.Profiles = make(map[string]agentconfig.AICLISessionRouteProfileOverride)
		}
		profile := main.Profiles[level]
		if err := chatRoutingApplyProfileOverride(&profile, field, value); err != nil {
			return err
		}
		main.Profiles[level] = profile
	}
	return nil
}

func chatRoutingApplySubKey(sub *agentconfig.AICLISessionSubAgentRoutingOverride, key, value string) error {
	switch key {
	case "enabled":
		parsed, err := chatRoutingParseBool(value)
		if err != nil {
			return err
		}
		sub.Enabled = &parsed
	case "default_difficulty":
		parsed, err := chatRoutingNonEmpty(value)
		if err != nil {
			return err
		}
		parsed = strings.ToLower(parsed)
		sub.DefaultDifficulty = &parsed
	default:
		level, field, ok := chatRoutingSplitLevelKey(key, "levels.")
		if !ok {
			return fmt.Errorf("未知键 %q；%s", key, chatRoutingKeySpaceHint(chatRoutingScopeSub))
		}
		if sub.Levels == nil {
			sub.Levels = make(map[string]agentconfig.AICLISessionRouteProfileOverride)
		}
		profile := sub.Levels[level]
		if err := chatRoutingApplyProfileOverride(&profile, field, value); err != nil {
			return err
		}
		sub.Levels[level] = profile
	}
	return nil
}

func chatRoutingApplyProfileOverride(profile *agentconfig.AICLISessionRouteProfileOverride, field, value string) error {
	switch field {
	case "provider":
		parsed, err := chatRoutingNonEmpty(value)
		if err != nil {
			return err
		}
		profile.Provider = &parsed
	case "model":
		parsed, err := chatRoutingNonEmpty(value)
		if err != nil {
			return err
		}
		profile.Model = &parsed
	case "reasoning_effort":
		parsed, err := chatRoutingNonEmpty(value)
		if err != nil {
			return err
		}
		profile.ReasoningEffort = &parsed
	case "thinking_effort":
		parsed, err := chatRoutingNonEmpty(value)
		if err != nil {
			return err
		}
		profile.ThinkingEffort = &parsed
	case "max_tokens":
		parsed, err := chatRoutingParseInt(value)
		if err != nil {
			return err
		}
		profile.MaxTokens = &parsed
	case "temperature":
		parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil {
			return fmt.Errorf("temperature 需要数字: %q", value)
		}
		profile.Temperature = &parsed
	case "timeout":
		parsed, err := chatRoutingNonEmpty(value)
		if err != nil {
			return err
		}
		profile.Timeout = &parsed
	case "availability":
		parsed, err := chatRoutingNonEmpty(value)
		if err != nil {
			return err
		}
		profile.Availability = &parsed
	case "availability_reason":
		parsed := strings.TrimSpace(value)
		profile.AvailabilityReason = &parsed
	case "prompt_cache":
		parsed, err := chatRoutingParseBool(value)
		if err != nil {
			return err
		}
		profile.PromptCache = &parsed
	case "candidates":
		return fmt.Errorf("candidates 为只读字段（§5.3：v1 不可经面板/命令替换整链），请在配置层维护")
	default:
		return fmt.Errorf("未知 profile 字段 %q", field)
	}
	return nil
}

// chatRoutingSplitLevelKey 解析 `profiles.<level>.<field>` / `levels.<level>.<field>`。
func chatRoutingSplitLevelKey(key, prefix string) (string, string, bool) {
	if !strings.HasPrefix(key, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(key, prefix)
	level, field, found := strings.Cut(rest, ".")
	level = strings.ToLower(strings.TrimSpace(level))
	field = strings.ToLower(strings.TrimSpace(field))
	if !found || level == "" || field == "" {
		return "", "", false
	}
	return level, field, true
}

func chatRoutingKeySpaceHint(scope string) string {
	if chatRoutingNormalizeScope(scope) == chatRoutingScopeSub {
		return "可用键: enabled | default_difficulty | levels.<level>.<field>"
	}
	return "可用键: enabled | allow_expert | levels | default_difficulty | cost_guard_mode | max_consecutive_expensive_steps | expensive_levels | profiles.<level>.<field>"
}

func chatRoutingParseBool(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "on", "yes", "y", "1", "enable", "enabled":
		return true, nil
	case "false", "off", "no", "n", "0", "disable", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("需要布尔值（true/false/on/off）: %q", value)
	}
}

func chatRoutingParseList(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.ToLower(strings.TrimSpace(part))
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("需要逗号分隔的档位列表（如 easy,normal,hard）: %q", value)
	}
	return out, nil
}

func chatRoutingParseInt(value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("需要整数: %q", value)
	}
	return parsed, nil
}

func chatRoutingNonEmpty(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", fmt.Errorf("值不能为空")
	}
	return trimmed, nil
}

// chatRoutingResetFromArgs 实现 `/routing reset [main|sub] [<level>] [session|workspace|config]`（§5.4）。
func chatRoutingResetFromArgs(session *ChatSession, args []string, layer string, confirm bool) string {
	// M16/INV-A3：reset 同样属于变更类命令，子会话一律拒绝（§5.6）。
	if chatRoutingSessionIsChildAgent(session) {
		return "错误: " + chatRoutingChildSessionReadOnlyNote
	}
	scope := ""
	level := ""
	for _, arg := range args {
		switch strings.ToLower(arg) {
		case chatRoutingScopeMain, chatRoutingScopeSub:
			scope = strings.ToLower(arg)
		case chatRoutingLayerSession, chatRoutingLayerWorkspace, chatRoutingLayerConfig:
			layer = strings.ToLower(arg)
		default:
			level = arg
		}
	}
	if scope == "" {
		scope = chatRoutingScopeMain
	}
	if layer != chatRoutingLayerSession {
		switch layer {
		case chatRoutingLayerWorkspace:
			return chatRoutingWithRuntimeRefreshText(session, chatRoutingResetWorkspaceLayer(session, scope, level))
		case chatRoutingLayerConfig:
			return chatRoutingWithRuntimeRefreshText(session, chatRoutingResetConfigLayer(session, scope, level, confirm))
		default:
			return "错误: 未知写入层 " + layer + "（可用 session|workspace|config）"
		}
	}
	// §3.4/M11：清除同样是「读-改-写」，与写入路径共用同一把会话锁。
	unlockSession := chatRoutingLockSessionWrite(session)
	defer unlockSession()

	override := chatSessionRoutingOverride(session)
	if override == nil {
		return "当前会话没有路由覆盖，无需清除"
	}
	level = strings.ToLower(strings.TrimSpace(level))
	switch scope {
	case chatRoutingScopeSub:
		if override.SubAgent == nil {
			return "子 Agent 没有会话覆盖，无需清除"
		}
		if level == "" {
			override.SubAgent = nil
		} else {
			delete(override.SubAgent.Levels, level)
		}
	default:
		if override.MainAgent == nil {
			return "主 Agent 没有会话覆盖，无需清除"
		}
		if level == "" {
			override.MainAgent = nil
		} else {
			delete(override.MainAgent.Profiles, level)
		}
	}
	if err := chatRoutingCommitOverride(session, override); err != nil {
		return "错误: " + err.Error()
	}
	unlockSession() // 落盘完成即释放：状态栏刷新与 runtime 重建不再占用写锁
	target := scope
	if level != "" {
		target = scope + "." + level
	}
	text := fmt.Sprintf("已清除会话路由覆盖（%s）（下一 turn 生效）", target)
	if projection := chatRoutingProjectionForScope(session, scope); projection.Enabled {
		text += "\n当前: " + chatRoutingCompactSummary(projection)
	}
	return chatRoutingWithRuntimeRefreshText(session, text)
}

// chatRoutingSaveFromArgs 实现 `/routing save [session|workspace|config]`（§5.4/I-2）。
// session 层随会话持久化；workspace/config 层把会话覆盖保存到目标层
// （字段级合并，不自动清除会话层；config 层需 --yes）。
func chatRoutingSaveFromArgs(session *ChatSession, args []string, layer string, confirm bool) string {
	// M16/INV-A3：save 会写 workspace/config 层（真实副作用），子会话必须挡在落盘前。
	if chatRoutingSessionIsChildAgent(session) {
		return "错误: " + chatRoutingChildSessionReadOnlyNote
	}
	target := layer
	for _, arg := range args {
		switch strings.ToLower(arg) {
		case chatRoutingLayerSession, chatRoutingLayerWorkspace, chatRoutingLayerConfig:
			target = strings.ToLower(arg)
		default:
			return "错误: 未知保存目标 " + arg + "（可用 session|workspace|config）"
		}
	}
	switch target {
	case chatRoutingLayerSession:
		return "会话层覆盖随会话持久化（Session.Metadata.Context，§3.4），无需额外保存"
	default:
		return chatRoutingWithRuntimeRefreshText(session, chatRoutingSaveToLayer(session, target, confirm))
	}
}
