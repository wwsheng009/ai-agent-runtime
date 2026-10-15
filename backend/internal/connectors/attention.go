// 连接器注意力出站（FR-7/FR-8，docs/bots/09 §7.4.1 / 03 §6.4）的宿主共享实现：
// 订阅审批/提问/回合结束事件 → 映射 AttentionNotice → 按 policy.notify（D5）
// 过滤 → 异步出站到绑定 IM。
//
// 单一实现点纪律（§4.1）：runtime-server / aicli / gateway 三宿主共用本文件，
// 事件→通知映射文案、去重键、D5 过滤口径不得分叉。宿主只需提供：
//   - SubscribeAttentionEvents：事件订阅源（runtimeapi.Handler / 裸 events.Bus）；
//   - Parked：§6.12 托管挂起探测（可 nil）；
//   - Store + NotifyAttention：D5 过滤与 IM 出站（Service.NotifyAttention）。
package connectors

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// attentionEventTypes 是注意力出站订阅的事件类型：审批请求、提问请求与回合
// 结束（completion）。事件名与 gateway_attention.go 的状态投影同源，避免两套
// attention 语义漂移。
var attentionEventTypes = []string{
	runtimechat.EventApprovalRequested,
	runtimechat.EventQuestionAsked,
	runtimeobserve.EventAgentTurnDone,
	// 工作类（S4 聚合订阅，13 §3.3）：仅 digest 形态投递（notice 订阅不生效）。
	eventToolRequested,
	eventToolCompleted,
	eventToolDenied,
	eventSubagentCompleted,
	eventSubagentBatchCompleted,
}

// AttentionEventTypes 返回注意力出站订阅的事件类型清单（宿主按类型订阅事件
// 总线时使用，如 aicli 裸 Bus；runtime-server 走 runtimeapi.Handler 聚合订阅
// 不需要）。返回副本，调用方可安全修改。
func AttentionEventTypes() []string {
	return append([]string(nil), attentionEventTypes...)
}

// AttentionNoticeForProjection 把 gateway 注意力状态投影（节点心跳上报的
// sess.Attention 字符串：approval/question/error/completed/waiting，与
// internal/notify PriorityOf 同一词表）映射为 IM 注意力通知。gateway 宿主
// 没有 LLM 事件总线——这是它的接入面（OnSessionTransition 回调调用）。
// 返回去重键（projection|session|kind；gateway 侧由调用方做 kind 变更判重，
// 状态投影本身已幂等）与 ok=false（空 kind/空会话不产生通知）。
func AttentionNoticeForProjection(sessionID, attention string) (AttentionNotice, string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	kind := strings.TrimSpace(attention)
	if sessionID == "" || kind == "" {
		return AttentionNotice{}, "", false
	}
	session := SessionRef{SessionID: sessionID}
	var text string
	switch kind {
	case AttentionKindApproval:
		text = "⏳ 会话有待审批的工具调用\n回复 /approve 或 /deny"
	case AttentionKindQuestion:
		text = "❓ 会话有待回答的提问\n回复 /answer 1 <内容>"
	case "error":
		text = "⚠️ 会话回合出错（详情见控制台）"
	case "completed":
		// gateway 投影词表用 "completed"（notify.PriorityOf 同源）；Notice.Kind
		// 用 connectors 统一常量 "completion"，保持与 LLM 事件出站同键。
		text = "✅ 会话回合已完成"
		return AttentionNotice{Kind: AttentionKindCompletion, Session: session, Text: text},
			"projection|" + sessionID + "|" + kind, true
	case "waiting":
		// waiting 是托管挂起/等待子任务：不算完成，IM 只收高层事件，这里跳过
		//（避免与后续 completed 双推）。
		return AttentionNotice{}, "", false
	default:
		// 未知 kind 保守跳过（notify.PriorityOf 按 normal 处理的旁路形态；
		// IM 出站宁缺毋滥，未知状态留给控制台）。
		return AttentionNotice{}, "", false
	}
	return AttentionNotice{Kind: kind, Session: session, Text: text},
		"projection|" + sessionID + "|" + kind, true
}

// RouteOverrideFromPolicy 把连接器策略路由（D6，docs/bots/09 §7.4.2）归一化为
// chat 回合级路由覆盖：零策略/全空白返回 nil（宿主走原路径，行为不变），
// 非空策略按项裁剪空白。runtime-server bootstrap 与 aicli connector_host 共用
// 该函数，保证两宿主对「什么算覆盖」的判定完全一致。
func RouteOverrideFromPolicy(route RoutePolicy) *runtimechat.RunRouteOverride {
	if !route.HasOverride() {
		return nil
	}
	return &runtimechat.RunRouteOverride{
		Provider:        strings.TrimSpace(route.Provider),
		Model:           strings.TrimSpace(route.Model),
		ReasoningEffort: strings.TrimSpace(route.ReasoningEffort),
	}
}

// AttentionDeps 聚合宿主侧依赖；Subscribe 为 nil 时整体退化为 no-op。
type AttentionDeps struct {
	// Subscribe 订阅注意力事件，返回退订函数；nil 表示事件总线不可用
	//（连接器退化为仅命令/回复闭环）。
	Subscribe func(handler func(runtimeevents.Event)) func()
	// Parked 报告会话是否处于 §6.12 托管挂起（run 结束 ≠ 托管 turn 结束）；
	// 可 nil（按未挂起处理）。
	Parked func(sessionID string) bool
	// Store 用于 D5 逐连接器过滤（policy.notify）；nil 跳过过滤（全部出站，
	// 仅测试口径，生产宿主必须提供）。
	Store Store
	// Notify 把通知出站到 IM（生产实现 Service.NotifyAttention）。
	Notify func(ctx context.Context, notice AttentionNotice) error
	// NotifyScoped 是带「接受连接器」上下文的出站（P2 修复：scope=all 跨会话
	// 投递，docs/bots/09 §7.4.1）：绑定会话事件按会话精确投递（旧语义），
	// 未绑定会话（scope=all 才可能通过过滤）回退广播到各接受连接器的绑定
	// chats——旧 Notify 对未绑定会话恒投递 0 目标（静默丢失）。
	// 生产实现 Service.NotifyAttentionScoped；nil 时回落 Notify（仅兼容测试）。
	NotifyScoped func(ctx context.Context, connectorIDs []string, notice AttentionNotice) error
	// Forward 把事件转发给其它 mesh 节点（P2 切片 D，scope=all 跨宿主扇出，
	// docs/bots/09 §7.4.1）：非 nil 时任一 bot 的 notify.enabled=true 即转发
	//（对端按自己的 policy 过滤）；fire-and-forget，失败只记日志。
	Forward func(notice AttentionNotice)
	// NotifyBroadcast 把聚合通知（digest 形态）广播到指定连接器的全部绑定 chats
	//（生产实现 Service.NotifyAttentionBroadcast；docs/bots/13 §7.1/S4）。nil 时
	// digest 订阅退化为即时 notice（可用性优先）。
	NotifyBroadcast func(ctx context.Context, connectorIDs []string, notice AttentionNotice) error
	// DigestWindow 是 digest 聚合窗口（<=0 用默认 30s；测试可缩短）。
	DigestWindow time.Duration
	// Audit 记录一次成功投递（connector/kind/session/命中订阅 id；宿主接
	// Service.RecordAttentionDelivery → ConnectorLog，13 §7.4）。可 nil。
	Audit func(connectorID, kind, sessionID, subscriptionID string)
	// AuditBroadcast 记录一次 digest 聚合广播的成功投递（connector/命中订阅
	// id/批内会话数；宿主接 Service.RecordAttentionDigestDelivery，13 §7.1/§7.4）。
	// 聚合条目跨会话，无单一 session 维度，故与 Audit 分开。可 nil。
	AuditBroadcast func(connectorID, subscriptionID string, count int)
	// Logf 出站失败/队列满日志；可 nil。
	Logf func(format string, args ...any)
}

// InletAttention 是跨宿主注意力接收面的宿主实现（restapi Deps.AttentionInlet，
// P2 切片 D）：对其它节点转发来的广播做**本地 D5 过滤**（逐 bot
// AcceptsNotice：绑定会话的 approval/question 始终收、completion 随开关；其它会话需 enabled+kinds+scope 命中——
// 发起侧只按"对端想收广播"意愿过滤，scope/kinds 的本地裁决在这里），
// 收集**全部**接受连接器后经 notifyScoped 出站（2026-10-14 修复补全：
// 与本地订阅链路同走 Service.NotifyAttentionScoped——先按事件会话精确投递
// （绑定场景），未命中（跨宿主未绑定会话）回退广播到接受连接器的绑定 chats；
// 修复前按事件会话反查绑定恒 0 投递，且首个命中即返回）。
// 出站用 Service 基 ctx（与 HandleInbound 的 Fix-1 语义一致：不绑 HTTP
// 请求 ctx）。返回 nil 表示本宿主无 bot 接收（调用方按静默成功处理——广播
// 本质是尽力而为）。
func InletAttention(store Store, notifyScoped func(ctx context.Context, connectorIDs []string, notice AttentionNotice) error) func(AttentionNotice) error {
	if store == nil || notifyScoped == nil {
		return nil
	}
	return func(notice AttentionNotice) error {
		ctx := context.Background()
		accepted := make([]string, 0, 2)
		for _, cfg := range store.ListConfigs() {
			if !cfg.Enabled {
				continue
			}
			if !cfg.Policy.AcceptsNotice(notice, func(session SessionRef) bool {
				for _, binding := range store.ListBindings(cfg.ID) {
					if binding.Session == session {
						return true
					}
				}
				return false
			}) {
				continue
			}
			accepted = append(accepted, cfg.ID)
		}
		if len(accepted) == 0 {
			return nil
		}
		return notifyScoped(ctx, accepted, notice)
	}
}

const (
	// attentionQueueSize 是注意力出站缓冲：事件回调不阻塞事件总线发布方，
	// 队列满时丢弃并记日志（IM 出站非可靠队列，后续事件仍会到达）。
	attentionQueueSize = 64
	// attentionDedupeCap 是事件级去重窗口（进程内）。
	attentionDedupeCap = 1024
)

// attentionTarget 是一次通知投递的目标（连接器 + 命中订阅 id；恒开/legacy 路径
// 给合成 id；审计用，docs/bots/13 §7.4）。
type attentionTarget struct {
	connectorID    string
	subscriptionID string
}

// delivery 是出站队列元素：通知 + 本宿主接受该通知的目标集合
// （scope=all 的跨会话投递需要它定位「投给谁的绑定 chats」）。
type delivery struct {
	notice  AttentionNotice
	targets []attentionTarget
}

// digestHit 是一次 digest 命中（连接器 + 命中订阅 id）。
type digestHit struct {
	connectorID    string
	subscriptionID string
}

// defaultAttentionDigestWindow 是 digest 聚合窗口默认值（13 §3.3）。
const defaultAttentionDigestWindow = 30 * time.Second

// attentionDigestSessionsCap 是聚合文案里最多列出的会话数（其余以「等」收口）。
const attentionDigestSessionsCap = 5

// attentionDigest 是 digest 形态的聚合缓冲（S4，docs/bots/13 §3.3/§7.1）：
// 按 (connector, subscription) 在窗口内聚合 completion 与工作类事件，flush 时
// 按批回调（宿主合并为一条广播）。批内按 kind 计数（completion/tool/subagent…）
// 供文案分列；窗口内重复会话只计一次标签；停用/退订时丢弃待发批。
type attentionDigest struct {
	mu     sync.Mutex
	window time.Duration
	timer  *time.Timer
	order  []string
	items  map[string]*digestBatch
	flush  func(connectorID, subscriptionID string, count int, sessions []string, kinds map[string]int)
}

type digestBatch struct {
	connectorID    string
	subscriptionID string
	count          int
	sessions       []string
	kinds          map[string]int
}

func newAttentionDigest(window time.Duration, flush func(connectorID, subscriptionID string, count int, sessions []string, kinds map[string]int)) *attentionDigest {
	if window <= 0 {
		window = defaultAttentionDigestWindow
	}
	return &attentionDigest{window: window, items: make(map[string]*digestBatch), flush: flush}
}

// add 把一次事件计入批；首批事件起窗口计时器。
func (d *attentionDigest) add(connectorID, subscriptionID, sessionID, kind string) {
	if d == nil || d.flush == nil {
		return
	}
	key := connectorID + "\x00" + subscriptionID
	d.mu.Lock()
	batch, ok := d.items[key]
	if !ok {
		batch = &digestBatch{connectorID: connectorID, subscriptionID: subscriptionID}
		d.items[key] = batch
		d.order = append(d.order, key)
	}
	batch.count++
	if kind = strings.TrimSpace(kind); kind != "" {
		if batch.kinds == nil {
			batch.kinds = make(map[string]int)
		}
		batch.kinds[kind]++
	}
	if label := subscriptionSessionLabel(sessionID); label != "" && len(batch.sessions) < attentionDigestSessionsCap {
		duplicate := false
		for _, item := range batch.sessions {
			if item == label {
				duplicate = true
				break
			}
		}
		if !duplicate {
			batch.sessions = append(batch.sessions, label)
		}
	}
	if d.timer == nil {
		d.timer = time.AfterFunc(d.window, d.flushAll)
	}
	d.mu.Unlock()
	// 指标（13 §7.4）：每吸收一个事件计一次「聚合」读数（不放大证据）。
	recordDigestMerge(connectorID)
}

// flushAll 投出窗口内全部批（仅 Dispatcher 内部定时器调用）。
func (d *attentionDigest) flushAll() {
	d.mu.Lock()
	items := d.items
	order := d.order
	d.items = make(map[string]*digestBatch)
	d.order = nil
	d.timer = nil
	d.mu.Unlock()
	for _, key := range order {
		batch, ok := items[key]
		if !ok || batch.count == 0 {
			continue
		}
		d.flush(batch.connectorID, batch.subscriptionID, batch.count, append([]string(nil), batch.sessions...), batch.kinds)
	}
}

// stop 停表并丢弃待发批（宿主 close / 退订路径）。
func (d *attentionDigest) stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	timer := d.timer
	d.timer = nil
	d.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
}

// digestBroadcastText 组装聚合广播文案（13 §7.1）：纯 completion 批保持既有
// 「N 个会话回合已收尾」口径（存量行为不变）；混合/工作类批按 kind 分列。
func digestBroadcastText(count int, sessions []string, kinds map[string]int) string {
	var text string
	if kinds[AttentionKindCompletion] == count {
		text = fmt.Sprintf("✅ %d 个会话回合已收尾", count)
	} else {
		text = fmt.Sprintf("🔔 %d 个事件已收尾", count)
		parts := make([]string, 0, 4)
		for _, entry := range []struct {
			kind  string
			label string
		}{
			{AttentionKindCompletion, "回合"},
			{AttentionKindTool, "工具"},
			{AttentionKindSubagent, "子代理"},
			{AttentionKindError, "错误"},
		} {
			if n := kinds[entry.kind]; n > 0 {
				parts = append(parts, fmt.Sprintf("%s ×%d", entry.label, n))
			}
		}
		if len(parts) > 0 {
			text += "\n（" + strings.Join(parts, "，") + "）"
		}
	}
	if len(sessions) == 0 {
		return text
	}
	line := strings.Join(sessions, ", ")
	if count > len(sessions) {
		line += " 等"
	}
	return text + "\n（会话 " + line + "）"
}

// digestNoticeKind 取批内主导 kind（聚合广播的标签；空/未知批回落 completion）。
func digestNoticeKind(kinds map[string]int) string {
	for _, kind := range []string{AttentionKindCompletion, AttentionKindTool, AttentionKindSubagent, AttentionKindError} {
		if kinds[kind] > 0 {
			return kind
		}
	}
	return AttentionKindCompletion
}

// SubscribeAttention 订阅审批/提问/回合结束事件并异步出站到 IM。
// store 非空时按各连接器的 policy.notify（D5）过滤：enabled=false 的连接器不收
// 任何事件通知；scope=sessions（默认）只收绑定到该 bot 的会话，scope=all 收
// 宿主全部会话事件（docs/bots/09-multi-instance-binding.md §7.4.1）。
//
// 投递语义（2026-10-14 修复）：接受后优先按事件会话精确投递（绑定场景，
// Service.NotifyAttentionScoped 内部先走 NotifySession）；未命中绑定
// （scope=all 的"其它会话"事件）回退广播到接受连接器的全部绑定 chats——
// 修复前该场景过滤通过但投递 0 目标（静默丢失）。
//
// 去重说明：同一事件（approval/question 的 request id、回合结束的 turn id/时间戳）
// 只出站一次——进程内窗口去重，保证重复订阅/重复投递不会推两条。与 Web Push/ntfy
// 的跨通道统一去重不在本切片（gateway 的 notify 差分在 gateway 宿主），留给
// notify.Dispatcher 切片；此处的键与 gateway_attention 使用的事件身份同源，
// 后续可迁移到共享去重账本。
//
// 返回退订函数；ctx 取消（宿主 close）时自动退订并停止 worker。
func SubscribeAttention(ctx context.Context, deps AttentionDeps) func() {
	if deps.Subscribe == nil || (deps.Notify == nil && deps.NotifyScoped == nil) {
		return func() {}
	}
	dedupe := newAttentionDeduper(attentionDedupeCap)
	// digest 聚合缓冲（S4，13 §7.1）：按 (connector, subscription) 在窗口内聚合
	// completion 与工作类事件，flush 时经 NotifyBroadcast 合并为一条
	//（未装配广播口则退化即时）。
	digest := newAttentionDigest(deps.DigestWindow, func(connectorID, subscriptionID string, count int, sessions []string, kinds map[string]int) {
		if deps.NotifyBroadcast == nil {
			return
		}
		text := digestBroadcastText(count, sessions, kinds)
		if err := deps.NotifyBroadcast(ctx, []string{connectorID}, AttentionNotice{Kind: digestNoticeKind(kinds), Text: text}); err != nil {
			recordConnectorDelivery(connectorID, DeliveryFormDigest, deliveryOutcomeFailed)
			if deps.Logf != nil {
				deps.Logf("connector attention digest delivery failed (connector=%s, subscription=%s): %v", connectorID, subscriptionID, err)
			}
		} else {
			// 成功投递：指标 + 审计（13 §7.4；聚合条目此前无审计条目）。
			recordConnectorDelivery(connectorID, DeliveryFormDigest, deliveryOutcomeSent)
			if deps.AuditBroadcast != nil {
				deps.AuditBroadcast(connectorID, subscriptionID, count)
			}
		}
	})
	queue := make(chan delivery, attentionQueueSize)
	stop := make(chan struct{})
	var stopOnce sync.Once
	stopWorker := func() { stopOnce.Do(func() { close(stop) }) }
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case item := <-queue:
				var err error
				if deps.NotifyScoped != nil {
					connectorIDs := make([]string, 0, len(item.targets))
					for _, target := range item.targets {
						connectorIDs = append(connectorIDs, target.connectorID)
					}
					err = deps.NotifyScoped(ctx, connectorIDs, item.notice)
				} else if deps.Notify != nil {
					err = deps.Notify(ctx, item.notice)
				}
				if err != nil {
					// 指标（13 §7.4）：投递失败按目标逐连接器计数（审计只在成功路径）。
					for _, target := range item.targets {
						recordConnectorDelivery(target.connectorID, DeliveryFormNotice, deliveryOutcomeFailed)
					}
					if deps.Logf != nil {
						deps.Logf("connector attention %s delivery failed (session=%s): %v", item.notice.Kind, item.notice.Session.SessionID, err)
					}
				} else if deps.Audit != nil && deps.NotifyScoped != nil {
					// S4 审计（13 §7.4）：成功投递按目标逐连接器落 ConnectorLog。
					for _, target := range item.targets {
						deps.Audit(target.connectorID, item.notice.Kind, item.notice.Session.SessionID, target.subscriptionID)
					}
				}
			}
		}
	}()
	unsubscribe := deps.Subscribe(func(event runtimeevents.Event) {
		notice, key, ok := AttentionNoticeForEvent(event, deps.Parked)
		if !ok || !dedupe.first(key) {
			return
		}
		subEvent, _ := subscriptionEventFor(event.Type)
		// 订阅求解（S2，docs/bots/13 §3.5）：逐连接器判定（一个事件可能命中
		// 多个 bot）；events 已配置走声明式订阅，否则回退 D5 的 AcceptsNotice
		//（双读单写，13 §4.1）。同时判定跨宿主转发（切片 D）：任一 bot
		// notify.enabled 或 events.enabled 即转发给 peers（对端再按自己的
		// 订阅过滤，两条链互不替代）。
		accepted := false
		targets := make([]attentionTarget, 0, 2)
		digestHits := make([]digestHit, 0, 2)
		anyBoundAccepted := false
		forwardWanted := deps.Forward != nil
		if deps.Store != nil {
			for _, cfg := range deps.Store.ListConfigs() {
				if !cfg.Enabled {
					continue
				}
				// 绑定反查按 SessionID（与 13 的会话选择器口径一致；绑定数
				// 几十~几百，全表扫描可行，见 Store 契约）。
				boundHere := sessionBoundLookup(deps.Store.ListBindings(cfg.ID))
				delivery, sub := subscriptionNoticeDelivery(cfg.Policy, subEvent, notice.Kind, notice.Session.SessionID, boundHere)
				switch delivery {
				case "notice":
					accepted = true
					targets = append(targets, attentionTarget{connectorID: cfg.ID, subscriptionID: sub.ID})
					if boundHere(notice.Session) {
						anyBoundAccepted = true
					}
				case "digest":
					if deps.NotifyBroadcast != nil {
						digestHits = append(digestHits, digestHit{connectorID: cfg.ID, subscriptionID: sub.ID})
					} else {
						// 未装配广播口：退化为即时 notice（可用性优先）。
						accepted = true
						targets = append(targets, attentionTarget{connectorID: cfg.ID, subscriptionID: sub.ID})
						if boundHere(notice.Session) {
							anyBoundAccepted = true
						}
					}
				}
				// 转发意愿：任一 bot 的 notify.enabled 或 events.enabled（无论
				// scope/kinds 命中与否——过滤是本宿主出站粒度，对端有自己的
				// 词表；enabled 是「这个 bot 想收广播」的意愿位）。
				if cfg.Policy.Notify.Enabled || cfg.Policy.Events.Enabled {
					forwardWanted = true
				}
			}
		} else {
			// 无 store（纯测试口径）：全量接收。
			accepted = true
		}
		if accepted && len(targets) > 0 && !anyBoundAccepted {
			// 跨会话订阅（13 §7.1）：纯跨会话投递时文案带会话标识，运维可区分来源。
			notice.Text = notice.Text + "\n（会话 " + subscriptionSessionLabel(notice.Session.SessionID) + "）"
		}
		for _, hit := range digestHits {
			digest.add(hit.connectorID, hit.subscriptionID, notice.Session.SessionID, notice.Kind)
		}
		if forwardWanted && deps.Forward != nil {
			// fire-and-forget：转发失败不重试（对端 absence 语义 = 收不到），
			// 不阻塞本地出站队列。
			go deps.Forward(notice)
		}
		if !accepted {
			return
		}
		select {
		case queue <- delivery{notice: notice, targets: targets}:
		default:
			if deps.Logf != nil {
				deps.Logf("connector attention queue full, dropping %s notice (session=%s)", notice.Kind, notice.Session.SessionID)
			}
		}
	})
	if unsubscribe == nil {
		// 事件总线不可用：无订阅，worker 由 ctx/stop 回收。
		return func() { stopWorker() }
	}
	var unsubOnce sync.Once
	unsub := func() {
		unsubOnce.Do(func() {
			unsubscribe()
			stopWorker()
			digest.stop()
		})
	}
	go func() {
		select {
		case <-ctx.Done():
			unsub()
		case <-stop:
		}
	}()
	return unsub
}

// AttentionNoticeForEvent 把运行时事件映射为 IM 注意力通知；第二个返回值是
// 事件级去重键，ok=false 表示该事件不产生通知。
func AttentionNoticeForEvent(event runtimeevents.Event, parked func(sessionID string) bool) (AttentionNotice, string, bool) {
	sessionID := strings.TrimSpace(event.SessionID)
	if sessionID == "" {
		return AttentionNotice{}, "", false
	}
	session := SessionRef{SessionID: sessionID}
	switch event.Type {
	case runtimechat.EventApprovalRequested:
		requestID := attentionEventPayloadString(event, "request_id")
		text := "⏳ 有待审批的工具调用"
		if tool := attentionEventPayloadString(event, "tool_name"); tool != "" {
			text += "：" + tool
		}
		if reason := attentionEventPayloadString(event, "reason"); reason != "" {
			text += "（" + reason + "）"
		}
		if requestID != "" {
			text += "\n回复 /approve " + requestID + " 或 /deny " + requestID
		} else {
			text += "\n回复 /approve 或 /deny"
		}
		return AttentionNotice{Kind: AttentionKindApproval, Session: session, Text: text},
			"approval|" + sessionID + "|" + attentionIdentity(requestID, event), true
	case runtimechat.EventQuestionAsked:
		questionID := attentionEventPayloadString(event, "question_id")
		text := "❓ 有待回答的提问"
		if prompt := attentionEventPayloadString(event, "prompt"); prompt != "" {
			text += "：" + prompt
		}
		text += "\n回复 /answer 1 <内容>"
		return AttentionNotice{Kind: AttentionKindQuestion, Session: session, Text: text},
			"question|" + sessionID + "|" + attentionIdentity(questionID, event), true
	case runtimeobserve.EventAgentTurnDone:
		text := "✅ 会话回合已完成"
		if parked != nil && parked(sessionID) {
			// §6.12 托管挂起：run 结束 ≠ 托管 turn 结束（与 gateway_attention 同口径）。
			text = "⏳ 回合已收尾，仍在等待子任务（完成后会继续推进）"
		}
		return AttentionNotice{Kind: AttentionKindCompletion, Session: session, Text: text},
			"completion|" + sessionID + "|" + attentionIdentity(attentionEventPayloadString(event, "turn_id"), event), true
	case eventToolRequested, eventToolCompleted, eventToolDenied:
		// 工作类（S4 聚合订阅，13 §3.3）：文案仅在「未装配广播口退化即时」时可见；
		// 正常路径只贡献 kind 计数（digestBroadcastText 分列）。
		text := "🔧 会话工具调用更新"
		switch event.Type {
		case eventToolRequested:
			text = "🔧 会话开始执行工具调用"
		case eventToolCompleted:
			text = "🔧 会话工具调用已完成"
		case eventToolDenied:
			text = "⛔ 会话工具调用被拒绝"
		}
		if tool := attentionEventPayloadString(event, "tool_name"); tool != "" {
			text += "：" + tool
		}
		return AttentionNotice{Kind: AttentionKindTool, Session: session, Text: text},
			"tool|" + sessionID + "|" + attentionIdentity(firstPayloadString(event, "tool_call_id", "call_id", "tool_use_id"), event), true
	case eventSubagentCompleted, eventSubagentBatchCompleted:
		text := "🧩 子代理任务已收尾"
		if event.Type == eventSubagentBatchCompleted {
			text = "🧩 子代理批次已收尾"
		}
		return AttentionNotice{Kind: AttentionKindSubagent, Session: session, Text: text},
			"subagent|" + sessionID + "|" + attentionIdentity(firstPayloadString(event, "subagent_id", "task_id", "batch_id"), event), true
	default:
		return AttentionNotice{}, "", false
	}
}

// firstPayloadString 依次返回载荷中第一个命中的非空字符串（身份提取用；
// 缺失时由 attentionIdentity 回落到事件时间戳）。
func firstPayloadString(event runtimeevents.Event, keys ...string) string {
	for _, key := range keys {
		if value := attentionEventPayloadString(event, key); value != "" {
			return value
		}
	}
	return ""
}

// attentionIdentity 返回事件级去重身份：优先业务 id（request/question/turn id），
// 缺失时回落到事件时间戳（同一 Event 重复投递仍能被拦截）。
func attentionIdentity(id string, event runtimeevents.Event) string {
	if id = strings.TrimSpace(id); id != "" {
		return id
	}
	if !event.Timestamp.IsZero() {
		return event.Timestamp.UTC().Format(time.RFC3339Nano)
	}
	return event.Type
}

// attentionEventPayloadString 读取事件载荷中的字符串字段（容忍非字符串值）。
func attentionEventPayloadString(event runtimeevents.Event, key string) string {
	if event.Payload == nil {
		return ""
	}
	value, ok := event.Payload[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// attentionDeduper 是进程内事件级去重窗口（FIFO 淘汰）。
type attentionDeduper struct {
	mu    sync.Mutex
	seen  map[string]struct{}
	order []string
	limit int
}

func newAttentionDeduper(limit int) *attentionDeduper {
	if limit <= 0 {
		limit = attentionDedupeCap
	}
	return &attentionDeduper{seen: make(map[string]struct{}, limit), limit: limit}
}

// first 报告 key 是否首次出现；非首次返回 false（空 key 视为首次，调用方
// 由映射层保证 key 非空）。
func (d *attentionDeduper) first(key string) bool {
	if d == nil || key == "" {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; ok {
		return false
	}
	d.seen[key] = struct{}{}
	d.order = append(d.order, key)
	if len(d.order) > d.limit {
		oldest := d.order[0]
		d.order = d.order[1:]
		delete(d.seen, oldest)
	}
	return true
}

// release 撤销一次认领（订阅投递入队失败时调用，使同一事件后续重试仍可投递；
// 罕见路径，O(n) 清理可接受）。key 不存在时为 no-op。
func (d *attentionDeduper) release(key string) {
	if d == nil || key == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.seen[key]; !ok {
		return
	}
	delete(d.seen, key)
	for i, item := range d.order {
		if item == key {
			d.order = append(d.order[:i], d.order[i+1:]...)
			break
		}
	}
}
