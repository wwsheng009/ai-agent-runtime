package ui

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"

// transcript_residency_status.go —— §15.2 的状态分区类型（T8b-5）。
//
// 口径约定：
//   - 本文件的 section 由 `TranscriptResidencyStatsSnapshot` / 端点现场填充；
//   - 无法归因的子系统**不出现在** JSON 里，而是登记到 `unavailable`
//     （宁可缺席也不要零值伪装成"健康"）；
//   - 字节口径是**估算**（source + 结构/呈现开销），用于分区归因与趋势对照，
//     不是 RSS 精确值（§15.3 的验收判定以"分区可归因"为前提）。

// TranscriptResidencyResidentStats 是 L1 驻留窗口（bridge scene 现场量取）。
type TranscriptResidencyResidentStats struct {
	Cells          int    `json:"cells"`
	Rows           int    `json:"rows"`
	Bytes          int64  `json:"bytes"`
	WindowStartSeq uint64 `json:"window_start_seq"`
	Generation     uint64 `json:"generation"`
}

// TranscriptResidencyColdStats 是已写穿/已裁剪的冷区累计（§14.2 ColdLedger 口径）。
type TranscriptResidencyColdStats struct {
	Cells         int      `json:"cells"`
	Bytes         int64    `json:"bytes"`
	DeliveredRows int64    `json:"delivered_rows"`
	Generations   []uint64 `json:"generations,omitempty"`
}

// TranscriptResidencyIndexStats 是 L2 索引摘要（prefix_fence/fold_target/revision）。
type TranscriptResidencyIndexStats struct {
	PrefixFence uint64 `json:"prefix_fence"`
	FoldTarget  uint64 `json:"fold_target"`
	Revision    uint64 `json:"revision"`
}

// TranscriptResidencyEvictionStats 是逐出计数与拒绝原因。
type TranscriptResidencyEvictionStats struct {
	Total          uint64            `json:"total"`
	LastReason     string            `json:"last_reason,omitempty"`
	PinnedByReason map[string]uint64 `json:"pinned_by_reason,omitempty"`
}

// TranscriptResidencyRematerializeStats 是重物化/回读计数（含在飞与耗时）。
type TranscriptResidencyRematerializeStats struct {
	Total      uint64 `json:"total"`
	MaxMs      uint64 `json:"max_ms"`
	Failures   uint64 `json:"failures"`
	BytesTotal int64  `json:"bytes_total"`
	Inflight   int64  `json:"inflight"`
}

// TranscriptResidencyBodyStoreStats 是写穿存储计数（store 自报口径）。
type TranscriptResidencyBodyStoreStats struct {
	Puts      uint64 `json:"puts"`
	Gets      uint64 `json:"gets"`
	PutErrors uint64 `json:"put_errors"`
	GetMisses uint64 `json:"get_misses"`
}

// TranscriptResidencyMemoryStats 是 §15.3 的分区内存口径（字节估算）。
type TranscriptResidencyMemoryStats struct {
	SceneCellsBytes      int64 `json:"scene_cells_bytes"`
	AppStateCellsBytes   int64 `json:"appstate_cells_bytes"`
	LedgerBytes          int64 `json:"ledger_bytes"`
	SeedUnitsBytes       int64 `json:"seed_units_bytes"`
	SessionMessagesBytes int64 `json:"session_messages_bytes"`
	RenderCacheBytes     int64 `json:"render_cache_bytes"`
	// ResumeHistoryBytes 是「展示历史快照」（ChatSession.ResumeHistory）的驻留
	// 口径。它与 session_messages_bytes（模型上下文）是两个独立数据面：前者供
	// 屏幕重放，后者是发给模型的投影。历史上该分区从未被计量，导致 18k 条展示
	// 历史常驻无法在 §15.3 中归因。装载交付后会裁剪到固定上限（512 条）。
	ResumeHistoryBytes int64 `json:"resume_history_bytes"`
	// ResumeHistoryMessages / ResumeHistoryTrimmed 是计数口径：条数与已裁剪
	// 条数（裁剪不得静默，见 §15.3 的"降级必须显式"约束）。
	ResumeHistoryMessages int `json:"resume_history_messages,omitempty"`
	ResumeHistoryTrimmed  int `json:"resume_history_trimmed,omitempty"`
	// LedgerEntries / SessionMessages 是计数口径：字节不可得时至少给出规模，
	// 让"未说明的线性增长"无法藏身（§15.3）。
	LedgerEntries   int `json:"ledger_entries,omitempty"`
	SessionMessages int `json:"session_messages,omitempty"`
}

// EstimateTranscriptCellBytes 是单个 cell 的字节估算（§15.3 口径）。
//
// 组成：Source 字节 + 固定结构开销（ID/Revision/Phase/boundary 键/时间戳等）
// + 呈现开销（结构化文档按行数与跨度粗估）。用途是**分区归因与趋势**：
// 绝对值有偏（Go 运行时开销、切片容量、字符串头未计），但同一口径下
// 子系统之间的比例与随会话的增长可对照。
func EstimateTranscriptCellBytes(cell *scene.TranscriptCell) int64 {
	if cell == nil {
		return 0
	}
	const fixedOverhead = 128
	bytes := int64(len(cell.Source)) + fixedOverhead
	bytes += int64(len(cell.ChainKey) + len(cell.BoundaryGroupKey) + len(cell.Provenance))
	for _, block := range cell.Presentation.Document.Blocks {
		bytes += 64
		for _, line := range block.Lines {
			bytes += 32
			for _, span := range line.Spans {
				bytes += int64(len(span.Text)) + 16
			}
		}
	}
	return bytes
}

// EstimateResidentSceneCellsBytes 估算 bridge scene 快照的 cell 字节总量。
func EstimateResidentSceneCellsBytes(cells []*scene.TranscriptCell) int64 {
	var total int64
	for _, cell := range cells {
		total += EstimateTranscriptCellBytes(cell)
	}
	return total
}

// EstimateAppStateCellsBytes 估算 AppState 语义 transcript 的 cell 字节总量。
func EstimateAppStateCellsBytes(cells []scene.TranscriptCell) int64 {
	var total int64
	for index := range cells {
		total += EstimateTranscriptCellBytes(&cells[index])
	}
	return total
}
