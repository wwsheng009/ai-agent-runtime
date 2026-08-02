// Package boundary 实现统一 block boundary 与 gap policy（unified plan §7）。
//
// 设计文档：
//   - 上位方案：docs/plan/aicli-tui-unified-render-architecture-refactor-plan.md
//     （§3.3 block 与 gap 不变量、§7 统一 block boundary 与 gap policy）
//   - 上游衔接：渲染模型（encoding.RenderModel）只提供有序信息块；相邻
//     top-level cell 之间的语义 gap 由本包纯函数生成，渲染层调用点不得
//     自行拼接空行（INV-GAP-03）。
//
// 核心思想：gap 是两个 top-level transcript cell 之间的语义边界，不是某个
// cell 尾部保存的空字符串，也不是"上一次是否打印过完整 block"的全局布尔值。
// 本包只做一件事：根据相邻 cell 的元数据决定 0/1 个 gap row。
package boundary

// BoundaryClass 是 cell 的边界类别（unified plan §7.1）。
type BoundaryClass uint8

const (
	// BoundaryDense 与相邻 cell 稠密衔接（无 gap row）。
	BoundaryDense BoundaryClass = iota
	// BoundaryNormal 独立 top-level cell 之间的常规边界（1 个 gap row）。
	BoundaryNormal
	// BoundarySection 显式分节边界。实现初期不输出多于 1 的 gap row；
	// 若未来确需 section divider，应引入显式 separator cell/style，
	// 不能把多个不可追踪空行塞进 history。
	BoundarySection
)

// CellKind 是 transcript cell 的最小语义分类（unified plan §5.2 Kind 的投影）。
// 本包只需要参与 gap 决策的分类；内容渲染细节由渲染层持有。
type CellKind uint8

const (
	KindUser         CellKind = iota // 用户消息
	KindAssistant                    // assistant 流式消息（含 reasoning/supplement 归属）
	KindTool                         // 工具调用/工具输出（tool chain）
	KindCommand                      // 本地命令执行结果
	KindSystem                       // 会话/诊断/notice/warning
)

// CellMeta 是 ResolveGap 所需的相邻 cell 元数据视图。
//
// 它是 unified plan TranscriptCell 的最小投影：Scene 终局落地后由
// TranscriptCell 派生，本包不依赖完整 Scene 结构，保持纯函数可单测。
type CellMeta struct {
	ID       string     // 稳定 cell 身份；空表示"无前项"
	Kind     CellKind   // 语义分类
	TopLevel bool       // 是否独立 top-level cell（tool-chain 内部成员为 false）
	ChainKey string     // 同一 tool-chain 的归组键（并行工具输出归组到父调用）；
	// 非空且与相邻 cell 相等 → 链内稠密（gap 0）
	Boundary BoundaryClass // 边界类别（默认 BoundaryNormal）
	Mutable  bool          // 是否 mutable cell（update/finalize 期间）
}

// GapRows 是相邻 cell 之间的语义 gap 行数。实现初期只允许 0 或 1
// （unified plan §7.1：GapRows 只允许 0 或 1）。
type GapRows int

const (
	GapNone GapRows = 0 // 稠密：无空行
	GapOne  GapRows = 1 // 独立 top-level cell 之间的一个语义 gap
)

// ResolveGap 根据相邻 cell 元数据计算语义 gap（unified plan §7.3 规则表）。
//
// 规则表（任何组合都满足 INV-GAP-02：最多一个语义 gap）：
//
//	| 前一项 | 后一项 | gap | 说明 |
//	| --- | --- | ---: | --- |
//	| 无 | 任意首 cell | 0 | transcript 不以空行开头 |
//	| user | assistant | 1 | 独立 top-level 对话块 |
//	| assistant | user | 1 | turn 边界 |
//	| 任意 committed top-level | 独立 command/system/notice | 1 | 最多一个语义 gap |
//	| 同一 tool-chain cell 内的 tool events | 下一 tool event | 0 | cell 内稠密 |
//	| 独立 final tool/event cell | 下一独立 final cell | 1 | top-level boundary |
//	| supplement/reasoning 属于同一 assistant cell | 同 cell 后续 section | 0 | 不读取全局 gap 状态 |
//	| mutable cell revision N | revision N+1 | 不适用 | replace，不创建边界 |
//	| mutable cell | 同 ID finalization | 不新增 | replace/commit transaction |
//	| ActiveBand/status/prompt/popup | 任意 transcript cell | 不参与 | overlay 不能改变 transcript gap |
//	| filtered/empty event | 任意 | 不参与 | 不推进 boundary |
//	| replay cell | replay next cell | 与 live 相同 | 禁止 replay 特例 |
//	| handoff range | retained next cell | 保持原 boundary | handoff 不重新计算业务顺序 |
//
// 调用约定（与规则表配合）：
//   - ActiveBand/status/prompt/popup、filtered/empty event 不调用本函数
//     （INV-GAP-05：不推进 boundary state）；
//   - replay 与 handoff 复用同一函数，禁止特例分支（规则表末两行）。
func ResolveGap(prev, next CellMeta) GapRows {
	// 首 cell：transcript 不以空行开头。
	if prev.ID == "" {
		return GapNone
	}
	// 同 ID 的 mutable update / finalize 是 replace/commit transaction，
	// 不创建新边界（规则表：mutable rev N -> N+1 不适用；同 ID finalize 不新增）。
	if prev.ID == next.ID {
		return GapNone
	}
	// 同一 tool-chain 内稠密：并行工具输出归组到父调用后，链内成员
	// 之间不插入 gap（规则表：同一 tool-chain cell 内的 tool events -> 0）。
	if prev.ChainKey != "" && prev.ChainKey == next.ChainKey {
		return GapNone
	}
	// supplement/reasoning 属于同一 assistant cell：其后续 section 由
	// cell 内 layout 决定，默认稠密（规则表：同 cell 后续 section -> 0）。
	// 本包通过 ChainKey（assistant 流身份）表达归属；无 ChainKey 的
	// 独立 supplement 按 top-level 处理。
	//
	// 其余全部组合：独立 top-level cell 之间最多一个语义 gap
	// （INV-GAP-02 / 规则表 user->assistant、assistant->user、
	//  committed top-level -> command/system、独立 final -> 独立 final）。
	return GapOne
}
