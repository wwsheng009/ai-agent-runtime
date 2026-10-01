package knowledge

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 认知层的复用/探索决策（06 §4 Phase 2 W4；语义定义见 04 §4.4 / §4.5）。
//
// 本文件分三层：
//
//   - EvaluatePlan 是纯函数内核：输入（查询/目标/版本观测/候选节点）完全决定
//     输出，不触 IO、不读时钟，因此可表驱动测试、同输入可复算；
//   - Planner（接口）+ NewPlanner 是 store 注入版实现：只读探索记忆，
//     任何失败（store 不可用 / 查询超时 / 上下文取消）都折成 Degraded + Reason，
//     绝不向调用方冒泡错误（04 §4.1 Degrade-Not-Fail）；
//   - Layer.Plan 是层入口：nil / off 返回空 Plan，reader 角色可用，workspace
//     未登记（索引不可用）时返回 Degraded。
//
// Planner 只产出决策、不执行动作：confidence < 0.90 的 Reuse 项带 Verify=true，
// 由调用方（W5 / agent 循环）用既有 grep / view 完成一次低成本验证读取；
// 本 Phase 不依赖 code.*（06 §4 W4 目标）。

// DefaultMinKnowledgeQueryLength 是 Planner 认为"值得查知识库"的最短查询长度
// （按 rune 计，CJK 与 ASCII 同口径）。它是防退化下界，不是典型查询长度。
//
// W7 校准依据（2026-09-30，有界演练：真实 grep/view 调用 n=385 条回放，见
// reports/phase2_exploration_report.md §校准）：实测查询长度中位数 57 rune、
// 95% CI [49,62]、p95 91、min 1；8 远低于实测分布主体，只拦极短查询，
// 实测未给出上调证据（上调会削弱复用召回）。真实 on-mode A/B 跑完后按
// 04 §7.6 复核；W5 可用 PlanInput.MinQueryLength 覆盖。
const DefaultMinKnowledgeQueryLength = 8

// Plan 的稳定 Reason token（供 W5 注入 metadata 与诊断；不得随版本漂移）。
const (
	// PlanReasonOK 表示决策正常产出（存在可复用项，或探索项已给出）。
	PlanReasonOK = "ok"
	// PlanReasonDisabled 表示知识层未启用（nil / off）。
	PlanReasonDisabled = "disabled"
	// PlanReasonQueryTooShort 表示查询短于最小长度，跳过知识层（不查 store）。
	PlanReasonQueryTooShort = "query_too_short"
	// PlanReasonNoCandidates 表示索引可用但没有命中任何探索记忆。
	PlanReasonNoCandidates = "no_candidates"
	// PlanReasonStoreUnavailable 表示 store 不可用（nil / 报错 / 取消）。
	PlanReasonStoreUnavailable = "store_unavailable"
	// PlanReasonStoreTimeout 表示 store 查询超时（deadline exceeded）。
	PlanReasonStoreTimeout = "store_timeout"
	// PlanReasonIndexUnavailable 表示 workspace 尚未登记（索引不可用）。
	PlanReasonIndexUnavailable = "index_unavailable"
	// PlanReasonInvalidInput 表示输入缺少必需字段（如 WorkspaceID）。
	PlanReasonInvalidInput = "invalid_input"
)

// PlanInput 是一次规划请求：查询/目标 + 复用作用域 + 版本观测。
//
// Query 必填：它既是最小长度门槛的判定对象，也是无候选时 Explore 的目标。
// Target 是可选精确查找键；WorkspaceID 由 Layer.Plan 补齐。
type PlanInput struct {
	// WorkspaceID 限定工作区（隔离边界）；Layer.Plan 会用层登记的 id 补齐。
	WorkspaceID string `json:"workspace_id"`
	// TaskID 是当前任务；同任务作用域用它圈定工作集。
	TaskID string `json:"task_id,omitempty"`
	// SessionID 是当前会话；无任务锚点（TaskID 为空）时按会话级工作集检索
	// （该会话下 task_id 为空的节点），与 W2 写入侧的回退口径一致——避免
	// 把自然语言查询当精确 target 而零命中。
	SessionID string `json:"session_id,omitempty"`
	// Query 是本次规划的自然语言查询/目标；必填。
	Query string `json:"query"`
	// Target 是可选的精确查找目标（workspace 相对路径 / 符号名 / 已记录的
	// 查询摘要）。为空时以 Query 作为精确查找键。
	Target string `json:"target,omitempty"`
	// Scope 是复用作用域；零值按跨任务（更保守）。
	// Scope=task 但 TaskID 与 SessionID 都为空时同样按跨任务处理
	// （无法证明同工作集归属）。
	Scope ReuseScope `json:"scope,omitempty"`
	// Write 表示本次复用服务于写操作（阈值更高且强制验证读取）。
	Write bool `json:"write,omitempty"`
	// Current 是当前工作区版本观测（W1 WorkspaceVersion + 采样时刻）；
	// 为空时 Layer.Plan 按 TTL 采样补齐，直接调用 Planner 则按版本未知处理。
	Current VersionObservation `json:"current"`
	// Now 是判定时刻；零值由注入时钟补齐。
	Now time.Time `json:"now,omitempty"`
	// VersionTTL 是版本观测信任窗口；<=0 取 DefaultReuseVersionTTL。
	VersionTTL time.Duration `json:"version_ttl,omitempty"`
	// Limit 限制候选节点数；<=0 用 store 默认上限。
	Limit int `json:"limit,omitempty"`
	// MinQueryLength 覆盖最小查询长度；<=0 取 DefaultMinKnowledgeQueryLength。
	MinQueryLength int `json:"min_query_length,omitempty"`
}

// ReuseItem 是一条可直接复用的探索记忆（带 confidence + knowledge_version）。
//
// Verify 为 true 表示必须安排一次验证读取：写操作 / 跨任务强制；confidence
// < verify_read_below（默认 0.90）；版本观测超出 TTL（滞后兜底）——任一命中。
// 调用方不得在 Verify=true 时把该条目当作"已确认事实"注入（04 §4.4）。
type ReuseItem struct {
	NodeID           string     `json:"node_id"`
	NodeType         NodeType   `json:"node_type"`
	Target           string     `json:"target"`
	Summary          string     `json:"summary,omitempty"`
	Confidence       float64    `json:"confidence"`
	KnowledgeVersion string     `json:"knowledge_version"`
	Scope            ReuseScope `json:"scope"`
	Verify           bool       `json:"verify"`
	Provisional      bool       `json:"provisional"`
	Reason           string     `json:"reason"`
}

// ExploreItem 是一条需要重新探索的目标：不可复用的候选（版本不匹配/未知、
// 低于硬下限）或完全没有命中时的查询本身。
type ExploreItem struct {
	Target   string   `json:"target"`
	NodeType NodeType `json:"node_type,omitempty"`
	Reason   string   `json:"reason"`
}

// Plan 是 Planner 的输出（04 §4.5）。
//
// Degraded=true 时 Reuse/Explore 必为空：知识层不可用，调用方走原有路径
// （04 §4.1 Degrade-Not-Fail）。Reason 是稳定的 plan/item 级 token。
type Plan struct {
	Reuse    []ReuseItem   `json:"reuse,omitempty"`
	Explore  []ExploreItem `json:"explore,omitempty"`
	Degraded bool          `json:"degraded"`
	Reason   string        `json:"reason"`
}

// Planner 是认知层的决策出口（04 §4.5）。实现必须纯读、可复算；
// Plan 不执行任何探索/写入，也不冒泡 IO 错误（失败折成 Degraded）。
type Planner interface {
	Plan(ctx context.Context, in PlanInput) (Plan, error)
}

// ExplorationNodeReader 是 Planner 所需的最小 store 读接口（Store 的子集）。
// 以窄接口注入，单测无需构造完整 Store。
type ExplorationNodeReader interface {
	LookupExplorationNodes(ctx context.Context, q ExplorationNodeQuery) ([]ExplorationNode, error)
}

// PlannerOptions 组装一个 store 版 Planner。
type PlannerOptions struct {
	// Reader 是探索记忆读句柄；nil 时 Plan 直接返回 Degraded。
	Reader ExplorationNodeReader
	// Config 是 W3 的阈值组；零值取 DefaultPlannerConfig。
	Config PlannerConfig
	// Now 便于测试注入时钟；nil 时用 time.Now。
	Now func() time.Time
	// VersionTTL 是版本观测信任窗口；<=0 取 DefaultReuseVersionTTL。
	VersionTTL time.Duration
	// MinQueryLength 是最小查询长度；<=0 取 DefaultMinKnowledgeQueryLength。
	MinQueryLength int
}

// normalize 补齐零值；幂等。
func (o PlannerOptions) normalize() PlannerOptions {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.VersionTTL <= 0 {
		o.VersionTTL = DefaultReuseVersionTTL
	}
	if o.MinQueryLength <= 0 {
		o.MinQueryLength = DefaultMinKnowledgeQueryLength
	}
	return o
}

// NewPlanner 返回 store 注入版 Planner：只读探索记忆，失败折成 Degraded。
func NewPlanner(opts PlannerOptions) Planner {
	return &storePlanner{opts: opts.normalize()}
}

type storePlanner struct{ opts PlannerOptions }

// Plan 执行一次只读规划：查询探索记忆 → 交给 EvaluatePlan 判定。
//
// 错误语义（DoD：Degraded=true 且不冒泡错误）：所有失败都返回
// (Plan{Degraded:true, Reason:...}, nil)，绝不把 store/超时错误抛给调用方。
func (p *storePlanner) Plan(ctx context.Context, in PlanInput) (Plan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if p == nil || p.opts.Reader == nil {
		return Plan{Degraded: true, Reason: PlanReasonStoreUnavailable}, nil
	}
	opts := p.opts.normalize()
	if in.Now.IsZero() {
		in.Now = opts.Now()
	}
	if in.VersionTTL <= 0 {
		in.VersionTTL = opts.VersionTTL
	}
	if in.MinQueryLength <= 0 {
		in.MinQueryLength = opts.MinQueryLength
	}
	if strings.TrimSpace(in.WorkspaceID) == "" {
		return Plan{Degraded: true, Reason: PlanReasonInvalidInput}, nil
	}
	if !queryMeetsMinLength(in.Query, in.MinQueryLength) {
		// 查询太短：不查 store，知识层让位给原有路径。
		return Plan{Reason: PlanReasonQueryTooShort}, nil
	}

	nodes, err := opts.Reader.LookupExplorationNodes(ctx, planLookupQuery(in))
	if err != nil {
		return Plan{Degraded: true, Reason: classifyStoreError(ctx, err)}, nil
	}
	return EvaluatePlan(in, nodes, opts.Config), nil
}

// queryMeetsMinLength 报告查询是否达到最小长度（按 rune 计；min<=0 取默认）。
func queryMeetsMinLength(query string, min int) bool {
	if min <= 0 {
		min = DefaultMinKnowledgeQueryLength
	}
	return utf8.RuneCountInString(strings.TrimSpace(query)) >= min
}

// planLookupTarget 返回精确查找键：Target 优先，为空时退回 Query。
func planLookupTarget(in PlanInput) string {
	if target := strings.TrimSpace(in.Target); target != "" {
		return target
	}
	return strings.TrimSpace(in.Query)
}

// effectiveScope 归一化复用作用域：显式 task 但既无 TaskID 也无 SessionID 时
// 按跨任务处理（无法证明同工作集归属，取更保守的阈值一侧）。
func effectiveScope(in PlanInput) ReuseScope {
	scope := in.Scope.Normalize()
	if scope == ReuseScopeTask && strings.TrimSpace(in.TaskID) == "" && strings.TrimSpace(in.SessionID) == "" {
		return ReuseScopeCrossTask
	}
	return scope
}

// planLookupQuery 选择 store 检索路径（04 §4.4 的三条读取路径）：
//   - 任务工作集：同任务作用域且 TaskID 非空（s.task_id 精确匹配）；
//   - 会话级工作集：同任务作用域、TaskID 为空但 SessionID 非空（该会话下
//     task_id 为空的节点，W1 DTO 回退口径）；
//   - 跨任务：其余情况按 Target（优先）或 Query 精确匹配。
func planLookupQuery(in PlanInput) ExplorationNodeQuery {
	q := ExplorationNodeQuery{WorkspaceID: in.WorkspaceID, Limit: in.Limit}
	if effectiveScope(in) == ReuseScopeTask {
		if taskID := strings.TrimSpace(in.TaskID); taskID != "" {
			q.TaskID = taskID
			return q
		}
		if sessionID := strings.TrimSpace(in.SessionID); sessionID != "" {
			q.SessionID = sessionID
			return q
		}
	}
	q.Target = planLookupTarget(in)
	return q
}

// classifyStoreError 把 store/上下文错误折成稳定的降级 token。
func classifyStoreError(ctx context.Context, err error) string {
	if errors.Is(err, context.DeadlineExceeded) ||
		(ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return PlanReasonStoreTimeout
	}
	return PlanReasonStoreUnavailable
}

// EvaluatePlan 是纯函数内核：给定输入与候选节点，返回确定性 Plan。
//
// 判定顺序（与 04 §4.4 阈值策略一致）：
//  1. 查询短于 MinQueryLength → 空 Plan（query_too_short），不判定候选；
//  2. 逐条候选走 W3 的 EvaluateReuseGate：可用（含待验证带）→ Reuse 项；
//     版本不匹配/未知、低于硬下限 → Explore 项（同 target 去重，保留最保守
//     的原因）；
//  3. 完全没有候选 → Explore 一个查询目标（no_candidates）。
//
// 确定性：候选先按 confidence 降序、target 升序、id 升序排序；同 target 的
// Reuse 只保留最优一条；Explore 按首次出现顺序输出。同输入两次调用结果一致。
// Plan 级 Reason：有可复用项 → ok；无候选 → no_candidates；否则取失败原因中
// 优先级最高者（版本不匹配 > 版本未知 > 低于硬下限）。
func EvaluatePlan(in PlanInput, candidates []ExplorationNode, cfg PlannerConfig) Plan {
	minLength := in.MinQueryLength
	if minLength <= 0 {
		minLength = DefaultMinKnowledgeQueryLength
	}
	if !queryMeetsMinLength(in.Query, minLength) {
		return Plan{Reason: PlanReasonQueryTooShort}
	}

	scope := effectiveScope(in)
	ordered := orderPlanCandidates(candidates)

	var reuse []ReuseItem
	var explore []ExploreItem
	exploreIndex := make(map[string]int, len(ordered))
	reuseTargets := make(map[string]bool, len(ordered))
	failureReason := ""
	failurePriority := 0

	for _, node := range ordered {
		gate := EvaluateReuseGate(ReuseGateInput{
			Confidence:    node.Confidence,
			Scope:         scope,
			Write:         in.Write,
			StoredVersion: node.KnowledgeVersion,
			Current:       in.Current,
			Now:           in.Now,
			VersionTTL:    in.VersionTTL,
			Thresholds:    cfg,
		})
		if gate.Usable {
			if !reuseTargets[node.Target] {
				reuseTargets[node.Target] = true
				reuse = append(reuse, ReuseItem{
					NodeID:           node.ID,
					NodeType:         node.NodeType,
					Target:           node.Target,
					Summary:          node.Summary,
					Confidence:       gate.Confidence,
					KnowledgeVersion: node.KnowledgeVersion,
					Scope:            scope,
					Verify:           gate.Verify,
					Provisional:      gate.Provisional,
					Reason:           gate.Reason,
				})
			}
			continue
		}

		// 不可用 → 探索该目标；同 target 保留最保守（优先级最高）的原因。
		if idx, ok := exploreIndex[node.Target]; ok {
			if planFailurePriority(gate.Reason) > planFailurePriority(explore[idx].Reason) {
				explore[idx].Reason = gate.Reason
			}
		} else {
			exploreIndex[node.Target] = len(explore)
			explore = append(explore, ExploreItem{
				Target:   node.Target,
				NodeType: node.NodeType,
				Reason:   gate.Reason,
			})
		}
		if priority := planFailurePriority(gate.Reason); priority > failurePriority {
			failurePriority = priority
			failureReason = gate.Reason
		}
	}

	switch {
	case len(reuse) > 0:
		return Plan{Reuse: reuse, Explore: explore, Reason: PlanReasonOK}
	case len(candidates) == 0:
		return Plan{
			Explore: []ExploreItem{{Target: planLookupTarget(in), Reason: PlanReasonNoCandidates}},
			Reason:  PlanReasonNoCandidates,
		}
	default:
		return Plan{Explore: explore, Reason: failureReason}
	}
}

// orderPlanCandidates 复制候选并做确定性排序：confidence 降序（最优在前），
// 其次 target / id 升序。不改写调用方切片。
func orderPlanCandidates(candidates []ExplorationNode) []ExplorationNode {
	ordered := make([]ExplorationNode, len(candidates))
	copy(ordered, candidates)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Confidence != ordered[j].Confidence {
			return ordered[i].Confidence > ordered[j].Confidence
		}
		if ordered[i].Target != ordered[j].Target {
			return ordered[i].Target < ordered[j].Target
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

// planFailurePriority 给出失败原因在 Plan 级 Reason 中的优先级：
// 版本不匹配 > 版本未知 > 低于硬下限 > 其他。数值越大越优先。
func planFailurePriority(reason string) int {
	switch reason {
	case ReuseReasonVersionMismatch:
		return 4
	case ReuseReasonVersionUnknown:
		return 3
	case ReuseReasonBelowExploreFloor:
		return 2
	default:
		return 1
	}
}

// versionCache 是 Layer 级的 WorkspaceVersion 采样缓存（TTL 与 W2 Recorder
// 同源）。版本计算要扫全量文件行，不能每次 Plan 都重算；缓存命中时把原始
// ObservedAt 一并返回，让 W3 Gate 的 TTL 滞后兜底仍然生效（过期快照即使
// 版本串相等也会被要求补一次验证读取）。
type versionCache struct {
	mu         sync.Mutex
	version    string
	observedAt time.Time
	// generation 是采样时的索引代次（ChangeQueue.Generation）：索引每落地一次
	// 代次 +1，旧代次的缓存立即失效——避免"失效后又被写回旧版本"的竞态。
	generation uint64
}

// invalidate 丢弃缓存，强制下一次 observe 重新采样。
// 索引落地（ChangeQueue.OnResult）或校正发现外部变更时调用：版本缓存是
// "30s 内不必重算"的优化，但索引已经变化时必须立即重算，否则复用判定
// 会拿到被 TTL 压住的旧版本（Phase 5 交付 3）。
func (c *versionCache) invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.version = ""
	c.observedAt = time.Time{}
	c.mu.Unlock()
}

// observe 返回当前版本观测：TTL 内**且代次未变**时复用缓存，否则重新采样并刷新缓存。
// 采样失败不返回陈旧值（store 已不可信，调用方应走 Degraded）。
func (c *versionCache) observe(ctx context.Context, store Store, workspaceID string, ttl time.Duration, now time.Time, generation uint64) (VersionObservation, error) {
	if ttl <= 0 {
		ttl = DefaultReuseVersionTTL
	}
	if now.IsZero() {
		now = time.Now()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.version != "" && !c.observedAt.IsZero() && c.generation == generation && now.Sub(c.observedAt) < ttl {
		return VersionObservation{Version: c.version, ObservedAt: c.observedAt}, nil
	}
	version, err := WorkspaceVersion(ctx, store, workspaceID)
	if err != nil {
		return VersionObservation{}, err
	}
	c.version = version
	c.observedAt = now
	c.generation = generation
	return VersionObservation{Version: version, ObservedAt: now}, nil
}
