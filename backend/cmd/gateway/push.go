package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
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
	ntfy *ntfy.Sender
	// appPush is the native App channel (nil when no relay/provider is
	// configured). It is deliberately separate from Web Push and ntfy.
	appPush *channels.AppPush
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
	// imSource 是网关自持 bot 的配置源（docs/gateway/18 §6；nil = 未配置 bot）。
	// Targets 为空时通道面与 primary 均与既有行为一致（§5.2 动态主通道）。
	imSource *notifyIMSource
	// delegateToNode 把通知委托给目标节点，由节点自己的 bot 按本地绑定发送
	// （docs/gateway/18 复用方案）。nil = 未接线。
	delegateToNode func(ctx context.Context, nodeID string, notice connectors.AttentionNotice) error
	// connectorStore / notifyScoped / sendOutbound 由 connectors 装配注入
	// （装配失败时保持 nil：IM 通道不参与，见 imChannel）。
	connectorStore connectors.Store
	notifyScoped   func(ctx context.Context, connectorIDs []string, notice connectors.AttentionNotice) error
	sendOutbound   func(ctx context.Context, connectorID, chat string, msgs []connectors.OutboundMessage) error
	// preferences 是三级通知偏好（global/session/channel；nil = 无偏好）。
	// 由 main.go 从 <data-dir>/notify-prefs.json 装载（缺失 = 行为不变）。
	preferences notify.PreferenceStore
	// policy 是通知策略热加载源（<data-dir>/notify-policy.json；缺失时只读
	// 回退旧 notify-prefs.json）。每次 observe 前增量重读；PUT API 保存后
	// 立即应用。nil = 无策略源（测试/旧装配）。
	policy *notifyPolicySource
	// baseQuietHours 是 --notify-quiet-hours 的解析结果；策略显式给出
	// global.quiet_hours 时被替换（空数组 = 清空）。
	baseQuietHours []notify.QuietRange
	// policyErr 去重策略装载告警，避免每次 observe 刷日志。
	policyErr string
	// normalChannels 是 --notify-normal-channels 的解析结果。为空时保持
	// D6 单主通道（webpush）；非空时 normal 按列表投递，供 ntfy/AppPush
	// 等通道显式接收 completed/waiting。
	normalChannels []string

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
	// bootID 是本次网关启动的唯一标识：事件 ID 含它，重启后不会与旧账本
	// 条目碰撞（旧条目按 TTL 淘汰）。
	bootID string
	// generation 是每会话的注意力代次（node|session → counter）：同一 attention
	// 清除后重新进入视为新事件，避免账本把合法重投吞掉。
	generation map[string]int64
	// deliveryLedger 是通道级交付账本（跨重启/多目标去重，P1）。
	deliveryLedger *notifyDeliveryLedger
	// logMu/loggedErrs 给"同一个投递失败"做限流：completed 事件持续到达时，同一条
	// 错误（例如微信缺 context_token）会刷满 journalctl。按错误文本去重——首次立刻
	// 打印，之后最多每 notifyErrLogEvery 打一次；错误消失后自然过期淘汰。
	logMu      sync.Mutex
	loggedErrs map[string]time.Time
}

func newPushNotifier(manager *push.Manager, baseURL string, presence notify.Presence) *pushNotifier {
	return &pushNotifier{
		seen:       make(map[string]string),
		manager:    manager,
		baseURL:    strings.TrimRight(baseURL, "/"),
		presence:   presence,
		now:        time.Now,
		bootID:     fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102T150405.000000000"), os.Getpid()),
		generation: make(map[string]int64),
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
	eventID   string
	// attachDetail 表示 note.Body 里带了会话专属内容（「已完成」附带的最后一条
	// 助手回复，docs/gateway/08 §5.1）。这类事件必须绕过合并窗口逐条投递：
	// Coalesce 的合并摘要只保留标题，会把正文内容丢掉。
	attachDetail bool
	// detail 是同一段会话专属内容的原文，随事件透传给通道（Event.Detail）。
	// IM 通道会用投影文案替换 Body，只靠 Body 会丢掉这段内容（见 §5.1）。
	detail string
}

// observe diffs the current attention states against the previous snapshot and
// notifies for each newly-attentive session.
func (p *pushNotifier) observe(nodes []*tunnel.Node) {
	// imEnabled 必须计入唤醒门：纯 bot 部署可能既无 Web Push 也无 ntfy/App
	// （docs/gateway/18 §11）。
	if p == nil || (p.manager == nil && !p.ntfyEnabled() && !p.appPushEnabled() && !p.imEnabled()) {
		return
	}
	// 策略热加载：stat 一次策略文件，变化才重读并热替换偏好/免打扰。
	p.applyPolicy()
	// Bot target 热加载（docs/gateway/18 §6.1）：外部直接改 gateway-im.json 时
	// 也要生效，不能只依赖 PUT 接口。stat 一次，戳未变则几乎零开销。
	if p.imSource != nil {
		p.imSource.ReloadIfChanged()
	}
	p.mu.Lock()
	next := make(map[string]string)
	if p.generation == nil {
		p.generation = make(map[string]int64)
	}
	var immediate []pendingNotification
	var batched []pendingNotification
	prefs := p.preferences
	policyChannels := p.policyChannelNames()
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
			// 例外：存在显式 ignore_presence 的 IM target 时，IM 仍应投递；
			// 其余通道由 Dispatcher 的在场门继续静音。
			if p.presence != nil && p.presence.Active(node.NodeID, sess.SessionID) &&
				!p.imPresenceExempt(node.NodeID, sess.SessionID, attention) {
				continue
			}
			next[key] = attention
			// kind 白名单（notify-policy.json allow_kinds）：被策略排除的
			// kind 不生成通知，但仍写入 seen 快照——策略相对"当前注意力"
			// 不回溯，重新打开开关后只通知后续变化，避免开关瞬间补发洪水。
			if !kindAllowedByPolicy(prefs, policyChannels, sess.SessionID, attention) {
				continue
			}
			if prev, ok := p.seen[key]; ok && prev == attention {
				continue
			}
			note := push.Notification{
				Title: attentionTitle(sess.Title),
				Body:  attentionBody(node, attention),
				Tag:   key,
				URL:   p.sessionURL(node.NodeID, sess.SessionID),
			}
			// docs/gateway/08 §5.1：「已完成」可选附带最后一条助手回复
			// （策略开关 + 长度上限，默认关闭）。
			detail := p.completedLastMessage(sess.LastMessageRole, sess.LastMessage, attention)
			if detail != "" {
				note.Body = strings.TrimSpace(note.Body) + "\n" + detail
			}
			applyDeliveryPolicy(&note, attention)
			p.generation[key]++
			eventID := p.bootID + "|" + key + "|" + attention + "|" + strconv.FormatInt(p.generation[key], 10)
			item := pendingNotification{
				attention:    attention,
				nodeID:       node.NodeID,
				sessionID:    sess.SessionID,
				note:         note,
				eventID:      eventID,
				attachDetail: detail != "",
				detail:       detail,
			}
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

// applyPolicy 增量热加载策略文件并热替换 Dispatcher 的偏好/免打扰。
// 非法文件保留最后一次成功策略（source 语义），只记录一次告警。
func (p *pushNotifier) applyPolicy() {
	if p == nil || p.policy == nil {
		return
	}
	prefs, quiet, quietSet, err := p.policy.Effective()
	if err != nil {
		p.mu.Lock()
		msg := err.Error()
		if p.policyErr != msg {
			p.policyErr = msg
			log.Printf("push: notify policy ignored: %v", err)
		}
		p.mu.Unlock()
		return
	}
	merged := p.baseQuietHours
	if quietSet {
		merged = quiet
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.policyErr != "" {
		p.policyErr = ""
		log.Printf("push: notify policy reloaded")
	}
	prefsChanged := p.preferences != prefs
	quietChanged := !equalQuietRanges(p.quietHours, merged)
	if !prefsChanged && !quietChanged {
		return
	}
	p.preferences = prefs
	p.quietHours = merged
	if p.dispatcher != nil {
		p.dispatcher.SetPreferences(prefs)
		p.dispatcher.SetQuietHours(merged)
	}
}

// policyChannelNames 返回当前已启用通道名（kind 白名单的通道级覆盖判定）。
func (p *pushNotifier) policyChannelNames() []string {
	var names []string
	if p.manager != nil {
		names = append(names, "webpush")
	}
	if p.ntfyEnabled() {
		names = append(names, "ntfy")
	}
	if p.appPushEnabled() {
		names = append(names, "apppush")
	}
	// v2 §5.2：im 必须进入通道级 allow_kinds 判定。否则只配 IM 不配 push 时，
	// 用户在「通知设置」里为 im 单独开的白名单完全不生效，且事件在此处就被
	// 提前丢弃（kindAllowedByPolicy 的通道级兜底依赖本列表）。
	if p.imEnabled() {
		names = append(names, "im")
	}
	return names
}

// imTargets 返回当前启用的全局/范围 target（配置源热加载后即时生效）。
func (p *pushNotifier) imTargets() []channels.IMTarget {
	if p == nil || p.imSource == nil {
		return nil
	}
	return p.imSource.Targets()
}

// imEnabled 报告是否配置了启用的 bot target（0 = 未配置 bot，行为零变化）。
func (p *pushNotifier) imEnabled() bool { return len(p.imTargets()) > 0 }

// imChannel 构造 IM 通道实例；未配置 target 或连接器未装配时返回 false，
// 通道面保持既有构成（docs/gateway/18 §5.1：扩展现有 channels.IM，不新建
// 同名通道）。
func (p *pushNotifier) imChannel() (notify.Channel, bool) {
	if p.imSource == nil || (p.sendOutbound == nil && p.delegateToNode == nil) {
		return nil, false
	}
	return &channels.IM{
		Store:             p.connectorStore,
		NotifyScoped:      p.notifyScoped,
		TargetsFn:         p.imTargets,
		SendOutbound:      p.sendOutbound,
		SendNodeAttention: p.delegateToNode,
		DeliveryLedger:    p.deliveryLedger,
	}, true
}

// applyIMConfig 在 Bot 通道配置保存后调用（notify_im.go 的 onSaved）。
// target 内容变化由 TargetsFn 即时生效，无需重建；只有"是否存在启用 target"
// 发生变化时才重建通道面——primary 与回退链在 ensureDispatcher 里决定。
func (p *pushNotifier) applyIMConfig(wasEnabled bool) {
	if p == nil || p.imEnabled() == wasEnabled {
		return
	}
	p.mu.Lock()
	p.dispatcher = nil
	p.mu.Unlock()
}

// kindAllowedByPolicy 按 global→session→channel 字段级覆盖判定 kind 是否
// 可能被任一启用通道放行。通道级 allow_kinds 可覆盖全局/会话级限制，因此
// 任一层级允许都保留事件，交给 Dispatcher 的通道门精确裁剪。
func kindAllowedByPolicy(prefs notify.PreferenceStore, channels []string, sessionID, kind string) bool {
	if prefs == nil {
		return true
	}
	if prefs.Resolve(sessionID, "").AllowsKind(kind) {
		return true
	}
	for _, name := range channels {
		if prefs.Resolve(sessionID, name).AllowsKind(kind) {
			return true
		}
	}
	return false
}

// equalQuietRanges 比较免打扰窗口集合（热加载只在真正变化时触碰 Dispatcher，
// 避免每次 observe 都写 Dispatcher 的共享配置）。
func equalQuietRanges(a, b []notify.QuietRange) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ntfyEnabled reports whether the secondary channel is configured.
func (p *pushNotifier) ntfyEnabled() bool {
	return p != nil && p.ntfy != nil && p.ntfy.Enabled()
}

// appPushEnabled reports whether a native App channel and relay are configured.
// It must be part of the observe/dispatch wake-up gates: an App-only gateway may
// have neither Web Push nor ntfy enabled and must still deliver attention events.
func (p *pushNotifier) appPushEnabled() bool {
	return p != nil && p.appPush != nil && p.appPush.Enabled()
}

func (p *pushNotifier) appPushDeviceCount() int {
	if !p.appPushEnabled() {
		return 0
	}
	return len(p.appPush.Store.List())
}

// parseChannelList 解析逗号分隔的通道名，去空、去重并保持顺序。
// 供 --notify-normal-channels 使用；语义与 Dispatcher.SetNormalChannels 对齐。
func parseChannelList(spec string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, part := range strings.Split(spec, ",") {
		name := strings.TrimSpace(part)
		if name == "" {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
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
	chans := make([]notify.Channel, 0, 3)
	if p.manager != nil {
		chans = append(chans, &channels.WebPush{Manager: p.manager})
	}
	if p.ntfy != nil {
		chans = append(chans, &channels.Ntfy{Sender: p.ntfy})
	}
	if p.appPush != nil && p.appPush.Enabled() {
		chans = append(chans, p.appPush)
	}
	if ch, ok := p.imChannel(); ok {
		chans = append(chans, ch)
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
	d.SetNormalChannels(p.normalChannels...)
	// v2 §5.2 动态主通道：只有存在启用 target 时才把 IM 设为主通道并启用回退
	// 链；未配置 bot 的部署保持 webpush，行为逐字节不变。回退在同一次
	// Dispatch 内完成（§5.6），不递归。
	if p.imEnabled() {
		d.SetPrimary("im")
		d.SetNormalFallback("webpush", "ntfy", "apppush")
	}
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
	webPushSubscribers := 0
	if p.manager != nil {
		webPushSubscribers = p.manager.Store().Count()
	}
	// 无收件人判定同样要把 im 计入（§11 唤醒/短路门槛）。
	if webPushSubscribers == 0 && !p.ntfyEnabled() && p.appPushDeviceCount() == 0 && !p.imEnabled() {
		// Without recipients the dispatch below silently delivers nothing, which
		// looks like "notifications never fire". Make that state visible.
		log.Printf("push: %d notification(s) ready but no device is subscribed; open %s and enable notifications", len(items), p.baseURL)
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		for _, item := range items {
			// D6 主通道：normal 类已路由 IM 时 push 侧静默；high 类仍扇出。
			// 配置了 bot target 时一律交给 Dispatcher（IM 通道 + 回退链），不在
			// 宿主侧静默——否则绑定会话的 normal 通知会因旧 hook 停用而双路径
			// 都收不到（docs/gateway/18 §5.3 收敛为单一路径）。
			if item.imMain && !p.imEnabled() && notify.PriorityOf(item.attention) == notify.PriorityNormal {
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
			ev.EventID = item.eventID
			ev.IMPresenceExempt = p.imPresenceExempt(item.nodeID, item.sessionID, item.attention)
			// 正文带会话专属内容时不进合并窗口（Coalesce 只保留标题）。
			ev.SkipCoalesce = item.attachDetail
			// 会话专属正文另走 Detail：IM 通道会用投影文案替换 Body，
			// 只靠 Body 会把「已完成」附带的内容丢掉（docs/gateway/08 §5.1）。
			ev.Detail = item.detail
			// Target 是通道专用寻址提示（docs/gateway/18 §5.3）：IM 通道用它
			// 定位会话做投影映射与 scope=sessions 命中。与 Session 同值，但
			// 语义分层——Session 供偏好解析，Target 供投递寻址。
			ev.Target = item.sessionID
			receipt, err := dispatcher.Dispatch(ctx, ev)
			if err != nil {
				// 同一错误限流打印：这条日志过去会随每个 completed 事件重复刷屏
				// （见 notifyErrLogEvery 的注释）。
				msg := fmt.Sprintf("push: dispatch failed (attention=%s): %v", item.attention, err)
				if p.shouldLogDispatchError(msg) {
					log.Print(msg)
				}
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

// imPresenceExempt 报告是否存在覆盖该事件的 IM target 且显式 ignore_presence。
// 与 channels.IM.Deliver 的 target 受理判定同源，避免两套口径。
func (p *pushNotifier) imPresenceExempt(nodeID, sessionID, kind string) bool {
	for _, target := range p.imTargets() {
		if target.IgnorePresence && target.MatchesEvent(nodeID, sessionID, kind) {
			return true
		}
	}
	return false
}

func (p *pushNotifier) sessionURL(nodeID, sessionID string) string {
	if p.baseURL == "" {
		return ""
	}
	// 通知点击落点是**网关控制台自己的会话深链**（#sessions?node=…&session=…）：
	// 不区分节点类型（aicli / runtime-server 都由控制台统一定位），也不再直接
	// 指向节点原版 Web（避免依赖各节点 route allowlist 与 kind 判定）。节点原版
	// Web 仍在卡片菜单里提供。
	q := url.Values{}
	if nodeID != "" {
		q.Set("node", nodeID)
	}
	if sessionID != "" {
		q.Set("session", sessionID)
	}
	u := p.baseURL + "/#sessions"
	if encoded := q.Encode(); encoded != "" {
		u += "?" + encoded
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

// completedLastMessage 返回「已完成」通知要附带的最后一条助手回复（已按策略
// 截断）；策略未开启 / kind 不是 completed / 最后一条不是助手消息时返回 ""。
// docs/gateway/08-notifications.md §5.1。
func (p *pushNotifier) completedLastMessage(lastRole, lastText, attention string) string {
	if p == nil || !strings.EqualFold(strings.TrimSpace(attention), "completed") {
		return ""
	}
	opts := p.contentOptions()
	if !opts.CompletedIncludeLastMessage {
		return ""
	}
	// 只带助手内容：最后一条可能是 user/tool（例如等待用户输入后中断），
	// 那种情况下它不是"结论"，宁可不带。
	if !strings.EqualFold(strings.TrimSpace(lastRole), "assistant") {
		return ""
	}
	text := strings.TrimSpace(lastText)
	if text == "" {
		return ""
	}
	return truncateNotificationText(text, opts.LastMessageMaxChars)
}

// contentOptions 读取"通知正文内容"策略（策略源缺失 = 默认关闭）。
func (p *pushNotifier) contentOptions() notifyContentOptions {
	if p == nil || p.policy == nil {
		return notifyContentOptions{LastMessageMaxChars: notifyDefaultLastMessageMaxChars}
	}
	return p.policy.ContentOptions()
}

// truncateNotificationText 按 rune 截断「已完成」附带的助手回复，并尽量停在
// 可读边界上（docs/gateway/08 §5.1）：
//
//  1. 上限是 rune 数（多字节安全）；
//  2. 优先回退到上限之前的最后一个换行——否则会把 JSON / 代码块 / 表格切在中间，
//     通知里只剩半截机器文本（2026-10-31 真机：微信收到
//     `notify-policy.json: {"global":{"completed_include_last…`）；
//     回退幅度过大（保留不足上限的 40%）时不回退，仍按上限硬切，避免为了找边界
//     把有用的正文丢光；
//  3. 丢掉末尾只剩符号噪声的行（孤立的 ``` / | / ---），再补 "…"。
func truncateNotificationText(text string, max int) string {
	if max <= 0 {
		max = notifyDefaultLastMessageMaxChars
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}

	cut := max
	if lastBreak := lastLineBreak(runes[:max]); lastBreak >= max*2/5 {
		cut = lastBreak
	}
	head := strings.TrimRight(string(runes[:cut]), " \t\n\r")
	for head != "" {
		line, at := trailingLine(head)
		if !isNoiseOnlyLine(line) {
			break
		}
		if at < 0 {
			head = ""
			break
		}
		head = strings.TrimRight(head[:at], " \t\n\r")
	}
	if head == "" {
		// 全部被判定为噪声（极端输入）：退回硬切，至少保留内容。
		head = strings.TrimRight(string(runes[:max]), " \t\n\r")
	}
	return head + "…"
}

// lastLineBreak 返回 head 中最后一个换行的下标；没有换行时返回 -1。
func lastLineBreak(head []rune) int {
	for i := len(head) - 1; i >= 0; i-- {
		if head[i] == '\n' {
			return i
		}
	}
	return -1
}

// trailingLine 返回最后一行与其起始下标（无换行时 at = -1）。
func trailingLine(text string) (string, int) {
	at := strings.LastIndex(text, "\n")
	if at < 0 {
		return text, -1
	}
	return text[at+1:], at
}

// isNoiseOnlyLine 报告一行去掉空白后是否只剩标记符号（```、|、---、> 等）。
// 这类行单独出现在截断处没有信息量，留着只会让通知以 "```" 收尾。
func isNoiseOnlyLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return true
	}
	for _, r := range trimmed {
		switch r {
		case '`', '|', '-', '*', '_', '>', '#', '~', '+', '=':
		default:
			return false
		}
	}
	return true
}

// notifyErrLogEvery 是"同一投递失败"的日志限流窗口。取 10 分钟：既保留第一次
// 的完整上下文（便于发现新问题），又不让一个持续存在的错误把 journal 刷满。
const notifyErrLogEvery = 10 * time.Minute

// shouldLogDispatchError 对同一错误文本限流（map 有界：超过 64 条先清理过期项）。
func (p *pushNotifier) shouldLogDispatchError(msg string) bool {
	if p == nil {
		return true
	}
	p.logMu.Lock()
	defer p.logMu.Unlock()
	if p.loggedErrs == nil {
		p.loggedErrs = make(map[string]time.Time)
	}
	now := time.Now()
	if last, ok := p.loggedErrs[msg]; ok && now.Sub(last) < notifyErrLogEvery {
		return false
	}
	if len(p.loggedErrs) >= 64 {
		for k, t := range p.loggedErrs {
			if now.Sub(t) >= notifyErrLogEvery {
				delete(p.loggedErrs, k)
			}
		}
	}
	p.loggedErrs[msg] = now
	return true
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
