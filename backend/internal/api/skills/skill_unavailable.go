package skills

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// unavailableSkillsForResponse 返回注册表中的不可用技能快照，并按列表的
// source_layer/source_dir 过滤条件过滤（与 filterSkillsBySource 语义对齐）。
func unavailableSkillsForResponse(registry *skill.Registry, layer, dir string) []skill.UnavailableSkill {
	if registry == nil {
		return []skill.UnavailableSkill{}
	}
	items := filterUnavailableSkillsBySource(registry.UnavailableSkills(), layer, dir)
	if items == nil {
		return []skill.UnavailableSkill{}
	}
	return items
}

func filterUnavailableSkillsBySource(items []skill.UnavailableSkill, layer, dir string) []skill.UnavailableSkill {
	if layer == "" && dir == "" {
		return items
	}

	cleanedDir := ""
	if dir != "" {
		cleanedDir = strings.ToLower(filepath.Clean(dir))
	}
	filtered := make([]skill.UnavailableSkill, 0, len(items))
	for _, item := range items {
		if layer != "" && !strings.EqualFold(strings.TrimSpace(item.Scope), layer) {
			continue
		}
		if cleanedDir != "" {
			path := strings.ToLower(filepath.Clean(strings.TrimSpace(item.Path)))
			if path == "" || path == "." || !strings.HasPrefix(path, cleanedDir) {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// searchUnavailableSkills 在不可用技能中做名称/缺失工具/引导文案的简单匹配，
// 使 search 契约与 list 一样能看到 unavailable 技能（SK-4）。
func searchUnavailableSkills(registry *skill.Registry, query, layer, dir string, limit int) []skill.UnavailableSkill {
	all := unavailableSkillsForResponse(registry, layer, dir)
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || len(all) == 0 {
		return []skill.UnavailableSkill{}
	}

	matched := make([]skill.UnavailableSkill, 0, len(all))
	for _, item := range all {
		if unavailableSkillMatchesQuery(item, query) {
			matched = append(matched, item)
		}
	}
	if limit > 0 && len(matched) > limit {
		matched = matched[:limit]
	}
	return matched
}

func unavailableSkillMatchesQuery(item skill.UnavailableSkill, query string) bool {
	if strings.Contains(strings.ToLower(item.Name), query) {
		return true
	}
	if strings.Contains(strings.ToLower(item.Message), query) {
		return true
	}
	for _, tool := range item.MissingTools {
		if strings.Contains(strings.ToLower(tool), query) {
			return true
		}
	}
	return false
}

// unavailableSkillError 把不可用记录转成可操作的结构化错误：
// code=SKILL_UNAVAILABLE，context 携带 missing_tools/reason/path/scope/hint。
// writeError 会把 code 与 context 原样展开到响应里。
func unavailableSkillError(item skill.UnavailableSkill) *errors.RuntimeError {
	message := strings.TrimSpace(item.Message)
	if message == "" {
		message = fmt.Sprintf("skill %q is unavailable (reason: %s)", item.Name, item.Reason)
	}

	context := map[string]interface{}{
		"skill":         item.Name,
		"reason":        item.Reason,
		"missing_tools": item.MissingTools,
	}
	if item.Path != "" {
		context["path"] = item.Path
	}
	if item.Scope != "" {
		context["scope"] = item.Scope
	}
	if message != "" {
		context["hint"] = message
	}
	return errors.WrapWithContext(errors.ErrSkillUnavailable, message, nil, context)
}

// writeUnavailableSkillError 统一按名解析路径的降级响应：409 + 结构化引导，
// 而不是只报 "skill not found"。
func (h *Handler) writeUnavailableSkillError(w http.ResponseWriter, item skill.UnavailableSkill) {
	h.writeError(w, http.StatusConflict, unavailableSkillError(item))
}

// attachUnavailableSkills 把注册表的 unavailable 快照挂到 Codex 兼容 list
// 响应上。缓存命中路径也会调用，保证诊断信息实时。
func (h *Handler) attachUnavailableSkills(response *codexSkillsListResponse) {
	if h == nil || response == nil {
		return
	}
	response.Unavailable = unavailableSkillsForResponse(h.skillRegistry, "", "")
	if len(response.Unavailable) == 0 {
		response.Unavailable = nil
	}
	response.UnavailableCount = len(response.Unavailable)
}
