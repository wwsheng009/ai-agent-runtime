package agent

// subagent_context.go — 子级上下文模式注入（DSH 借鉴方案 P2-1'）。
//
// 设计来源：docs/plan/deepseek-harness-agent-borrowing-plan-review-v2-20261016.md
// §4 P2-1'。fork = 父级"已完成轮次的一次性快照"注入子会话初始历史。
//
// v2 方案 G5 修正后的快照对象：父级**当前有效派生请求前缀**（经 compaction
// 后的已完成轮次），而非原始日志。数据源 = 子会话 loop 的 options.History
// 注入点（loop.run 支持外部历史，scheduler 路径子级本就不持久化）——快照
// 在 fork 时刻一次性取走并冻结，此后父会话追加轮次不再到达子级。
//
// 约束（全量移植 DSH，一处有意偏离）：
//   1. 一次性快照：注入即冻结（本文件在 Build 内取值，无实时共享通道）；
//   2. 只继承对话：不继承工具面/权限/写能力（resolveChildToolSurface 照常
//      独立执行，由 capabilities 协商保证 fork 不携带路由覆盖）；
//   3. KV Cache 保持：fork 子级继承父级 provider/model（准入期拒绝路由
//      覆盖），且快照内容是父级已缓存前缀的同一形态；
//   4. （AAR 特有偏离）快照超过 MaxForkSnapshotTokens 时整体拒绝并建议
//      fresh——不静默截断历史（避免子级看到半截上下文却不知道）。

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// MaxForkSnapshotTokens fork 快照的 token 上限（对齐单轮预算的保守缺省）。
// 宿主可经 agents.fork.maxSnapshotTokens 覆盖（装配层接线）；0 用缺省。
const MaxForkSnapshotTokens = 96_000

// forkSnapshotTokenLimit 返回生效上限（<=0 时回落缺省）。
func forkSnapshotTokenLimit(configured int) int {
	if configured > 0 {
		return configured
	}
	return MaxForkSnapshotTokens
}

// stripForkSystemPrefix 去掉父级历史中的 system 消息（fork 注入的是对话
// 轮次；子级自己的 system prompt 由其 prompt_builder 独立组装，混入父级
// system 会破坏子级工具面/角色措辞）。
func stripForkSystemPrefix(messages []types.Message) []types.Message {
	out := make([]types.Message, 0, len(messages))
	for _, message := range messages {
		if strings.EqualFold(strings.TrimSpace(message.Role), "system") {
			continue
		}
		out = append(out, message)
	}
	return out
}

// forkBalancedPrefix 返回父级已配平的已完成轮次前缀：以最后一条 assistant
// 消息（含 tool 交互轮）为界的已完成对话。进行中的父级 turn 不会出现在
// parent.GetMessages() 的持久化历史里，因此这里只需再截掉"最后一条未回复
// 的 user 尾巴"（若存在）——即快照终止于最后一条 assistant/tool 消息。
func forkBalancedPrefix(messages []types.Message) []types.Message {
	balanced := stripForkSystemPrefix(messages)
	for len(balanced) > 0 {
		last := balanced[len(balanced)-1]
		role := strings.ToLower(strings.TrimSpace(last.Role))
		if role == "user" {
			balanced = balanced[:len(balanced)-1]
			continue
		}
		break
	}
	return balanced
}

// BuildForkHistory 计算 fork 子会话的初始历史。
//
// parentMessages 是父会话当前持久化消息；countTokens 用运行时的消息 token
// 估算。快照为空（父级尚无已完成轮次）时返回 nil——fork 等价 fresh，与
// DSH "首轮 fork 无继承历史" 一致。
//
// 返回错误仅在快照超限时：整体拒绝（建议 fresh），绝不静默截断。
func BuildForkHistory(parentMessages []types.Message, countTokens func([]types.Message) int, maxSnapshotTokens int) ([]types.Message, error) {
	prefix := forkBalancedPrefix(parentMessages)
	if len(prefix) == 0 {
		return nil, nil
	}
	limit := forkSnapshotTokenLimit(maxSnapshotTokens)
	// 无运行时时用保守回退估算：token 上限在任何宿主上都必须生效，
	// 不能因缺少计数器而静默跳过（G5：拒绝而非截断，拒绝本身也要无条件）。
	counter := countTokens
	if counter == nil {
		counter = forkSnapshotFallbackTokens
	}
	if used := counter(prefix); used > limit {
		return nil, fmt.Errorf(
			"fork context snapshot exceeds the token ceiling (%d > %d); next_action=use_fresh_context_or_raise_agents.fork.max_snapshot_tokens",
			used, limit,
		)
	}
	// 深拷贝：快照一次性冻结，父会话后续改写历史不影响子级视角。
	snapshot := make([]types.Message, 0, len(prefix))
	for _, message := range prefix {
		clone := message.Clone()
		if clone == nil {
			continue
		}
		snapshot = append(snapshot, *clone)
	}
	return snapshot, nil
}

// forkSnapshotRuntimeTokens 用运行时估算消息 token（nil 安全）；返回 nil 时
// BuildForkHistory 回退到 forkSnapshotFallbackTokens。
func forkSnapshotRuntimeTokens(runtime *llm.LLMRuntime) func([]types.Message) int {
	if runtime == nil {
		return nil
	}
	return func(messages []types.Message) int {
		return runtime.CountMessagesTokens(messages)
	}
}

// forkSnapshotFallbackTokens 是 runtime 缺席时的保守估算（≈符文数/4，加每条
// 消息的固定开销），量级与常规 token 估算器一致，只用于让上限判定在无 LLM
// 运行时的宿主上仍然生效。
func forkSnapshotFallbackTokens(messages []types.Message) int {
	total := 0
	for _, message := range messages {
		total += len([]rune(message.Content))/4 + 4
	}
	return total
}

// subagentTasksRequestFork 报告批次中是否有任务请求 context=fork。
// 协调器据此决定是否加载父会话历史（普通批次不支付该读取成本）。
func subagentTasksRequestFork(tasks []SubagentTask) bool {
	for _, task := range tasks {
		if strings.TrimSpace(task.Context) == SubagentContextFork {
			return true
		}
	}
	return false
}
