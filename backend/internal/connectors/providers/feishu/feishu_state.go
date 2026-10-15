package feishu

// msg_id 回复目标记忆的跨重启持久化（connectors.StateAwareProvider）。
//
// 背景：Send 优先 reply 到「该会话最近一次入站 msg_id」（quote 效果），此前
// 只存进程内 map——重启后记忆清空，直到该会话下一条入站才有回复目标，期间
// 主动回复全部退化为 chat 直发（无引用）。宿主已具备 StateAwareProvider 接线
// （轮询启动/入站/出站三处 AttachState），本文件把记忆快照写入持久化 KV 并在
// 未命中内存时回填（对齐微信 context_token 切片 2d 的模式）。
//
// 约定：
//   - 快照键 replyTargetsStateKey = "reply_targets"：JSON map[chat]msgID
//     （按 connector 隔离——PollingState 命名空间本身按连接器隔离）；
//   - 防御上限与内存一致（maxTrackedChats，超限清空重记，快照非关键数据）；
//   - 未附着状态视图时静默跳过（与宿主未实现该能力时行为一致）。

import (
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

// replyTargetsStateKey 是回复目标快照在持久化状态里的键。
const replyTargetsStateKey = "reply_targets"

// AttachState 实现 connectors.StateAwareProvider：附着本连接器的持久化 KV
// 视图，用于回复目标快照落盘/回填。重复附着幂等（宿主每次使用前调用）。
func (p *Provider) AttachState(connectorID string, state connectors.PollingState) {
	connectorID = strings.TrimSpace(connectorID)
	if connectorID == "" || state == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.states == nil {
		p.states = make(map[string]connectors.PollingState)
	}
	p.states[connectorID] = state
	// 附着即回填：从持久化快照恢复内存记忆（内存已有值优先——进程内的
	// 记忆总是比快照新）。
	p.restoreReplyTargetsLocked(connectorID)
}

// stateFor 返回该连接器已附着的持久化状态视图（未附着返回 nil）。
func (p *Provider) stateFor(connectorID string) connectors.PollingState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.states[strings.TrimSpace(connectorID)]
}

// persistReplyTargets 把该连接器的内存回复目标快照写入持久化状态
// （JSON map[chat]msgID）。未附着状态视图时静默跳过。
func (p *Provider) persistReplyTargets(connectorID string) {
	connectorID = strings.TrimSpace(connectorID)
	if connectorID == "" {
		return
	}
	state := p.stateFor(connectorID)
	if state == nil {
		return
	}
	prefix := connectorID + "|"
	p.mu.Lock()
	snapshot := make(map[string]string, len(p.replyTargets))
	for key, messageID := range p.replyTargets {
		if chat, ok := strings.CutPrefix(key, prefix); ok && chat != "" {
			snapshot[chat] = messageID
		}
	}
	p.mu.Unlock()
	if len(snapshot) == 0 {
		return
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		p.logf("feishu: encode reply_targets snapshot failed: %v", err)
		return
	}
	if err := state.Set(replyTargetsStateKey, string(raw)); err != nil {
		p.logf("feishu: persist reply_targets failed: %v", err)
	}
}

// restoreReplyTargetsLocked 从持久化快照回填内存记忆（调用方须持 p.mu）。
// 快照缺失/损坏时静默跳过（保持空记忆，下次入站重新学习）。
func (p *Provider) restoreReplyTargetsLocked(connectorID string) {
	state := p.states[connectorID]
	if state == nil {
		return
	}
	raw, ok := state.Get(replyTargetsStateKey)
	if !ok || strings.TrimSpace(raw) == "" {
		return
	}
	var snapshot map[string]string
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		p.logf("feishu: decode reply_targets snapshot failed: %v", err)
		return
	}
	if p.replyTargets == nil {
		p.replyTargets = make(map[string]string)
	}
	for chat, messageID := range snapshot {
		chat = strings.TrimSpace(chat)
		messageID = strings.TrimSpace(messageID)
		if chat == "" || messageID == "" {
			continue
		}
		key := connectorID + "|" + chat
		if _, exists := p.replyTargets[key]; !exists {
			p.replyTargets[key] = messageID
		}
	}
}
