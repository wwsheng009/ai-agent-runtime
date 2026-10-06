package commands

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// parseSkillsReloadQuery 解析 /skills 的重载子命令：`reload` / `refresh`。
//
// 与 list/select/enable/disable 同级：单 token 保留字，优先于“按名称过滤”。
func parseSkillsReloadQuery(query string) bool {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if len(fields) != 1 {
		return false
	}
	switch fields[0] {
	case "reload", "refresh":
		return true
	default:
		return false
	}
}

// skillReloadReport 是 /skills reload 的结构化结果（文本与 --json 共用）。
type skillReloadReport struct {
	Status    string   `json:"status"`
	HotReload bool     `json:"hot_reload"`
	Before    int      `json:"before"`
	After     int      `json:"after"`
	Added     []string `json:"added,omitempty"`
	Removed   []string `json:"removed,omitempty"`
	Roots     []string `json:"roots,omitempty"`
}

// Text 渲染人类可读回执：动作、前后计数、增删明细与扫描根。
func (r *skillReloadReport) Text() string {
	if r == nil {
		return ""
	}
	mode := "重新发现"
	if r.HotReload {
		mode = "热重载"
	}
	lines := []string{fmt.Sprintf("已重载 skills（%s）: total=%d（before=%d）", mode, r.After, r.Before)}
	if len(r.Added) > 0 {
		lines = append(lines, "新增: "+strings.Join(r.Added, ", "))
	}
	if len(r.Removed) > 0 {
		lines = append(lines, "移除: "+strings.Join(r.Removed, ", "))
	}
	if len(r.Roots) > 0 {
		lines = append(lines, "Roots: "+strings.Join(r.Roots, ", "))
	}
	return strings.Join(lines, "\n")
}

// executeStructuredSkillReloadCommand 是统一渲染通道下的 /skills reload 入口。
func executeStructuredSkillReloadCommand(session *ChatSession, jsonOutput bool) CommandResult {
	report, err := runSkillReloadCommand(session)
	if jsonOutput {
		if err != nil {
			return commandTextResult(marshalIndentedJSON(map[string]string{
				"status": "error",
				"error":  err.Error(),
			}))
		}
		return commandTextResult(marshalIndentedJSON(report))
	}
	if err != nil {
		return commandErrorResult(err)
	}
	return commandTextResult(report.Text())
}

// runSkillReloadCommand 重扫技能目录并热刷新当前会话的函数面：
//
//  1. 有可用 HotReload 时走整表重载（同时更新 watcher 记账与事件）；
//     热重载不可用（未启用/已停/没有扫描根）时退化为与 REST /skills/reload
//     相同的语义：清空 registry → InvalidateAllHydratedSkills → 重新发现为 stub。
//  2. 无论哪条路径，最后都调用 refreshSkillsRuntimeBinding 重建 skill__* 函数面，
//     保证清单与模型工具面立刻反映磁盘现状。
//
// 报告 before/after 的差集（新增/移除），让“重载后到底变了什么”可见。
func runSkillReloadCommand(session *ChatSession) (*skillReloadReport, error) {
	if session == nil {
		return nil, fmt.Errorf("当前没有活动会话")
	}
	// 与结构化 /skills 入口一致：新会话可能还没挂载能力面，先补齐。
	_ = awaitChatCapabilitiesForTurn(context.Background(), session)

	cfg := effectiveChatSkillConfig(session.Config, session)
	binding := session.SkillsBinding
	if binding == nil {
		return nil, fmt.Errorf("技能运行时尚未挂载；请确认 skills_runtime.enabled 后重试")
	}
	manager := binding.Manager()
	if manager == nil {
		return nil, fmt.Errorf("技能运行时尚未挂载；请确认 skills_runtime.enabled 后重试")
	}
	registry := manager.Registry()
	loader := manager.Loader()
	if registry == nil || loader == nil {
		return nil, fmt.Errorf("技能运行时未就绪（registry/loader 缺失）")
	}

	before := skillNameSetFromRegistry(registry)
	report := &skillReloadReport{Status: "success", Before: len(before)}

	// 扫描面 = manager 已登记根 ∪ 会话当前可发现根：安装动作新建的标准目录在
	// watcher 接管前也可能已经存在，手工 reload 必须能覆盖到。
	report.Roots = normalizeReloadSkillDirs(manager.SkillDirs(), resolveChatSkillDirs(cfg, session, nil))

	usedHotReload := false
	if hotReload := manager.HotReload(); hotReload != nil && len(hotReload.SkillDirs()) > 0 {
		err := hotReload.Reload()
		switch {
		case err == nil:
			usedHotReload = true
		case strings.Contains(err.Error(), "disabled"), strings.Contains(err.Error(), "empty"):
			// 热重载不可用：走显式重建路径，不把"没开监听"当成重载失败。
		default:
			return nil, fmt.Errorf("重载 skills 失败: %w", err)
		}
	}

	if !usedHotReload {
		registry.Clear()
		runtimeskill.InvalidateAllHydratedSkills()
		registry.ClearLoadedCache()
		if len(report.Roots) > 0 {
			if err := loader.DiscoverAllWithRegistry(report.Roots, registry); err != nil {
				return nil, fmt.Errorf("重载 skills 失败: %w", err)
			}
		}
	}
	report.HotReload = usedHotReload

	if err := refreshSkillsRuntimeBinding(session, cfg); err != nil {
		return nil, err
	}

	after := skillNameSetFromRegistry(registry)
	report.After = len(after)
	report.Added, report.Removed = diffSkillNameSets(before, after)
	return report, nil
}

// normalizeReloadSkillDirs 合并扫描根：去重（精确路径）并保持先后顺序。
func normalizeReloadSkillDirs(groups ...[]string) []string {
	seen := make(map[string]struct{})
	result := make([]string, 0, 8)
	for _, group := range groups {
		for _, dir := range group {
			dir = filepath.Clean(strings.TrimSpace(dir))
			if dir == "" || dir == "." {
				continue
			}
			if _, exists := seen[dir]; exists {
				continue
			}
			seen[dir] = struct{}{}
			result = append(result, dir)
		}
	}
	return result
}

// skillNameSetFromRegistry 返回 registry 当前已登记 skill 的小写名 → 展示名。
func skillNameSetFromRegistry(registry *runtimeskill.Registry) map[string]string {
	names := make(map[string]string)
	if registry == nil {
		return names
	}
	for _, summary := range registry.ListSummaries() {
		if summary == nil {
			continue
		}
		name := strings.TrimSpace(summary.Name)
		key := strings.ToLower(name)
		if key == "" {
			continue
		}
		if _, exists := names[key]; !exists {
			names[key] = name
		}
	}
	return names
}

// diffSkillNameSets 计算前后差集，返回排序后的展示名列表。
func diffSkillNameSets(before, after map[string]string) (added, removed []string) {
	for key, name := range after {
		if _, exists := before[key]; !exists {
			added = append(added, name)
		}
	}
	for key, name := range before {
		if _, exists := after[key]; !exists {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}
