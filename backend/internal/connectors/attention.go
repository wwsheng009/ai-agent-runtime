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
	// Logf 出站失败/队列满日志；可 nil。
	Logf func(format string, args ...any)
}

// InletAttention 是跨宿主注意力接收面的宿主实现（restapi Deps.AttentionInlet，
// P2 切片 D）：对其它节点转发来的广播做**本地 D5 过滤**（逐 bot
// AcceptsNotice：绑定会话始终收，其它会话需 enabled+kinds+scope 命中——
// 发起侧只按"对端想收广播"意愿过滤，scope/kinds 的本地裁决在这里），
// 命中者经 NotifyAttention 出站到该 bot 绑定。出站用 Service 基 ctx（与
// HandleInbound 的 Fix-1 语义一致：不绑 HTTP 请求 ctx）。返回 nil 表示
// 本宿主无 bot 接收（调用方按静默成功处理——广播本质是尽力而为）。
func InletAttention(store Store, notify func(ctx context.Context, notice AttentionNotice) error) func(AttentionNotice) error {
	if store == nil || notify == nil {
		return nil
	}
	return func(notice AttentionNotice) error {
		ctx := context.Background()
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
			if err := notify(ctx, notice); err != nil {
				return err
			}
			return nil // 同一通知本宿主出站一次即可（多 bot 命中不去重——
			// 不同 bot 绑定不同 chat，NotifySession 内部按各自绑定扇出）
		}
		return nil
	}
}

const (
	// attentionQueueSize 是注意力出站缓冲：事件回调不阻塞事件总线发布方，
	// 队列满时丢弃并记日志（IM 出站非可靠队列，后续事件仍会到达）。
	attentionQueueSize = 64
	// attentionDedupeCap 是事件级去重窗口（进程内）。
	attentionDedupeCap = 1024
)

// delivery 是出站队列元素：通知 + 本宿主接受该通知的连接器集合
// （scope=all 的跨会话投递需要它定位「投给谁的绑定 chats」）。
type delivery struct {
	notice       AttentionNotice
	connectorIDs []string
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
					err = deps.NotifyScoped(ctx, item.connectorIDs, item.notice)
				} else if deps.Notify != nil {
					err = deps.Notify(ctx, item.notice)
				}
				if err != nil && deps.Logf != nil {
					deps.Logf("connector attention %s delivery failed (session=%s): %v", item.notice.Kind, item.notice.Session.SessionID, err)
				}
			}
		}
	}()
	unsubscribe := deps.Subscribe(func(event runtimeevents.Event) {
		notice, key, ok := AttentionNoticeForEvent(event, deps.Parked)
		if !ok || !dedupe.first(key) {
			return
		}
		// D5 过滤：逐连接器判定（一个事件可能命中多个 bot 的通知开关）；
		// 同时判定跨宿主转发（切片 D）：任一 bot notify.enabled=true 即转发
		// 给 peers（对端再按自己的 policy 过滤，两条链互不替代）。
		accepted := false
		acceptedConnectors := make([]string, 0, 2)
		forwardWanted := deps.Forward != nil
		if deps.Store != nil {
			for _, cfg := range deps.Store.ListConfigs() {
				if !cfg.Enabled {
					continue
				}
				accepts := cfg.Policy.AcceptsNotice(notice, func(session SessionRef) bool {
					// scope=sessions 的语义：事件会话已绑定到本 bot（按 SessionRef
					// 反查绑定表；绑定数几十~几百，全表扫描可行，见 Store 契约）。
					for _, binding := range deps.Store.ListBindings(cfg.ID) {
						if binding.Session == session {
							return true
						}
					}
					return false
				})
				if accepts {
					accepted = true
					acceptedConnectors = append(acceptedConnectors, cfg.ID)
				}
				// 转发意愿：任一 bot 的 notify.enabled（无论 scope/kinds 命中与否
				// ——kinds/scope 是本宿主出站粒度，对端有自己的词表；enabled 是
				// 「这个 bot 想收广播」的意愿位，转发以它为门槛控制噪音）。
				if cfg.Policy.Notify.Enabled {
					forwardWanted = true
				}
			}
		} else {
			// 无 store（纯测试口径）：全量接收。
			accepted = true
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
		case queue <- delivery{notice: notice, connectorIDs: acceptedConnectors}:
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
	default:
		return AttentionNotice{}, "", false
	}
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
