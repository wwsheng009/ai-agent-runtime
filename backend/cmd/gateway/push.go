package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/ntfy"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/push"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify"
	"github.com/wwsheng009/ai-agent-runtime/internal/notify/channels"
)

// pushNotifier watches the registry and delivers a Web Push notification when a
// session newly enters an attention state (docs/gateway/08-notifications.md).
//
// Notification policy (§5.5-5.6): approval/question/error are high priority and
// are delivered immediately, piercing quiet hours; completed/waiting are
// coalesced within coalesceWindow and muted during quietHours.
type pushNotifier struct {
	mu      sync.Mutex
	seen    map[string]string // "nodeID|sessionID" -> last attention value
	manager *push.Manager
	// ntfy is the secondary channel (nil when disabled). Web Push and ntfy are
	// independent: ntfy still fires when no browser has subscribed.
	ntfy    *ntfy.Sender
	baseURL string
	// presence suppresses pushes for sessions a console client is watching.
	// 接口而非具体登记表（docs/bots/12 §4 G1）：测试可注入可翻转实现，
	// 确定性复现"observe 之后在场恰好续期"的双门竞态；*presenceRegistry
	// 天然满足该签名，生产装配不变。
	presence notify.Presence
	// imMainChannel 判定 normal 类通知是否以 IM 为主通道（06 §1 D6：已绑定 IM
	// 则 IM，否则 Web Push；nil = 一律走 push 侧）。由 connectors 装配供给
	// （newIMMainChannelPredicate），与该宿主的投影 hook 投递口径逐字同源。
	imMainChannel func(nodeID, sessionID, kind string) bool
	// preferences 是三级通知偏好（global/session/channel；nil = 无偏好）。
	// 由 main.go 从 <data-dir>/notify-prefs.json 装载（缺失 = 行为不变）。
	preferences notify.PreferenceStore

	// coalesceWindow merges low-priority notifications arriving close together
	// (0 disables coalescing). quietHours mutes low-priority notifications.
	// S4b G3：两者均已下沉内核（SetCoalesceWindow/SetQuietHours），字段仅为
	// 装配输入保留；批处理由内核持有（batch/timer 退役），now 为装配时钟
	// （经 SetCoalesceClock 注入内核，生产恒 time.Now）。
	coalesceWindow time.Duration
	quietHours     []notify.QuietRange
	now            func() time.Time

	// dispatcher 是惰性缓存的内核分发器（S4b G1：构造上提，避免每次 dispatch
	// 重建；G2 账本接线后单实例语义才完整）。由 ensureDispatcher 按 mu 加锁
	// 构建/失效，锁外仅读引用。
	dispatcher *notify.Dispatcher
	// ledger 是跨路径去重账本（S4b G2）：实例上提为字段——Release（换值/
	// 清除时释放旧键）必须作用在与 Dispatch 相同的账本上；每次新建会使
	// 释放失效。键 = tag|attention（§3 D2）；生命周期镜像 seen 快照。
	ledger *notify.MemoryLedger
}

func newPushNotifier(manager *push.Manager, baseURL string, presence notify.Presence) *pushNotifier {
	return &pushNotifier{
		seen:     make(map[string]string),
		manager:  manager,
		baseURL:  strings.TrimRight(baseURL, "/"),
		presence: presence,
		now:      time.Now,
	}
}

// SetCoalesceClockForTest 注入静默/合并判定时钟并失效 Dispatcher 缓存
// （测试用；生产时钟恒为 time.Now）。
func (p *pushNotifier) SetCoalesceClockForTest(now func() time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = now
	p.dispatcher = nil
}

// Seed pre-populates the dedup snapshot from persisted/last-known registry
// state (gateway restart): sessions that already had an attention state before
// the restart are not re-pushed. Callers pass the restored nodes (typically
// marked stale) before any agent reconnects.
func (p *pushNotifier) Seed(nodes []*tunnel.Node) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, node := range nodes {
		if node == nil {
			continue
		}
		for _, sess := range node.Sessions {
			attention := strings.TrimSpace(sess.Attention)
			if attention == "" {
				continue
			}
			p.seen[node.NodeID+"|"+sess.SessionID] = attention
		}
	}
}

// pendingNotification pairs a notification with the attention kind that
// produced it so coalescing can group by kind. nodeID/sessionID 供 D6 主通道
// 判定；imMain 表示该事件以 IM 为主通道（normal 类时 push 侧静默）。
type pendingNotification struct {
	attention string
	nodeID    string
	sessionID string
	imMain    bool
	note      push.Notification
}

// observe diffs the current attention states against the previous snapshot and
// notifies for each newly-attentive session.
func (p *pushNotifier) observe(nodes []*tunnel.Node) {
	if p == nil || (p.manager == nil && !p.ntfyEnabled()) {
		return
	}
	p.mu.Lock()
	next := make(map[string]string)
	var immediate []pendingNotification
	var batched []pendingNotification
	for _, node := range nodes {
		for _, sess := range node.Sessions {
			attention := strings.TrimSpace(sess.Attention)
			if attention == "" {
				continue
			}
			key := node.NodeID + "|" + sess.SessionID
			// 账本键生命周期镜像 seen 快照（docs/bots/12 §3 D2）：换值即
			// 释放旧账本键——"clear→re-arm 同值"是新的代次事件，必须可重投
			// （TestPushNotifierObserve 的既有契约）。释放动作本身幂等。
			if prev, ok := p.seen[key]; ok && prev != attention && p.ledger != nil {
				p.ledger.Release(key + "|" + prev)
			}
			// Suppress while a console client is viewing this session, and leave
			// it unrecorded so the notification still fires once they look away.
			if p.presence != nil && p.presence.Active(node.NodeID, sess.SessionID) {
				continue
			}
			next[key] = attention
			if prev, ok := p.seen[key]; ok && prev == attention {
				continue
			}
			note := push.Notification{
				Title: attentionTitle(sess.Title),
				Body:  attentionBody(node, attention),
				Tag:   key,
				URL:   p.sessionURL(node.NodeID, sess.SessionID),
			}
			applyDeliveryPolicy(&note, attention)
			item := pendingNotification{attention: attention, nodeID: node.NodeID, sessionID: sess.SessionID, note: note}
			if p.imMainChannel != nil && p.imMainChannel(node.NodeID, sess.SessionID, attention) {
				item.imMain = true
			}
			if notify.IsHighPriority(attention) {
				immediate = append(immediate, item)
			} else {
				batched = append(batched, item)
			}
		}
	}
	// 清除路径：本轮快照中消失的键（attention 已清空）同样释放旧账本键。
	// 与上一循环的换值释放互补，共同保证账本键生命周期 == seen 快照生命周期。
	// 必须在替换 seen **之前**遍历旧快照（替换后旧键已不可达）。
	for key, prev := range p.seen {
		if _, still := next[key]; !still && p.ledger != nil {
			p.ledger.Release(key + "|" + prev)
		}
	}
	p.seen = next
	// 合并/静默策略下沉内核（docs/bots/12 §3 D3）：batched 事件原样进
	// dispatch，由 Dispatcher 的 coalesce/quiet 门接管攒批与免打扰；宿主
	// 不再自持批处理（batch/timer 字段退役）。
	immediate = append(immediate, batched...)
	p.mu.Unlock()

	p.dispatch(immediate)
}

// ntfyEnabled reports whether the secondary channel is configured.
func (p *pushNotifier) ntfyEnabled() bool {
	return p != nil && p.ntfy != nil && p.ntfy.Enabled()
}

// ntfyMessage 保留为通道适配器映射的薄包装（cmd/gateway 既有测试引用；
// 实现已收敛到 channels.NtfyMessage）。
func ntfyMessage(n push.Notification) ntfy.Message {
	return channels.NtfyMessage(n)
}

// ensureDispatcher 返回缓存的内核分发器（S4b G1 构造上提）。并发安全：dispatch
// 可能被多个触发点并发调用，构建/读取均在 p.mu 下（observe/flush 均在 Unlock
// 后才调用 dispatch，无重入）。生产路径通道面启动后固定；测试改字段后置
// p.dispatcher = nil 可重建。
func (p *pushNotifier) ensureDispatcher() *notify.Dispatcher {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dispatcher != nil {
		return p.dispatcher
	}
	chans := make([]notify.Channel, 0, 2)
	if p.manager != nil {
		chans = append(chans, &channels.WebPush{Manager: p.manager})
	}
	if p.ntfy != nil {
		chans = append(chans, &channels.Ntfy{Sender: p.ntfy})
	}
	if len(chans) == 0 {
		return nil
	}
	if p.ledger == nil {
		p.ledger = notify.NewMemoryLedger(1024)
	}
	d := notify.NewDispatcher(p.ledger, chans...)
	// 跨路径去重账本（docs/bots/12 §3 D2）：键 = tag|attention（tag=node|session）。
	// attention 维度保证 approval→error 序列不会被吞；换值/清除时 observe
	// 释放旧账本键（生命周期镜像 seen 快照，TestPushNotifierObserve 的
	// clear→re-arm 场景依赖此点）；G1 的 Suppressed 回滚语义同前。
	d.SetPrimary("webpush")
	d.SetPreferences(p.preferences)
	// 在场门下沉到内核（docs/bots/10 §5 S4a）：登记表即 Presence 权威。
	// observe 的 seen 前置检查仍是"跳过且不记 seen → 补推一次"的语义门；
	// 这里兜住未自行前置检查的生产者（内核门同样不认领去重键）。
	if p.presence != nil {
		d.SetPresence(p.presence)
	}
	// 静默/合并下沉内核（docs/bots/12 §3 D3）：窗口与判定时钟注入 Dispatcher；
	// 宿主自持的批处理已退役（observe 直投，攒批在内核）。
	d.SetQuietHours(p.quietHours)
	d.SetCoalesceWindow(p.coalesceWindow)
	if p.now != nil {
		d.SetCoalesceClock(p.now)
	}
	p.dispatcher = d
	return d
}

// dispatch 经 notify.Dispatcher 异步投递（docs/bots/06 §2.4 切流）：按 D6
// 路由——high=已配置通道全扇出、normal=主通道（webpush，缺省回落首个已配置
// 通道）、low=跳过。消息形态/合并/静默/在场语义沿用本文件既有链路不变；
// 跨路径去重账本留待切片三接线（此处 nil ledger，回放语义仍由 seen 快照保证）。
func (p *pushNotifier) dispatch(items []pendingNotification) {
	if len(items) == 0 {
		return
	}
	dispatcher := p.ensureDispatcher()
	if dispatcher == nil {
		return
	}
	if p.manager != nil && p.manager.Store().Count() == 0 && !(p.ntfy != nil && p.ntfy.Enabled()) {
		// Without subscribers the dispatch below silently delivers nothing, which
		// looks like "notifications never fire". Make that state visible.
		log.Printf("push: %d notification(s) ready but no device is subscribed; open %s and enable notifications", len(items), p.baseURL)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, item := range items {
			// D6 主通道：normal 类已路由 IM 时 push 侧静默；high 类仍扇出。
			if item.imMain && notify.PriorityOf(item.attention) == notify.PriorityNormal {
				log.Printf("push: %s routed to IM main channel, push suppressed (%s)", item.attention, item.note.Tag)
				continue
			}
			// 账本键（docs/bots/12 §3 D2）= tag|attention：tag（=node|session）
			// 供控制台折叠同会话通知；attention 维度保证同一会话先后两种
			// attention（approval→error）不互相吞——与 seen 快照的换值重推
			// 语义逐字一致。agent NOTIFY 的 DedupKey 目前不落 registry 摘要，
			// 跨路径透传另行切片（12 号 §4 G2 备注）。
			ev := notify.EventFor(item.attention, item.note.Tag+"|"+item.attention, item.note)
			ev.Node = item.nodeID
			ev.Session = item.sessionID
			receipt, err := dispatcher.Dispatch(ctx, ev)
			if err != nil {
				log.Printf("push: dispatch failed (attention=%s): %v", item.attention, err)
			}
			// 双门收敛（docs/bots/12 §3 D1）+ 账本不变式（§3 D2）：内核在场门
			// 抑制（Suppressed）时撤销 observe 已记的 seen——抑制不是投递，
			// seen 不能占位，否则竞态窗口内的通知被永久吞掉且永不补推。账本
			// 键无需回收：内核在场门位于 Claim **之前**，Suppressed 路径根本
			// 没认领过键（Dispatch 源码顺序保证）。回滚后下一次 observe 把
			// 该事件视作新事件重投，与"跳过且不记 seen"语义逐字等价。
			if receipt.Suppressed {
				p.mu.Lock()
				if p.seen[item.note.Tag] == item.attention {
					delete(p.seen, item.note.Tag)
				}
				p.mu.Unlock()
			}
		}
	}()
}

func (p *pushNotifier) sessionURL(nodeID, sessionID string) string {
	if p.baseURL == "" {
		return ""
	}
	u := p.baseURL + "/n/" + nodeID + "/"
	if sessionID != "" {
		u += "?session=" + sessionID
	}
	return u
}

func attentionTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return "Session needs attention"
	}
	return title
}

func attentionBody(node *tunnel.Node, attention string) string {
	name := node.Name
	if strings.TrimSpace(name) == "" {
		name = node.NodeID
	}
	return fmt.Sprintf("%s: %s", name, attention)
}

// applyDeliveryPolicy maps an attention kind onto push-service delivery hints
// (RFC 8030): high-priority attention (approval/question/error) is sent with
// Urgency: high so Android/iOS do not defer it to a Doze maintenance window,
// and with a short TTL so a stale alert does not pop long after it matters.
// Low-priority attention keeps normal urgency and may be retained for later.
func applyDeliveryPolicy(note *push.Notification, attention string) {
	if notify.IsHighPriority(attention) {
		note.Urgency = "high"
		note.TTL = 10 * time.Minute
		return
	}
	note.Urgency = "normal"
	note.TTL = time.Hour
}

// handlePushConfig returns the VAPID public key so the service worker can
// subscribe, plus which secondary channels are available.
func handlePushConfig(manager *push.Manager, ntfySender *ntfy.Sender) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ntfyEnabled := ntfySender != nil && ntfySender.Enabled()
		if manager == nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"enabled": false,
				"ntfy":    map[string]any{"enabled": ntfyEnabled, "topic": ntfySender.Topic()},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":    true,
			"public_key": manager.PublicKey(),
			"ntfy":       map[string]any{"enabled": ntfyEnabled, "topic": ntfySender.Topic()},
		})
	}
}

// handlePushSubscriptions lists or adds push subscriptions.
func handlePushSubscriptions(manager *push.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "push disabled"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"subscriptions": manager.Store().List()})
		case http.MethodPost:
			var req struct {
				Endpoint string `json:"endpoint"`
				Keys     struct {
					P256dh string `json:"p256dh"`
					Auth   string `json:"auth"`
				} `json:"keys"`
				Label string `json:"label"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid request body"})
				return
			}
			if strings.TrimSpace(req.Endpoint) == "" || strings.TrimSpace(req.Keys.P256dh) == "" || strings.TrimSpace(req.Keys.Auth) == "" {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endpoint and keys are required"})
				return
			}
			sub := push.Subscription{
				Endpoint: strings.TrimSpace(req.Endpoint),
				P256dh:   strings.TrimSpace(req.Keys.P256dh),
				Auth:     strings.TrimSpace(req.Keys.Auth),
				Label:    strings.TrimSpace(req.Label),
			}
			if err := manager.Store().Add(sub); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "failed to store subscription"})
				return
			}
			writeJSON(w, http.StatusCreated, map[string]any{"subscribed": true})
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

// handlePushUnsubscribe removes a subscription by endpoint.
func handlePushUnsubscribe(manager *push.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if manager == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "push disabled"})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Endpoint string `json:"endpoint"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Endpoint) == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "endpoint is required"})
			return
		}
		if !manager.Store().Remove(strings.TrimSpace(req.Endpoint)) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "subscription not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"unsubscribed": true})
	}
}

// handlePushTest sends a test notification through every configured channel.
// The legacy top-level delivered/subscriptions fields still describe Web Push;
// the channels map reports each channel separately.
func handlePushTest(manager *push.Manager, ntfySender *ntfy.Sender) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ntfyEnabled := ntfySender != nil && ntfySender.Enabled()
		if manager == nil && !ntfyEnabled {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "push disabled"})
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()

		note := push.Notification{
			Title:   "aicli-gateway",
			Body:    "Test notification",
			Tag:     "test",
			Urgency: "high",
			TTL:     10 * time.Minute,
		}
		webpush := map[string]any{"enabled": manager != nil}
		delivered := 0
		if manager != nil {
			delivered = manager.Broadcast(ctx, note)
			webpush["delivered"] = delivered
			webpush["subscriptions"] = manager.Store().Count()
		}
		ntfyResult := map[string]any{"enabled": ntfyEnabled, "topic": ntfySender.Topic()}
		if ntfyEnabled {
			if err := ntfySender.Send(ctx, ntfyMessage(note)); err != nil {
				ntfyResult["delivered"] = 0
				ntfyResult["error"] = err.Error()
			} else {
				ntfyResult["delivered"] = 1
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"delivered":     delivered,
			"subscriptions": pushSubscriptionCount(manager),
			"channels":      map[string]any{"webpush": webpush, "ntfy": ntfyResult},
		})
	}
}
