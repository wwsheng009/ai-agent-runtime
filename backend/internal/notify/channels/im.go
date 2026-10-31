package channels

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
)

// IM 是连接器 IM 出站通道的适配器（docs/bots/06-target-architecture.md §3
// 「IM 作为 notify Channel」）：按 kind+会话 映射 AttentionNotice，收集接受
// 连接器（D5 过滤）后经 NotifyScoped 出站——与 gateway 投影 hook 同口径
// （同一实现从 connector_bootstrap 内联逻辑收敛到本适配器）。
//
// docs/gateway/18 §5.1 起，本适配器同时承载网关自持 bot 的**全局/范围
// target**（Targets/SendOutbound）。两条路径共存但语义不同：
//
//   - 会话绑定路径：Store + AcceptsNotice 决定收件人，按会话寻址（既有）。
//   - 全局 target 路径：配置里显式声明的 (connector_id, chat)，按 scope 命中。
//
// 两者都命中时按 connector_id|chat 去重，同一事件只发一次。
type IM struct {
	// Store 用于 D5 逐连接器过滤（AcceptsNotice）与接受集合收集；nil = 纯测试
	// 口径（不查绑定，全量投递）。
	Store connectors.Store
	// NotifyScoped 是连接器侧 scoped 出站（生产实现 Service.NotifyAttentionScoped）。
	NotifyScoped func(ctx context.Context, connectorIDs []string, notice connectors.AttentionNotice) error

	// Targets 是网关自持 bot 的全局/范围收件人（docs/gateway/18 §5.3）。
	// 空 = 未配置 bot，行为与既有一致（无收件人时静默跳过，不返回
	// ErrNoRecipient）。
	Targets []IMTarget
	// TargetsFn 是 Targets 的动态来源（生产装配用）：配置热加载后即时生效，
	// 无需重建 Dispatcher（重建会丢账本去重与合并状态）。非空时优先于
	// Targets。
	TargetsFn func() []IMTarget
	// SendOutbound 是全局 target 的出站（按 connector_id 发到指定 chat）。
	// 生产实现为 connectors.Service.SendOutbound 的按目标包装。
	SendOutbound func(ctx context.Context, connectorID, chat string, msgs []connectors.OutboundMessage) error
	// SendNodeAttention 是“节点委托”出站（docs/gateway/18 复用方案）：把事件
	// 交给目标节点，由节点自己的 bot 按本地绑定发送。生产实现为网关隧道
	// POST /api/connectors/attention。nil = 未接线。
	SendNodeAttention func(ctx context.Context, nodeID string, notice connectors.AttentionNotice) error
	// DeliveryLedger 是通道级交付账本（跨事件/重启）：Claim=false 表示
	// (channel|chat|event) 已投递；发送失败必须 Release 以便重试。
	DeliveryLedger DeliveryLedger
}

// DeliveryLedger 是通道级去重账本的最小接口（gateway 实现持久化版本）。
type DeliveryLedger interface {
	Claim(key string) bool
	Release(key string)
}

// targets 返回当前生效的全局/范围 target（TargetsFn 优先）。
func (c *IM) targets() []IMTarget {
	if c == nil {
		return nil
	}
	if c.TargetsFn != nil {
		return c.TargetsFn()
	}
	return c.Targets
}

// sessionOf 解析事件归属的会话 id：优先 notify.Event.Target（通道专用寻址
// 提示），回退 Event.Session。两者通常相同，但宿主只填 Session 时（当前
// cmd/gateway 的 Dispatch 调用即如此）必须有回退，否则会话绑定路径与
// scope=sessions 目标都会静默失效。
func sessionOf(ev notify.Event) string {
	if id := strings.TrimSpace(ev.Target); id != "" {
		return id
	}
	return strings.TrimSpace(ev.Session)
}

// 全局 target 的投递预算（docs/gateway/18 §5.6）。三者必须满足
// ceil(N/C)*T <= dispatchBudget，以不等式为准而非默认值。
const (
	// defaultTextLimit 是 provider 未声明上限时的纯文本长度（§5.7）。
	defaultTextLimit = 2000
	// maxTargetConcurrency 是全局 target 的并发上限。
	maxTargetConcurrency = 4
	// perTargetTimeout 是单个 target 的发送超时。
	perTargetTimeout = 5 * time.Second
	// dispatchBudget 是整体投递预算（与 Dispatcher 的 30s ctx 对齐）。
	dispatchBudget = 30 * time.Second
)

// IMTarget 是网关自持 bot 的一个全局/范围收件人（docs/gateway/18 §5.3）。
// Scope 决定命中条件：all=全部、nodes=按节点、sessions=按会话。
type IMTarget struct {
	ID          string
	ConnectorID string
	Chat        string
	Label       string
	// Source 区分投递来源：""/"gateway" = 网关自持 bot；"node" = 委托节点 bot。
	Source string
	// NodeID 是 source=node 的目标节点。
	NodeID string
	// Kinds 限定该 target 接收的事件 kind（空 = 全部；node target 缺省
	// error/waiting，避免与节点本地 hook 已发的 approval/question/completion 重复）。
	Kinds    []string
	Scope    string
	Nodes    []string
	Sessions []string
	// IgnorePresence 标记该 target 是否无视在场门（全局运维告警应为 true，
	// 见 docs/gateway/18 §5.5）。在场门在生产侧判定，本字段仅随配置下发。
	IgnorePresence bool
	// Limit 是该 provider 的纯文本长度上限（0 = defaultTextLimit）。
	Limit int
}

// Matches 报告 target 是否覆盖该事件（按 scope 判定；未知 scope 不命中，
// 避免配置漂移导致误发）。
func (t IMTarget) Matches(node, session string) bool {
	if t.isNode() {
		// 节点委托只处理属于该节点的事件，避免把 A 节点会话推给 B 节点。
		if nid := strings.TrimSpace(t.NodeID); nid != "" && !strings.EqualFold(nid, strings.TrimSpace(node)) {
			return false
		}
		switch strings.TrimSpace(t.Scope) {
		case "", "all", "nodes":
			return true
		case "sessions":
			return imContains(t.Sessions, session)
		default:
			return false
		}
	}
	switch strings.TrimSpace(t.Scope) {
	case "", "all":
		return true
	case "nodes":
		return imContains(t.Nodes, node)
	case "sessions":
		return imContains(t.Sessions, session)
	default:
		return false
	}
}

// MatchesEvent 报告 target 是否覆盖指定 node/session/kind；供 gateway 的
// 在场门豁免判定使用（与 Deliver 的受理判定同源）。
func (t IMTarget) MatchesEvent(node, session, kind string) bool {
	return t.acceptsKind(kind) && t.Matches(node, session)
}

// isNode 报告 target 是否走“委托节点 bot”路径。
func (t IMTarget) isNode() bool {
	return strings.EqualFold(strings.TrimSpace(t.Source), "node")
}

// acceptsKind 报告该 target 是否接收指定事件 kind。node target 在未显式配置
// Kinds 时只接收 error/waiting——这是节点本地 hook 不发送的两类，避免双发。
func (t IMTarget) acceptsKind(kind string) bool {
	kind = strings.TrimSpace(kind)
	if len(t.Kinds) == 0 {
		if t.isNode() {
			return strings.EqualFold(kind, "error") || strings.EqualFold(kind, "waiting")
		}
		return true
	}
	for _, item := range t.Kinds {
		if strings.EqualFold(strings.TrimSpace(item), kind) {
			return true
		}
	}
	return false
}

// Name 实现 notify.Channel。
func (c *IM) Name() string { return "im" }

// Deliver 实现 notify.Channel：ev.Target = 会话 id（SessionRef.SessionID），
// ev.Kind = 投影词表（approval/question/completed/error/waiting）。
//
// 会话绑定路径：store 非空且无接受连接器 → 静默跳过；未知 kind
// （AttentionNoticeForProjection 不映射）→ 跳过。
//
// 全局 target 路径（docs/gateway/18 §5.3，仅当 Targets 非空时启用）：
// 未配置 bot 时该方法**逐字节保持既有语义**（不返回 ErrNoRecipient），
// 使回退链只在显式启用 bot 的部署上生效。
func (c *IM) Deliver(ctx context.Context, ev notify.Event) error {
	if c == nil {
		return nil
	}
	delivered := 0
	var errs []error

	// ---- 1. 会话绑定路径（既有语义，逐字保留）----
	if c.NotifyScoped != nil {
		sessionID := sessionOf(ev)
		if sessionID != "" {
			if notice, _, ok := connectors.AttentionNoticeForProjection(sessionID, ev.Kind); ok {
				accepted, skip := c.acceptedFor(sessionID, notice)
				if !skip {
					if err := c.NotifyScoped(ctx, accepted, notice); err != nil {
						errs = append(errs, err)
					} else {
						delivered++
					}
				}
			}
		}
	}

	// ---- 2. 全局/范围 target 路径（网关 bot + 节点委托；未配置时不参与）----
	targets := c.targets()
	suppressed := 0
	if len(targets) > 0 {
		gatewayTargets := make([]IMTarget, 0, len(targets))
		nodeTargets := make([]IMTarget, 0, len(targets))
		for _, t := range targets {
			if t.isNode() {
				nodeTargets = append(nodeTargets, t)
				continue
			}
			gatewayTargets = append(gatewayTargets, t)
		}
		if len(gatewayTargets) > 0 && c.SendOutbound != nil {
			n, sup, terrs := c.deliverTargets(ctx, ev, gatewayTargets)
			delivered += n
			suppressed += sup
			errs = append(errs, terrs...)
		}
		if len(nodeTargets) > 0 && c.SendNodeAttention != nil {
			n, sup, terrs := c.deliverNodeTargets(ctx, ev, nodeTargets)
			delivered += n
			suppressed += sup
			errs = append(errs, terrs...)
		}
	}

	if delivered > 0 {
		return errors.Join(errs...)
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	// 账本命中（已经投递过）视为幂等成功：不能返回 ErrNoRecipient，否则
	// Dispatcher 会把同一条事件在回退通道上再发一次。
	if suppressed > 0 {
		return nil
	}
	// 未配置全局 target：保持既有"静默跳过"（未配置 bot 行为零变化）。
	if len(targets) == 0 || (c.SendOutbound == nil && c.SendNodeAttention == nil) {
		return nil
	}
	// 配置了 bot 但没有任何 target 受理：交给回退链改走 push。
	return notify.ErrNoRecipient
}

// acceptedFor 复刻既有 D5 过滤：store 为空=纯测试口径（不查绑定，返回空集合
// 且不跳过）；无接受连接器时 skip=true（静默跳过）。
func (c *IM) acceptedFor(sessionID string, notice connectors.AttentionNotice) (accepted []string, skip bool) {
	accepted = make([]string, 0, 2)
	if c.Store == nil {
		return accepted, false
	}
	for _, cfg := range c.Store.ListConfigs() {
		if !cfg.Enabled {
			continue
		}
		if cfg.Policy.AcceptsNotice(notice, func(session connectors.SessionRef) bool {
			for _, binding := range c.Store.ListBindings(cfg.ID) {
				if binding.Session == session {
					return true
				}
			}
			return false
		}) {
			accepted = append(accepted, cfg.ID)
		}
	}
	if len(accepted) == 0 {
		return nil, true
	}
	return accepted, false
}

// deliverTargets 把事件发往命中的全局 target：按 connector_id|chat 去重、
// 受并发与超时预算约束，返回成功数与错误集合。
func (c *IM) deliverTargets(ctx context.Context, ev notify.Event, configured []IMTarget) (int, int, []error) {
	sessionID := sessionOf(ev)
	nodeID := strings.TrimSpace(ev.Node)

	seen := make(map[string]struct{}, len(configured))
	targets := make([]IMTarget, 0, len(configured))
	ledgerKeys := make(map[string]string, len(configured))
	eventID := strings.TrimSpace(ev.EventID)
	suppressed := 0
	for _, t := range configured {
		if strings.TrimSpace(t.ConnectorID) == "" || strings.TrimSpace(t.Chat) == "" {
			continue
		}
		if !t.acceptsKind(ev.Kind) {
			continue
		}
		if !t.Matches(nodeID, sessionID) {
			continue
		}
		key := t.ConnectorID + "|" + t.Chat
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if eventID != "" && c.DeliveryLedger != nil {
			ledgerKey := connectorChannelKey(c.Store, t.ConnectorID) + "|" + strings.TrimSpace(t.Chat) + "|" + eventID
			if !c.DeliveryLedger.Claim(ledgerKey) {
				suppressed++
				continue
			}
			ledgerKeys[key] = ledgerKey
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		return 0, suppressed, nil
	}

	conc := maxTargetConcurrency
	if len(targets) < conc {
		conc = len(targets)
	}
	// 预算裁剪：按剩余 ctx 时间分配给每一"波"发送，避免整体被 ctx 截断后
	// 有 target 完全没发（v2 §5.6 的不等式要求）。
	perTarget := perTargetTimeout
	if deadline, ok := ctx.Deadline(); ok {
		waves := (len(targets) + conc - 1) / conc
		if avail := time.Until(deadline) / time.Duration(waves); avail < perTarget {
			perTarget = avail
		}
	}
	if perTarget <= 0 {
		// 预算已耗尽：不启动半截发送，并以 skipped(deadline_exceeded) 上报——
		// 这是"来不及发"而非"发送失败"，不应计入错误率（§9 指标）。
		// 已认领的账本键必须释放，否则该事件会被误记为"已投递"而永不重试。
		for _, key := range ledgerKeys {
			if key != "" {
				c.DeliveryLedger.Release(key)
			}
		}
		return 0, suppressed, []error{&notify.SkippedError{Reason: notify.SkipReasonDeadlineExceeded}}
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ok   int
		errs []error
	)
	sem := make(chan struct{}, conc)
	for _, t := range targets {
		wg.Add(1)
		go func(t IMTarget, ledgerKey string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				if ledgerKey != "" {
					c.DeliveryLedger.Release(ledgerKey)
				}
				mu.Lock()
				errs = append(errs, fmt.Errorf("im target %s: %w", t.ID, ctx.Err()))
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			tctx, cancel := context.WithTimeout(ctx, perTarget)
			defer cancel()
			msgs := []connectors.OutboundMessage{{Text: c.messageText(ev, t.Limit)}}
			if err := c.SendOutbound(tctx, t.ConnectorID, t.Chat, msgs); err != nil {
				if ledgerKey != "" {
					c.DeliveryLedger.Release(ledgerKey)
				}
				mu.Lock()
				errs = append(errs, fmt.Errorf("im target %s: %w", t.ID, err))
				mu.Unlock()
				return
			}
			mu.Lock()
			ok++
			mu.Unlock()
		}(t, ledgerKeys[t.ConnectorID+"|"+t.Chat])
	}
	wg.Wait()
	return ok, suppressed, errs
}

// deliverNodeTargets 把事件委托给目标节点（POST /api/connectors/attention）：
// 同一节点只发一次（节点侧会把通知扇出到自己的全部受理 bot/绑定），并按
// target 的 Kinds 过滤。节点在线但无绑定视为成功（节点侧幂等 no-op）。
func (c *IM) deliverNodeTargets(ctx context.Context, ev notify.Event, configured []IMTarget) (int, int, []error) {
	sessionID := sessionOf(ev)
	nodeID := strings.TrimSpace(ev.Node)
	text := strings.TrimSpace(c.messageText(ev, 0))
	if text == "" {
		return 0, 0, nil
	}
	seen := make(map[string]struct{}, len(configured))
	var errs []error
	ok := 0
	suppressed := 0
	for _, t := range configured {
		nid := strings.TrimSpace(t.NodeID)
		if nid == "" || !t.acceptsKind(ev.Kind) || !t.Matches(nodeID, sessionID) {
			continue
		}
		if _, dup := seen[nid]; dup {
			continue
		}
		seen[nid] = struct{}{}
		ledgerKey := ""
		if eventID := strings.TrimSpace(ev.EventID); eventID != "" && c.DeliveryLedger != nil {
			ledgerKey = "node:" + nid + "|*|" + eventID
			if !c.DeliveryLedger.Claim(ledgerKey) {
				suppressed++
				continue
			}
		}
		notice := connectors.AttentionNotice{
			Kind:    strings.TrimSpace(ev.Kind),
			Session: connectors.SessionRef{NodeID: nodeID, SessionID: sessionID},
			Text:    text,
		}
		if err := c.SendNodeAttention(ctx, nid, notice); err != nil {
			if ledgerKey != "" {
				c.DeliveryLedger.Release(ledgerKey)
			}
			errs = append(errs, fmt.Errorf("im node %s: %w", nid, err))
			continue
		}
		ok++
	}
	return ok, suppressed, errs
}

// connectorChannelKey 解析网关连接器的跨宿主通道实例键；store 缺失/未知
// 连接器退化为本宿主 connector id（仅本宿主内去重）。
func connectorChannelKey(store connectors.Store, connectorID string) string {
	if store != nil {
		if cfg, ok := store.GetConfig(connectorID); ok {
			return connectors.ChannelKey(cfg)
		}
	}
	return "connector:" + strings.TrimSpace(connectorID)
}

// messageText 组装发往全局 target 的正文（docs/gateway/18 §5.7）：优先用投影
// 提示（含 /approve、/deny、/answer 等可操作指令）；waiting 等不映射的 kind
// 回退通知正文。超长按 provider 上限截断，但**保留末尾 URL**。
func (c *IM) messageText(ev notify.Event, limit int) string {
	head := strings.TrimSpace(ev.Notification.Title)
	body := strings.TrimSpace(ev.Notification.Body)
	if notice, _, ok := connectors.AttentionNoticeForProjection(sessionOf(ev), ev.Kind); ok {
		if t := strings.TrimSpace(notice.Text); t != "" {
			body = t
		}
	}
	// 会话专属正文（如「已完成」附带的最后一条助手回复，docs/gateway/08 §5.1）
	// 必须显式追加：上面的投影文案**替换**了 Body，只渲染 Body 会把这段内容丢掉
	//（2026-10-31 真机复盘：策略已开、事件 Detail 已算、IM 侧却看不到正文）。
	// 投影未命中时 Body 本身就含该内容（宿主已拼进正文），Contains 判定避免重复。
	if detail := strings.TrimSpace(ev.Detail); detail != "" && !strings.Contains(body, detail) {
		if body == "" {
			body = detail
		} else {
			body += "\n" + detail
		}
	}
	text := head
	switch {
	case text == "":
		text = body
	case body != "":
		text += "\n" + body
	}
	return imTruncateKeepURL(text, strings.TrimSpace(ev.Notification.URL), limit)
}

// imTruncateKeepURL 截断正文但保留可点击入口：可点性比正文完整性更重要。
func imTruncateKeepURL(text, u string, limit int) string {
	if limit <= 0 {
		limit = defaultTextLimit
	}
	runes := []rune(text)
	if u == "" {
		if len(runes) > limit {
			return string(runes[:limit-1]) + "…"
		}
		return text
	}
	keep := limit - len([]rune(u)) - 2
	if keep <= 0 {
		// 上限连 URL 都放不下：只发 URL，保证入口可用。
		return u
	}
	if len(runes) > keep {
		return string(runes[:keep-1]) + "…\n" + u
	}
	return text + "\n" + u
}

// imContains 报告 list 是否含 target（忽略首尾空白）。
func imContains(list []string, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, item := range list {
		if strings.TrimSpace(item) == target {
			return true
		}
	}
	return false
}
