package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// sessionStoreWriteContext 为会话存储的**写**操作创建 context：与读查询一样带
// 5s 截止时间，但刻意不继承请求 ctx 的取消——客户端断开（刷新页面、关标签页、
// SSE 重连）只终止推送，不能连带丢掉「本回合已经产生的对话」，否则权威历史会
// 永久缺失整轮内容，而事件仓库里却留着完整帧序列（回放/实时视图因此对不上）。
func sessionStoreWriteContext(r *http.Request) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(sessionStoreQueryTimeout)
	base := context.Background()
	if r != nil && r.Context() != nil {
		base = context.WithoutCancel(r.Context())
	}
	return context.WithDeadline(base, deadline)
}

// persistAgentChatTurnHistory 把 agent 回合已提交的 durable 历史写回会话存储。
//
// inPlace=true 用于 turn 收尾（与既有语义一致：就地更新请求持有的 session 对象，
// 后续 result/done 帧继续使用同一份权威历史）；
// inPlace=false 用于中途 checkpoint——此时仍在写的会话对象（agent 循环可能还持有
// 它、后续 step 还要读它）不能被就地改写，改为按 durable 转录克隆一份快照落库；
// 存储层的 canonical 前缀比对会在下一次写入时把这些快照行增量续上。
func (h *Handler) persistAgentChatTurnHistory(
	r *http.Request,
	session *chat.Session,
	history []types.Message,
	contextMessages []types.Message,
	inPlace bool,
) error {
	if h == nil || h.sessionManager == nil || session == nil || len(history) == 0 {
		return nil
	}
	// 只落对话轮次；剥离请求级上下文前缀，避免多轮历史不断累积它。
	durable := stripLeadingContextMessages(history, contextMessages)
	if len(durable) == 0 {
		return nil
	}
	target := session
	if !inPlace {
		target = session.Clone()
	}
	target.ReplaceHistory(durable)
	ctx, cancel := sessionStoreWriteContext(r)
	defer cancel()
	// 与 chat.SessionActor.persistSession 同一口径：broker 的句柄别名注册表由
	// background_task / spawn_agent 经独立的会话行读改写维护，而本回合持有的
	// session/execSession 快照早于那次写入。整行写回前必须重新读取并带上，
	// 否则中途 checkpoint 与收尾落库都会抹掉本回合已交付给模型的
	// job_ref_* / session_ref_* 句柄（现场表现为 task_output 报
	// [JOB_NOT_FOUND] background job reference not found）。
	if err := mergeBrokerHandleAliasesIntoSession(ctx, h.sessionManager, target); err != nil {
		return err
	}
	return h.sessionManager.Update(ctx, target)
}

// mergeBrokerHandleAliasesIntoSession 把权威会话行里的 broker 句柄别名注册表合并进
// 即将整行写回的 target。读取失败时返回错误而不是继续写：宁可让这次落库失败
// （中途 checkpoint 会回退节流戳立即重试，收尾落库由调用方记录），也不能再用陈旧
// 快照把别名注册表覆盖掉。
func mergeBrokerHandleAliasesIntoSession(ctx context.Context, manager *chat.SessionManager, target *chat.Session) error {
	if manager == nil || target == nil || strings.TrimSpace(target.ID) == "" {
		return nil
	}
	latest, err := manager.Get(ctx, target.ID)
	if err != nil {
		return err
	}
	if latest == nil {
		return nil
	}
	aliases, ok := latest.GetContext(toolbroker.SessionHandleAliasesContextKey)
	if !ok {
		return nil
	}
	cloned, err := cloneSessionContextValue(aliases)
	if err != nil {
		return fmt.Errorf("clone broker handle aliases: %w", err)
	}
	target.SetContext(toolbroker.SessionHandleAliasesContextKey, cloned)
	return nil
}

// cloneSessionContextValue 深拷贝会话上下文值（JSON 可序列化），避免 latest 与
// target 共享同一份 map 后在后续写入里互相改写。
func cloneSessionContextValue(value interface{}) (interface{}, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var cloned interface{}
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

// agentChatHistoryCheckpointer 把 runtime HTTP agent-chat（/api/agent/chat）回合
// 中途提交的 durable 历史增量落回会话存储。
//
// 背景：ReAct 循环每次提交 durable 历史（assistant 文本、tool 结果、预算与压缩
// 改写）都会触发 OnHistoryCheckpoint；此前只有 aicli chat actor 挂了该回调，HTTP
// 链路要等整个 turn 结束才落库——长 turn（实测 20+ 分钟）期间权威历史停在起始
// 状态，按 /history 渲染的视图（刷新、新标签页、其他观察者）拿不到任何对话内容，
// 而轨迹（事件仓库）却有完整帧序列，回放与实时因此衔接不上。
//
// 契约与 actor 侧完全一致：
//   - 尽力而为：失败只记录（日志 + session.checkpoint_persist_error 事件），绝不
//     冒泡成 turn 失败；turn 收尾的 PersistFinal 仍是最终一致性保证；
//   - 节流：窗口内至多一次写入，策略复用 chat.CheckpointWindow /
//     chat.DefaultSessionCheckpointInterval（两条入口同一份实现，不各自演化）；
//   - 写入的转录与 turn 收尾完全同形：剥离请求级上下文前缀、按 durable 身份增量
//     追加，因此中途写入不会与收尾写入重复建行。
//
// 历史来源有两种，两条入口共用同一条写路径：
//   - 流式 ReAct：宿主直接持有运行中的 execSession（history 读取器）；
//   - 非流式 runtimechatcore.ExecuteNonStream：execSession 在 core 内部创建、失败
//     时不返回，因此退化为记录每次 OnCheckpoint 提交的快照。
type agentChatHistoryCheckpointer struct {
	handler         *Handler
	request         *http.Request
	session         *chat.Session
	contextMessages []types.Message
	turnID          string
	traceID         string
	window          *chat.CheckpointWindow
	// history 返回当前已提交的 durable 历史；为 nil 时用 latest 快照。
	history func() []types.Message
	mu      sync.Mutex
	latest  []types.Message
}

// newAgentChatHistoryCheckpointer 构造中途落库器。execSession 可为 nil——非流式
// 入口拿不到运行中的会话句柄，此时只有提交点快照可用（见结构体注释）。
func newAgentChatHistoryCheckpointer(
	handler *Handler,
	r *http.Request,
	session *chat.Session,
	execSession *chat.Session,
	contextMessages []types.Message,
	turnID string,
) *agentChatHistoryCheckpointer {
	checkpointer := &agentChatHistoryCheckpointer{
		handler:         handler,
		request:         r,
		session:         session,
		contextMessages: contextMessages,
		turnID:          turnID,
		window:          chat.NewCheckpointWindow(chat.DefaultSessionCheckpointInterval),
	}
	if execSession != nil {
		checkpointer.history = execSession.GetMessages
	}
	return checkpointer
}

// enabled 报告中途落库是否可用：没有持久化目标（无 session / 无存储管理器）时，
// 回调应保持零成本，也不产生任何副作用。
func (c *agentChatHistoryCheckpointer) enabled() bool {
	if c == nil || c.handler == nil || c.session == nil {
		return false
	}
	if c.handler.sessionManager == nil || c.window == nil || !c.window.Enabled() {
		return false
	}
	return true
}

// currentHistory 返回当前 durable 历史：优先读运行中的会话，其次用最近一次提交点
// 的快照（克隆一次，避免长期持有循环内部切片）。
func (c *agentChatHistoryCheckpointer) currentHistory() []types.Message {
	if c == nil {
		return nil
	}
	if c.history != nil {
		return c.history()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest
}

// rememberSnapshot 在拿不到 execSession 句柄时记录提交点快照。循环约定回调不得
// 长期持有 messages，这里按消息深拷贝一份只读副本。
func (c *agentChatHistoryCheckpointer) rememberSnapshot(messages []types.Message) {
	if c == nil || c.history != nil || len(messages) == 0 {
		return
	}
	snapshot := make([]types.Message, len(messages))
	for index := range messages {
		snapshot[index] = *messages[index].Clone()
	}
	c.mu.Lock()
	c.latest = snapshot
	c.mu.Unlock()
}

// OnCheckpoint 是 agent.LoopReActConfig.OnHistoryCheckpoint 的落点：每个 durable
// 历史提交点被调用一次，按窗口节流落库。它只读会话/快照，绝不修改循环持有的对象。
func (c *agentChatHistoryCheckpointer) OnCheckpoint(_ context.Context, messages []types.Message) {
	if !c.enabled() {
		return
	}
	c.rememberSnapshot(messages)
	undo, ok := c.window.Reserve()
	if !ok {
		return
	}
	if err := c.handler.persistAgentChatTurnHistory(
		c.request, c.session, c.currentHistory(), c.contextMessages, false,
	); err != nil {
		// 失败回退节流戳：让下一个提交点立刻重试，而不是再等一个窗口；
		// 同时上报事件，便于与 actor 侧的中途落库失败用同一套观测手段排查。
		if undo != nil {
			undo()
		}
		// 与 actor 侧同一策略：取消/超时属于正常收尾路径（turn 结束会统一落库），
		// 不为它们制造噪声；其余失败记录日志并上报事件。
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		logger.Warnf("agent chat: checkpoint turn history: %s", err)
		c.handler.publishSessionRuntimeEvent(
			"session.checkpoint_persist_error",
			c.traceID,
			c.session.ID,
			map[string]interface{}{
				"error":   err.Error(),
				"stage":   "mid_turn_checkpoint",
				"source":  "agent_chat",
				"turn_id": c.turnID,
			},
		)
	}
}

// PersistFinal 在回合收尾（成功、失败或客户端断开）写一次权威转录。
//
// 与中途 checkpoint 的区别：它不做节流、直接就地更新 session（后续 result/done
// 帧继续使用同一份历史），并把错误返回给调用方决定如何记录——turn 收尾的写入是
// 最终一致性保证，值得让调用方知道失败。
func (c *agentChatHistoryCheckpointer) PersistFinal() error {
	if c == nil || c.handler == nil || c.session == nil {
		return nil
	}
	if c.handler.sessionManager == nil {
		return nil
	}
	return c.handler.persistAgentChatTurnHistory(
		c.request, c.session, c.currentHistory(), c.contextMessages, true,
	)
}
