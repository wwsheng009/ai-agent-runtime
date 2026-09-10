package cacheanalytics

import (
	"sort"
	"sync"
)

// correlationTable 维护请求与消息身份的关联（§5.2）。
//
//	turn_id ──► user_message_id          （turn 开始时登记，1:1）
//	trace_id ──► llm_request_id          （request_started 登记，1:1）
//	turn_id ──► [llm_request_id...]      （同 turn 多步 ReAct，1:N，按 step 顺序）
//	message_id ──► llm_request_id        （assistant 消息产出关联）
//
// 全部为内存结构，随 Projector 生命周期；并发安全。
type correlationTable struct {
	mu sync.RWMutex

	// turnID → 该 turn 的请求 id 列表（按登记顺序 = step 顺序）。
	turnRequests map[string][]string
	// llmRequestID → turnID。
	requestTurn map[string]string
	// traceID → 最近一次登记的 llm_request_id（同 trace 多请求时取最后）。
	traceRequest map[string]string
	// turnID → user message_id（事件流携带时登记；兜底经 HistoryLookup）。
	turnUser map[string]string
	// messageID → 产出该消息的 llm_request_id。
	messageProducer map[string]string
	// sessionID → 已见 turnID 集合（保持 turn 首次出现顺序）。
	turnOrder map[string][]string
	turnSeen  map[string]map[string]struct{}
}

func newCorrelationTable() *correlationTable {
	return &correlationTable{
		turnRequests:    make(map[string][]string),
		requestTurn:     make(map[string]string),
		traceRequest:    make(map[string]string),
		turnUser:        make(map[string]string),
		messageProducer: make(map[string]string),
		turnOrder:       make(map[string][]string),
		turnSeen:        make(map[string]map[string]struct{}),
	}
}

// registerStarted 登记一次请求开始。
func (c *correlationTable) registerStarted(sessionID, llmRequestID, traceID, turnID string) {
	if llmRequestID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if turnID != "" {
		c.requestTurn[llmRequestID] = turnID
		c.turnRequests[turnID] = append(c.turnRequests[turnID], llmRequestID)
		if sessionID != "" {
			seen := c.turnSeen[sessionID]
			if seen == nil {
				seen = make(map[string]struct{})
				c.turnSeen[sessionID] = seen
			}
			if _, ok := seen[turnID]; !ok {
				seen[turnID] = struct{}{}
				c.turnOrder[sessionID] = append(c.turnOrder[sessionID], turnID)
			}
		}
	}
	if traceID != "" {
		c.traceRequest[traceID] = llmRequestID
	}
}

// registerUserMessage 登记触发 turn 的 user 消息（事件流携带 message_id 时）。
func (c *correlationTable) registerUserMessage(turnID, messageID string) {
	if turnID == "" || messageID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.turnUser[turnID]; !ok {
		c.turnUser[turnID] = messageID
	}
}

// registerAssistantMessage 关联 assistant 消息与产出请求。
// messageID 为空时仅记录 turn 终态消息事件（供查询期经 HistoryLookup 回填 id）。
// 产出请求取该 turn 内最后一个已登记请求（ReAct 终步）。
func (c *correlationTable) registerAssistantMessage(turnID, traceID, messageID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	producer := ""
	if traceID != "" {
		if id, ok := c.traceRequest[traceID]; ok {
			producer = id
		}
	}
	if producer == "" && turnID != "" {
		if ids := c.turnRequests[turnID]; len(ids) > 0 {
			producer = ids[len(ids)-1]
		}
	}
	if producer == "" || messageID == "" {
		return
	}
	if _, ok := c.messageProducer[messageID]; !ok {
		c.messageProducer[messageID] = producer
	}
}

// turnRequestIDs 返回 turn 内全部请求 id（登记顺序）。
func (c *correlationTable) turnRequestIDs(turnID string) []string {
	if turnID == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := c.turnRequests[turnID]
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// producerOfMessage 返回产出该消息的请求 id（事件流登记）。
func (c *correlationTable) producerOfMessage(messageID string) (string, bool) {
	if messageID == "" {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	id, ok := c.messageProducer[messageID]
	return id, ok
}

// userMessageOfTurn 返回事件流登记的 user message id。
func (c *correlationTable) userMessageOfTurn(turnID string) (string, bool) {
	if turnID == "" {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	id, ok := c.turnUser[turnID]
	return id, ok
}

// turnIDsForSession 返回会话内 turn 的首次出现顺序。
func (c *correlationTable) turnIDsForSession(sessionID string) []string {
	if sessionID == "" {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := c.turnOrder[sessionID]
	out := make([]string, len(ids))
	copy(out, ids)
	return out
}

// sortConsumers 按请求登记顺序稳定排序消费方。
func sortConsumers(consumers []ConsumedBy) {
	sort.SliceStable(consumers, func(i, j int) bool {
		if consumers[i].StartedAt != nil && consumers[j].StartedAt != nil {
			return consumers[i].StartedAt.Before(*consumers[j].StartedAt)
		}
		return false
	})
}
