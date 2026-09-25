package commands

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
)

// ChatWebAPISkillsPath 微型 Web 客户端 skills 端点前缀。
// 子路径：无（当前会话的 skill 目录）、/{name}（单个 skill 详情）。
const ChatWebAPISkillsPath = "/web/api/skills"

// webSkillToggleBodyLimit 限制启停请求体大小：该端点的入参只有一个布尔，
// 不需要为超大 body 分配内存。
const webSkillToggleBodyLimit = 8 << 10

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
	// Disabled 标记"已停用"的 skill（skills_runtime.disabled_skills）：
	// 它没有可调用函数，只出现在列表/详情里供页面开关启用。
	Disabled bool `json:"disabled,omitempty"`
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

// chatWebSkillToggleRequest 是 POST /web/api/skills/{name} 的请求体：
// enabled 显式给出目标状态；toggle=true 时按当前状态取反（二者取其一）。
type chatWebSkillToggleRequest struct {
	Enabled *bool `json:"enabled"`
	Toggle  bool  `json:"toggle"`
}

// HandleChatWebAPISkills 提供当前 aicli chat 会话的 skill catalog 与启停：
//
//	GET  /web/api/skills        → {"count":N,"skills":[...]}
//	GET  /web/api/skills/{name} → 单个 skill 详情（name 可用目录名或可调用名）
//	POST /web/api/skills/{name} → 启停该 skill（{"enabled":bool} 或 {"toggle":true}），
//	                              返回新状态 + 刷新后的列表（一次往返即可重绘页面）
//
// 数据源与 TUI `/skills` 完全一致（session.FunctionCatalog 中的 skill 描述符），
// 启停复用 TUI 的同一条链路（写配置 + 运行面热刷新），因此页面、命令与 API
// 不会出现两套口径；无活动会话 / catalog 未初始化时返回稳定错误码
// skills_unavailable（503），未知 skill 返回 skill_not_found（404）。
func HandleChatWebAPISkills(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		handleChatWebSkillsGet(w, r)
	case http.MethodPost:
		handleChatWebSkillsToggle(w, r)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, skillsErrorBody("method_not_allowed", "only GET and POST are supported"))
	}
}

func handleChatWebSkillsGet(w http.ResponseWriter, r *http.Request) {
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
		items := buildChatWebSkillSummaries(session, report)
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
	// 已停用的 skill 不在函数面里，但详情要能打开：页面在详情里提供启用开关。
	if canonical, ok := skillDisabledNameLookup(session, name); ok {
		writeWebAPIJSON(w, http.StatusOK, chatWebSkillDetail{
			chatWebSkillSummary: chatWebSkillSummary{Name: canonical, Disabled: true, Description: "已停用"},
		})
		return
	}
	writeWebAPIJSON(w, http.StatusNotFound, skillsErrorBody("skill_not_found", "skill not found: "+name))
}

// handleChatWebSkillsToggle 把页面的启停开关接到与 TUI 相同的写入链路：
// 落盘失败、没有可写配置路径都会明确报错，不做只在内存里生效的假开关。
func handleChatWebSkillsToggle(w http.ResponseWriter, r *http.Request) {
	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusServiceUnavailable, skillsErrorBody("skills_unavailable", "chat session not ready"))
		return
	}

	name, nested := skillsSubPathName(r.URL.Path)
	if nested {
		writeWebAPIJSON(w, http.StatusNotFound, skillsErrorBody("skill_not_found", "skill not found"))
		return
	}
	if name == "" {
		w.Header().Set("Allow", http.MethodGet)
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, skillsErrorBody("method_not_allowed", "POST requires /web/api/skills/{name}"))
		return
	}

	var payload chatWebSkillToggleRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, webSkillToggleBodyLimit))
	if err := decoder.Decode(&payload); err != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, skillsErrorBody("invalid_request", "invalid JSON body: "+err.Error()))
		return
	}

	canonical := name
	currentlyDisabled := false
	if resolved, ok := skillDisabledNameLookup(session, name); ok {
		canonical = resolved
		currentlyDisabled = true
	}
	enabled := !currentlyDisabled
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}
	if payload.Toggle {
		enabled = currentlyDisabled
	}

	// 目标状态必须与当前状态相反，否则请求本身自相矛盾：明确 404 而不是
	// 返回一个什么都没做的 200（页面据此重绘会得到误导性的状态）。
	if enabled {
		if !currentlyDisabled {
			writeWebAPIJSON(w, http.StatusNotFound, skillsErrorBody("skill_not_disabled", "skill is not disabled: "+name))
			return
		}
	} else if currentlyDisabled {
		writeWebAPIJSON(w, http.StatusNotFound, skillsErrorBody("skill_already_disabled", "skill is already disabled: "+name))
		return
	} else if _, found := chatSessionSkillName(session, name); !found {
		writeWebAPIJSON(w, http.StatusNotFound, skillsErrorBody("skill_not_found", "skill not found: "+name))
		return
	}

	message, err := runSkillToggleCommand(session, enabled, canonical)
	if err != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, skillsErrorBody("skill_toggle_failed", err.Error()))
		return
	}

	_, stillDisabled := skillDisabledNameLookup(session, name)
	body := map[string]interface{}{
		"name":    canonical,
		"enabled": !stillDisabled,
		"message": message,
	}
	// 顺带回传刷新后的目录：页面一次往返就能重绘（与 GET 列表同形状）。
	if catalog := ensureFunctionCatalog(session); catalog != nil {
		if report := buildFunctionCatalogReport(catalog); report != nil {
			items := buildChatWebSkillSummaries(session, report)
			body["count"] = len(items)
			body["skills"] = items
		}
	}
	writeWebAPIJSON(w, http.StatusOK, body)
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

// skillDisabledNameLookup 在会话生效配置的停用名单里按名字（大小写不敏感）
// 查找，返回名单里的原始写法。
func skillDisabledNameLookup(session *ChatSession, name string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return "", false
	}
	resolved, ok := skillDisabledNameSet(session)[key]
	if !ok {
		return "", false
	}
	return resolved, true
}

// buildChatWebSkillSummaries 组装列表条目：函数面里的 skill 之后补上停用中的
// skill（后者没有可调用函数，只供页面在详情里启用），顺序与 TUI 选择器一致。
func buildChatWebSkillSummaries(session *ChatSession, report *aicliFunctionCatalogReport) []chatWebSkillSummary {
	if report == nil {
		return nil
	}
	entries := report.Skills
	if session != nil {
		entries = buildSkillPickerCatalogEntries(session, report.Skills)
	}
	items := make([]chatWebSkillSummary, 0, len(entries))
	for _, entry := range entries {
		item := chatWebSkillSummaryOf(entry)
		item.Disabled = entry.Disabled
		if entry.Disabled && item.Description == "" {
			item.Description = "已停用"
		}
		items = append(items, item)
	}
	return items
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
