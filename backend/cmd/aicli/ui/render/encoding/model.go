// Package encoding 实现统一渲染编码器（RenderModel / EventEncoder）。
//
// 设计文档：
//   - 上位方案：docs/plan/aicli-event-stream-rendering-order-unified-encoder-plan.md
//   - 数据结构规格：docs/plan/aicli-event-stream-rendering-order-render-model-spec.md
//   - 接口设计：docs/plan/aicli-event-stream-rendering-order-event-encoder-api-design.md
//
// 核心思想（对齐 Codex Thread/ThreadHistoryBuilder）：渲染顺序不来自事件
// 到达顺序，而来自 RenderModel.Items 的数组位置；每个信息块（Item）带全局
// 唯一 ID 与单调 Seq；子事件通过 CauseID 锚定父块。编码器是唯一把上游事件
// 转换为模型的入口，渲染层只消费模型（或增量变更集）。
package encoding

// ItemKind 信息块类型（编码层统一枚举，对应渲染层 historyCellKind）。
type ItemKind string

const (
	KindUser       ItemKind = "user"       // 用户消息
	KindAssistant  ItemKind = "assistant"  // assistant 流式消息
	KindReasoning  ItemKind = "reasoning"  // reasoning 块
	KindSupplement ItemKind = "supplement" // 无 runtime 事件的本地补充
	// KindPriorityPrompt is the completed retained transcript for a runtime
	// approval/question that synchronously owns stdin. The request identity is
	// tracked outside RenderModel; only the final visible transcript is appended.
	KindPriorityPrompt  ItemKind = "priority_prompt"
	KindToolCall        ItemKind = "tool_call"        // 工具调用（CauseID 宿主）
	KindToolOutput      ItemKind = "tool_output"      // 工具输出（CauseID 指向 ToolCall）
	KindCommand         ItemKind = "command"          // 本地命令执行
	KindSystem          ItemKind = "system"           // 会话/诊断事件
	KindUserInteraction ItemKind = "user_interaction" // /debug、/model 等用户交互输出
)

// ItemStatus 信息块生命周期状态机：
// pending -> running -> completed / failed / canceled（终态后仅允许 remove）。
type ItemStatus string

const (
	StatusPending   ItemStatus = "pending"
	StatusRunning   ItemStatus = "running"
	StatusCompleted ItemStatus = "completed"
	StatusFailed    ItemStatus = "failed"
	StatusCanceled  ItemStatus = "canceled"
)

// Terminal 报告状态是否为终态（终态后不得再 upsert）。
func (s ItemStatus) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCanceled
}

// Item 是渲染模型的最小信息块，对应 Codex ThreadItem。
// ID/Seq/Created/Updated 全部由编码器分配；渲染层只读。
type Item struct {
	ID      string     // 全局唯一，编码器单调分配："item-{n}"
	Seq     uint64     // 提交序号（单调、仅追加语义）
	Kind    ItemKind   // 信息块类型
	CauseID string     // 父事件 id（工具输出 -> 工具调用 id）；"" 表示 top-level
	Status  ItemStatus // 生命周期状态
	Head    string     // 当前渲染内容快照（流式增量的最新文本）
	Created uint64     // 创建时的编码器时钟
	Updated uint64     // 最近一次 upsert 的编码器时钟
}

// Tail 是模型尾部指针：/debug、/model 等用户交互输出以此为锚点参与总序。
type Tail struct {
	ItemID string // 触发时刻最后一项的 ID
	Seq    uint64 // 触发时刻最后提交序号
}

// RenderModel 是渲染层唯一事实源：有序信息块集合。
// 渲染顺序 = Items 数组顺序；所有变更经 EventEncoder 发生。
type RenderModel struct {
	Items []*Item
	Tail  *Tail
}

// Clone 返回模型的深拷贝（渲染层可安全持有快照）。
func (m *RenderModel) Clone() *RenderModel {
	if m == nil {
		return nil
	}
	out := &RenderModel{Items: make([]*Item, 0, len(m.Items))}
	for _, it := range m.Items {
		if it == nil {
			continue
		}
		cp := *it
		out.Items = append(out.Items, &cp)
	}
	if m.Tail != nil {
		t := *m.Tail
		out.Tail = &t
	}
	return out
}

// Op 是变更集操作类型。
type Op int

const (
	OpAppend Op = iota // 追加新信息块
	OpUpsert           // 按 ID 更新既有信息块（找不到时编码器退化为 append）
	OpRemove           // 按 ID 移除
)

func (o Op) String() string {
	switch o {
	case OpAppend:
		return "append"
	case OpUpsert:
		return "upsert"
	case OpRemove:
		return "remove"
	default:
		return "unknown"
	}
}

// ItemChange 描述一次对单个 Item 的变更（增量渲染的最小单位）。
type ItemChange struct {
	Op       Op
	Item     *Item
	Revision uint64 // 该 Item 的修订号（upsert 递增，append 为 1）
	AfterID  string // 锚定插入：OpAppend 且非空时表示插入到该 Item 之后
	// （/debug、/model 等用户交互输出的 Tail 锚定语义，设计文档 §1.3 行 12；
	// 空表示追加到模型末尾）。
}

// ChangeSet 是编码器一次 Encode 的增量输出（对应 Codex ThreadHistoryChangeSet）。
// 同一 Item 的多次更新已按 (ID, Revision) 去重合并为最新快照。
type ChangeSet struct {
	Changes []ItemChange
	Tail    *Tail
}

// Stats 是编码器运行统计（双跑模式审计与 /debug 诊断用）。
type Stats struct {
	EncodeCount     uint64 // 已编码事件总数
	OutOfOrderCount uint64 // 检测到的乱序事件数（按流内 sequence 判定）
	DuplicateCount  uint64 // 幂等跳过数（同 ID 同内容重复 upsert）
	UnknownCount    uint64 // 未映射事件类型数（按默认策略处理）
	AppendCount     uint64
	UpsertCount     uint64
	RemoveCount     uint64
}
