package commands

import (
	"fmt"
	"strings"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// parseSkillToggleQuery 解析 /skills 的启停子命令：
// `disable <name>` / `off <name>` / `enable <name>` / `on <name>`。
func parseSkillToggleQuery(query string) (enable bool, name string, ok bool) {
	fields := strings.Fields(strings.TrimSpace(query))
	if len(fields) < 2 {
		return false, "", false
	}
	switch strings.ToLower(fields[0]) {
	case "disable", "off", "stop":
		return false, strings.TrimSpace(strings.Join(fields[1:], " ")), true
	case "enable", "on", "start":
		return true, strings.TrimSpace(strings.Join(fields[1:], " ")), true
	default:
		return false, "", false
	}
}

// executeStructuredSkillToggleCommand 是统一渲染通道下的 /skills 启停入口。
func executeStructuredSkillToggleCommand(session *ChatSession, enable bool, name string, jsonOutput bool) CommandResult {
	message, err := runSkillToggleCommand(session, enable, name)
	if jsonOutput {
		payload := map[string]interface{}{
			"skill":   strings.TrimSpace(name),
			"enabled": enable,
			"changed": err == nil,
		}
		if err != nil {
			payload["error"] = err.Error()
		} else {
			payload["message"] = message
		}
		return commandTextResult(marshalIndentedJSON(payload))
	}
	if err != nil {
		return commandErrorResult(err)
	}
	return commandTextResult(message)
}

// runSkillToggleCommand 执行 per-skill 启停：
// 校验（停用要求 skill 在当前函数面内）→ 内存生效配置更新 → 配置文件落盘 →
// 运行面热刷新（loader 过滤器 + registry + 函数面撤销）。
//
// 落盘失败不吞：宁可报错也不做"只在内存里、重启即失效"的假开关。
func runSkillToggleCommand(session *ChatSession, enable bool, name string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}
	cfg := effectiveChatSkillConfig(session.Config, session)
	if cfg == nil || cfg.SkillsRuntime == nil {
		return "", fmt.Errorf("技能运行时未启用（skills_runtime.enabled）")
	}
	requested := strings.TrimSpace(name)
	if requested == "" {
		return "", fmt.Errorf("用法: /skills disable|enable <skill-name>")
	}

	current := append([]string(nil), cfg.SkillsRuntime.DisabledSkillNames()...)
	index := -1
	for i, item := range current {
		if strings.EqualFold(strings.TrimSpace(item), requested) {
			index = i
			break
		}
	}

	target := requested
	if enable {
		if index < 0 {
			return fmt.Sprintf("skill %q 当前未停用", requested), nil
		}
		current = append(current[:index], current[index+1:]...)
	} else {
		canonical, found := chatSessionSkillName(session, requested)
		if !found {
			return fmt.Sprintf("未找到 skill %q；已停用的 skill 不在列表中，可用 /skills enable %s 恢复", requested, requested), nil
		}
		if index >= 0 {
			return fmt.Sprintf("skill %q 已是停用状态", canonical), nil
		}
		target = canonical
		current = append(current, canonical)
	}

	configPath := strings.TrimSpace(cfg.ConfigFilePath)
	if configPath == "" {
		// 没有可写配置文件时不做内存-only 的"假开关"：重启即失效会让用户
		// 以为禁用已生效。
		return "", fmt.Errorf("当前会话没有可写的配置文件路径，无法持久化 %q 的启停状态", requested)
	}
	if err := config.UpdateSkillsDisabledSkills(configPath, current); err != nil {
		return "", fmt.Errorf("写入 skills_runtime.disabled_skills 失败: %w", err)
	}
	applyDisabledSkillsToConfig(cfg, current)
	if err := refreshSkillsRuntimeBinding(session, cfg); err != nil {
		return "", err
	}

	action := "已停用"
	if enable {
		action = "已启用"
	}
	message := fmt.Sprintf("%s skill: %s", action, target)
	if len(current) == 0 {
		message += "（disabled_skills 已清空）"
	}
	return message, nil
}

// chatSessionSkillName 在当前会话函数面内按名字（大小写不敏感）找 skill，
// 返回其规范名。
func chatSessionSkillName(session *ChatSession, name string) (string, bool) {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return "", false
	}
	report := buildFunctionCatalogReport(catalog)
	if report == nil {
		return "", false
	}
	for _, item := range report.Skills {
		descriptorName := ""
		if item.Descriptor != nil {
			descriptorName = strings.TrimSpace(item.Descriptor.Name)
		}
		candidates := []string{descriptorName}
		if descriptorName == "" {
			// 没有 capability descriptor 时退回函数名（skill__<name>），
			// 仅用于匹配，不把它当作规范 skill 名。
			candidates = append(candidates, strings.TrimSpace(item.FunctionName))
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(candidate, strings.TrimSpace(name)) {
				return candidate, true
			}
		}
	}
	return "", false
}

// applyDisabledSkillsToConfig 把新名单写回内存生效配置（含 profile 覆盖视图），
// 与落盘内容保持一致；兼容 key 一并清空，避免两个 key 语义分叉。
func applyDisabledSkillsToConfig(cfg *config.Config, names []string) {
	if cfg == nil || cfg.SkillsRuntime == nil {
		return
	}
	cfg.SkillsRuntime.DisabledSkills = append([]string(nil), names...)
	cfg.SkillsRuntime.DisabledSkillsCompat = nil
}
