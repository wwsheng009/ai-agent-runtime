package knowledge

// Context Compiler（06 §4 Phase 6 交付 1/4/5；语义见 03 §14、02 §58）。
//
// 本文件是 Phase 6 的语义内核：把知识层输出（Planner 的复用项，以及后续切片
// 接入的代码智能候选）编译为**最小、安全、可解释**的上下文条目——即
// `context_items` 的镜像（item_type / ref_id / source / trust / version /
// reason / stale / tokens）。
//
// 边界：
//   - **纯函数、无 IO、可复算**：同输入必然同输出（不读时钟/环境/随机数）；
//   - **只读**：编译不写库；快照落库是 Phase 6 切片 6 的职责；
//   - **stale 绝不进入可注入集合**：04 §7.3 硬门槛 `stale_item_injected=0`
//     的内核保证（注入前仍有 contextmgr 的第二道防线）；
//   - **trust 与 confidence 是两个维度**：trust 回答"来源可不可信"（03 §14.3），
//     confidence 回答"这条记忆还能不能复用"（04 §4.4）。两者不得互相替代，
//     也不得混用（confidence.go 头注；06 §4 W3 风险项）。
//
// 冲突解决口径（03 §14.4）：LSP > 自定义语义适配器 > Tree-sitter > 启发式 >
// FTS5 > 正则。本实现直接复用 04 §4.4 的 `SourceWeight` 取值闭集作为优先级
// （04 是落地口径的事实源；supplement 14 §14.4 的顺序示意与闭集在 FTS/Regex
// 两项上的先后不一致，以 04 为准，见 Phase 6 报告"登记"节）。

import (
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// 信任等级（03 §14.3 闭集）
// ---------------------------------------------------------------------------

// TrustLevel 是 03 §14.3 的信任等级闭集。
type TrustLevel string

const (
	// TrustSystem 是系统提示/运行时约束（最高信任）。
	TrustSystem TrustLevel = "SYSTEM"
	// TrustTrustedTool 是可信工具的输出（本仓库自有的索引/检索通道）。
	TrustTrustedTool TrustLevel = "TRUSTED_TOOL"
	// TrustCodeIntelligence 是代码智能产出（探索记忆、符号/引用事实）。
	TrustCodeIntelligence TrustLevel = "CODE_INTELLIGENCE"
	// TrustUntrustedTool 是不可信工具输出：**永不注入**（03 §14.5 规则 5）。
	TrustUntrustedTool TrustLevel = "UNTRUSTED_TOOL"
	// TrustUserContent 是用户内容（用户意图，不作为"证据"覆盖代码事实）。
	TrustUserContent TrustLevel = "USER_CONTENT"
	// TrustCodeComment 是代码注释/字符串里的文本（提示注入的高风险面）。
	TrustCodeComment TrustLevel = "CODE_COMMENT"
	// TrustGenerated 是模型生成内容（不得作为事实证据复用）。
	TrustGenerated TrustLevel = "GENERATED"
)

// trustLevelOrder 固定闭集顺序（文档/测试共用；不参与排序语义）。
var trustLevelOrder = []TrustLevel{
	TrustSystem,
	TrustTrustedTool,
	TrustCodeIntelligence,
	TrustUntrustedTool,
	TrustUserContent,
	TrustCodeComment,
	TrustGenerated,
}

// TrustLevels 返回信任等级闭集副本。
func TrustLevels() []TrustLevel {
	out := make([]TrustLevel, len(trustLevelOrder))
	copy(out, trustLevelOrder)
	return out
}

// Valid 报告取值是否在闭集内；零值不是合法等级。
func (t TrustLevel) Valid() bool {
	for _, level := range trustLevelOrder {
		if t == level {
			return true
		}
	}
	return false
}

// Injectable 报告该等级是否允许进入 prompt。
//
// 规则（03 §14.5）：不可信工具输出永不注入；未知等级 fail closed（不注入）。
// 低信任等级**可以**注入，但不得覆盖高信任内容（由 ResolveConflicts 保证）。
func (t TrustLevel) Injectable() bool {
	switch t {
	case TrustSystem, TrustTrustedTool, TrustCodeIntelligence, TrustUserContent, TrustCodeComment, TrustGenerated:
		return true
	default:
		return false
	}
}

// DBTrust 映射到 `context_items.trust` 的 v1 取值（high|medium|low|untrusted）。
//
// 未知/零值等级 fail closed 到 untrusted：宁可不可注入，也不得被当作高信任。
func (t TrustLevel) DBTrust() string {
	switch t {
	case TrustSystem, TrustTrustedTool:
		return "high"
	case TrustCodeIntelligence, TrustUserContent:
		return "medium"
	case TrustCodeComment, TrustGenerated:
		return "low"
	default:
		return "untrusted"
	}
}

// ---------------------------------------------------------------------------
// 来源与冲突优先级（03 §14.4；权重取自 04 §4.4 闭集）
// ---------------------------------------------------------------------------

// SourceClass 是 `context_items.source` 的取值闭集。
type SourceClass string

const (
	// SourceClassLSP 是 LSP 跨文件解析结果（语义通道，最高优先级）。
	SourceClassLSP SourceClass = "lsp"
	// SourceClassRuntimeEvidence 是运行时证据（测试/执行轨迹）。
	SourceClassRuntimeEvidence SourceClass = "runtime_evidence"
	// SourceClassTreeSitter 是 tree-sitter 解析产出。
	SourceClassTreeSitter SourceClass = "tree-sitter"
	// SourceClassHeuristic 是启发式兜底产出（含 tree-sitter 的启发式回退）。
	SourceClassHeuristic SourceClass = "heuristic"
	// SourceClassRegex 是内置正则适配器产出。
	SourceClassRegex SourceClass = "regex"
	// SourceClassFTS 是纯词法检索命中（最弱的代码智能来源）。
	SourceClassFTS SourceClass = "fts"
	// SourceClassMemory 是探索记忆（复用项）。
	SourceClassMemory SourceClass = "memory"
	// SourceClassArtifact 是工具产物（压缩后的观察值）。
	SourceClassArtifact SourceClass = "artifact"
	// SourceClassFact 是长期事实/笔记。
	SourceClassFact SourceClass = "fact"
)

// sourceClassOrder 固定闭集顺序（文档/测试共用）。
var sourceClassOrder = []SourceClass{
	SourceClassLSP,
	SourceClassRuntimeEvidence,
	SourceClassTreeSitter,
	SourceClassHeuristic,
	SourceClassRegex,
	SourceClassFTS,
	SourceClassMemory,
	SourceClassArtifact,
	SourceClassFact,
}

// SourceClasses 返回来源闭集副本。
func SourceClasses() []SourceClass {
	out := make([]SourceClass, len(sourceClassOrder))
	copy(out, sourceClassOrder)
	return out
}

// Valid 报告取值是否在闭集内；零值不是合法来源。
func (s SourceClass) Valid() bool {
	for _, class := range sourceClassOrder {
		if s == class {
			return true
		}
	}
	return false
}

// ConflictPriority 返回 03 §14.4 的冲突解决优先级（越大越优先）。
//
// 只对"同一目标的代码智能断言"竞争有效：lsp/runtime_evidence/tree-sitter/
// heuristic/regex/fts 返回其 04 §4.4 source_weight；memory/artifact/fact 是
// 独立通道（聚合由 Planner/调用方负责），返回 0 表示不参与该序。
func (s SourceClass) ConflictPriority() float64 {
	switch s {
	case SourceClassLSP:
		return float64(SourceWeightLSPResolved)
	case SourceClassRuntimeEvidence:
		return float64(SourceWeightRuntimeEvidence)
	case SourceClassTreeSitter:
		return float64(SourceWeightTreeSitterResolved)
	case SourceClassHeuristic:
		return float64(SourceWeightTreeSitterHeuristic)
	case SourceClassRegex:
		return float64(SourceWeightRegexBuiltin)
	case SourceClassFTS:
		return float64(SourceWeightFTSLexical)
	default:
		return 0
	}
}

// ---------------------------------------------------------------------------
// 编译输入 / 输出
// ---------------------------------------------------------------------------

// 稳定 Reason token（供 metadata / context_items.reason / 审计；不随版本漂移）。
const (
	// CompileReasonOK 表示编译正常产出（Items/Explore 已给出）。
	CompileReasonOK = "ok"
	// CompileReasonDegraded 表示 Plan 降级：零编译、零注入（Degrade-Not-Fail）。
	CompileReasonDegraded = "degraded"
	// CompileReasonStale 表示条目因 stale/版本未知/未稳定被丢弃。
	CompileReasonStale = "stale"
	// CompileReasonBelowFloor 表示条目低于置信度下限被丢弃。
	CompileReasonBelowFloor = "below_confidence_floor"
	// CompileReasonUntrusted 表示条目来源不可注入（信任等级 fail closed）。
	CompileReasonUntrusted = "untrusted_source"
	// CompileReasonInvalid 表示条目缺必需字段（ref/target），不可编译。
	CompileReasonInvalid = "invalid_item"
	// CompileReasonBudget 表示条目超出 token 预算被截断。
	CompileReasonBudget = "budget_truncated"
	// CompileReasonOverridden 表示条目被更高优先级的同目标断言覆盖（03 §14.4）。
	CompileReasonOverridden = "overridden_by_higher_priority_source"
)

// CompileRequest 是一次上下文编译请求。
//
// Plan 必填；其余为可选策略。零值语义：
//   - TokenBudget <= 0 表示不按预算截断（由调用方决定是否设置上限）；
//   - ConfidenceFloor <= 0 表示不设置信度下限（stale 规则仍然生效）。
type CompileRequest struct {
	Plan            Plan
	TokenBudget     int
	ConfidenceFloor float64
}

// CompiledItem 是编译后的一条上下文条目：`context_items` 的语义镜像。
//
// Stale 恒为 false 才允许出现在 CompileResult.Items 中；被丢弃的条目
// （stale/低置信/超预算/被覆盖）连同 DropReason 进入 CompileResult.Dropped，
// 供可解释性与审计使用，绝不进入 prompt。
type CompiledItem struct {
	ItemType    string      `json:"item_type"`
	RefID       string      `json:"ref_id"`
	Target      string      `json:"target"`
	Source      SourceClass `json:"source"`
	Trust       TrustLevel  `json:"trust"`
	Version     string      `json:"version,omitempty"`
	Confidence  float64     `json:"confidence"`
	Reason      string      `json:"reason,omitempty"`
	Stale       bool        `json:"stale"`
	Verify      bool        `json:"verify,omitempty"`
	Provisional bool        `json:"provisional,omitempty"`
	Tokens      int         `json:"tokens"`
	Content     string      `json:"content,omitempty"`
	Explanation string      `json:"explanation,omitempty"`
}

// DroppedItem 是一条未进入可注入集合的条目及其原因。
type DroppedItem struct {
	CompiledItem
	DropReason string `json:"drop_reason"`
}

// CompileResult 是一次编译的输出。
type CompileResult struct {
	// Items 是**可注入**集合：Stale 恒为 false，已按预算截断。
	Items []CompiledItem `json:"items,omitempty"`
	// Explore 透传 Planner 的探索目标：不进 prompt，由调用方执行探索。
	Explore []ExploreItem `json:"explore,omitempty"`
	// Dropped 是未注入条目（含原因），供可解释性/审计。
	Dropped []DroppedItem `json:"dropped,omitempty"`
	// Reason 是本次编译的稳定 token。
	Reason string `json:"reason"`
	// CacheHit 表示结果来自 compile 层缓存（切片 2）；编译内核恒为 false。
	CacheHit bool `json:"cache_hit,omitempty"`
}

// ---------------------------------------------------------------------------
// 编译内核
// ---------------------------------------------------------------------------

const (
	// CompiledTierHot 是「高置信、无需验证」的直接复用条目（进入下一请求主承载）。
	CompiledTierHot = "hot"
	// CompiledTierWarm 是需验证读取 / 暂定 / 置信度未达 hot 阈值的条目。
	CompiledTierWarm = "warm"
	// CompiledTierHotConfidence 是 hot 阈值：与 Verify 读取阈值同口径（0.90）。
	CompiledTierHotConfidence = 0.90
)

// CompiledItemTier 把**注入条目**映射到 hot/warm（04 §5 Phase 6 交付 2）。
//
// cold 只属于未注入条目（dropped：stale/低置信/超预算/被覆盖），不出现在
// CompiledResult.Items 里；调用方用 dropped 计数承载 cold 一侧。
func CompiledItemTier(item CompiledItem) string {
	if item.Verify || item.Provisional || item.Confidence < CompiledTierHotConfidence {
		return CompiledTierWarm
	}
	return CompiledTierHot
}

// DefaultCompileItemOverhead 是单条条目进入 prompt 的固定开销上界：data block
// 块头（type/source/trust/version/ref/stale/reason）+ 块体脚手架 + 起止标签。
//
// 取值覆盖典型上界（version ≤64 / ref ≤64 rune）：块头 ≈260 + 块体脚手架 ≈60，
// 取整 320。预算按「内容 rune + 本开销」计，保证**渲染后的整条消息**不超预算
// （与 contextmgr 的 1 rune ≈ 1 token 上界同口径；Phase 6 切片 3 校准）。
const DefaultCompileItemOverhead = 320

// IsReuseItemStale 是复用项的**规范 stale 判据**（contextmgr 注入前的第二道
// 防线与编译器共用同一语义）：
//
//   - 版本为空：无从证明内容状态 → stale；
//   - 未稳定 token（带 `#pendingN`，Phase 5 交付 3）：索引落后于磁盘 → stale；
//   - Reason 为版本不匹配/未知：复用判定已给出保守结论 → stale。
func IsReuseItemStale(item ReuseItem) bool {
	version := strings.TrimSpace(item.KnowledgeVersion)
	if version == "" {
		return true
	}
	if IsVersionUnstable(version) {
		return true
	}
	switch item.Reason {
	case ReuseReasonVersionMismatch, ReuseReasonVersionUnknown:
		return true
	default:
		return false
	}
}

// CompilePlan 把一次 Plan 编译为最小上下文。纯函数、可复算。
//
// 编译规则（按序）：
//  1. Plan.Degraded → 零产出（Reason=degraded）；
//  2. 复用项：缺 ref/target 丢弃（invalid_item）；stale 丢弃（stale）；
//     低于置信度下限丢弃（below_confidence_floor）；
//  3. 信任等级不可注入 → 丢弃（untrusted_source）；
//  4. 同目标代码智能断言冲突 → 保留最高优先级，其余丢弃（overridden...）；
//  5. token 预算截断：确定性排序（confidence 降序 → tokens 升序 → ref_id 升序）
//     后取前缀，其余丢弃（budget_truncated）。
func CompilePlan(req CompileRequest) CompileResult {
	result := CompileResult{Reason: CompileReasonOK}
	plan := req.Plan
	if plan.Degraded {
		result.Reason = CompileReasonDegraded
		return result
	}
	result.Explore = append(result.Explore, plan.Explore...)

	items := make([]CompiledItem, 0, len(plan.Reuse))
	for _, reuse := range plan.Reuse {
		item, dropReason := compileReuseItem(reuse, req.ConfidenceFloor)
		if dropReason != "" {
			item.Stale = dropReason == CompileReasonStale
			result.Dropped = append(result.Dropped, DroppedItem{CompiledItem: item, DropReason: dropReason})
			continue
		}
		items = append(items, item)
	}

	items, overridden := ResolveConflicts(items)
	result.Dropped = append(result.Dropped, overridden...)

	items, truncated := applyTokenBudget(items, req.TokenBudget)
	result.Dropped = append(result.Dropped, truncated...)

	result.Items = items
	if len(items) == 0 && len(result.Dropped) > 0 {
		result.Reason = result.Dropped[0].DropReason
	}
	return result
}

// compileReuseItem 编译单条复用项；返回空 dropReason 表示可注入。
func compileReuseItem(reuse ReuseItem, floor float64) (CompiledItem, string) {
	item := CompiledItem{
		ItemType:    "exploration",
		RefID:       strings.TrimSpace(reuse.NodeID),
		Target:      strings.TrimSpace(reuse.Target),
		Source:      SourceClassMemory,
		Trust:       TrustCodeIntelligence,
		Version:     strings.TrimSpace(reuse.KnowledgeVersion),
		Confidence:  reuse.Confidence,
		Reason:      reuse.Reason,
		Verify:      reuse.Verify,
		Provisional: reuse.Provisional,
		Content:     strings.TrimSpace(reuse.Summary),
	}
	item.Tokens = estimateCompiledTokens(item.Content)
	item.Explanation = reuseExplanation(reuse)

	if item.RefID == "" || item.Target == "" {
		return item, CompileReasonInvalid
	}
	if IsReuseItemStale(reuse) {
		return item, CompileReasonStale
	}
	if floor > 0 && (math.IsNaN(item.Confidence) || item.Confidence < floor) {
		return item, CompileReasonBelowFloor
	}
	if !item.Trust.Injectable() {
		return item, CompileReasonUntrusted
	}
	return item, ""
}

// reuseExplanation 生成可解释性说明（context_items.explanation 的语义来源）。
func reuseExplanation(reuse ReuseItem) string {
	parts := make([]string, 0, 4)
	parts = append(parts, "node_type="+string(reuse.NodeType))
	if reuse.Scope != "" {
		parts = append(parts, "scope="+string(reuse.Scope))
	}
	if reuse.Verify {
		parts = append(parts, "verify=true")
	}
	if reuse.Provisional {
		parts = append(parts, "provisional=true")
	}
	return strings.Join(parts, " ")
}

// ResolveConflicts 执行 03 §14.4 的冲突解决：同一 (ItemType, Target) 上来自
// 不同**代码智能来源**（ConflictPriority > 0）的断言只保留最高优先级一条，
// 其余进入 dropped（reason=overridden...）。非竞争来源（memory/artifact/fact）
// 不参与该序，原样保留。
//
// 优先级相同时保留 Confidence 更高者，再相同则保留 RefID 更小者（确定性）；
// 完全相同的重复断言只保留首次出现的一条。输出保持输入顺序（可复算）。
func ResolveConflicts(items []CompiledItem) ([]CompiledItem, []DroppedItem) {
	type groupKey struct {
		itemType string
		target   string
	}
	winner := make(map[groupKey]CompiledItem)
	for _, item := range items {
		if !itemCompetes(item) {
			continue
		}
		key := groupKey{itemType: item.ItemType, target: item.Target}
		current, seen := winner[key]
		if !seen || preferCompiled(item, current) {
			winner[key] = item
		}
	}

	kept := make([]CompiledItem, 0, len(items))
	dropped := make([]DroppedItem, 0)
	consumed := make(map[groupKey]bool)
	for _, item := range items {
		if !itemCompetes(item) {
			kept = append(kept, item)
			continue
		}
		key := groupKey{itemType: item.ItemType, target: item.Target}
		if !consumed[key] && sameCompiled(item, winner[key]) {
			consumed[key] = true
			kept = append(kept, item)
			continue
		}
		dropped = append(dropped, DroppedItem{CompiledItem: item, DropReason: CompileReasonOverridden})
	}
	return kept, dropped
}

// itemCompetes 报告条目是否参与 03 §14.4 的冲突解决（代码智能来源 + 有目标）。
func itemCompetes(item CompiledItem) bool {
	return item.Source.ConflictPriority() > 0 && strings.TrimSpace(item.Target) != ""
}

// sameCompiled 报告两条编译条目是否等价（用于识别胜者实例）。
func sameCompiled(a, b CompiledItem) bool {
	return a.ItemType == b.ItemType && a.Target == b.Target && a.Source == b.Source &&
		a.RefID == b.RefID && a.Version == b.Version && a.Content == b.Content &&
		a.Confidence == b.Confidence && a.Tokens == b.Tokens
}

// preferCompiled 报告 candidate 是否应替换 current（确定性决胜）。
func preferCompiled(candidate, current CompiledItem) bool {
	cp, pp := candidate.Source.ConflictPriority(), current.Source.ConflictPriority()
	if cp != pp {
		return cp > pp
	}
	if candidate.Confidence != current.Confidence {
		return candidate.Confidence > current.Confidence
	}
	return candidate.RefID < current.RefID
}

// applyTokenBudget 按预算截断：确定性排序后取前缀。
//
// 排序键：Confidence 降序 → Tokens 升序 → RefID 升序（保证同输入同输出）。
// budget <= 0 表示不截断。
func applyTokenBudget(items []CompiledItem, budget int) ([]CompiledItem, []DroppedItem) {
	if budget <= 0 || len(items) == 0 {
		return items, nil
	}
	ordered := make([]CompiledItem, len(items))
	copy(ordered, items)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		if a.Confidence != b.Confidence {
			return a.Confidence > b.Confidence
		}
		if a.Tokens != b.Tokens {
			return a.Tokens < b.Tokens
		}
		return a.RefID < b.RefID
	})
	used := 0
	kept := make([]CompiledItem, 0, len(ordered))
	dropped := make([]DroppedItem, 0)
	for _, item := range ordered {
		if used+item.Tokens > budget {
			dropped = append(dropped, DroppedItem{CompiledItem: item, DropReason: CompileReasonBudget})
			continue
		}
		used += item.Tokens
		kept = append(kept, item)
	}
	return kept, dropped
}

// estimateCompiledTokens 估算一条条目的 token 上界：1 rune ≈ 1 token（与
// contextmgr 同口径）+ 固定包裹开销。
func estimateCompiledTokens(content string) int {
	return utf8.RuneCountInString(content) + DefaultCompileItemOverhead
}

// ---------------------------------------------------------------------------
// data block 渲染（03 §14.5 规则 2/4）
// ---------------------------------------------------------------------------

// RenderDataBlock 把一条条目渲染为 data block：代码与工具输出进入 Context 前
// 必须包裹为数据块（不得作为 instruction）；块头携带来源与版本（规则 4）。
//
// 内容中的 `</data` 序列会被中性化（防提前闭合块）。
func RenderDataBlock(item CompiledItem) string {
	var builder strings.Builder
	builder.WriteString("<data")
	writeDataAttr(&builder, "type", item.ItemType)
	writeDataAttr(&builder, "source", string(item.Source))
	writeDataAttr(&builder, "trust", string(item.Trust))
	if item.Version != "" {
		writeDataAttr(&builder, "version", item.Version)
	}
	if item.RefID != "" {
		writeDataAttr(&builder, "ref", item.RefID)
	}
	builder.WriteString(" stale=\"false\"")
	if item.Reason != "" {
		writeDataAttr(&builder, "reason", item.Reason)
	}
	builder.WriteString(">\n")
	builder.WriteString(sanitizeDataContent(item.Content))
	builder.WriteString("\n</data>")
	return builder.String()
}

// writeDataAttr 写一个转义后的属性（防属性注入）。
func writeDataAttr(builder *strings.Builder, name, value string) {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(value)
	builder.WriteString(" ")
	builder.WriteString(name)
	builder.WriteString(`="`)
	builder.WriteString(escaped)
	builder.WriteString(`"`)
}

// sanitizeDataContent 中性化内容里的块闭合序列（大小写不敏感）。
func sanitizeDataContent(content string) string {
	lower := strings.ToLower(content)
	if !strings.Contains(lower, "</data") {
		return content
	}
	var builder strings.Builder
	for i := 0; i < len(content); {
		if i+6 <= len(content) && strings.EqualFold(content[i:i+6], "</data") {
			builder.WriteString(`<\/data`)
			i += 6
			continue
		}
		builder.WriteByte(content[i])
		i++
	}
	return builder.String()
}
