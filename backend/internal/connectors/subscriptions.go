// subscriptions.go 是声明式事件订阅的求解层（S2，docs/bots/13
// §3.1/§3.4/§3.5）：事件词表归一化、类型/会话匹配、legacy 回退判定。
//
// 单一实现点纪律（与 attention.go/content_mirror.go 同）：订阅求值只在本文件，
// 宿主只负责提供订阅源（事件总线）与投递函数。
//
// 兼容语义（13 §4.1 双读单写）：
//   - `policy.events` 已配置（enabled + 非空订阅）→ 走新模型；
//   - 否则回退 legacy：`notify`（注意力）与 `content_mirror`（正文副本）逐字保留
//     旧行为（含绑定会话 approval/question 恒开、completion 随开关、kinds/scope 判定）。
package connectors

import (
	"fmt"
	"strings"
)

// subscriptionEvent 是订阅词表里一个运行时事件的归一化形态（13 §3.1）。
type subscriptionEvent struct {
	// Canonical 是规范类型名（配置里可直接写）。
	Canonical string
	// Aliases 是兼容别名（事件名/旧注意力 kind）。
	Aliases []string
	// Family 是族（turn/conversation/interaction/system），供 "族.*" 通配。
	Family string
	// Kind 是注意力 kind（approval/question/completion/error）；非注意力事件为空。
	Kind string
}

// 工作类运行时事件名（宿主镜像，docs/bots/13 §3.1）：规范名 = 总线事件名。
// 仅经 digest 形态投递（见 subscriptionNoticeDelivery 的 work 规则）。
const (
	eventToolRequested          = "tool.requested"
	eventToolCompleted          = "tool.completed"
	eventToolDenied             = "tool.denied"
	eventSubagentCompleted      = "subagent.completed"
	eventSubagentBatchCompleted = "subagent.batch.completed"
)

var subscriptionEventTable = []subscriptionEvent{
	{Canonical: "assistant_message", Aliases: []string{"assistant.message"}, Family: "conversation"},
	{Canonical: "user_message", Family: "conversation"},
	{Canonical: "assistant_delta", Family: "conversation"},
	{Canonical: "agent.turn.started", Family: "turn"},
	{Canonical: "agent.turn.finished", Aliases: []string{"completion"}, Family: "turn", Kind: AttentionKindCompletion},
	{Canonical: "turn.suspended", Family: "turn"},
	{Canonical: "turn.resumed", Family: "turn"},
	{Canonical: "session_start", Family: "turn"},
	{Canonical: "session_end", Family: "turn"},
	{Canonical: "session.interrupted", Aliases: []string{"interrupted"}, Family: "turn"},
	{Canonical: "approval_requested", Aliases: []string{"approval"}, Family: "interaction", Kind: AttentionKindApproval},
	{Canonical: "question_asked", Aliases: []string{"question"}, Family: "interaction", Kind: AttentionKindQuestion},
	{Canonical: "error", Family: "system", Kind: AttentionKindError},
	// 工作类（S4 聚合订阅）：仅 digest 形态匹配（notice 订阅对工作类不生效）。
	{Canonical: eventToolRequested, Aliases: []string{"tool.started"}, Family: "work"},
	{Canonical: eventToolCompleted, Aliases: []string{"tool.finished", "tool_finished"}, Family: "work"},
	{Canonical: eventToolDenied, Family: "work"},
	{Canonical: eventSubagentCompleted, Family: "work"},
	{Canonical: eventSubagentBatchCompleted, Family: "work"},
}

var subscriptionEventIndex = func() map[string]subscriptionEvent {
	index := make(map[string]subscriptionEvent, len(subscriptionEventTable)*2)
	for _, ev := range subscriptionEventTable {
		index[ev.Canonical] = ev
		for _, alias := range ev.Aliases {
			index[alias] = ev
		}
	}
	return index
}()

// subscriptionEventFor 归一化运行时事件类型（含点分隔别名）；未知返回 ok=false。
func subscriptionEventFor(eventType string) (subscriptionEvent, bool) {
	ev, ok := subscriptionEventIndex[strings.TrimSpace(eventType)]
	return ev, ok
}

// matchesPattern 报告订阅 types 里的一个模式是否命中本事件：
// 精确规范名/别名、族名、族通配（"turn.*"）或 "*"（显式全匹配）。
func (e subscriptionEvent) matchesPattern(pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if pattern == "*" || pattern == e.Canonical || pattern == e.Family {
		return true
	}
	for _, alias := range e.Aliases {
		if pattern == alias {
			return true
		}
	}
	if strings.HasSuffix(pattern, ".*") {
		return strings.TrimSuffix(pattern, ".*") == e.Family
	}
	return false
}

// normalizeEventsSubscriptions 归一化订阅并过滤非法项：
//   - mode 缺省=bound；ids 模式必须带非空 ids；未知 mode 忽略整条；
//   - types 必填（空白项裁剪后为空则忽略整条）；deliver 缺省=notice。
func normalizeEventsSubscriptions(subs []EventSubscription) []EventSubscription {
	out := make([]EventSubscription, 0, len(subs))
	for i, sub := range subs {
		sub.ID = strings.TrimSpace(sub.ID)
		if sub.ID == "" {
			sub.ID = fmt.Sprintf("sub-%d", i+1)
		}
		switch mode := strings.ToLower(strings.TrimSpace(sub.Sessions.Mode)); mode {
		case "", "bound":
			sub.Sessions.Mode = "bound"
		case "ids":
			ids := make([]string, 0, len(sub.Sessions.IDs))
			for _, id := range sub.Sessions.IDs {
				if trimmed := strings.TrimSpace(id); trimmed != "" {
					ids = append(ids, trimmed)
				}
			}
			if len(ids) == 0 {
				continue
			}
			sub.Sessions.Mode = "ids"
			sub.Sessions.IDs = ids
		case "all":
			sub.Sessions.Mode = "all"
		default:
			continue
		}
		types := make([]string, 0, len(sub.Types))
		for _, item := range sub.Types {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				types = append(types, trimmed)
			}
		}
		if len(types) == 0 {
			continue
		}
		sub.Types = types
		sub.Deliver = strings.ToLower(strings.TrimSpace(sub.Deliver))
		if sub.Deliver == "" {
			sub.Deliver = "notice"
		}
		out = append(out, sub)
	}
	return out
}

// subscriptionTypesMatch 报告订阅类型集合是否命中事件（词表见上表）。
func subscriptionTypesMatch(types []string, ev subscriptionEvent) bool {
	for _, pattern := range types {
		if ev.matchesPattern(pattern) {
			return true
		}
	}
	return false
}

// subscriptionSessionMatches 报告会话选择器是否命中目标会话。
func subscriptionSessionMatches(selector SessionSelector, sessionID string, boundHere func(SessionRef) bool) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(selector.Mode)) {
	case "", "bound":
		return boundHere != nil && boundHere(SessionRef{SessionID: sessionID})
	case "ids":
		for _, id := range selector.IDs {
			if strings.TrimSpace(id) == sessionID {
				return true
			}
		}
		return false
	case "all":
		return true
	default:
		return false
	}
}

// matchingSubscription 返回第一条「形态 + 类型 + 会话」全命中的有效订阅
// （显式停用项跳过；审计需要命中订阅的 id）。
func matchingSubscription(subs []EventSubscription, ev subscriptionEvent, sessionID string, boundHere func(SessionRef) bool, deliver string) (EventSubscription, bool) {
	for _, sub := range normalizeEventsSubscriptions(subs) {
		if !sub.IsEnabled() {
			continue
		}
		if sub.Deliver != deliver {
			continue
		}
		if !subscriptionTypesMatch(sub.Types, ev) {
			continue
		}
		if !subscriptionSessionMatches(sub.Sessions, sessionID, boundHere) {
			continue
		}
		return sub, true
	}
	return EventSubscription{}, false
}

// anySubscription 报告是否存在命中订阅（matchingSubscription 的布尔投影）。
func anySubscription(subs []EventSubscription, ev subscriptionEvent, sessionID string, boundHere func(SessionRef) bool, deliver string) bool {
	_, ok := matchingSubscription(subs, ev, sessionID, boundHere, deliver)
	return ok
}

// subscriptionNoticeDelivery 返回该 bot 对一条注意力通知的处理方式与命中订阅：
//   - "notice"：即时投递（命中订阅供审计；恒开/legacy 路径给合成 id）；
//   - "digest"：聚合投递（completion 与工作类；S4，13 §7.1）；
//   - ""：不接收。
//
// 语义（13 §6.2 + §4.1 双读单写）：绑定会话的 approval/question 恒开；`events`
// 已配置走新模型（types 族通配；ids/all 支持跨会话）；否则回退 D5 的
// `AcceptsNotice`（逐字保留 kinds/scope 判定）。digest 订阅被配到交互类
// （approval/question/error）上时按即时 notice 处理——安全优先，告警不因聚合迟到。
// 工作类（work 族）**仅聚合形态**（13 §3.3：逐条订阅会刷屏）：只匹配
// `deliver=digest` 订阅；notice 订阅即使列出工作类类型也不生效。
func subscriptionNoticeDelivery(policy Policy, ev subscriptionEvent, kind, sessionID string, boundHere func(SessionRef) bool) (string, EventSubscription) {
	bound := boundHere != nil && boundHere(SessionRef{SessionID: sessionID})
	if bound && (ev.Canonical == "approval_requested" || ev.Canonical == "question_asked") {
		return "notice", EventSubscription{ID: "bound-command-loop"}
	}
	if policy.Events.Configured() {
		if ev.Canonical == "" {
			return "", EventSubscription{}
		}
		if ev.Family == "work" {
			if sub, ok := matchingSubscription(policy.Events.Subscriptions, ev, sessionID, boundHere, "digest"); ok {
				return "digest", sub
			}
			return "", EventSubscription{}
		}
		if sub, ok := matchingSubscription(policy.Events.Subscriptions, ev, sessionID, boundHere, "notice"); ok {
			return "notice", sub
		}
		if sub, ok := matchingSubscription(policy.Events.Subscriptions, ev, sessionID, boundHere, "digest"); ok && ev.Kind != "" {
			if ev.Kind == AttentionKindCompletion {
				return "digest", sub
			}
			return "notice", sub
		}
		return "", EventSubscription{}
	}
	// legacy 回退（events 未配置）：旧 D5 词表只有 4 类注意力 kind（13 §4.1），
	// 工作类（tool/subagent）是新模型概念且仅有 digest 形态——legacy 无聚合
	// 通道，逐条 notice 直投违反「不放大：禁止逐 tool 事件直投」（13 §3.7#2）。
	// 2026-10-15 真机：未配 events 的微信 bot 对每个工具事件收到
	// `subscription=legacy-notify` 投递（绑定会话非 completion 恒开所致）。
	if ev.Family == "work" {
		return "", EventSubscription{}
	}
	notice := AttentionNotice{Kind: strings.TrimSpace(kind), Session: SessionRef{SessionID: strings.TrimSpace(sessionID)}}
	if policy.AcceptsNotice(notice, boundHere) {
		return "notice", EventSubscription{ID: "legacy-notify"}
	}
	return "", EventSubscription{}
}

// subscriptionNoticeAccepted 是 subscriptionNoticeDelivery 的布尔投影（兼容层）。
func subscriptionNoticeAccepted(policy Policy, ev subscriptionEvent, kind, sessionID string, boundHere func(SessionRef) bool) bool {
	delivery, _ := subscriptionNoticeDelivery(policy, ev, kind, sessionID, boundHere)
	return delivery != ""
}

// subscriptionContentMatch 报告该 bot 是否接收事件的正文副本（content 形态），
// 并返回命中的订阅（审计用；legacy 回退命中时返回合成的稳定伪 id）。
func subscriptionContentMatch(policy Policy, ev subscriptionEvent, sessionID string, boundHere func(SessionRef) bool) (EventSubscription, bool) {
	if policy.Events.Configured() {
		if ev.Canonical == "" {
			return EventSubscription{}, false
		}
		return matchingSubscription(policy.Events.Subscriptions, ev, sessionID, boundHere, "content")
	}
	if policy.ContentMirror {
		return EventSubscription{ID: "legacy-content", Deliver: "content"}, true
	}
	return EventSubscription{}, false
}

// subscriptionContentAllowed 是 subscriptionContentMatch 的布尔投影。
func subscriptionContentAllowed(policy Policy, ev subscriptionEvent, sessionID string, boundHere func(SessionRef) bool) bool {
	_, ok := subscriptionContentMatch(policy, ev, sessionID, boundHere)
	return ok
}

// subscriptionStreamMatch 报告该 bot 是否接收事件的流式形态（deliver=stream，
// 仅 assistant 增量类事件的订阅；13 §3.4/§7.1 S4），并返回命中的订阅（审计用）。
// 语义：events 未配置（legacy）恒不命中——stream 必须显式声明（无 legacy 等价物）。
func subscriptionStreamMatch(policy Policy, ev subscriptionEvent, sessionID string, boundHere func(SessionRef) bool) (EventSubscription, bool) {
	if !policy.Events.Configured() || ev.Canonical == "" {
		return EventSubscription{}, false
	}
	return matchingSubscription(policy.Events.Subscriptions, ev, sessionID, boundHere, "stream")
}

// sessionBoundLookup 由绑定列表构造「该会话是否绑定到本 bot」判定：只比较
// SessionID（与 13 的会话选择器口径一致；NodeID 形态差异/陈旧不参与匹配）。
func sessionBoundLookup(bindings []Binding) func(SessionRef) bool {
	return func(ref SessionRef) bool {
		sessionID := strings.TrimSpace(ref.SessionID)
		if sessionID == "" {
			return false
		}
		for _, binding := range bindings {
			if strings.TrimSpace(binding.Session.SessionID) == sessionID {
				return true
			}
		}
		return false
	}
}

// subscriptionSessionLabel 生成跨会话通知里的会话短标识（13 §7.1）：
// 取最后一个 "_" 之后的段（如 session_2026…_z7m74wJq → z7m74wJq），
// 无下划线时取尾部 8 字符。
func subscriptionSessionLabel(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if idx := strings.LastIndex(sessionID, "_"); idx >= 0 && idx+1 < len(sessionID) {
		if label := sessionID[idx+1:]; label != "" {
			return label
		}
	}
	if len(sessionID) > 12 {
		return sessionID[len(sessionID)-8:]
	}
	return sessionID
}
