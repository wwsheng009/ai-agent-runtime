package skill

import (
	stderrors "errors"
	"fmt"
	"sort"
	"strings"

	runtimerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

const (
	// UnavailableReasonMissingTools 是"技能依赖的工具未在当前运行时 surface
	// 注册"这一不可用原因的稳定机器码。客户端应基于 reason 分支处理，而不是
	// 解析 message 文案。
	UnavailableReasonMissingTools = "missing_tools"

	// unavailableMissingToolsContextKey 是 Registry.validate 在工具缺失错误上
	// 挂载的结构化上下文键；loader 依赖它拿到完整的缺失工具列表。
	unavailableMissingToolsContextKey = "missing_tools"
)

// UnavailableSkill 描述一个因依赖不满足而未被注册、但需要保持可见性的技能。
// 它同时服务于列表/统计的 unavailable 分组与按名解析时的可操作错误。
//
// JSON 契约：name/path/scope/missing_tools/reason 为固定字段；message 是
// 面向人的恢复引导（缺哪些工具、如何启用/安装），可安全忽略。
type UnavailableSkill struct {
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Scope        string   `json:"scope"`
	MissingTools []string `json:"missing_tools"`
	Reason       string   `json:"reason"`
	Message      string   `json:"message,omitempty"`
}

// cloneUnavailableSkill 返回深拷贝，避免调用方修改注册表内部状态。
func cloneUnavailableSkill(item *UnavailableSkill) *UnavailableSkill {
	if item == nil {
		return nil
	}
	cloned := *item
	cloned.MissingTools = append([]string(nil), item.MissingTools...)
	return &cloned
}

// normalizeUnavailableSkill 规整记录：去空格、缺失工具去重排序、补齐
// reason 与 message。返回 nil 表示输入无效（无名称）。
func normalizeUnavailableSkill(item *UnavailableSkill) *UnavailableSkill {
	if item == nil {
		return nil
	}
	cloned := cloneUnavailableSkill(item)
	cloned.Name = strings.TrimSpace(cloned.Name)
	if cloned.Name == "" {
		return nil
	}
	cloned.Path = strings.TrimSpace(cloned.Path)
	cloned.Scope = strings.TrimSpace(cloned.Scope)
	if cloned.Reason == "" {
		cloned.Reason = UnavailableReasonMissingTools
	}
	cloned.MissingTools = normalizeUnavailableToolNames(cloned.MissingTools)
	if strings.TrimSpace(cloned.Message) == "" {
		cloned.Message = unavailableSkillMessage(cloned)
	}
	return cloned
}

func normalizeUnavailableToolNames(names []string) []string {
	normalized := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, name)
	}
	sort.Strings(normalized)
	return normalized
}

// unavailableSkillMessage 生成恢复引导文案。保持英文以与运行时其他
// 用户可见错误一致；结构化字段（missing_tools/reason）才是稳定契约。
func unavailableSkillMessage(item *UnavailableSkill) string {
	if item == nil {
		return ""
	}
	tools := strings.Join(item.MissingTools, ", ")
	if tools == "" {
		return fmt.Sprintf("skill %q is currently unavailable (reason: %s)", item.Name, item.Reason)
	}
	return fmt.Sprintf(
		"skill %q is unavailable because required tools are not available in the current runtime surface: %s. "+
			"Enable or install the missing tools (for example via MCP server configuration), reload skills, then retry.",
		item.Name, tools)
}

func unavailableSkillSourcePath(source *SkillSource) string {
	if source == nil {
		return ""
	}
	if path := strings.TrimSpace(source.Path); path != "" {
		return path
	}
	return strings.TrimSpace(source.Dir)
}

func unavailableSkillSourceScope(codex *CodexSkillMetadata, source *SkillSource) string {
	if codex != nil {
		if scope := strings.TrimSpace(codex.Scope); scope != "" {
			return scope
		}
	}
	if source != nil {
		if layer := strings.TrimSpace(source.Layer); layer != "" {
			return layer
		}
	}
	return SkillSourceLayerUnknown
}

// unavailableSkillFromSkill 从完整技能构建不可用记录。
func unavailableSkillFromSkill(s *Skill, missingTools []string) *UnavailableSkill {
	if s == nil {
		return nil
	}
	return normalizeUnavailableSkill(&UnavailableSkill{
		Name:         s.Name,
		Path:         unavailableSkillSourcePath(s.Source),
		Scope:        unavailableSkillSourceScope(s.Codex, s.Source),
		MissingTools: missingTools,
		Reason:       UnavailableReasonMissingTools,
	})
}

// unavailableSkillFromSummary 从 discovery 摘要构建不可用记录。
func unavailableSkillFromSummary(s *SkillSummary, missingTools []string) *UnavailableSkill {
	if s == nil {
		return nil
	}
	return unavailableSkillFromSkill(s.ToSkillStub(), missingTools)
}

// missingToolsFromError 从 Registry.validate 返回的工具缺失错误中提取
// 完整的 missing_tools 上下文。非工具缺失错误返回 nil。
func missingToolsFromError(err error) []string {
	if err == nil {
		return nil
	}
	var runtimeErr *runtimerrors.RuntimeError
	if !stderrors.As(err, &runtimeErr) || runtimeErr == nil {
		return nil
	}
	raw, ok := runtimeErr.GetContextValue(unavailableMissingToolsContextKey)
	if !ok {
		return nil
	}
	return normalizeUnavailableToolNames(coerceUnavailableToolNames(raw))
}

func coerceUnavailableToolNames(raw interface{}) []string {
	switch typed := raw.(type) {
	case []string:
		return typed
	case []interface{}:
		names := make([]string, 0, len(typed))
		for _, entry := range typed {
			if name, ok := entry.(string); ok {
				names = append(names, name)
			}
		}
		return names
	default:
		return nil
	}
}

// RecordUnavailable 记录（或覆盖）一个不可用技能。同名查找大小写不敏感，
// 后写入的记录覆盖先前的记录。
func (r *Registry) RecordUnavailable(item *UnavailableSkill) {
	if r == nil {
		return
	}
	normalized := normalizeUnavailableSkill(item)
	if normalized == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.storeUnavailableLocked(normalized)
}

func (r *Registry) storeUnavailableLocked(item *UnavailableSkill) {
	if r.unavailable == nil {
		r.unavailable = make(map[string]*UnavailableSkill)
	}
	r.unavailable[strings.ToLower(item.Name)] = cloneUnavailableSkill(item)
}

// UnavailableSkills 返回不可用技能快照，按名称（大小写不敏感）排序。
func (r *Registry) UnavailableSkills() []UnavailableSkill {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	items := make([]UnavailableSkill, 0, len(r.unavailable))
	for _, item := range r.unavailable {
		if item == nil {
			continue
		}
		items = append(items, *cloneUnavailableSkill(item))
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	return items
}

// LookupUnavailable 按技能名称查找不可用记录，大小写不敏感。
func (r *Registry) LookupUnavailable(name string) (UnavailableSkill, bool) {
	if r == nil {
		return UnavailableSkill{}, false
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return UnavailableSkill{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	item, ok := r.unavailable[key]
	if !ok || item == nil {
		return UnavailableSkill{}, false
	}
	return *cloneUnavailableSkill(item), true
}

// UnavailableCount 返回当前不可用技能数量。
func (r *Registry) UnavailableCount() int {
	if r == nil {
		return 0
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.unavailable)
}

// clearUnavailableLocked 在技能注册成功时移除同名不可用记录，使"已恢复可用"
// 的技能不再出现在 unavailable 分组中。调用方需持有写锁。
func (r *Registry) clearUnavailableLocked(name string) {
	if r == nil || r.unavailable == nil {
		return
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return
	}
	delete(r.unavailable, key)
}
