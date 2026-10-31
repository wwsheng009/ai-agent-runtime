package main

// notify_im.go 是网关自持 bot（IM 通道）的配置层（docs/gateway/18 §6）：
// <data-dir>/gateway-im.json 是唯一写入口，PUT 校验失败即返回 400 且不落盘、
// 不改变生效配置。
//
// 模型（与控制台「Bot 通道」目标列表一一对应）：
//
//	{
//	  "version": 1,
//	  "targets": [
//	    {"id":"ops-main","connector_id":"qq-ops","chat":"123456789",
//	     "label":"运维群","scope":"all","ignore_presence":true}
//	  ]
//	}
//
// 语义（§5.3/§5.5）：scope=all/nodes/sessions 决定 target 命中条件；全局/范围
// target 默认 ignore_presence=true——运维告警不应被"有人正在看控制台"抑制，
// 否则 owner 打开控制台时恰好静音运维群。会话绑定（scope=sessions）沿用 push
// 语义。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify/channels"
)

const (
	// notifyIMVersion 是 gateway-im.json 的格式版本。
	notifyIMVersion = 1
	// notifyIMTargetLimit 是全局 target 数量上限（§6.3 = 32）。它与
	// channels.IM 的并发/超时预算共同满足 ceil(N/C)*T <= 30s（§5.6）。
	notifyIMTargetLimit = 32
)

// notifyIMTarget 是 gateway-im.json 里的单个收件人。
type notifyIMTarget struct {
	ID          string `json:"id"`
	ConnectorID string `json:"connector_id"`
	Chat        string `json:"chat"`
	Label       string `json:"label,omitempty"`
	// Source 区分投递来源：""/"gateway"（网关自持 bot）或 "node"（委托节点 bot）。
	Source string `json:"source,omitempty"`
	// NodeID 是 source=node 的目标节点（必填）。
	NodeID string `json:"node_id,omitempty"`
	// Kinds 限定该 target 接收的事件类型；node target 缺省 error/waiting
	// （节点本地 hook 已发 approval/question/completion，避免双发）。
	Kinds []string `json:"kinds,omitempty"`
	// Scope 命中条件：all（缺省）/nodes/sessions。未知值校验期即拒绝，
	// 避免配置漂移导致投递被静默丢弃。
	Scope    string   `json:"scope,omitempty"`
	Nodes    []string `json:"nodes,omitempty"`
	Sessions []string `json:"sessions,omitempty"`
	// IgnorePresence 缺省按 scope 推导：all/nodes/sessions 的全局 target 默认
	// true（§5.5），显式给出则以显式值为准。
	IgnorePresence *bool `json:"ignore_presence,omitempty"`
	// Limit 是该 provider 的纯文本上限（0 = channels 侧默认 2000）。
	Limit int `json:"limit,omitempty"`
	// Enabled 缺省 true（指针区分"未设置"与"显式关闭"）。
	Enabled *bool `json:"enabled,omitempty"`
}

// normalizeNotifyIMSource 归一化投递来源；未知值原样返回以便校验报错。
func normalizeNotifyIMSource(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "gateway":
		return "gateway"
	case "node":
		return "node"
	default:
		return strings.ToLower(strings.TrimSpace(raw))
	}
}

// notifyIMKinds 是网关事件词表（notify.PriorityOf / 投影同源）。
var notifyIMKinds = map[string]struct{}{
	"approval": {}, "question": {}, "completed": {}, "error": {}, "waiting": {},
}

func notifyIMKindAllowed(kind string) bool {
	_, ok := notifyIMKinds[strings.ToLower(strings.TrimSpace(kind))]
	return ok
}

func (t notifyIMTarget) enabled() bool { return t.Enabled == nil || *t.Enabled }

// notifyIMIgnoresPresence 解析 ignore_presence 的生效值。
func (t notifyIMTarget) ignoresPresence() bool {
	if t.IgnorePresence != nil {
		return *t.IgnorePresence
	}
	return true // §5.5：全局/范围 target 默认无视在场门
}

// notifyIMFile 是 <data-dir>/gateway-im.json 的磁盘形状。
type notifyIMFile struct {
	Version int              `json:"version,omitempty"`
	Targets []notifyIMTarget `json:"targets,omitempty"`
}

// notifyIMValidationError 区分 400（配置非法）与 500（写盘失败）。
type notifyIMValidationError struct{ msg string }

func (e *notifyIMValidationError) Error() string { return e.msg }

func isNotifyIMValidationError(err error) bool {
	var target *notifyIMValidationError
	return errors.As(err, &target)
}

// validateNotifyIM 校验配置：id 唯一、必填字段非空、scope 合法且与
// nodes/sessions 匹配、target 数不超过上限。
func validateNotifyIM(file notifyIMFile) error {
	if len(file.Targets) > notifyIMTargetLimit {
		return &notifyIMValidationError{fmt.Sprintf("too many targets: %d (limit %d)", len(file.Targets), notifyIMTargetLimit)}
	}
	seen := make(map[string]struct{}, len(file.Targets))
	for i, t := range file.Targets {
		id := strings.TrimSpace(t.ID)
		if id == "" {
			return &notifyIMValidationError{fmt.Sprintf("targets[%d]: id is required", i)}
		}
		if _, dup := seen[id]; dup {
			return &notifyIMValidationError{fmt.Sprintf("targets[%d]: duplicate id %q", i, id)}
		}
		seen[id] = struct{}{}
		source := normalizeNotifyIMSource(t.Source)
		switch source {
		case "gateway":
			if strings.TrimSpace(t.ConnectorID) == "" {
				return &notifyIMValidationError{fmt.Sprintf("targets[%d] (%s): connector_id is required", i, id)}
			}
			if strings.TrimSpace(t.Chat) == "" {
				return &notifyIMValidationError{fmt.Sprintf("targets[%d] (%s): chat is required", i, id)}
			}
		case "node":
			if strings.TrimSpace(t.NodeID) == "" {
				return &notifyIMValidationError{fmt.Sprintf("targets[%d] (%s): node_id is required for source=node", i, id)}
			}
		default:
			return &notifyIMValidationError{fmt.Sprintf("targets[%d] (%s): unknown source %q (want gateway|node)", i, id, t.Source)}
		}
		for _, kind := range t.Kinds {
			if !notifyIMKindAllowed(kind) {
				return &notifyIMValidationError{fmt.Sprintf("targets[%d] (%s): unknown kind %q (want approval|question|completed|error|waiting)", i, id, kind)}
			}
		}
		switch strings.TrimSpace(t.Scope) {
		case "", "all":
		case "nodes":
			// source=node 的 node_id 本身就是节点范围，不再要求 nodes 明细。
			if source != "node" && len(t.Nodes) == 0 {
				return &notifyIMValidationError{fmt.Sprintf("targets[%d] (%s): scope=nodes requires nodes", i, id)}
			}
		case "sessions":
			if len(t.Sessions) == 0 {
				return &notifyIMValidationError{fmt.Sprintf("targets[%d] (%s): scope=sessions requires sessions", i, id)}
			}
		default:
			return &notifyIMValidationError{fmt.Sprintf("targets[%d] (%s): unknown scope %q (want all|nodes|sessions)", i, id, t.Scope)}
		}
		if t.Limit < 0 {
			return &notifyIMValidationError{fmt.Sprintf("targets[%d] (%s): limit must not be negative", i, id)}
		}
	}
	return nil
}

// compiledTargets 把配置编译为 channels.IMTarget（仅启用项）。
func (f notifyIMFile) compiledTargets() []channels.IMTarget {
	out := make([]channels.IMTarget, 0, len(f.Targets))
	for _, t := range f.Targets {
		if !t.enabled() {
			continue
		}
		source := normalizeNotifyIMSource(t.Source)
		scope := strings.TrimSpace(t.Scope)
		if scope == "" {
			scope = "all"
		}
		kinds := trimAll(t.Kinds)
		if source == "node" && len(kinds) == 0 {
			// 节点本地 hook 已发 approval/question/completion；委托只补缺口，
			// 避免同一 chat 双发（docs/gateway/18 §1.3）。
			kinds = []string{"error", "waiting"}
		}
		out = append(out, channels.IMTarget{
			ID:             strings.TrimSpace(t.ID),
			ConnectorID:    strings.TrimSpace(t.ConnectorID),
			Chat:           strings.TrimSpace(t.Chat),
			Label:          strings.TrimSpace(t.Label),
			Source:         source,
			NodeID:         strings.TrimSpace(t.NodeID),
			Kinds:          kinds,
			Scope:          scope,
			Nodes:          trimAll(t.Nodes),
			Sessions:       trimAll(t.Sessions),
			IgnorePresence: t.ignoresPresence(),
			Limit:          t.Limit,
		})
	}
	return out
}

func trimAll(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// notifyIMSource 管理 gateway-im.json 的内存视图与热加载。
type notifyIMSource struct {
	mu      sync.RWMutex
	path    string
	view    notifyIMFile
	stamp   string
	lastErr string
	// sendTest 由装配注入（生产 = connectors.Service.SendOutbound 包装）；
	// nil 时测试接口返回 501，而不是伪装成功。
	sendTest func(ctx context.Context, connectorID, chat, text string) error
	// sendNodeAttention 由装配注入（生产 = 隧道 POST /api/connectors/attention）；
	// nil 时节点委托目标不可用。
	sendNodeAttention func(ctx context.Context, nodeID string, notice connectors.AttentionNotice) error
}

// newNotifyIMSource 构造配置源并首次装载（文件缺失 = 未配置 bot，不是错误）。
func newNotifyIMSource(path string) *notifyIMSource {
	s := &notifyIMSource{path: path}
	s.mu.Lock()
	_ = s.reloadLocked()
	s.mu.Unlock()
	return s
}

// Path 返回配置文件路径（GET 元数据用）。
func (s *notifyIMSource) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// SetSendTest 注入测试消息发送器（装配期调用）。
func (s *notifyIMSource) SetSendTest(fn func(ctx context.Context, connectorID, chat, text string) error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.sendTest = fn
	s.mu.Unlock()
}

// SetSendNodeAttention 注入节点委托发送器（装配期调用）。
func (s *notifyIMSource) SetSendNodeAttention(fn func(ctx context.Context, nodeID string, notice connectors.AttentionNotice) error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.sendNodeAttention = fn
	s.mu.Unlock()
}

// reloadLocked 按文件戳增量重读；只在戳变化时真正解析。
func (s *notifyIMSource) reloadLocked() error {
	stamp := "missing"
	if info, err := os.Stat(s.path); err == nil {
		stamp = fmt.Sprintf("file:%d:%d", info.ModTime().UnixNano(), info.Size())
	} else if !os.IsNotExist(err) {
		s.lastErr = fmt.Sprintf("stat %s: %v", s.path, err)
		return errors.New(s.lastErr)
	}
	if stamp == s.stamp {
		return nil
	}
	s.stamp = stamp
	if stamp == "missing" {
		s.view, s.lastErr = notifyIMFile{Version: notifyIMVersion}, ""
		return nil
	}
	raw, err := os.ReadFile(s.path)
	if err != nil {
		s.lastErr = fmt.Sprintf("read %s: %v", s.path, err)
		return errors.New(s.lastErr)
	}
	var view notifyIMFile
	if err := json.Unmarshal(raw, &view); err != nil {
		s.lastErr = fmt.Sprintf("parse %s: %v", s.path, err)
		return errors.New(s.lastErr)
	}
	if err := validateNotifyIM(view); err != nil {
		s.lastErr = err.Error()
		return err
	}
	if view.Version == 0 {
		view.Version = notifyIMVersion
	}
	s.view, s.lastErr = view, ""
	return nil
}

// ReloadIfChanged 是 observe 前调用的热加载入口：stat 一次，变化才重读。
func (s *notifyIMSource) ReloadIfChanged() {
	if s == nil {
		return
	}
	s.mu.Lock()
	_ = s.reloadLocked()
	s.mu.Unlock()
}

// Current 返回当前配置副本与最近一次装载错误。
func (s *notifyIMSource) Current() (notifyIMFile, string) {
	if s == nil {
		return notifyIMFile{Version: notifyIMVersion}, ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.view, s.lastErr
}

// Targets 返回编译后的启用 target（通道装配与在场判定用）。
func (s *notifyIMSource) Targets() []channels.IMTarget {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	view := s.view
	s.mu.RUnlock()
	return view.compiledTargets()
}

// TargetCount 返回启用 target 数（唤醒门/主通道选择用；0 = 未配置 bot）。
func (s *notifyIMSource) TargetCount() int {
	return len(s.Targets())
}

// LocalSuppression 返回该节点应关闭的本地自动通知规则集。
//
// 只由 source=node 的 target 推导：这类 target 不新增通道实例，而是把事件
// 委托给节点自己的 bot。节点本地 SubscribeAttention 若同时发送，同一
// (通道实例, chat, 事件) 就会投两次。规则按 target 的 scope/kinds 精确
// 生成；source=gateway 的 target 不属于节点的通道实例，不参与抑制，因此
// 多通道 fan-out 不受影响。
//
// 返回空表示节点本地行为不变。target 热加载后由下一次心跳/注册确认生效。
func (s *notifyIMSource) LocalSuppression(nodeID string) []wire.NotifySuppressRule {
	if s == nil {
		return nil
	}
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil
	}
	// 心跳也会调用本函数：外部直接编辑 gateway-im.json 时，先补一次热加载，
	// 避免规则落后于配置（PUT 路径已即时更新内存视图）。
	s.ReloadIfChanged()
	targets := s.Targets()
	rules := make([]wire.NotifySuppressRule, 0, len(targets))
	for _, t := range targets {
		if !strings.EqualFold(strings.TrimSpace(t.Source), "node") {
			continue
		}
		// 节点委托只处理属于该节点的事件；空 node_id 在配置校验期已拒绝。
		if !strings.EqualFold(strings.TrimSpace(t.NodeID), nodeID) {
			continue
		}
		kinds := append([]string(nil), t.Kinds...)
		if len(kinds) == 0 {
			// 与 channels.IM.acceptsKind 的 node target 缺省一致：只补
			// error/waiting，节点本地本来就不发这两类，抑制集为空也可。
			kinds = []string{"error", "waiting"}
		}
		var sessions []string
		if strings.TrimSpace(t.Scope) == "sessions" {
			sessions = append([]string(nil), t.Sessions...)
		}
		rules = append(rules, wire.NotifySuppressRule{
			Kinds:    kinds,
			Sessions: sessions,
		})
	}
	return rules
}

// SendTest 发送测试消息（nil 发送器 = 未接线）。
func (s *notifyIMSource) SendTest(ctx context.Context, connectorID, chat, text string) error {
	if s == nil {
		return errors.New("im source is not configured")
	}
	s.mu.RLock()
	fn := s.sendTest
	s.mu.RUnlock()
	if fn == nil {
		return errNotifyIMTestUnavailable
	}
	return fn(ctx, connectorID, chat, text)
}

// SendNodeTest 发送一条节点委托测试（nil 发送器 = 未接线）。
func (s *notifyIMSource) SendNodeTest(ctx context.Context, nodeID string, notice connectors.AttentionNotice) error {
	if s == nil {
		return errors.New("im source is not configured")
	}
	s.mu.RLock()
	fn := s.sendNodeAttention
	s.mu.RUnlock()
	if fn == nil {
		return errNotifyIMTestUnavailable
	}
	return fn(ctx, nodeID, notice)
}

var errNotifyIMTestUnavailable = errors.New("test send is not wired (connectors unavailable)")

// Apply 校验并落盘新配置，成功后热替换内存视图（失败不改生效配置）。
// expectVersion 非 nil 时做 CAS 校验（§6.3 竞态处置），版本不符返回 409。
func (s *notifyIMSource) Apply(view notifyIMFile, expectVersion *int) error {
	if s == nil {
		return errors.New("im source is not configured")
	}
	if err := validateNotifyIM(view); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectVersion != nil && view.Version != 0 && *expectVersion != s.view.Version {
		return errNotifyIMVersionConflict
	}
	next := view
	next.Version = s.view.Version + 1
	if next.Version == 0 || next.Version < notifyIMVersion {
		next.Version = notifyIMVersion
	}
	if err := writeNotifyIMFile(s.path, next); err != nil {
		return err
	}
	s.view, s.lastErr = next, ""
	s.stamp = "" // 强制下次 stat 重新取戳
	return nil
}

var errNotifyIMVersionConflict = errors.New("config changed since it was read; reload and retry")

// writeNotifyIMFile 原子写盘（与 notify-policy.json 同口径：tmp + rename，
// Windows 不允许 rename 覆盖时先删目标再重试）。
func writeNotifyIMFile(path string, view notifyIMFile) error {
	raw, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(path)
		if err2 := os.Rename(tmp, path); err2 != nil {
			_ = os.Remove(tmp)
			return err2
		}
	}
	return nil
}

// notifyIMMeta 是 GET 响应的元数据：控制台据此渲染目标列表与测试按钮，
// 不在前端硬编码 provider 词表。
type notifyIMMeta struct {
	Path        string   `json:"path"`
	TargetLimit int      `json:"target_limit"`
	Channels    []string `json:"channels"`
	Testable    bool     `json:"testable"`
	Sources     []string `json:"sources,omitempty"`
	Kinds       []string `json:"kinds,omitempty"`
}

// handleNotifyIM 暴露 owner 会话保护的 Bot 通道配置 API：
//
//	GET  /api/notify/im       → {"config": {...}, "meta": {...}}
//	PUT  /api/notify/im       ← {"version":1,"targets":[...]}
//	POST /api/notify/im/test  ← {"target_id":"ops-main"}
//
// PUT 校验失败返回 400 且不落盘；版本冲突返回 409。onSaved 在成功后触发
// （让 pushNotifier 立即重建通道面）。
func handleNotifyIM(source *notifyIMSource, meta func() notifyIMMeta, onSaved func(wasEnabled bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			view, warning := source.Current()
			resp := map[string]any{"config": view, "meta": buildNotifyIMMeta(source, meta)}
			if warning != "" {
				resp["warning"] = warning
			}
			writeJSON(w, http.StatusOK, resp)
		case http.MethodPut:
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			var view notifyIMFile
			if err := json.NewDecoder(r.Body).Decode(&view); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid config body: " + err.Error()})
				return
			}
			// 保存前记录"是否存在启用 target"：只有该布尔翻转时才需要重建通道面
			// （primary 与回退链在 ensureDispatcher 内决定），单纯的 target 内容
			// 变化由 TargetsFn 即时生效，无需重建。
			wasEnabled := source.TargetCount() > 0
			if err := source.Apply(view, nil); err != nil {
				status := http.StatusInternalServerError
				if isNotifyIMValidationError(err) {
					status = http.StatusBadRequest
				} else if errors.Is(err, errNotifyIMVersionConflict) {
					status = http.StatusConflict
				}
				writeJSON(w, status, map[string]any{"error": err.Error()})
				return
			}
			if onSaved != nil {
				onSaved(wasEnabled)
			}
			saved, warning := source.Current()
			resp := map[string]any{"config": saved, "meta": buildNotifyIMMeta(source, meta)}
			if warning != "" {
				resp["warning"] = warning
			}
			writeJSON(w, http.StatusOK, resp)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// handleNotifyIMTest 向指定 target 发送一条测试消息（最小 UI 的"测试"按钮）。
// 未接线连接器服务时返回 501，绝不伪装成功。
func handleNotifyIMTest(source *notifyIMSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		var body struct {
			TargetID string `json:"target_id"`
			// Target 是可选的完整 target 定义：UI 在“尚未保存”的草稿上点测试时
			// 直接带上它，避免只查已落盘配置导致 target not found。
			Target *notifyIMTarget `json:"target"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid body: " + err.Error()})
			return
		}
		var hit *notifyIMTarget
		if body.Target != nil {
			candidate := *body.Target
			if strings.TrimSpace(candidate.ID) == "" {
				candidate.ID = strings.TrimSpace(body.TargetID)
			}
			if err := validateNotifyIM(notifyIMFile{Targets: []notifyIMTarget{candidate}}); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			hit = &candidate
		} else {
			view, _ := source.Current()
			for i := range view.Targets {
				if view.Targets[i].ID == strings.TrimSpace(body.TargetID) {
					hit = &view.Targets[i]
					break
				}
			}
		}
		if hit == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "target not found: " + body.TargetID})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		text := fmt.Sprintf("网关通知测试 · %s（%s）", strings.TrimSpace(hit.Label), time.Now().Format("15:04:05"))
		var err error
		if normalizeNotifyIMSource(hit.Source) == "node" {
			sessionID := ""
			if len(hit.Sessions) > 0 {
				sessionID = strings.TrimSpace(hit.Sessions[0])
			}
			if sessionID == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "节点目标测试需要 scope=sessions 且至少一个会话（用于让节点反查绑定）"})
				return
			}
			err = source.SendNodeTest(ctx, strings.TrimSpace(hit.NodeID), connectors.AttentionNotice{
				Kind:    "error",
				Session: connectors.SessionRef{NodeID: strings.TrimSpace(hit.NodeID), SessionID: sessionID},
				Text:    text,
			})
		} else {
			err = source.SendTest(ctx, hit.ConnectorID, hit.Chat, text)
		}
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, errNotifyIMTestUnavailable) {
				status = http.StatusNotImplemented
			}
			writeJSON(w, status, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "target_id": hit.ID})
	}
}

func buildNotifyIMMeta(source *notifyIMSource, meta func() notifyIMMeta) notifyIMMeta {
	out := notifyIMMeta{
		TargetLimit: notifyIMTargetLimit,
		Sources:     []string{"gateway", "node"},
		Kinds:       []string{"approval", "question", "completed", "error", "waiting"},
	}
	if meta != nil {
		out = meta()
		if out.TargetLimit <= 0 {
			out.TargetLimit = notifyIMTargetLimit
		}
	}
	if len(out.Sources) == 0 {
		out.Sources = []string{"gateway", "node"}
	}
	if len(out.Kinds) == 0 {
		out.Kinds = []string{"approval", "question", "completed", "error", "waiting"}
	}
	if source != nil {
		if out.Path == "" {
			out.Path = source.Path()
		}
		source.mu.RLock()
		out.Testable = source.sendTest != nil || source.sendNodeAttention != nil
		source.mu.RUnlock()
	}
	return out
}
