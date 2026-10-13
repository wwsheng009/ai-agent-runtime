package restapi

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ConnectorView 是连接器对外视图（密钥只回字段名，不回明文）。
type ConnectorView struct {
	ID         string            `json:"id"`
	Provider   string            `json:"provider"`
	Name       string            `json:"name"`
	Enabled    bool              `json:"enabled"`
	Mode       string            `json:"mode"`
	Settings   map[string]string `json:"settings,omitempty"`
	SecretKeys []string          `json:"secret_keys,omitempty"`
	Policy     PolicyView        `json:"policy"`
}

// PolicyView 是"用户操作逻辑"配置的对外视图。
type PolicyView struct {
	CommandPrefix string   `json:"command_prefix,omitempty"`
	AllowedUsers  []string `json:"allowed_users,omitempty"`
	SessionMode   string   `json:"session_mode,omitempty"`
	ReplyMode     string   `json:"reply_mode,omitempty"`
	RuntimeOwner  string   `json:"runtime_owner,omitempty"`
}

// listResponse 是 GET /connectors 的载荷。
type listResponse struct {
	Items []ConnectorView `json:"items"`
}

// submitRequest 是新建/更新的提交体：
//   - settings/secrets 按 Schema 合并（留空=保持，`__clear__`=清除）；
//   - enabled 缺省时新建为 true、更新为保持不变。
type submitRequest struct {
	ID       string            `json:"id"`
	Provider string            `json:"provider"`
	Name     string            `json:"name"`
	Enabled  *bool             `json:"enabled"`
	Mode     string            `json:"mode"`
	Settings map[string]string `json:"settings"`
	Secrets  map[string]string `json:"secrets"`
	Policy   *PolicyView       `json:"policy"`
}

func (h *handlers) list(w http.ResponseWriter, _ *http.Request) {
	if !h.ready(w) {
		return
	}
	configs := h.deps.Store.ListConfigs()
	items := make([]ConnectorView, 0, len(configs))
	for _, cfg := range configs {
		items = append(items, h.view(cfg))
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items})
}

func (h *handlers) get(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	cfg, ok := h.deps.Store.GetConfig(mux.Vars(r)["id"])
	if !ok {
		writeError(w, http.StatusNotFound, "未知连接器")
		return
	}
	writeJSON(w, http.StatusOK, h.view(cfg))
}

// schemaDocument 返回全部已注册通道的 Schema（三端渲染器的唯一输入）。
func (h *handlers) schemaDocument(w http.ResponseWriter, _ *http.Request) {
	if !h.ready(w) {
		return
	}
	writeJSON(w, http.StatusOK, api.Schema{Version: SchemaVersion, Providers: h.deps.Service.Providers()})
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	var request submitRequest
	if !decodeBody(h.deps, w, r, &request) {
		return
	}
	id := strings.TrimSpace(request.ID)
	if !idPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, "id 必填，且只能包含字母、数字、._-（最长 64）")
		return
	}
	if _, exists := h.deps.Store.GetConfig(id); exists {
		writeError(w, http.StatusConflict, "连接器已存在："+id)
		return
	}
	provider, ok := h.deps.Service.Provider(strings.TrimSpace(request.Provider))
	if !ok {
		writeError(w, http.StatusBadRequest, "未知通道："+request.Provider)
		return
	}
	schema := provider.Schema()

	merged := api.MergeSubmit(schema, nil, combined(request.Settings, request.Secrets))
	if errs := api.Validate(schema, merged, nil); len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "配置校验失败", "fields": errs})
		return
	}
	settings, secrets := splitValues(schema, merged)

	enabled := true
	if request.Enabled != nil {
		enabled = *request.Enabled
	}
	cfg := connectors.Config{
		ID:       id,
		Provider: schema.Provider,
		Name:     firstNonEmpty(request.Name, id),
		Enabled:  enabled,
		Mode:     firstNonEmpty(request.Mode, merged["mode"], schemaDefault(schema, "mode")),
		Settings: settings,
		Secrets:  secrets,
		Policy:   policyFrom(request.Policy),
	}
	if err := h.deps.Store.PutConfig(cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "保存失败："+err.Error())
		return
	}
	if enabled {
		if err := h.deps.Service.ReloadConnector(r.Context(), cfg.ID); err != nil {
			h.deps.logf("connectors: reload %s failed: %v", cfg.ID, err)
		}
	}
	writeJSON(w, http.StatusCreated, h.view(cfg))
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	id := mux.Vars(r)["id"]
	existing, ok := h.deps.Store.GetConfig(id)
	if !ok {
		writeError(w, http.StatusNotFound, "未知连接器")
		return
	}
	var request submitRequest
	if !decodeBody(h.deps, w, r, &request) {
		return
	}
	if provider := strings.TrimSpace(request.Provider); provider != "" && provider != existing.Provider {
		writeError(w, http.StatusBadRequest, "provider 不可变更；如需换通道请新建连接器")
		return
	}
	provider, ok := h.deps.Service.Provider(existing.Provider)
	if !ok {
		writeError(w, http.StatusBadRequest, "通道未注册："+existing.Provider)
		return
	}
	schema := provider.Schema()

	current := combined(existing.Settings, existing.Secrets)
	merged := api.MergeSubmit(schema, current, combined(request.Settings, request.Secrets))
	if errs := api.Validate(schema, merged, current); len(errs) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "配置校验失败", "fields": errs})
		return
	}
	settings, secrets := splitValues(schema, merged)

	updated := existing
	updated.Name = firstNonEmpty(request.Name, existing.Name)
	updated.Mode = firstNonEmpty(request.Mode, existing.Mode, merged["mode"], schemaDefault(schema, "mode"))
	updated.Settings = settings
	updated.Secrets = secrets
	if request.Enabled != nil {
		updated.Enabled = *request.Enabled
	}
	if request.Policy != nil {
		updated.Policy = policyFrom(request.Policy)
	}
	if err := h.deps.Store.PutConfig(updated); err != nil {
		writeError(w, http.StatusInternalServerError, "保存失败："+err.Error())
		return
	}
	if err := h.deps.Service.ReloadConnector(r.Context(), updated.ID); err != nil {
		h.deps.logf("connectors: reload %s failed: %v", updated.ID, err)
	}
	writeJSON(w, http.StatusOK, h.view(updated))
}

func (h *handlers) remove(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	id := mux.Vars(r)["id"]
	if _, ok := h.deps.Store.GetConfig(id); !ok {
		writeError(w, http.StatusNotFound, "未知连接器")
		return
	}
	h.deps.Service.StopConnector(id)
	if err := h.deps.Store.DeleteConfig(id); err != nil {
		writeError(w, http.StatusInternalServerError, "删除失败："+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reload 应用配置变更（重启轮询/重连）。
func (h *handlers) reload(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	id := mux.Vars(r)["id"]
	if err := h.deps.Service.ReloadConnector(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

// test 向指定 IM 会话发送测试消息（控制台「发送测试消息」按钮）。
func (h *handlers) test(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	id := mux.Vars(r)["id"]
	if _, ok := h.deps.Store.GetConfig(id); !ok {
		writeError(w, http.StatusNotFound, "未知连接器")
		return
	}
	var request struct {
		Chat string `json:"chat"`
		Text string `json:"text"`
	}
	if !decodeBody(h.deps, w, r, &request) {
		return
	}
	chat := strings.TrimSpace(request.Chat)
	if chat == "" {
		writeError(w, http.StatusBadRequest, "chat 必填（用于测试消息的目标会话）")
		return
	}
	text := firstNonEmpty(request.Text, "这是一条来自控制台的测试消息。")
	if err := h.deps.Service.SendOutbound(r.Context(), id, chat, []connectors.OutboundMessage{{Text: text}}); err != nil {
		writeError(w, http.StatusBadGateway, "发送失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ── 视图/合并助手 ──────────────────────────────────────────────────────────

func (h *handlers) view(cfg connectors.Config) ConnectorView {
	view := ConnectorView{
		ID:       cfg.ID,
		Provider: cfg.Provider,
		Name:     cfg.Name,
		Enabled:  cfg.Enabled,
		Mode:     cfg.Mode,
		Policy:   policyViewFrom(cfg.Policy),
	}
	if len(cfg.Settings) > 0 {
		view.Settings = make(map[string]string, len(cfg.Settings))
		for key, value := range cfg.Settings {
			view.Settings[key] = value
		}
	}
	if provider, ok := h.deps.Service.Provider(cfg.Provider); ok {
		schema := provider.Schema()
		view.SecretKeys = api.SortedSecretKeys(schema, cfg.Secrets)
		if view.Mode == "" {
			view.Mode = firstNonEmpty(cfg.Setting("mode", ""), schemaDefault(schema, "mode"))
		}
	}
	return view
}

func policyViewFrom(policy connectors.Policy) PolicyView {
	return PolicyView{
		CommandPrefix: policy.CommandPrefix,
		AllowedUsers:  append([]string(nil), policy.AllowedUsers...),
		SessionMode:   policy.SessionMode,
		ReplyMode:     policy.ReplyMode,
		RuntimeOwner:  policy.RuntimeOwner,
	}
}

func policyFrom(view *PolicyView) connectors.Policy {
	if view == nil {
		return connectors.Policy{}
	}
	return connectors.Policy{
		CommandPrefix: view.CommandPrefix,
		AllowedUsers:  append([]string(nil), view.AllowedUsers...),
		SessionMode:   view.SessionMode,
		ReplyMode:     view.ReplyMode,
		RuntimeOwner:  view.RuntimeOwner,
	}
}

// combined 把 settings 与 secrets 合成 Schema 的单一命名空间（MergeSubmit 的输入）。
func combined(settings, secrets map[string]string) map[string]string {
	size := len(settings) + len(secrets)
	if size == 0 {
		return nil
	}
	merged := make(map[string]string, size)
	for key, value := range settings {
		merged[key] = value
	}
	for key, value := range secrets {
		merged[key] = value
	}
	return merged
}

// splitValues 按 Schema 把合并值拆回 settings/secrets（未知键归入 settings，向前兼容）。
func splitValues(schema api.ProviderSchema, merged map[string]string) (map[string]string, map[string]string) {
	secretKeys := make(map[string]struct{})
	for _, field := range schema.Fields {
		if field.IsSecret() {
			secretKeys[field.Key] = struct{}{}
		}
	}
	settings := make(map[string]string)
	secrets := make(map[string]string)
	for key, value := range merged {
		if _, isSecret := secretKeys[key]; isSecret {
			secrets[key] = value
			continue
		}
		settings[key] = value
	}
	return settings, secrets
}

func schemaDefault(schema api.ProviderSchema, key string) string {
	for _, field := range schema.Fields {
		if field.Key == key {
			return field.Default
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
