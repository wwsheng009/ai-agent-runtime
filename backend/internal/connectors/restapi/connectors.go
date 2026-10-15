package restapi

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	// ActiveOwner 是当前运行归属的宿主（空 = 未认领；含已陈旧记录，重启/接管
	// 后由下一次认领刷新）。字段 additive：旧客户端忽略即可（07 §5.6「冲突可见」）。
	ActiveOwner string `json:"active_owner,omitempty"`
	// OwnedByCurrentHost 报告归属是否属于本宿主（Deps.HostID）。
	OwnedByCurrentHost bool `json:"owned_by_current_host"`
	// CreatedAt/UpdatedAt 是配置生命周期时间戳（RFC3339 UTC；零值省略 = 旧数据
	// 未打戳）。additive：旧客户端忽略即可；列表/详情/创建响应统一携带。
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	// BotIdentity 是扫码通道的所有者稳定标识（微信 ilink_user_id，平台唯一
	// 注意：ilink_bot_id/account_id 每次扫码都会新建 bot 并解除旧连接，不稳
	// 定）。UI 唯一标记与重复扫码判定统一用它；未回填时省略。
	BotIdentity string `json:"bot_identity,omitempty"`
	// BotAccount 是当前持有的平台 bot 实例标识（微信 ilink_bot_id，随扫码变化）。
	BotAccount string `json:"bot_account,omitempty"`
	// Runtime 是运行可观测快照（additive，旧客户端忽略）：每连接器累计计数与
	// 最近错误；日志明细走 GET /connectors/{id}/logs。
	Runtime *RuntimeView `json:"runtime,omitempty"`
}

// RuntimeView 是连接器运行可观测视图（stats 与 GET /{id}/logs 载荷中的 stats 同构）。
type RuntimeView struct {
	Stats connectors.ConnectorStats `json:"stats"`
}

// PolicyView 是"用户操作逻辑"配置的对外视图。
type PolicyView struct {
	CommandPrefix string            `json:"command_prefix,omitempty"`
	AllowedUsers  []string          `json:"allowed_users,omitempty"`
	SessionMode   string            `json:"session_mode,omitempty"`
	ReplyMode     string            `json:"reply_mode,omitempty"`
	RuntimeOwner  string            `json:"runtime_owner,omitempty"`
	Route         *RoutePolicyView  `json:"route,omitempty"`
	Notify        *NotifyPolicyView `json:"notify,omitempty"`
	// ContentMirror 是 S1a 内容镜像开关（docs/bots/13 §8）：本地来源回合的
	// 终态正文作为副本投递到该 bot 的绑定 chats（缺省关）。
	ContentMirror bool `json:"content_mirror,omitempty"`
	// Events 是 S2 声明式事件订阅（docs/bots/13 §6.1；缺省回退 notify/content_mirror）。
	Events *EventsPolicyView `json:"events,omitempty"`
}

// RoutePolicyView 是 D6 连接器级模型路由的对外形状（docs/bots/09 §5.5）。
// 三项均可省略：省略项回落宿主全局默认。
type RoutePolicyView struct {
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// NotifyPolicyView 是 D5 事件通知接收配置的对外形状（docs/bots/09 §7.4.1）。
type NotifyPolicyView struct {
	Enabled bool     `json:"enabled"`
	Kinds   []string `json:"kinds,omitempty"`
	Scope   string   `json:"scope,omitempty"`
}

// EventsPolicyView 是 S2 声明式事件订阅的对外形状（docs/bots/13 §6.1）。
type EventsPolicyView struct {
	Enabled       bool                    `json:"enabled"`
	Subscriptions []EventSubscriptionView `json:"subscriptions,omitempty"`
}

// EventSubscriptionView 是一条订阅：会话选择器 × 事件类型 × 投递形态。
type EventSubscriptionView struct {
	ID string `json:"id,omitempty"`
	// Enabled 控制单条订阅启停（S4）：缺省（nil，省略）= 启用；false 显式停用。
	Enabled  *bool                `json:"enabled,omitempty"`
	Sessions *SessionSelectorView `json:"sessions,omitempty"`
	Types    []string             `json:"types,omitempty"`
	Deliver  string               `json:"deliver,omitempty"`
}

// SessionSelectorView 是订阅的会话范围（bound 缺省 | ids | all）。
type SessionSelectorView struct {
	Mode string   `json:"mode,omitempty"`
	IDs  []string `json:"ids,omitempty"`
}

// listResponse 是 GET /connectors 的载荷。
type listResponse struct {
	Items []ConnectorView `json:"items"`
}

// logsResponse 是 GET /connectors/{id}/logs 的载荷（entries 倒序，最新在前）。
type logsResponse struct {
	Entries []connectors.LogEntry     `json:"entries"`
	Stats   connectors.ConnectorStats `json:"stats"`
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
	// SkipCredentialCheck 跳过保存前的在线凭据校验（默认 false）。仅用于
	// 离线配置/平台暂不可达等必须强制落盘的场景（见 verifyCredentials）。
	SkipCredentialCheck bool `json:"skip_credential_check,omitempty"`
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

// logs 返回连接器最近日志与累计计数（管理面「可观测」入口，04 FR-12）。
//
// 语义：未知连接器 404（停用仍可查——日志恰是排查停用原因的入口）；limit 缺省 50、
// 数值越界收敛（<=0→50、>200→200）、非整数 400；entries 倒序（最新在前）。
func (h *handlers) logs(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	id := mux.Vars(r)["id"]
	if _, ok := h.deps.Store.GetConfig(id); !ok {
		writeError(w, http.StatusNotFound, "未知连接器")
		return
	}
	limit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "limit 必须是整数")
			return
		}
		limit = parsed
	}
	entries, ok := h.deps.Service.Logs(id, limit)
	if !ok {
		writeError(w, http.StatusNotFound, "未知连接器")
		return
	}
	stats, _ := h.deps.Service.Stats(id)
	if entries == nil {
		entries = []connectors.LogEntry{}
	}
	writeJSON(w, http.StatusOK, logsResponse{Entries: entries, Stats: stats})
}

// schemaDocument 返回全部已注册通道的 Schema（三端渲染器的唯一输入）。
func (h *handlers) schemaDocument(w http.ResponseWriter, _ *http.Request) {
	if !h.ready(w) {
		return
	}
	writeJSON(w, http.StatusOK, api.Schema{Version: SchemaVersion, Providers: h.deps.Service.Providers()})
}

// runnerPreferenceStore 断言宿主存储实现了全局 runner 偏好能力（D2）。
func (h *handlers) runnerPreferenceStore() connectors.RunnerPreferenceStore {
	if h == nil || h.deps.Store == nil {
		return nil
	}
	store, ok := h.deps.Store.(connectors.RunnerPreferenceStore)
	if !ok {
		return nil
	}
	return store
}

// getRunnerPreference 处理 GET /connectors/runner-preference：
// 返回 {runner_preference: "<规范词>"}；存储未实现能力接口 → 501。
func (h *handlers) getRunnerPreference(w http.ResponseWriter, _ *http.Request) {
	store := h.runnerPreferenceStore()
	if store == nil {
		writeError(w, http.StatusNotImplemented, "宿主存储未实现 runner 偏好能力")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"runner_preference": store.RunnerPreference()})
}

// putRunnerPreference 处理 PUT /connectors/runner-preference：
// body {runner_preference: "runtime-server|aicli|round-robin|"}；空串=清除。
func (h *handlers) putRunnerPreference(w http.ResponseWriter, r *http.Request) {
	store := h.runnerPreferenceStore()
	if store == nil {
		writeError(w, http.StatusNotImplemented, "宿主存储未实现 runner 偏好能力")
		return
	}
	var body struct {
		RunnerPreference string `json:"runner_preference"`
	}
	if !decodeBody(h.deps, w, r, &body) {
		return
	}
	// 显式写入 fail-fast：只接受规范词（大小写不敏感）或空串（清除）。
	// NormalizeRunnerPreference 的"未知值回落默认"是读取路径的配置容错，
	// 不适用于显式写入口（静默纠正拼写会掩盖用户意图）。
	trimmed := strings.ToLower(strings.TrimSpace(body.RunnerPreference))
	switch trimmed {
	case "":
		// 清除（回落默认）。
	case connectors.RunnerPreferenceRuntimeServer, connectors.RunnerPreferenceAICLI, connectors.RunnerPreferenceRoundRobin:
		// 合法。
	default:
		writeError(w, http.StatusBadRequest, "runner_preference 仅支持 runtime-server | aicli | round-robin（空串=清除）")
		return
	}
	if err := store.SetRunnerPreference(body.RunnerPreference); err != nil {
		writeError(w, http.StatusInternalServerError, "保存失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"runner_preference": store.RunnerPreference()})
}

// dispatchJSONRequest 是 POST /connectors/dispatch 的请求体（docs/bots/09
// §7.3 契约的 JSON 形状；字段名 snake_case 与管理面其余端点一致）。
type dispatchJSONRequest struct {
	ConnectorID string           `json:"connector_id"`
	Chat        string           `json:"chat"`
	Text        string           `json:"text"`
	UserID      string           `json:"user_id"`
	UserName    string           `json:"user_name"`
	MessageID   string           `json:"message_id"`
	Session     dispatchJSONSess `json:"session_ref"`
	Command     bool             `json:"is_command"`
}

type dispatchJSONSess struct {
	NodeID    string `json:"node_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// dispatch 处理 POST /connectors/dispatch（runner 侧接收面）：校验必填 →
// Runner 执行（prompt 注入或命令回环）→ 回终态 JSON。
// 幂等（message_id 二次校验）由 runner 侧的连接器 Service 处理（claim 在
// poller 已写，runner 侧重放由平台层兜底）；鉴权在管理面中间件（宿主内部
// 令牌/owner 会话）。
func (h *handlers) dispatch(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	if h.deps.Runner == nil {
		writeError(w, http.StatusNotImplemented, "本宿主未启用 connector runner 能力")
		return
	}
	var body dispatchJSONRequest
	if !decodeBody(h.deps, w, r, &body) {
		return
	}
	if strings.TrimSpace(body.ConnectorID) == "" || strings.TrimSpace(body.Chat) == "" || strings.TrimSpace(body.Text) == "" {
		writeError(w, http.StatusBadRequest, "connector_id / chat / text 必填")
		return
	}
	result, err := h.deps.Runner.DispatchTurn(r.Context(), connectors.DispatchRequest{
		ConnectorID: body.ConnectorID,
		Chat:        body.Chat,
		Text:        body.Text,
		UserID:      body.UserID,
		UserName:    body.UserName,
		MessageID:   body.MessageID,
		// Target.NodeID 由宿主 HTTP 层路由保证（请求只会打到被选中的节点），
		// 这里不重复校验；会话亲和由 Session 携带。
		Target:  connectors.RunnerPeer{NodeID: body.Session.NodeID},
		Session: connectors.SessionRef{NodeID: body.Session.NodeID, SessionID: body.Session.SessionID},
		Command: body.Command,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "派发执行失败："+err.Error())
		return
	}
	resp := struct {
		Reply   []connectors.OutboundMessage `json:"reply"`
		Session dispatchJSONSess             `json:"session"`
		Success bool                         `json:"success"`
		Error   string                       `json:"error,omitempty"`
	}{
		Reply:   result.Reply,
		Success: true,
		Session: dispatchJSONSess{NodeID: result.Session.NodeID, SessionID: result.Session.SessionID},
	}
	writeJSON(w, http.StatusOK, resp)
}

// attentionInlet 处理 POST /connectors/attention（P2 切片 D，D5 跨宿主扇出
// 接收面，docs/bots/09 §7.4.1）：其它节点的注意力广播经此进入本宿主。
// 发起侧已按「对端有 bot 想收广播」过滤；这里再做**本地 D5 过滤**
// （逐 bot AcceptsNotice：绑定会话的 approval/question 始终收、completion 随开关；
// 其它会话需 enabled+kinds+scope 命中），命中者经 scoped 出站到该 bot 绑定
// （NotifyAttentionScoped，2026-10-14 起与本地链路同语义）。AttentionInlet 未
// 装配（gateway 纯 poller 无出站 bot）→ 501。
func (h *handlers) attentionInlet(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	if h.deps.AttentionInlet == nil {
		writeError(w, http.StatusNotImplemented, "本宿主未启用注意力接收")
		return
	}
	var body struct {
		Kind    string `json:"kind"`
		Session struct {
			NodeID    string `json:"node_id,omitempty"`
			SessionID string `json:"session_id"`
		} `json:"session"`
		Text string `json:"text"`
	}
	if !decodeBody(h.deps, w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Session.SessionID) == "" || strings.TrimSpace(body.Text) == "" {
		writeError(w, http.StatusBadRequest, "session_ref.session_id 与 text 必填")
		return
	}
	notice := connectors.AttentionNotice{
		Kind:    strings.TrimSpace(body.Kind),
		Session: connectors.SessionRef{NodeID: body.Session.NodeID, SessionID: body.Session.SessionID},
		Text:    body.Text,
	}
	if err := h.deps.AttentionInlet(notice); err != nil {
		writeError(w, http.StatusBadGateway, "注意力接收失败："+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
		ID:        id,
		Provider:  schema.Provider,
		Name:      firstNonEmpty(request.Name, id),
		Enabled:   enabled,
		Mode:      firstNonEmpty(request.Mode, merged["mode"], schemaDefault(schema, "mode")),
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
		Settings:  settings,
		Secrets:   secrets,
		Policy:    policyFrom(request.Policy),
	}
	// 保存前凭据校验（2026-10-15 加固）：实现 CredentialVerifier 的通道在
	// 落盘前做真实校验，拒绝「保存成功但凭据无效」的静默失败（qqbot 真机复盘）。
	if !request.SkipCredentialCheck {
		if verifyErr := h.verifyCredentials(r.Context(), provider, cfg); verifyErr != nil {
			writeError(w, http.StatusBadRequest, credentialCheckFailedMessage(verifyErr))
			return
		}
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
	// 时间戳：CreatedAt 保持创建值不动；UpdatedAt 每次更新刷新（幂等补戳：
	// 旧数据首次 PATCH 时会同时补上 CreatedAt）。空更新也刷新 UpdatedAt——
	// 语义即「配置被触碰过」，与文件 mtime 一致。
	if existing.CreatedAt.IsZero() {
		updated.CreatedAt = time.Now().UTC()
	}
	updated.UpdatedAt = time.Now().UTC()
	if request.Enabled != nil {
		updated.Enabled = *request.Enabled
	}
	if request.Policy != nil {
		updated.Policy = policyFrom(request.Policy)
	}
	// 保存前凭据校验：仅在凭据相关字段变化时发起在线校验（普通配置微调不
	// 依赖平台可用性）；失败不落盘（见 verifyCredentials/credentialsChanged）。
	// credentialsReplaced 同时决定保存后的应用路径：凭据替换走"立即生效"
	//（必要时自动接管），普通微调走重载。
	credentialsReplaced := credentialsChanged(schema, current, merged)
	if !request.SkipCredentialCheck && credentialsReplaced {
		if verifyErr := h.verifyCredentials(r.Context(), provider, updated); verifyErr != nil {
			writeError(w, http.StatusBadRequest, credentialCheckFailedMessage(verifyErr))
			return
		}
	}
	// 重复扫码收敛（三宿主共用语义，docs/bots/03 §6）：本次 PATCH 带入了
	// owner_user_id（微信 ilink_user_id，平台唯一）且与另一个既有连接器
	// （同 provider）的 owner_user_id 相同 = 同一用户重新扫码（微信会新建
	// bot 并解除旧连接）→ 凭据/设置合并进既有连接器，本次 PATCH 的目标
	// （通常是自动创建的壳）被删除；响应回既有连接器视图并带 adopted=true
	// 与 superseded_account（被解除的旧 ilink_bot_id）。
	if strings.TrimSpace(updated.BotIdentity()) != "" {
		if existingSame, found := connectors.FindSameBotConfig(h.deps.Store.ListConfigs(), updated); found {
			superseded := existingSame.BotAccount()
			merged := existingSame
			merged.Settings = updated.Settings
			merged.Secrets = updated.Secrets
			merged.Enabled = updated.Enabled
			merged.UpdatedAt = time.Now().UTC()
			if err := h.deps.Store.PutConfig(merged); err != nil {
				writeError(w, http.StatusInternalServerError, "保存失败："+err.Error())
				return
			}
			h.deps.Service.StopConnector(updated.ID)
			if err := h.deps.Store.DeleteConfig(updated.ID); err != nil {
				writeError(w, http.StatusInternalServerError, "清理重复连接器失败："+err.Error())
				return
			}
			h.applySavedConnector(r.Context(), merged.ID, credentialsReplaced)
			view := h.view(merged)
			writeJSON(w, http.StatusOK, adoptResponse{
				ConnectorView:     view,
				Adopted:           true,
				RemovedID:         updated.ID,
				SupersededAccount: superseded,
			})
			return
		}
	}
	if err := h.deps.Store.PutConfig(updated); err != nil {
		writeError(w, http.StatusInternalServerError, "保存失败："+err.Error())
		return
	}
	h.applySavedConnector(r.Context(), updated.ID, credentialsReplaced)
	writeJSON(w, http.StatusOK, h.view(updated))
}

// applySavedConnector 在保存落盘后应用变更：
//   - 凭据被替换（重新扫码 / 密钥轮换）→ Service.ApplyCredentialReplacement：
//     本宿主非持有者时自动接管，保证「扫完码立即生效」，不再出现旧 poller
//     抱着失效 token 空转、页面只留一条「未运行（由 X 持有）」的静默失联
//     （2026-10-15 微信真机复盘）；
//   - 普通配置微调 → ReloadConnector：非持有者保持只读，持有者会在心跳周期内
//     自应用（Service.applyConfigChange）。
func (h *handlers) applySavedConnector(ctx context.Context, id string, credentialsReplaced bool) {
	if h.deps.Service == nil {
		return
	}
	if credentialsReplaced {
		if err := h.deps.Service.ApplyCredentialReplacement(ctx, id); err != nil {
			h.deps.logf("connectors: apply credential replacement %s failed: %v", id, err)
		}
		return
	}
	if err := h.deps.Service.ReloadConnector(ctx, id); err != nil {
		h.deps.logf("connectors: reload %s failed: %v", id, err)
	}
}

// adoptResponse 是 PATCH 命中「重复扫码 → 替换既有机器人」时的响应：
// ConnectorView 是收敛后的既有连接器；Adopted=true 告知客户端发生了替换；
// RemovedID 是被清理的壳连接器 id（客户端据此从本地列表移除）；
// SupersededAccount 是被新扫码**解除**的旧平台连接（微信旧 ilink_bot_id——
// 微信在扫码时会提示解除原连接，旧凭据已随覆写失效）。
type adoptResponse struct {
	ConnectorView
	Adopted           bool   `json:"adopted"`
	RemovedID         string `json:"removed_id"`
	SupersededAccount string `json:"superseded_account,omitempty"`
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

// takeover 强制抢占运行归属（管理面「抢占」按钮，07 §5.6）：以本宿主身份覆盖
// 旧归属并重启轮询；对端宿主的心跳观察到归属丢失后自行停止（尽力围栏）。
// 错误语义：未配置 HostID → 503；未知连接器 → 404；停用或策略指定其它宿主 → 409；
// 其余存储失败 → 500。成功后返回更新后的连接器视图。
func (h *handlers) takeover(w http.ResponseWriter, r *http.Request) {
	if !h.ready(w) {
		return
	}
	id := mux.Vars(r)["id"]
	hostID := strings.TrimSpace(h.deps.HostID)
	if hostID == "" {
		writeError(w, http.StatusServiceUnavailable, "本宿主未配置运行归属身份（HostID），无法接管")
		return
	}
	if _, ok := h.deps.Store.GetConfig(id); !ok {
		writeError(w, http.StatusNotFound, "未知连接器")
		return
	}
	err := h.deps.Service.TakeoverConnector(r.Context(), id)
	switch {
	case err == nil:
		updated, ok := h.deps.Store.GetConfig(id)
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "active_owner": hostID, "owned_by_current_host": true})
			return
		}
		writeJSON(w, http.StatusOK, h.view(updated))
	case errors.Is(err, connectors.ErrHostIDRequired):
		writeError(w, http.StatusServiceUnavailable, "本宿主未配置运行归属身份（HostID），无法接管")
	case errors.Is(err, connectors.ErrConnectorDisabled):
		writeError(w, http.StatusConflict, "接管失败：连接器已停用，请先启用")
	case errors.Is(err, connectors.ErrOwnerPolicyMismatch):
		writeError(w, http.StatusConflict, "接管失败：policy.runtime_owner 指定了其它宿主，请先修改策略")
	case errors.Is(err, connectors.ErrServiceNotConfigured):
		writeError(w, http.StatusServiceUnavailable, "连接器服务未接线")
	default:
		writeError(w, http.StatusInternalServerError, "接管失败："+err.Error())
	}
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
		ID:          cfg.ID,
		Provider:    cfg.Provider,
		Name:        cfg.Name,
		Enabled:     cfg.Enabled,
		Mode:        cfg.Mode,
		Policy:      policyViewFrom(cfg.Policy),
		CreatedAt:   formatViewTime(cfg.CreatedAt),
		UpdatedAt:   formatViewTime(cfg.UpdatedAt),
		BotIdentity: botIdentityFrom(cfg),
		BotAccount:  botAccountFrom(cfg),
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
	if owner, ok := h.deps.Store.OwnerOf(cfg.ID); ok {
		view.ActiveOwner = strings.TrimSpace(owner.Owner)
		hostID := strings.TrimSpace(h.deps.HostID)
		view.OwnedByCurrentHost = hostID != "" && strings.EqualFold(view.ActiveOwner, hostID)
	}
	if h.deps.Service != nil {
		if stats, ok := h.deps.Service.Stats(cfg.ID); ok {
			view.Runtime = &RuntimeView{Stats: stats}
		}
	}
	return view
}

// formatViewTime 把零值省略、非零转 RFC3339 UTC。
func formatViewTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// botIdentityFrom / botAccountFrom 直接读 Config 的稳定标识助手（判定键
// 与取值统一在 connectors 包，这里只是视图映射）。
func botIdentityFrom(cfg connectors.Config) string {
	return cfg.BotIdentity()
}

func botAccountFrom(cfg connectors.Config) string {
	return cfg.BotAccount()
}

func policyViewFrom(policy connectors.Policy) PolicyView {
	view := PolicyView{
		CommandPrefix: policy.CommandPrefix,
		AllowedUsers:  append([]string(nil), policy.AllowedUsers...),
		SessionMode:   policy.SessionMode,
		ReplyMode:     policy.ReplyMode,
		RuntimeOwner:  policy.RuntimeOwner,
		ContentMirror: policy.ContentMirror,
	}
	if policy.Notify.Enabled {
		view.Notify = &NotifyPolicyView{
			Enabled: true,
			Kinds:   append([]string(nil), policy.Notify.Kinds...),
			Scope:   policy.Notify.Scope,
		}
	}
	if policy.Route.HasOverride() {
		view.Route = &RoutePolicyView{
			Provider:        strings.TrimSpace(policy.Route.Provider),
			Model:           strings.TrimSpace(policy.Route.Model),
			ReasoningEffort: strings.TrimSpace(policy.Route.ReasoningEffort),
		}
	}
	if policy.Events.Enabled && len(policy.Events.Subscriptions) > 0 {
		events := &EventsPolicyView{Enabled: true, Subscriptions: make([]EventSubscriptionView, 0, len(policy.Events.Subscriptions))}
		for _, sub := range policy.Events.Subscriptions {
			item := EventSubscriptionView{
				ID:      strings.TrimSpace(sub.ID),
				Enabled: sub.Enabled,
				Types:   append([]string(nil), sub.Types...),
				Deliver: strings.TrimSpace(sub.Deliver),
			}
			if mode := strings.TrimSpace(sub.Sessions.Mode); mode != "" || len(sub.Sessions.IDs) > 0 {
				item.Sessions = &SessionSelectorView{Mode: mode, IDs: append([]string(nil), sub.Sessions.IDs...)}
			}
			events.Subscriptions = append(events.Subscriptions, item)
		}
		view.Events = events
	}
	return view
}

func policyFrom(view *PolicyView) connectors.Policy {
	if view == nil {
		return connectors.Policy{}
	}
	policy := connectors.Policy{
		CommandPrefix: view.CommandPrefix,
		AllowedUsers:  append([]string(nil), view.AllowedUsers...),
		SessionMode:   view.SessionMode,
		ReplyMode:     view.ReplyMode,
		RuntimeOwner:  view.RuntimeOwner,
		ContentMirror: view.ContentMirror,
	}
	if view.Notify != nil && view.Notify.Enabled {
		policy.Notify = connectors.NotifyPolicy{
			Enabled: true,
			Kinds:   append([]string(nil), view.Notify.Kinds...),
			Scope:   strings.TrimSpace(view.Notify.Scope),
		}
	}
	if view.Route != nil {
		policy.Route = connectors.RoutePolicy{
			Provider:        strings.TrimSpace(view.Route.Provider),
			Model:           strings.TrimSpace(view.Route.Model),
			ReasoningEffort: strings.TrimSpace(view.Route.ReasoningEffort),
		}
	}
	if view.Events != nil && view.Events.Enabled {
		policy.Events.Enabled = true
		for _, sub := range view.Events.Subscriptions {
			item := connectors.EventSubscription{
				ID:      strings.TrimSpace(sub.ID),
				Enabled: sub.Enabled,
				Types:   append([]string(nil), sub.Types...),
				Deliver: strings.TrimSpace(sub.Deliver),
			}
			if sub.Sessions != nil {
				item.Sessions = connectors.SessionSelector{
					Mode: strings.TrimSpace(sub.Sessions.Mode),
					IDs:  append([]string(nil), sub.Sessions.IDs...),
				}
			}
			policy.Events.Subscriptions = append(policy.Events.Subscriptions, item)
		}
	}
	return policy
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

// credentialCheckTimeout 是保存前在线校验的超时上限（平台调用 + 网络抖动留裕量）。
const credentialCheckTimeout = 15 * time.Second

// verifyCredentials 对实现了 CredentialVerifier 的 Provider 做保存前的在线
// 凭据校验（2026-10-15 真机加固：qqbot 曾把无效 app_secret 静默落盘，重启后
// 才以「订阅投递失败」暴露）。未实现该能力的通道直接通过。
func (h *handlers) verifyCredentials(ctx context.Context, provider connectors.Provider, cfg connectors.Config) error {
	verifier, ok := provider.(connectors.CredentialVerifier)
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, credentialCheckTimeout)
	defer cancel()
	return verifier.VerifyCredentials(ctx, cfg)
}

// credentialCheckFailedMessage 组合保存前校验失败的响应文案（含显式跳过开关
// 提示：平台不可达/离线配置场景允许强制保存，避免把可用性问题升级为无法落盘）。
func credentialCheckFailedMessage(err error) string {
	return "凭据校验失败：" + err.Error() + "（如需跳过在线校验强制保存，可附加 skip_credential_check=true 重试）"
}

// credentialsChanged 报告本次提交是否需要做保存前的在线凭据校验：任一 secret
// 字段或 required 字段（如 QQ 的 app_id——非 secret 但属于凭据对的一部分）
// 变为非空新值。普通配置微调（名称/策略/主动消息配额等）不校验；显式清空
// （`__clear__`/去掉键）视为有意失效也不校验（与既有「更新允许清空 required
// 密钥」语义一致），避免把平台可用性问题升级为无法保存。
func credentialsChanged(schema api.ProviderSchema, before, after map[string]string) bool {
	changed := make(map[string]struct{})
	for key, value := range after {
		if before[key] != value {
			changed[key] = struct{}{}
		}
	}
	for key, value := range before {
		if _, present := after[key]; !present && value != "" {
			changed[key] = struct{}{}
		}
	}
	if len(changed) == 0 {
		return false
	}
	for _, field := range schema.Fields {
		if _, ok := changed[field.Key]; !ok {
			continue
		}
		if !field.IsSecret() && !field.Required {
			continue
		}
		if strings.TrimSpace(after[field.Key]) == "" {
			continue
		}
		return true
	}
	return false
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
