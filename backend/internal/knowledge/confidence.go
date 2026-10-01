package knowledge

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Reuse Gate：04 §4.4 的 confidence 可执行定义与阈值判定（06 §4 Phase 2 W3）。
//
// 本文件只放纯函数与取值闭集：不触碰 store、不执行查询、不做 IO，因此
// 结果可复算、可单测，也便于 W4 的 Planner 直接消费：
//
//   - ComputeConfidence：把 source_weight / agreement / staleness / ambiguity
//     四个分量折成 [0,1] 的置信度（04 §4.4 的乘法公式）；
//   - CompareKnowledgeVersion：比较节点落库时的 knowledge_version（W1/W2
//     写入）与当前工作区版本（W1 的 WorkspaceVersion）；不匹配 → 直接不可用；
//   - EvaluateReuseGate：把置信度 + 作用域（同任务/跨任务）+ 写语义 + 版本
//     判定折成 ReuseDecision 与验证读取标志。
//
// 与 contextmgr 的 trust（来源可信性）不同：confidence 回答的是"这条探索记忆
// 现在还能不能复用"，两者不得混用（06 §4 W3 风险项）。per-file content_hash /
// adapter 版本的 stale 信号由调用方在准备输入前折算（StalenessSignals），
// 本 Gate 只负责 knowledge_version 维度与阈值语义。

// SourceWeight 是 04 §4.4 source_weight 的取值闭集：产生一条事实的通道权重。
type SourceWeight float64

const (
	// SourceWeightLSPResolved 是 LSP 跨文件解析结果。
	SourceWeightLSPResolved SourceWeight = 0.95
	// SourceWeightRuntimeEvidence 是运行时证据（测试/执行轨迹）。
	SourceWeightRuntimeEvidence SourceWeight = 0.90
	// SourceWeightTreeSitterResolved 是 tree-sitter 解析器产出。
	SourceWeightTreeSitterResolved SourceWeight = 0.80
	// SourceWeightTreeSitterHeuristic 是 tree-sitter 的启发式兜底产出。
	SourceWeightTreeSitterHeuristic SourceWeight = 0.65
	// SourceWeightRegexBuiltin 是内置正则适配器产出。
	SourceWeightRegexBuiltin SourceWeight = 0.55
	// SourceWeightFTSLexical 是纯词法检索命中（最弱来源）。
	SourceWeightFTSLexical SourceWeight = 0.40
)

// Valid 报告权重是否在 04 §4.4 的闭集内。
func (w SourceWeight) Valid() bool {
	switch w {
	case SourceWeightLSPResolved,
		SourceWeightRuntimeEvidence,
		SourceWeightTreeSitterResolved,
		SourceWeightTreeSitterHeuristic,
		SourceWeightRegexBuiltin,
		SourceWeightFTSLexical:
		return true
	default:
		return false
	}
}

// SourceWeightFromConfidence 把知识库现有的 Confidence 等级（refs/symbols 的
// 来源枚举）映射到 04 §4.4 的 source_weight。
//
// 与 version.go 的 Confidence.Score() 必须保持一致（单测钉住）：后者继续
// 服务于 refs.confidence 落库，本函数服务于 Reuse Gate。该枚举无法区分
// LSP 与 runtime（同属 semantic），需要更细的权重时调用方应显式传
// SourceWeightRuntimeEvidence。
func SourceWeightFromConfidence(c Confidence) SourceWeight {
	switch c {
	case ConfidenceSemantic:
		return SourceWeightLSPResolved
	case ConfidenceSyntax:
		return SourceWeightTreeSitterResolved
	case ConfidenceHeuristic:
		return SourceWeightRegexBuiltin
	default:
		return SourceWeightFTSLexical
	}
}

// AgreementFactor 是 04 §4.4 agreement_factor 的取值闭集。
type AgreementFactor float64

const (
	// AgreementMultiSource 表示多来源一致（≥2 个来源给出同一结论）。
	AgreementMultiSource AgreementFactor = 1.00
	// AgreementSingleSource 表示只有单一来源。
	AgreementSingleSource AgreementFactor = 0.90
	// AgreementConflict 表示多来源冲突（取最高 source_weight；调用方同时
	// 需要写 conflict 事件——04 §4.4；事件写入不在本文件范围）。
	AgreementConflict AgreementFactor = 0.70
)

// Valid 报告因子是否在 04 §4.4 的闭集内。
func (a AgreementFactor) Valid() bool {
	switch a {
	case AgreementMultiSource, AgreementSingleSource, AgreementConflict:
		return true
	default:
		return false
	}
}

// StalenessPenalty 是 04 §4.4 staleness_penalty 的取值闭集。
//
// adapter 与 parser 版本不一致同为 0.30（两个常量取值相同，保留两个名字是
// 为了让调用点自解释；同时命中按一次计罚，见 StalenessSignals.Penalty）。
type StalenessPenalty float64

const (
	// StalenessContentHashMismatch 表示 file.content_hash 与索引记录不一致。
	StalenessContentHashMismatch StalenessPenalty = 0.50
	// StalenessAdapterVersionMismatch 表示 adapter_version 与当前 adapter 不一致。
	StalenessAdapterVersionMismatch StalenessPenalty = 0.30
	// StalenessParserVersionMismatch 表示 parser_version 不一致。
	StalenessParserVersionMismatch StalenessPenalty = 0.30
	// StalenessKnowledgeVersionMismatch 表示 knowledge_version 不匹配：
	// staleness=1.00，置信度归零（直接不可用）。
	StalenessKnowledgeVersionMismatch StalenessPenalty = 1.00
)

// StalenessSignals 汇总一条记忆的多个 stale 信号；04 §4.4 给出单信号罚分，
// 同时命中时取最大罚分（最保守），而不是相加——同一根因（例如 adapter 升级
// 同时导致 parser 版本变化）不应被重复计罚。
type StalenessSignals struct {
	// ContentHashMismatch 是 file.content_hash 与索引记录不一致。
	ContentHashMismatch bool
	// AdapterVersionMismatch 是 adapter 版本与当前 adapter 不一致。
	AdapterVersionMismatch bool
	// ParserVersionMismatch 是 parser 版本不一致。
	ParserVersionMismatch bool
	// KnowledgeVersionMismatch 是 knowledge_version 不匹配（直接不可用）。
	KnowledgeVersionMismatch bool
}

// Penalty 返回信号集合对应的 staleness_penalty（未命中为 0）。
func (s StalenessSignals) Penalty() StalenessPenalty {
	switch {
	case s.KnowledgeVersionMismatch:
		return StalenessKnowledgeVersionMismatch
	case s.ContentHashMismatch:
		return StalenessContentHashMismatch
	case s.AdapterVersionMismatch, s.ParserVersionMismatch:
		return StalenessAdapterVersionMismatch
	default:
		return 0
	}
}

// AmbiguityPenalty 是 04 §4.4 ambiguity_penalty 的取值闭集。
type AmbiguityPenalty float64

const (
	// AmbiguityNone 表示目标唯一。
	AmbiguityNone AmbiguityPenalty = 0
	// AmbiguityResolved 表示候选目标数 > 1 但可按类型/导入消解。
	AmbiguityResolved AmbiguityPenalty = 0.10
	// AmbiguityUnresolved 表示候选目标数 > 1 且无法消解。
	AmbiguityUnresolved AmbiguityPenalty = 0.30
)

// ConfidenceInput 是 04 §4.4 公式的四个分量。
type ConfidenceInput struct {
	SourceWeight SourceWeight
	Agreement    AgreementFactor
	Staleness    StalenessPenalty
	Ambiguity    AmbiguityPenalty
}

// Validate 报告四个分量是否都在合法域内（source_weight/agreement 必须命中
// 闭集；staleness/ambiguity 必须落在 [0,1]）。它供接受外部输入的调用方使用，
// ComputeConfidence 本身不做闭集校验（见其注释）。
func (in ConfidenceInput) Validate() error {
	if !in.SourceWeight.Valid() {
		return fmt.Errorf("knowledge: confidence: source_weight %v is not in 04 §4.4 closed set", float64(in.SourceWeight))
	}
	if !in.Agreement.Valid() {
		return fmt.Errorf("knowledge: confidence: agreement %v is not in 04 §4.4 closed set", float64(in.Agreement))
	}
	if in.Staleness < 0 || in.Staleness > 1 {
		return fmt.Errorf("knowledge: confidence: staleness %v out of range [0,1]", float64(in.Staleness))
	}
	if in.Ambiguity < 0 || in.Ambiguity > 1 {
		return fmt.Errorf("knowledge: confidence: ambiguity %v out of range [0,1]", float64(in.Ambiguity))
	}
	return nil
}

// ComputeConfidence 执行 04 §4.4 的可执行公式并返回 [0,1] 内的置信度：
//
//	confidence = source_weight × agreement × (1 - staleness) × (1 - ambiguity)
//
// 语义与边界：
//   - 各分量取闭集值时乘积自然落在 [0,1]；仍做一次 clamp，防止越界输入
//     造出 >1 的置信度（越界输入应由 ConfidenceInput.Validate 显式暴露）；
//   - staleness=1.00（knowledge_version 不匹配）时结果为 0——直接不可用；
//   - NaN 按 0 处理（保守：无法计算的置信度不得被当作可用）。
func ComputeConfidence(in ConfidenceInput) float64 {
	value := float64(in.SourceWeight) *
		float64(in.Agreement) *
		(1 - float64(in.Staleness)) *
		(1 - float64(in.Ambiguity))
	return clamp01(value)
}

// clamp01 把置信度收敛到 [0,1]；NaN → 0（fail closed）。
func clamp01(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// VersionStatus 是"节点落库版本 vs 当前工作区版本"的比较结果。
type VersionStatus string

const (
	// VersionStatusMatch 表示两侧版本非空且相等：版本分量不扣分。
	VersionStatusMatch VersionStatus = "match"
	// VersionStatusMismatch 表示两侧版本非空且不等：staleness=1.00，直接不可用。
	VersionStatusMismatch VersionStatus = "mismatch"
	// VersionStatusUnknown 表示任一侧为空（未生成/未落库）：按不可用处理
	// （fail closed，不假设"没有版本=足够新鲜"）。
	VersionStatusUnknown VersionStatus = "unknown"
)

// CompareKnowledgeVersion 比较当前工作区版本与节点落库版本。
//
// 版本 token 是 W1 WorkspaceVersion 的不透明字符串，只做去空白后的相等比较，
// 不解析内部结构。
//
// 例外：**未稳定 token**（带 `#pendingN` 外部变更标记，Phase 5 交付 3）一律按
// unknown 处理。理由：pending token 的含义是"索引落后于磁盘"，它不钉住任何内容
// 状态——两侧都是 `#pending3` 并不表示"同一份知识"，只表示"当时都有 3 个待处理
// 变更"。相等即放行会让不稳定快照被当作可复用知识（fail open），因此这里
// fail closed：未稳定 → unknown → 探索。
func CompareKnowledgeVersion(current, stored string) VersionStatus {
	cur := strings.TrimSpace(current)
	prev := strings.TrimSpace(stored)
	if cur == "" || prev == "" {
		return VersionStatusUnknown
	}
	if IsVersionUnstable(cur) || IsVersionUnstable(prev) {
		return VersionStatusUnknown
	}
	if cur == prev {
		return VersionStatusMatch
	}
	return VersionStatusMismatch
}

// IsVersionUnstable 报告版本 token 是否带"索引落后于磁盘"的未稳定标记。
//
// 供复用判定（本文件）与注入前过滤（contextmgr）共用：任何拿到版本 token 的
// 决策点都应把未稳定 token 当作"不能作为可复用证据"（fail closed）。
func IsVersionUnstable(version string) bool {
	return strings.Contains(version, externalVersionPendingMarker)
}

// VersionObservation 是一次 WorkspaceVersion 采样。
//
// W2 的 ExplorationRecorder 以 30s TTL 缓存版本（defaultRecorderVersionTTL）；
// W4/W5 若复用同一缓存，应把采样时刻一并传进来，让 Gate 能做 TTL 滞后兜底。
type VersionObservation struct {
	// Version 是 WorkspaceVersion 返回的不透明 token。
	Version string
	// ObservedAt 是采样时刻；零值表示调用方不跟踪观测时间，此时退化为
	// 纯版本串比较（不做滞后兜底）。
	ObservedAt time.Time
}

// DefaultReuseVersionTTL 是版本快照的信任窗口，与 W2 Recorder 的
// defaultRecorderVersionTTL 同源（30s）：超过窗口的版本快照视为可能滞后，
// 即使版本串相等也不允许直接复用（必须补一次验证读取）。
const DefaultReuseVersionTTL = defaultRecorderVersionTTL

// ReuseScope 是复用判定的作用域：同任务工作集或跨任务。
type ReuseScope string

const (
	// ReuseScopeTask 是同任务内复用（04 §4.4：直接复用下限 0.80）。
	ReuseScopeTask ReuseScope = "task"
	// ReuseScopeCrossTask 是跨任务复用（04 §4.4：下限 0.90 且强制验证读取）。
	ReuseScopeCrossTask ReuseScope = "cross_task"
)

// Normalize 归一化作用域：未知/零值按跨任务处理（更保守的一侧）。
func (s ReuseScope) Normalize() ReuseScope {
	if s == ReuseScopeTask {
		return ReuseScopeTask
	}
	return ReuseScopeCrossTask
}

// ReuseDecision 是 Reuse Gate 的判定结果。
type ReuseDecision string

const (
	// ReuseDecisionExplore 表示不得复用（版本不可用或置信度低于硬下限）：
	// 触发探索，探索失败再 fallback 到既有 grep/view（04 §4.4）。
	ReuseDecisionExplore ReuseDecision = "explore"
	// ReuseDecisionReuseVerify 表示 0.50–直接复用阈值之间：允许复用，但必须
	// 标记"待验证"并补一次低成本确认（04 §4.4）。
	ReuseDecisionReuseVerify ReuseDecision = "reuse_verify"
	// ReuseDecisionReuse 表示达到直接复用阈值；是否需要验证读取另见
	// ReuseGateResult.Verify（写操作/跨任务/低于 verify_read_below 仍为 true）。
	ReuseDecisionReuse ReuseDecision = "reuse"
)

// Reuse Gate 的 Reason 取值（稳定 token，供 W4/W5 写入 reason 与诊断）。
const (
	// ReuseReasonOK 表示版本匹配、达到直接复用阈值且无需验证读取。
	ReuseReasonOK = "ok"
	// ReuseReasonVersionMismatch 表示节点版本与当前工作区版本不一致。
	ReuseReasonVersionMismatch = "knowledge_version_mismatch"
	// ReuseReasonVersionUnknown 表示任一侧版本缺失。
	ReuseReasonVersionUnknown = "knowledge_version_unknown"
	// ReuseReasonBelowExploreFloor 表示置信度低于硬下限，触发探索。
	ReuseReasonBelowExploreFloor = "below_explore_floor"
	// ReuseReasonProvisional 表示处于待验证带：复用但必须标记待验证。
	ReuseReasonProvisional = "provisional_reuse"
	// ReuseReasonVersionObservationLag 表示版本快照超出 TTL，需要验证读取兜底。
	ReuseReasonVersionObservationLag = "version_observation_lag"
	// ReuseReasonWriteVerify 表示写操作复用，强制一次验证读取。
	ReuseReasonWriteVerify = "write_verify"
	// ReuseReasonCrossTaskVerify 表示跨任务复用，强制验证读取。
	ReuseReasonCrossTaskVerify = "cross_task_verify"
	// ReuseReasonVerifyReadBelow 表示置信度低于 verify_read_below。
	ReuseReasonVerifyReadBelow = "verify_read_below"
)

// ReuseGateInput 是 Reuse Gate 的完整输入：全部来自已落库行（W2 产物）与
// 当前版本快照，判定过程不重算、不伪造数据。
type ReuseGateInput struct {
	// Confidence 是待判定内容的置信度：已落库节点直接取
	// ExplorationNode.Confidence（W2 写入的观察值）；需要按 04 §4.4 重算时
	// 用 ComputeConfidence 得到。
	Confidence float64
	// Scope 是同任务/跨任务作用域；零值按跨任务处理（更保守）。
	Scope ReuseScope
	// Write 表示这次复用服务于写操作（04 §4.4：≥0.90 且强制验证读取）。
	Write bool
	// StoredVersion 是节点落库时的 knowledge_version
	// （ExplorationNode.KnowledgeVersion）。
	StoredVersion string
	// Current 是当前工作区版本快照（W1 WorkspaceVersion）。
	Current VersionObservation
	// Now 是判定时刻；零值取 time.Now。
	Now time.Time
	// VersionTTL 是版本快照信任窗口；<=0 取 DefaultReuseVersionTTL。
	VersionTTL time.Duration
	// Thresholds 是阈值组；零值取 DefaultPlannerConfig。
	Thresholds PlannerConfig
}

// ReuseGateResult 是 Reuse Gate 的判定结果。
//
// 字段语义：
//   - Decision：explore / reuse_verify / reuse（见 ReuseDecision）；
//   - Usable：是否允许进入复用（注入/使用）路径。版本不匹配/未知，或置信度
//     低于 explore_below 时为 false——此时任何使用都必须计入
//     unsafe_reuse_count（04 §4.4；硬门槛为 0）；
//   - Verify：是否必须安排一次验证读取（写操作/跨任务强制；confidence <
//     verify_read_below；版本快照 TTL 滞后兜底）；
//   - Provisional：处于 [explore_below, 直接复用阈值) 的"待验证"带，注入时
//     必须写 context_items.reason（04 §4.4）；
//   - Stale：版本不可用（不匹配/未知）——直接不可用；
//   - Version：版本比较结果；
//   - Confidence：生效置信度（版本不可用时归零，避免下游误用原值）；
//   - Reason：稳定 token，主原因按 版本 → 下限 → 待验证 → 滞后 → 强制验证
//     → 验证读取 的固定优先级给出，保证同输入可复算。
type ReuseGateResult struct {
	Decision    ReuseDecision
	Usable      bool
	Verify      bool
	Provisional bool
	Stale       bool
	Version     VersionStatus
	Confidence  float64
	Reason      string
}

// EvaluateReuseGate 执行 04 §4.4 的阈值判定（纯函数、无 IO、可复算）。
//
// 判定顺序：
//  1. 版本比较：不匹配/未知 → explore、不可用、Stale=true（直接不可用）；
//  2. 置信度 < explore_below（默认 0.50）→ explore、不可用；
//  3. 达到当前作用域/写语义的直接复用阈值（ReuseFloor）→ reuse，否则
//     reuse_verify（待验证带）；
//  4. Verify 标志：写操作/跨任务强制；confidence < verify_read_below；
//     版本快照超过 TTL（滞后兜底）——任一命中即 true。
//
// 阈值边界按"== 阈值视为通过"（≥/≥），与 04 §4.4 的 "≥ 0.80 / ≥ 0.90" 一致。
func EvaluateReuseGate(in ReuseGateInput) ReuseGateResult {
	cfg := in.Thresholds.Normalize()
	scope := in.Scope.Normalize()

	result := ReuseGateResult{
		Version: CompareKnowledgeVersion(in.Current.Version, in.StoredVersion),
	}
	switch result.Version {
	case VersionStatusMatch:
		// 继续判定。
	case VersionStatusMismatch:
		result.Decision = ReuseDecisionExplore
		result.Stale = true
		result.Reason = ReuseReasonVersionMismatch
		return result
	default:
		result.Decision = ReuseDecisionExplore
		result.Stale = true
		result.Reason = ReuseReasonVersionUnknown
		return result
	}

	confidence := clamp01(in.Confidence)
	result.Confidence = confidence
	if confidence < cfg.ExploreBelow {
		result.Decision = ReuseDecisionExplore
		result.Reason = ReuseReasonBelowExploreFloor
		return result
	}

	if confidence >= cfg.ReuseFloor(scope, in.Write) {
		result.Decision = ReuseDecisionReuse
	} else {
		result.Decision = ReuseDecisionReuseVerify
		result.Provisional = true
	}

	lagging := versionObservationLagging(in.Current.ObservedAt, in.Now, in.VersionTTL)
	result.Verify = result.Provisional ||
		in.Write ||
		scope == ReuseScopeCrossTask ||
		confidence < cfg.VerifyReadBelow ||
		lagging
	result.Usable = true

	switch {
	case result.Provisional:
		result.Reason = ReuseReasonProvisional
	case lagging:
		result.Reason = ReuseReasonVersionObservationLag
	case in.Write:
		result.Reason = ReuseReasonWriteVerify
	case scope == ReuseScopeCrossTask:
		result.Reason = ReuseReasonCrossTaskVerify
	case result.Verify:
		result.Reason = ReuseReasonVerifyReadBelow
	default:
		result.Reason = ReuseReasonOK
	}
	return result
}

// versionObservationLagging 报告版本快照是否已超出信任窗口（TTL 滞后兜底）。
//
// 零值 ObservedAt 表示调用方不跟踪观测时间：此时不做滞后判定（退化为纯版本
// 串比较），避免 W4 在未接缓存时凭空增加摩擦。TTL<=0 取 DefaultReuseVersionTTL。
func versionObservationLagging(observedAt, now time.Time, ttl time.Duration) bool {
	if observedAt.IsZero() {
		return false
	}
	if ttl <= 0 {
		ttl = DefaultReuseVersionTTL
	}
	if now.IsZero() {
		now = time.Now()
	}
	return now.Sub(observedAt) >= ttl
}

// GateInput 返回该探索节点（W2 已落库行）的 Reuse Gate 输入骨架：只搬运
// confidence 与 knowledge_version，不重新计算、不伪造数据。scope / write /
// current 由调用方（W4 的 Planner）按查询路径与用途补齐。
func (n ExplorationNode) GateInput(scope ReuseScope, write bool, current VersionObservation) ReuseGateInput {
	return ReuseGateInput{
		Confidence:    n.Confidence,
		Scope:         scope,
		Write:         write,
		StoredVersion: n.KnowledgeVersion,
		Current:       current,
	}
}
