package knowledge

import (
	"fmt"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// Counters 是知识层的度量计数器，字段与 `usageledger` 的 9 个知识层列一一对应
// （04 §5 交付 2 / §7.2 / §7.3）。
//
// 它是**纯数据**：Phase 0 只冻结契约，没有任何代码路径会累加它；Phase 1 起由
// shadow 索引与 code.* 工具面写入，再由 usageledger 落库，从而回答
// “每个任务平均多少 token 花在探索 / 重复读取”。
//
// 零值表示“知识层未参与”，与 mode=off 时的落库结果一致。
type Counters struct {
	// ExplorationTokens 是花在“找路”上的 token（搜索、列举、重复打开文件）。
	ExplorationTokens int
	// ReuseTokens 是复用既有结论（索引命中、缓存）省下的 token。
	ReuseTokens int
	// IndexLookupCount 是查询知识库的次数（命中率分母）。
	IndexLookupCount int
	// IndexHit 是命中并可直接使用的次数（命中率分子）。
	IndexHit int
	// FallbackCount 是知识库不可用/未命中而回退到既有工具的调用次数。
	FallbackCount int
	// UnsafeReuseCount 是复用 stale 或低置信内容的次数（硬门槛：必须为 0）。
	UnsafeReuseCount int
	// ToolCallsPerTask 是该任务的工具调用数（护栏：不得因知识层而增加）。
	ToolCallsPerTask int
	// RepeatedReadCount 是同一 session 内对同一 file/symbol 的重复读取次数。
	RepeatedReadCount int
	// KnowledgeVersionMismatchCount 是版本不一致却仍被使用的次数（硬门槛：必须为 0）。
	KnowledgeVersionMismatchCount int
}

// Add 逐字段累加 other，返回新值；接收者不被修改。
func (c Counters) Add(other Counters) Counters {
	return Counters{
		ExplorationTokens:             c.ExplorationTokens + other.ExplorationTokens,
		ReuseTokens:                   c.ReuseTokens + other.ReuseTokens,
		IndexLookupCount:              c.IndexLookupCount + other.IndexLookupCount,
		IndexHit:                      c.IndexHit + other.IndexHit,
		FallbackCount:                 c.FallbackCount + other.FallbackCount,
		UnsafeReuseCount:              c.UnsafeReuseCount + other.UnsafeReuseCount,
		ToolCallsPerTask:              c.ToolCallsPerTask + other.ToolCallsPerTask,
		RepeatedReadCount:             c.RepeatedReadCount + other.RepeatedReadCount,
		KnowledgeVersionMismatchCount: c.KnowledgeVersionMismatchCount + other.KnowledgeVersionMismatchCount,
	}
}

// IsZero 报告所有计数器是否都为 0（即知识层未参与）。
func (c Counters) IsZero() bool { return c == Counters{} }

// IndexHitRate 返回索引命中率（04 §7.2，目标 ≥ 70%）。分母为 0 时返回 0，
// 避免把“没查过”误报成“全miss”。
func (c Counters) IndexHitRate() float64 {
	if c.IndexLookupCount <= 0 {
		return 0
	}
	return float64(c.IndexHit) / float64(c.IndexLookupCount)
}

// FallbackRate 返回回退率（04 §7.2，目标 ≤ 30%）。分母为 0 时返回 0。
func (c Counters) FallbackRate() float64 {
	if c.IndexLookupCount <= 0 {
		return 0
	}
	return float64(c.FallbackCount) / float64(c.IndexLookupCount)
}

// SafetyViolations 返回被违反的硬门槛名称（04 §7.3）：两个“必须为 0”的计数器
// 非零即违反。返回空切片表示通过。
func (c Counters) SafetyViolations() []string {
	violations := make([]string, 0, 2)
	if c.UnsafeReuseCount != 0 {
		violations = append(violations,
			fmt.Sprintf("unsafe_reuse_count=%d (必须为 0)", c.UnsafeReuseCount))
	}
	if c.KnowledgeVersionMismatchCount != 0 {
		violations = append(violations,
			fmt.Sprintf("knowledge_version_mismatch_count=%d (必须为 0)", c.KnowledgeVersionMismatchCount))
	}
	return violations
}

// Apply 把计数器写入 ledger 记录。这是度量从知识层流向 usageledger 的唯一出口；
// 零值计数器不改变记录（mode=off 时 ledger 与改动前逐字节一致）。
func (c Counters) Apply(record *entity.TokenUsageHistory) {
	if record == nil {
		return
	}
	record.ExplorationTokens += c.ExplorationTokens
	record.ReuseTokens += c.ReuseTokens
	record.IndexLookupCount += c.IndexLookupCount
	record.IndexHit += c.IndexHit
	record.FallbackCount += c.FallbackCount
	record.UnsafeReuseCount += c.UnsafeReuseCount
	record.ToolCallsPerTask += c.ToolCallsPerTask
	record.RepeatedReadCount += c.RepeatedReadCount
	record.KnowledgeVersionMismatchCount += c.KnowledgeVersionMismatchCount
}

// Recorder 是并发安全的 Counters 累加器。
//
// 一个任务（或一个 session）持有一个 Recorder，结束时 Snapshot 并 Apply 到
// ledger 记录。Recorder 本身不做任何 I/O，也不决定何时采样——采样点属于
// Phase 1+ 的实现细节。
type Recorder struct {
	mu       sync.Mutex
	counters Counters
}

// NewRecorder 创建空累加器。
func NewRecorder() *Recorder { return &Recorder{} }

// Add 累加一组计数。
func (r *Recorder) Add(c Counters) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters = r.counters.Add(c)
}

// RecordIndexLookup 记录一次索引查询及其是否命中。
func (r *Recorder) RecordIndexLookup(hit bool) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters.IndexLookupCount++
	if hit {
		r.counters.IndexHit++
	}
}

// RecordFallback 记录一次回退到既有工具。
func (r *Recorder) RecordFallback() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters.FallbackCount++
}

// RecordRepeatedRead 记录一次对同一 file/symbol 的重复读取。
func (r *Recorder) RecordRepeatedRead() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters.RepeatedReadCount++
}

// RecordUnsafeReuse 记录一次不安全复用（stale 或低置信内容）。硬门槛要求
// 最终 Snapshot 的该计数为 0。
func (r *Recorder) RecordUnsafeReuse() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters.UnsafeReuseCount++
}

// RecordVersionMismatch 记录一次版本不一致却仍被使用。硬门槛要求为 0。
func (r *Recorder) RecordVersionMismatch() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters.KnowledgeVersionMismatchCount++
}

// Snapshot 返回当前计数副本。
func (r *Recorder) Snapshot() Counters {
	if r == nil {
		return Counters{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counters
}

// Reset 清零计数。
func (r *Recorder) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counters = Counters{}
}

// ApplyTo 把当前计数写入 ledger 记录（不重置计数）。
func (r *Recorder) ApplyTo(record *entity.TokenUsageHistory) {
	if r == nil {
		return
	}
	r.Snapshot().Apply(record)
}
