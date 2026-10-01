package knowledge

// context_snapshots / context_items 的语义镜像与记录器
// （06 §4 Phase 6 切片 5；04 §5 Phase 6 交付 5）。
//
// 不变量：
//   - **只记录注入条目**：stale / 低置信 / 超预算 / 被覆盖的条目只进 dropped
//     计数（写进 snapshot.budget_json），不落 context_items——这样
//     `context_items.stale=1` 的条数就是 `stale_item_injected` 本身（04 §7.3），
//     表里出现 stale=1 行即代表注入违规；
//   - **确定性命中即幂等**：snapshot id 由内容哈希派生（同一次编译重复记录不会
//     产生重复快照），item id 由 (snapshot, ref, item_type) 派生；
//   - **Degrade-Not-Fail**：记录失败绝不阻断请求；reader 角色第一次写失败后
//     置位 read_only 并静默跳过（后续不再尝试、不再报错）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// ContextItemTierHot / Warm 与 CompiledItemTier 同口径（表内取值）。
	ContextItemTierHot  = CompiledTierHot
	ContextItemTierWarm = CompiledTierWarm
)

// ContextSnapshotMeta 是一次上下文快照的请求侧元数据。
type ContextSnapshotMeta struct {
	SessionID        string
	TaskID           string
	WorkspaceID      string
	KnowledgeVersion string
	// BudgetJSON 是预算/过滤摘要（mode、预算、dropped 原因计数等），可为空串。
	BudgetJSON string
	// Now 为零值时取记录时刻。
	Now time.Time
}

// ContextItemRecord 是 context_items 一行的语义镜像（注入条目）。
type ContextItemRecord struct {
	ID          string  `json:"id"`
	ItemType    string  `json:"item_type"`
	RefID       string  `json:"ref_id"`
	Source      string  `json:"source"`
	Trust       string  `json:"trust"`
	Tokens      int     `json:"tokens"`
	Reason      string  `json:"reason"`
	Stale       bool    `json:"stale"`
	Version     string  `json:"version"`
	Confidence  float64 `json:"confidence"`
	Tier        string  `json:"tier"`
	Provisional bool    `json:"provisional"`
	Explanation string  `json:"explanation"`
}

// ContextSnapshotRecord 是一次编译快照（含其注入条目）。
type ContextSnapshotRecord struct {
	ID               string              `json:"id"`
	SessionID        string              `json:"session_id"`
	TaskID           string              `json:"task_id"`
	WorkspaceID      string              `json:"workspace_id"`
	KnowledgeVersion string              `json:"knowledge_version"`
	CompilerVersion  string              `json:"compiler_version"`
	BudgetJSON       string              `json:"budget_json"`
	CreatedAt        time.Time           `json:"created_at"`
	Items            []ContextItemRecord `json:"items"`
}

// Validate 校验落库必需字段（ID 可缺省，由派生函数补齐）。
func (r ContextSnapshotRecord) Validate() error {
	if strings.TrimSpace(r.SessionID) == "" {
		return errors.New("knowledge: context snapshot: session_id is required")
	}
	if strings.TrimSpace(r.CompilerVersion) == "" {
		return errors.New("knowledge: context snapshot: compiler_version is required")
	}
	for index, item := range r.Items {
		if strings.TrimSpace(item.ItemType) == "" {
			return fmt.Errorf("knowledge: context snapshot: item %d: item_type is required", index)
		}
		if strings.TrimSpace(item.Source) == "" {
			return fmt.Errorf("knowledge: context snapshot: item %d: source is required", index)
		}
		if item.Tokens < 0 {
			return fmt.Errorf("knowledge: context snapshot: item %d: tokens must not be negative", index)
		}
	}
	return nil
}

// ContextSnapshotID 返回 context_snapshots 行的稳定主键：同一次编译（同内容）
// 重复记录幂等，不同内容产生不同快照。
func ContextSnapshotID(sessionID, taskID, knowledgeVersion, compilerVersion string, items []ContextItemRecord) string {
	parts := []string{sessionID, taskID, knowledgeVersion, compilerVersion}
	for _, item := range items {
		parts = append(parts, item.ItemType, item.RefID, item.Reason, fmt.Sprintf("%d", item.Tokens), fmt.Sprintf("%t", item.Stale))
	}
	return "cs_" + digest(parts...)
}

// ContextItemID 返回 context_items 行的稳定主键（快照内按 ref+类型唯一；
// 编译器已按 ref 去重，因此同快照不会撞键）。
func ContextItemID(snapshotID, itemType, refID string) string {
	return "ci_" + digest(snapshotID, itemType, refID)
}

// BuildContextSnapshotRecord 把编译结果映射为快照记录（纯函数，可复算）。
//
// 只映射 **Items**（注入条目）；result.Dropped 只进 BudgetJSON（由调用方组装）。
func BuildContextSnapshotRecord(meta ContextSnapshotMeta, compilerVersion string, result CompileResult) ContextSnapshotRecord {
	if strings.TrimSpace(compilerVersion) == "" {
		compilerVersion = CompileCacheVersion
	}
	record := ContextSnapshotRecord{
		SessionID:        strings.TrimSpace(meta.SessionID),
		TaskID:           strings.TrimSpace(meta.TaskID),
		WorkspaceID:      strings.TrimSpace(meta.WorkspaceID),
		KnowledgeVersion: strings.TrimSpace(meta.KnowledgeVersion),
		CompilerVersion:  compilerVersion,
		BudgetJSON:       strings.TrimSpace(meta.BudgetJSON),
		CreatedAt:        meta.Now,
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	record.Items = make([]ContextItemRecord, 0, len(result.Items))
	for _, item := range result.Items {
		record.Items = append(record.Items, ContextItemRecord{
			ItemType:    item.ItemType,
			RefID:       item.RefID,
			Source:      string(item.Source),
			Trust:       string(item.Trust),
			Tokens:      item.Tokens,
			Reason:      item.Reason,
			Stale:       item.Stale,
			Version:     item.Version,
			Confidence:  item.Confidence,
			Tier:        CompiledItemTier(item),
			Provisional: item.Provisional,
			Explanation: item.Explanation,
		})
	}
	record.ID = ContextSnapshotID(record.SessionID, record.TaskID, record.KnowledgeVersion, record.CompilerVersion, record.Items)
	for index := range record.Items {
		record.Items[index].ID = ContextItemID(record.ID, record.Items[index].ItemType, record.Items[index].RefID)
	}
	return record
}

// ContextSnapshotStore 是快照落库所需的最小 store 接口（*sqliteStore 满足）。
type ContextSnapshotStore interface {
	RecordContextSnapshot(ctx context.Context, rec ContextSnapshotRecord) error
}

// ContextRecorderMetrics 是记录器的有界计数（无需外部监控系统即可断言）。
type ContextRecorderMetrics struct {
	Recorded  int64
	Skipped   int64
	Failed    int64
	LastError string
	// ReadOnly 为 true 表示 store 是 reader 角色，记录器已静默停用。
	ReadOnly bool
}

// ContextRecorder 把编译结果写入 context_snapshots / context_items。
type ContextRecorder struct {
	store   ContextSnapshotStore
	version string
	now     func() time.Time

	mu       sync.Mutex
	metrics  ContextRecorderMetrics
	readOnly bool
}

// NewContextRecorder 创建记录器；store 为 nil 时所有调用都是无副作用跳过。
func NewContextRecorder(store ContextSnapshotStore) *ContextRecorder {
	return &ContextRecorder{store: store, version: CompileCacheVersion, now: time.Now}
}

// Record 记录一次快照；返回是否真正写入。
//
// 契约（Degrade-Not-Fail）：无条目 / store 为 nil / reader 角色一律 (false, nil)
// 静默跳过；只有真实写入失败才返回 error（调用方记 metadata 并继续）。
func (r *ContextRecorder) Record(ctx context.Context, meta ContextSnapshotMeta, result CompileResult) (bool, error) {
	if r == nil || r.store == nil || len(result.Items) == 0 {
		if r != nil {
			r.countSkipped("")
		}
		return false, nil
	}
	r.mu.Lock()
	disabled := r.readOnly
	r.mu.Unlock()
	if disabled {
		r.countSkipped("read_only")
		return false, nil
	}

	record := BuildContextSnapshotRecord(meta, r.compilerVersion(), result)
	if err := record.Validate(); err != nil {
		r.countFailed(err)
		return false, err
	}
	if err := r.store.RecordContextSnapshot(ctx, record); err != nil {
		if errors.Is(err, ErrReadOnlyStore) {
			r.markReadOnly()
			return false, nil
		}
		r.countFailed(err)
		return false, err
	}
	r.mu.Lock()
	r.metrics.Recorded++
	r.mu.Unlock()
	return true, nil
}

// Metrics 返回计数快照。
func (r *ContextRecorder) Metrics() ContextRecorderMetrics {
	if r == nil {
		return ContextRecorderMetrics{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.metrics
}

func (r *ContextRecorder) compilerVersion() string {
	if r != nil && strings.TrimSpace(r.version) != "" {
		return r.version
	}
	return CompileCacheVersion
}

func (r *ContextRecorder) markReadOnly() {
	r.mu.Lock()
	r.readOnly = true
	r.metrics.ReadOnly = true
	r.metrics.Skipped++
	r.mu.Unlock()
}

func (r *ContextRecorder) countSkipped(reason string) {
	r.mu.Lock()
	r.metrics.Skipped++
	r.mu.Unlock()
}

func (r *ContextRecorder) countFailed(err error) {
	r.mu.Lock()
	r.metrics.Failed++
	if err != nil {
		r.metrics.LastError = err.Error()
	}
	r.mu.Unlock()
}
