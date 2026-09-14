package commands

import (
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
)

// ChatWebAPISkillsPath 微型 Web 客户端 skills 端点前缀。
// 子路径：无（当前会话的 skill 目录）、/{name}（单个 skill 详情）。
const ChatWebAPISkillsPath = "/web/api/skills"

// chatWebSkillSummary 是列表条目的对外形状：字段直接取自 capability.Descriptor，
// 后端没有的字段不出现（omitempty），前端按实际字段渲染、不补默认值。
type chatWebSkillSummary struct {
	Name         string   `json:"name"`
	FunctionName string   `json:"function_name,omitempty"`
	Kind         string   `json:"kind,omitempty"`
	Description  string   `json:"description,omitempty"`
	Category     string   `json:"category,omitempty"`
	Version      string   `json:"version,omitempty"`
	Labels       []string `json:"labels,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// chatWebSkillDetail 在列表字段之外附带描述符的其余信息（触发词、依赖、来源、
// 元数据），供详情面板展示；描述符没有的字段同样省略。
type chatWebSkillDetail struct {
	chatWebSkillSummary
	Triggers     []capability.Trigger    `json:"triggers,omitempty"`
	Dependencies []capability.Dependency `json:"dependencies,omitempty"`
	Source       *capability.Source      `json:"source,omitempty"`
	Metadata     map[string]interface{}  `json:"metadata,omitempty"`
}

// HandleChatWebAPISkills 提供当前 aicli chat 会话的 skill catalog：
//
//	GET /web/api/skills        → {"count":N,"skills":[...]}
//	GET /web/api/skills/{name} → 单个 skill 详情（name 可用目录名或可调用名）
//
// 数据源与 TUI `/skills` 完全一致（session.FunctionCatalog 中的 skill 描述符），
// 因此页面目录与命令输出不会出现两套口径；无活动会话 / catalog 未初始化时
// 返回稳定错误码 skills_unavailable（503），未知 skill 返回 skill_not_found（404）。
func HandleChatWebAPISkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, skillsErrorBody("method_not_allowed", "only GET is supported"))
		return
	}
	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, skillsErrorBody("skills_unavailable", "chat session not ready"))
		return
	}
	catalog := ensureFunctionCatalog(session)
	report := buildFunctionCatalogReport(catalog)
	if report == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, skillsErrorBody("skills_unavailable", "function catalog not initialized"))
		return
	}

	name, nested := skillsSubPathName(r.URL.Path)
	if nested {
		// 子路径含多段不是合法 skill 名：明确 404，避免静默退化成列表响应。
		writeWebAPIJSON(w, http.StatusNotFound, skillsErrorBody("skill_not_found", "skill not found: "+strings.Trim(strings.TrimPrefix(r.URL.Path, ChatWebAPISkillsPath), "/")))
		return
	}
	if name == "" {
		items := make([]chatWebSkillSummary, 0, len(report.Skills))
		for _, entry := range report.Skills {
			items = append(items, chatWebSkillSummaryOf(entry))
		}
		writeWebAPIJSON(w, http.StatusOK, map[string]interface{}{
			"count":  len(items),
			"skills": items,
		})
		return
	}

	for _, entry := range report.Skills {
		if skillEntryMatchesName(entry, name) {
			writeWebAPIJSON(w, http.StatusOK, chatWebSkillDetailOf(entry))
			return
		}
	}
	writeWebAPIJSON(w, http.StatusNotFound, skillsErrorBody("skill_not_found", "skill not found: "+name))
}

// skillsSubPathName 取出 {prefix}/{name} 中的 name。
// name 为空且 nested=false 表示列表请求；nested=true 表示子路径含多段（非法名称）。
func skillsSubPathName(requestPath string) (name string, nested bool) {
	rest := strings.TrimPrefix(requestPath, ChatWebAPISkillsPath)
	rest = strings.Trim(rest, "/")
	if rest == "" {
		return "", false
	}
	if strings.Contains(rest, "/") {
		return "", true
	}
	return rest, false
}

// skillEntryMatchesName 按目录名（Descriptor.Name）或可调用名（function_name）
// 匹配，二者都是列表对外暴露过的标识。
func skillEntryMatchesName(entry aicliFunctionDescriptorReport, name string) bool {
	if strings.EqualFold(strings.TrimSpace(entry.FunctionName), name) {
		return true
	}
	if desc := entry.Descriptor; desc != nil {
		return strings.EqualFold(strings.TrimSpace(desc.Name), name)
	}
	return false
}

func chatWebSkillSummaryOf(entry aicliFunctionDescriptorReport) chatWebSkillSummary {
	item := chatWebSkillSummary{FunctionName: strings.TrimSpace(entry.FunctionName)}
	if desc := entry.Descriptor; desc != nil {
		item.Name = strings.TrimSpace(desc.Name)
		item.Kind = string(desc.Kind)
		item.Description = desc.Description
		item.Category = desc.Category
		item.Version = desc.Version
		item.Labels = desc.Labels
		item.Capabilities = desc.Capabilities
	}
	if item.Name == "" {
		// 描述符缺 Name 时退回可调用名，保证条目始终有可展示/可点击的标识。
		item.Name = item.FunctionName
	}
	return item
}

func chatWebSkillDetailOf(entry aicliFunctionDescriptorReport) chatWebSkillDetail {
	detail := chatWebSkillDetail{chatWebSkillSummary: chatWebSkillSummaryOf(entry)}
	if desc := entry.Descriptor; desc != nil {
		detail.Triggers = desc.Triggers
		detail.Dependencies = desc.Dependencies
		detail.Source = desc.Source
		detail.Metadata = desc.Metadata
	}
	return detail
}

// skillsErrorBody 与其它 web 端点一致的稳定错误 envelope。
func skillsErrorBody(code, message string) map[string]interface{} {
	return map[string]interface{}{
		"error": map[string]interface{}{
			"code":    code,
			"message": message,
		},
	}
}
