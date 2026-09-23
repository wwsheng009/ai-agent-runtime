package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// 会话级路由的三层写入（方案 §5.4/I-2）。
//
// session 层在 chat_routing_command.go（会话 context）；本文件补齐 workspace/config
// 两层：写前校验 → 落盘 → 快照刷新 → 状态栏刷新，语义与 runtime API 的
// PATCH /routing（session_routing_handlers.go 的 validateWorkspaceRoutingWrite /
// writeConfigRoutingSection）保持同源，避免 TUI 与 API 两条写入路径漂移。

// chatRoutingPatchForKey 把 `<key> <value>` 物化为层补丁（workspace/config 写入用）。
// 复用会话层的键解析（§5.4 同一键空间），只取覆盖结构，不触碰会话 context。
func chatRoutingPatchForKey(scope, key, value string) (*agentconfig.SessionRoutingPatch, error) {
	candidate := &agentconfig.AICLISessionRoutingOverride{}
	if err := chatRoutingApplyKey(candidate, scope, key, value); err != nil {
		return nil, chatRoutingFieldEcho(key, err)
	}
	return &agentconfig.SessionRoutingPatch{
		MainAgent: candidate.MainAgent,
		SubAgent:  candidate.SubAgent,
		UpdatedBy: "aicli-tui",
	}, nil
}

// chatRoutingPatchFromOverride 把会话覆盖整体物化为层补丁（`/routing save` 用）。
func chatRoutingPatchFromOverride(override *agentconfig.AICLISessionRoutingOverride) *agentconfig.SessionRoutingPatch {
	if override == nil {
		return nil
	}
	return &agentconfig.SessionRoutingPatch{
		MainAgent: override.MainAgent,
		SubAgent:  override.SubAgent,
		UpdatedBy: "aicli-tui",
	}
}

// chatRoutingRefreshStatus 让状态栏/面板立即消费新投影（§6.2）。
// 三层写入共用：session 层在 chatRoutingCommitOverride 内已有等价刷新。
func chatRoutingRefreshStatus(session *ChatSession) {
	if session != nil && session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}

// chatRoutingWorkspaceWriteTarget 返回会话绑定 workspace 的写入路径与回显路径（N9）。
func chatRoutingWorkspaceWriteTarget(session *ChatSession) (string, string, error) {
	path := strings.TrimSpace(chatSessionRoutingWorkspacePath(session))
	if path == "" {
		return "", "", fmt.Errorf("当前会话未绑定工作区，无法写入 workspace 层")
	}
	target := strings.TrimSpace(agentconfig.WorkspaceRoutingTargetPath(path))
	if target == "" {
		return "", "", fmt.Errorf("无法解析工作区偏好文件路径（%s）", path)
	}
	return path, target, nil
}

// chatRoutingValidateWorkspacePatch 是 workspace 层的写前校验（§5.4/U-3）：
// 物化到既有 workspace 配置之上后必须通过配置校验，失败不落盘。
// 与 API 侧 validateWorkspaceRoutingWrite 同口径（读失败即拒绝，不按「无覆盖」继续）。
func chatRoutingValidateWorkspacePatch(workspacePath string, patch *agentconfig.SessionRoutingPatch) error {
	if patch == nil {
		return nil
	}
	existing, err := agentconfig.LoadWorkspaceRoutingPreferencesForPath(workspacePath)
	if err != nil {
		return fmt.Errorf("读取工作区路由偏好失败: %w", err)
	}
	var baseMain *agentconfig.AICLIMainAgentRoutingConfig
	var baseSub *agentconfig.AICLISubagentRoutingConfig
	if existing != nil {
		baseMain = existing.MainAgent
		baseSub = existing.SubAgent
	}
	if patch.MainAgent != nil {
		candidate := agentconfig.MaterializeMainAgentRoutingConfig(baseMain, patch.MainAgent)
		if _, err := agentconfig.ValidateMainAgentRoutingConfig(candidate); err != nil {
			return err
		}
	}
	if patch.SubAgent != nil {
		candidate := agentconfig.MaterializeSubagentRoutingConfig(baseSub, patch.SubAgent)
		if _, err := agentconfig.ValidateSubagentRoutingConfig("aicli.subagents.routing", candidate); err != nil {
			return err
		}
	}
	return nil
}

// chatRoutingWriteWorkspaceLayer 写入 workspace 层（§5.4/N9）：
// 写前校验 → chat-prefs.yaml → 状态栏刷新。
// workspace 偏好每次解析都从磁盘读取（chatRoutingWorkspacePreferences），无需快照刷新。
func chatRoutingWriteWorkspaceLayer(session *ChatSession, scope, key, value string) (string, error) {
	workspacePath, targetPath, err := chatRoutingWorkspaceWriteTarget(session)
	if err != nil {
		return "", err
	}
	patch, err := chatRoutingPatchForKey(scope, key, value)
	if err != nil {
		return "", err
	}
	if err := chatRoutingValidateWorkspacePatch(workspacePath, patch); err != nil {
		return "", chatRoutingWithValidationSuggestion(scope, key, err)
	}
	if err := agentconfig.UpdateWorkspaceRoutingSection(workspacePath, patch); err != nil {
		return "", err
	}
	chatRoutingRefreshStatus(session)
	text := fmt.Sprintf("已写入工作区路由偏好: %s.%s = %s（目标: %s，下一 turn 生效）", scope, key, value, targetPath)
	if projection := chatRoutingProjectionForScope(session, scope); projection.Enabled {
		text += "\n当前: " + chatRoutingCompactSummary(projection)
	}
	return text, nil
}

// chatRoutingConfigSnapshotMain / Sub 读取进程内配置快照的 routing 节作为写入基准
// （绝不原地改写快照：Materialize 先深拷贝，INV-A5）。
func chatRoutingConfigSnapshotMain(session *ChatSession) *agentconfig.AICLIMainAgentRoutingConfig {
	if session == nil || session.Config == nil || session.Config.AICLI == nil || session.Config.AICLI.MainAgent == nil {
		return nil
	}
	return session.Config.AICLI.MainAgent.Routing
}

func chatRoutingConfigSnapshotSub(session *ChatSession) *agentconfig.AICLISubagentRoutingConfig {
	if session == nil || session.Config == nil || session.Config.AICLI == nil || session.Config.AICLI.Subagents == nil {
		return nil
	}
	return session.Config.AICLI.Subagents.Routing
}

// chatRoutingWriteConfigLayer 写入 config 层（§5.4：按层路由 + 二次确认 + 快照刷新）。
// 目标文件由 AICLIConfigWriteTargetForRouting 按层路由（无项目层时落全局）。
func chatRoutingWriteConfigLayer(session *ChatSession, scope, key, value string, confirm bool) (string, error) {
	configPath, layerKind := agentconfig.AICLIConfigWriteTargetForRouting()
	if strings.TrimSpace(configPath) == "" {
		return "", fmt.Errorf("配置写入目标不可用（无可用配置层）")
	}
	patch, err := chatRoutingPatchForKey(scope, key, value)
	if err != nil {
		return "", err
	}
	if !confirm {
		return "", fmt.Errorf("config 层写入影响所有会话，需二次确认：追加 --yes（目标: %s，%s）", configPath, layerKind)
	}

	// 与 API writeConfigRoutingSection 同源：先物化到快照之上，再校验，最后落盘。
	var nextMain *agentconfig.AICLIMainAgentRoutingConfig
	var nextSub *agentconfig.AICLISubagentRoutingConfig
	if patch.MainAgent != nil {
		nextMain = agentconfig.MaterializeMainAgentRoutingConfig(chatRoutingConfigSnapshotMain(session), patch.MainAgent)
		if _, err := agentconfig.ValidateMainAgentRoutingConfig(nextMain); err != nil {
			return "", chatRoutingWithValidationSuggestion(scope, key, err)
		}
	}
	if patch.SubAgent != nil {
		nextSub = agentconfig.MaterializeSubagentRoutingConfig(chatRoutingConfigSnapshotSub(session), patch.SubAgent)
		if _, err := agentconfig.ValidateSubagentRoutingConfig("aicli.subagents.routing", nextSub); err != nil {
			return "", chatRoutingWithValidationSuggestion(scope, key, err)
		}
	}
	if err := agentconfig.UpdateAICLIRoutingSection(configPath, nextMain, patch.MainAgent != nil, nextSub, patch.SubAgent != nil); err != nil {
		return "", err
	}
	if err := chatRoutingReloadConfigSnapshot(session, configPath); err != nil {
		return "", fmt.Errorf("配置已写入 %s，但刷新内存快照失败（重启后生效）: %w", configPath, err)
	}
	chatRoutingRefreshStatus(session)
	text := fmt.Sprintf("已写入配置层路由: %s.%s = %s（目标: %s，%s，影响所有会话，下一 turn 生效）",
		scope, key, value, configPath, layerKind)
	if projection := chatRoutingProjectionForScope(session, scope); projection.Enabled {
		text += "\n当前: " + chatRoutingCompactSummary(projection)
	}
	return text, nil
}

// chatRoutingReloadConfigSnapshot 重新加载配置并替换会话持有的快照，
// 与 /model 的既有权衡一致（chat_model_command.go）：投影与接线读取 session.Config，
// 不刷新会一直回显旧配置。
func chatRoutingReloadConfigSnapshot(session *ChatSession, configPath string) error {
	reloaded, err := agentconfig.ReloadGlobalConfig(configPath)
	if err != nil {
		return err
	}
	if reloaded == nil {
		return fmt.Errorf("重新读取配置 %s 失败: 配置为空", configPath)
	}
	if session != nil {
		session.Config = reloaded
	}
	return nil
}

// chatRoutingClearFieldsFor 生成某作用域（可含档位）在目标层的清除键路径（§3.5.1）。
func chatRoutingClearFieldsFor(scope, level string) []string {
	level = strings.ToLower(strings.TrimSpace(level))
	if chatRoutingNormalizeScope(scope) == chatRoutingScopeSub {
		if level == "" {
			return []string{"sub_agent"}
		}
		return []string{"sub_agent.levels." + level}
	}
	if level == "" {
		return []string{"main_agent"}
	}
	return []string{"main_agent.profiles." + level}
}

// chatRoutingResetWorkspaceLayer 清除 workspace 层的路由字段（§5.4 逐层回退）。
func chatRoutingResetWorkspaceLayer(session *ChatSession, scope, level string) string {
	workspacePath, targetPath, err := chatRoutingWorkspaceWriteTarget(session)
	if err != nil {
		return "错误: " + err.Error()
	}
	patch := &agentconfig.SessionRoutingPatch{
		ClearFields: chatRoutingClearFieldsFor(scope, level),
		UpdatedBy:   "aicli-tui",
	}
	if err := agentconfig.UpdateWorkspaceRoutingSection(workspacePath, patch); err != nil {
		return "错误: " + err.Error()
	}
	chatRoutingRefreshStatus(session)
	text := fmt.Sprintf("已清除工作区路由偏好（%s）（目标: %s，下一 turn 生效）",
		chatRoutingClearTargetLabel(scope, level), targetPath)
	if projection := chatRoutingProjectionForScope(session, scope); projection.Enabled {
		text += "\n当前: " + chatRoutingCompactSummary(projection)
	}
	return text
}

// chatRoutingResetConfigLayer 清除 config 层的路由字段（§5.4 逐层回退）。
// 主 Agent 支持整节/整档清除（ClearMainAgentRoutingConfigFields + 空节删除）；
// 子 Agent 支持整节清除；逐档清除缺少 config 层减法原语（与 API 同边界）。
func chatRoutingResetConfigLayer(session *ChatSession, scope, level string, confirm bool) string {
	configPath, layerKind := agentconfig.AICLIConfigWriteTargetForRouting()
	if strings.TrimSpace(configPath) == "" {
		return "错误: 配置写入目标不可用（无可用配置层）"
	}
	if !confirm {
		return fmt.Sprintf("错误: config 层清除影响所有会话，需二次确认：追加 --yes（目标: %s，%s）", configPath, layerKind)
	}
	level = strings.ToLower(strings.TrimSpace(level))
	if chatRoutingNormalizeScope(scope) == chatRoutingScopeSub {
		if level != "" {
			return "错误: config 层子 Agent 逐档清除尚未支持；可用 /routing reset sub --to config 清除整节"
		}
		if err := agentconfig.UpdateAICLIRoutingSection(configPath, nil, false, nil, true); err != nil {
			return "错误: " + err.Error()
		}
		if err := chatRoutingReloadConfigSnapshot(session, configPath); err != nil {
			return "错误: " + err.Error()
		}
		chatRoutingRefreshStatus(session)
		return fmt.Sprintf("已清除配置层子 Agent 路由（目标: %s，%s，下一 turn 生效）", configPath, layerKind)
	}

	next := agentconfig.MaterializeMainAgentRoutingConfig(chatRoutingConfigSnapshotMain(session), nil)
	agentconfig.ClearMainAgentRoutingConfigFields(next, chatRoutingClearFieldsFor(scope, level))
	if agentconfig.IsEmptyMainAgentRoutingConfig(next) {
		next = nil
	} else if _, err := agentconfig.ValidateMainAgentRoutingConfig(next); err != nil {
		return "错误: " + err.Error()
	}
	if err := agentconfig.UpdateAICLIRoutingSection(configPath, next, true, nil, false); err != nil {
		return "错误: " + err.Error()
	}
	if err := chatRoutingReloadConfigSnapshot(session, configPath); err != nil {
		return "错误: " + err.Error()
	}
	chatRoutingRefreshStatus(session)
	text := fmt.Sprintf("已清除配置层主 Agent 路由（%s）（目标: %s，%s，下一 turn 生效）",
		chatRoutingClearTargetLabel(scope, level), configPath, layerKind)
	if projection := chatRoutingProjectionForScope(session, scope); projection.Enabled {
		text += "\n当前: " + chatRoutingCompactSummary(projection)
	}
	return text
}

// chatRoutingClearTargetLabel 生成清除目标的可读标签（main / main.profiles.hard / sub / sub.levels.hard）。
func chatRoutingClearTargetLabel(scope, level string) string {
	scope = chatRoutingNormalizeScope(scope)
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		return scope
	}
	if scope == chatRoutingScopeSub {
		return scope + ".levels." + level
	}
	return scope + ".profiles." + level
}

// chatRoutingSaveToLayer 把当前会话覆盖保存到 workspace/config 层（§5.4）。
// 语义：目标层做字段级合并写入；**不自动清除会话层**（会话层仍优先，
// 需显式 `/routing reset --to session` 才回落到新写入的下层值）。
func chatRoutingSaveToLayer(session *ChatSession, layer string, confirm bool) string {
	override := chatSessionRoutingOverride(session)
	if chatRoutingOverrideIsEmpty(override) {
		return "当前会话没有路由覆盖，无可保存内容"
	}
	patch := chatRoutingPatchFromOverride(override)
	switch layer {
	case chatRoutingLayerWorkspace:
		workspacePath, targetPath, err := chatRoutingWorkspaceWriteTarget(session)
		if err != nil {
			return "错误: " + err.Error()
		}
		if err := chatRoutingValidateWorkspacePatch(workspacePath, patch); err != nil {
			return "错误: " + err.Error()
		}
		if err := agentconfig.UpdateWorkspaceRoutingSection(workspacePath, patch); err != nil {
			return "错误: " + err.Error()
		}
		chatRoutingRefreshStatus(session)
		return fmt.Sprintf("已保存到工作区路由偏好（目标: %s，下一 turn 生效）\n说明: 会话层覆盖仍优先；如需回落请 /routing reset --to session",
			targetPath)
	case chatRoutingLayerConfig:
		configPath, layerKind := agentconfig.AICLIConfigWriteTargetForRouting()
		if strings.TrimSpace(configPath) == "" {
			return "错误: 配置写入目标不可用（无可用配置层）"
		}
		if !confirm {
			return fmt.Sprintf("错误: config 层保存影响所有会话，需二次确认：追加 --yes（目标: %s，%s）", configPath, layerKind)
		}
		var nextMain *agentconfig.AICLIMainAgentRoutingConfig
		var nextSub *agentconfig.AICLISubagentRoutingConfig
		if patch.MainAgent != nil {
			nextMain = agentconfig.MaterializeMainAgentRoutingConfig(chatRoutingConfigSnapshotMain(session), patch.MainAgent)
			if _, err := agentconfig.ValidateMainAgentRoutingConfig(nextMain); err != nil {
				return "错误: " + err.Error()
			}
		}
		if patch.SubAgent != nil {
			nextSub = agentconfig.MaterializeSubagentRoutingConfig(chatRoutingConfigSnapshotSub(session), patch.SubAgent)
			if _, err := agentconfig.ValidateSubagentRoutingConfig("aicli.subagents.routing", nextSub); err != nil {
				return "错误: " + err.Error()
			}
		}
		if err := agentconfig.UpdateAICLIRoutingSection(configPath, nextMain, patch.MainAgent != nil, nextSub, patch.SubAgent != nil); err != nil {
			return "错误: " + err.Error()
		}
		if err := chatRoutingReloadConfigSnapshot(session, configPath); err != nil {
			return "错误: " + err.Error()
		}
		chatRoutingRefreshStatus(session)
		return fmt.Sprintf("已保存到配置层（目标: %s，%s，影响所有会话，下一 turn 生效）\n说明: 会话层覆盖仍优先；如需回落请 /routing reset --to session",
			configPath, layerKind)
	default:
		return "错误: 未知保存目标 " + layer + "（可用 session|workspace|config）"
	}
}
