package usageanalytics

import "time"

// ============================================================================
// 采集健康快照（方案 §4 批次 3.2）——只读增量，不改既有方法签名/语义。
//
// 行数与最近写入时间全部由 usage_* 表现算：多进程共享同一分析库时，反映的
// 是库级真值而不是某一个进程的计数器；只读降级（库/表缺失）时不报错，
// 返回零值 + Degraded=true，由调用方决定展示。
// ============================================================================

// HealthTableCounts 是各表行数（键稳定，供 /status 可视化与基线对比）。
type HealthTableCounts struct {
	Requests  int64 `json:"requests"`
	Sessions  int64 `json:"sessions"`
	ToolCalls int64 `json:"tool_calls"`
	Subagents int64 `json:"subagents"`
	Turns     int64 `json:"turns"`
}

// AnalyticsHealth 是采集健康的只读快照。
type AnalyticsHealth struct {
	// IngestedTotal 各表行数之和（含骨架行，重复事件按主键覆盖不重复计）。
	IngestedTotal int64 `json:"ingested_total"`
	// ConflictTotal usage_subagents 冲突累计（同一子代理多来源载荷口径不一致）。
	ConflictTotal int64 `json:"conflict_total"`
	// LastIngestAt 最近一条已入库事实的时间（取各表时间列最大值）；
	// 库为空时为 nil（字段仍存在，语义为"未知/无数据"）。
	LastIngestAt *time.Time        `json:"last_ingest_at"`
	TableCounts  HealthTableCounts `json:"table_counts"`
	// Degraded 报告分析库不可查询（只读降级/库不存在）：计数全 0 而非报错。
	Degraded bool `json:"degraded"`
	// StatsReady 报告预聚合统计列可用（schema v3 且未命中逃生开关）。
	StatsReady bool `json:"stats_ready"`
	// StatsDrift 预聚合列抽样对账（§6.3）；StatsReady=false 时为 nil。
	StatsDrift *StatsDrift `json:"stats_drift,omitempty"`
}

// AnalyticsHealth 返回采集健康快照；nil / 只读降级 / 空库均返回零值快照。
func (s *Store) AnalyticsHealth() AnalyticsHealth {
	health := AnalyticsHealth{Degraded: true}
	if s == nil || s.db == nil || s.empty {
		return health
	}
	health.Degraded = false
	health.TableCounts = HealthTableCounts{
		Requests:  s.scalarInt64("SELECT COUNT(*) FROM usage_requests"),
		Sessions:  s.scalarInt64("SELECT COUNT(*) FROM usage_sessions"),
		ToolCalls: s.scalarInt64("SELECT COUNT(*) FROM usage_tool_calls"),
		Subagents: s.scalarInt64("SELECT COUNT(*) FROM usage_subagents"),
		Turns:     s.scalarInt64("SELECT COUNT(*) FROM usage_turns"),
	}
	health.IngestedTotal = health.TableCounts.Requests + health.TableCounts.Sessions +
		health.TableCounts.ToolCalls + health.TableCounts.Subagents + health.TableCounts.Turns
	health.ConflictTotal = s.scalarInt64("SELECT COALESCE(SUM(conflict_count), 0) FROM usage_subagents")
	if last := maxTimestampNano(
		s.scalarInt64("SELECT COALESCE(MAX(updated_at_unix_nano), 0) FROM usage_sessions"),
		s.scalarInt64("SELECT COALESCE(MAX(started_at_unix_nano), 0) FROM usage_requests"),
		s.scalarInt64("SELECT COALESCE(MAX(completed_at_unix_nano), 0) FROM usage_tool_calls"),
		s.scalarInt64("SELECT COALESCE(MAX(completed_at_unix_nano), 0) FROM usage_subagents"),
		s.scalarInt64("SELECT COALESCE(MAX(ended_at_unix_nano), 0) FROM usage_turns"),
	); last > 0 {
		at := time.Unix(0, last).UTC()
		health.LastIngestAt = &at
	}
	health.StatsReady = s.StatsReady()
	if health.StatsReady {
		health.StatsDrift = s.sampleStatsDrift(statsDriftSampleSize)
	}
	return health
}

// AnalyticsHealth 返回底层库的采集健康快照；未挂载/降级时返回 Degraded=true。
func (s *Service) AnalyticsHealth() AnalyticsHealth {
	if store := s.Query(); store != nil {
		return store.AnalyticsHealth()
	}
	return AnalyticsHealth{Degraded: true}
}

// scalarInt64 执行单值查询；表缺失或只读降级时返回 0（不报错）。
func (s *Store) scalarInt64(query string) int64 {
	rows, ok, err := s.query(query)
	if err != nil || !ok {
		return 0
	}
	defer rows.Close()
	if !rows.Next() {
		return 0
	}
	var value int64
	if err := rows.Scan(&value); err != nil {
		return 0
	}
	return value
}

// maxTimestampNano 返回各表最近时间的最大值（均为 UnixNano，0 表示无数据）。
func maxTimestampNano(values ...int64) int64 {
	var max int64
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}
