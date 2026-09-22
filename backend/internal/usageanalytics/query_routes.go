package usageanalytics

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// 路由观测查询：RouteStats（总览 + 分布）与 RouteEvents（明细）。
//
// 空库 / 只读库缺表时返回空结果而非错误（复用 Store.query 的缺表降级）。
// ---------------------------------------------------------------------------

const (
	// maxRouteEventRows 是明细单页上限。
	maxRouteEventRows = 500
)

// RouteStats 聚合路由切换观测：精确 totals + 维度分布桶。
func (s *Store) RouteStats(q RouteQuery) (RouteStatsResult, error) {
	result := RouteStatsResult{
		SchemaVersion:      SchemaVersion,
		GeneratedAt:        time.Now().UTC(),
		ByScope:            []RouteBucket{},
		ByKind:             []RouteBucket{},
		ByReason:           []RouteBucket{},
		BySource:           []RouteBucket{},
		ByProvider:         []RouteBucket{},
		ByModel:            []RouteBucket{},
		ByDifficulty:       []RouteBucket{},
		ByDifficultySource: []RouteBucket{},
		ByRole:             []RouteBucket{},
		ByTaskType:         []RouteBucket{},
		Warnings:           []RouteBucket{},
	}
	where, args := routeWhere(q)
	totals, err := s.routeTotals(where, args)
	if err != nil {
		return result, err
	}
	result.Totals = totals

	// 每个维度一次 GROUP BY 全量聚合；列名是包内常量，不接受外部输入。
	// task_type 是 v6 增量列：只读旧库缺列时表达式退化为空串常量，聚合自然得到
	// 空桶，而不是让整个 stats 查询报错（与明细读路径的列存在性退化同策略）。
	taskTypeColumn := s.routeColumnExpr("task_type")
	for _, dimension := range []struct {
		column string
		target *[]RouteBucket
	}{
		{"scope", &result.ByScope},
		{"kind", &result.ByKind},
		{"reason", &result.ByReason},
		{"source", &result.BySource},
		{"provider", &result.ByProvider},
		{"model", &result.ByModel},
		{"difficulty", &result.ByDifficulty},
		{"difficulty_source", &result.ByDifficultySource},
		{"role", &result.ByRole},
		{taskTypeColumn, &result.ByTaskType},
	} {
		buckets, err := s.routeBuckets(where, args, dimension.column)
		if err != nil {
			return result, err
		}
		*dimension.target = buckets
	}
	warnings, err := s.routeWarningBuckets(where, args)
	if err != nil {
		return result, err
	}
	result.Warnings = warnings
	// 桶覆盖过滤集全量（与 totals 同源）：SampleSize 是覆盖行数，Sampled 恒为 false。
	result.SampleSize = totals.Total
	result.Sampled = false
	return result, nil
}

// routeBuckets 对单个维度做全量精确聚合（GROUP BY，与 totals 同源同过滤集）。
//
// 早期实现按 recorded_at 倒序只扫前 2000 行再在 Go 侧聚合：超过上限时桶只覆盖
// 「最近 2000 行」，与全量精确的 totals 形成口径差（同一张图一半是全量、一半是样本）。
// 现在每个维度都走 SQL 聚合，桶计数之和恒等于 totals.Total。
//
// column 是包内常量列名，不接受外部输入，因此直接内插；空维度（事件未携带）不进桶，
// 避免把「未知」伪装成某个具体取值。
func (s *Store) routeBuckets(where string, args []interface{}, column string) ([]RouteBucket, error) {
	rows, ok, err := s.query(fmt.Sprintf(`
SELECT %s AS bucket_key,
       COUNT(*) AS bucket_count,
       COALESCE(SUM(CASE WHEN route_changed = 1 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN fallback_used = 1 THEN 1 ELSE 0 END), 0)
FROM usage_routes
WHERE %s AND TRIM(%s) <> ''
GROUP BY %s
ORDER BY bucket_count DESC, bucket_key ASC`, column, where, column, column), args...)
	if err != nil {
		return nil, fmt.Errorf("query route buckets(%s): %w", column, err)
	}
	if !ok {
		return []RouteBucket{}, nil
	}
	defer rows.Close()
	accumulator := newRouteBucketAccumulator()
	for rows.Next() {
		var key string
		var count, changed, fallback int
		if err := rows.Scan(&key, &count, &changed, &fallback); err != nil {
			return nil, fmt.Errorf("scan route buckets(%s): %w", column, err)
		}
		accumulator.addWeighted(key, count, changed, fallback)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan route buckets(%s): %w", column, err)
	}
	return accumulator.list(), nil
}

// routeWarningBuckets 聚合告警维度：告警是多值维度，不能按列 GROUP BY，先按
// warnings_json 文本分组（组数远小于行数），再在 Go 侧展开成单个告警。
// 告警桶沿用既有语义：只计出现次数，route_changed / fallback_used 保持 0。
func (s *Store) routeWarningBuckets(where string, args []interface{}) ([]RouteBucket, error) {
	rows, ok, err := s.query(fmt.Sprintf(`
SELECT warnings_json, COUNT(*)
FROM usage_routes
WHERE %s AND warnings_json <> ''
GROUP BY warnings_json`, where), args...)
	if err != nil {
		return nil, fmt.Errorf("query route warnings: %w", err)
	}
	if !ok {
		return []RouteBucket{}, nil
	}
	defer rows.Close()
	accumulator := newRouteBucketAccumulator()
	for rows.Next() {
		var raw string
		var count int
		if err := rows.Scan(&raw, &count); err != nil {
			return nil, fmt.Errorf("scan route warnings: %w", err)
		}
		for _, warning := range decodeRouteWarnings(raw) {
			accumulator.addWeighted(warning, count, 0, 0)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan route warnings: %w", err)
	}
	return accumulator.list(), nil
}

// RouteEvents 返回路由观测明细（时间倒序分页）。
func (s *Store) RouteEvents(q RouteQuery) (RouteEventsResult, error) {
	limit := normalizeLimit(q.Limit, 100, maxRouteEventRows)
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	result := RouteEventsResult{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Events:        []RouteEvent{},
		Limit:         limit,
		Offset:        offset,
	}
	where, args := routeWhere(q)
	if err := s.routeEventCount(where, args, &result); err != nil {
		return result, err
	}
	// goal（v5）/ task_type、task_subject（v6）是增量列：只读旧库缺列时退化为空串
	// （前端显示「未记录」），而不是让整个明细查询报错。
	goalExpr := s.routeColumnExpr("goal")
	taskTypeExpr := s.routeColumnExpr("task_type")
	taskSubjectExpr := s.routeColumnExpr("task_subject")
	rows, ok, err := s.query(fmt.Sprintf(`
SELECT recorded_at_unix_nano, session_id, parent_session_id, child_session_id, trace_id, scope, kind,
       agent_id, role, %s, %s, %s, step, reason, source, difficulty, difficulty_source, provider, model, reasoning_effort,
       route_changed, fallback_used, fallback_reason, candidate_count, attempt, max_attempts, batch_id, warnings_json
FROM usage_routes
WHERE %s
ORDER BY recorded_at_unix_nano DESC, route_event_id ASC
LIMIT ? OFFSET ?`, goalExpr, taskTypeExpr, taskSubjectExpr, where), append(args, limit, offset)...)
	if err != nil {
		return result, fmt.Errorf("query route events: %w", err)
	}
	if !ok {
		return result, nil
	}
	defer rows.Close()
	for rows.Next() {
		var (
			event                     RouteEvent
			recordedNano              int64
			changedFlag, fallbackFlag *int
			warningsJSON              string
		)
		if err := rows.Scan(
			&recordedNano, &event.SessionID, &event.ParentSessionID, &event.ChildSessionID, &event.TraceID,
			&event.Scope, &event.Kind, &event.AgentID, &event.Role, &event.Goal, &event.TaskType, &event.TaskSubject,
			&event.Step, &event.Reason, &event.Source,
			&event.Difficulty, &event.DifficultySource, &event.Provider, &event.Model, &event.ReasoningEffort,
			&changedFlag, &fallbackFlag, &event.FallbackReason, &event.CandidateCount,
			&event.Attempt, &event.MaxAttempts, &event.BatchID, &warningsJSON,
		); err != nil {
			return result, fmt.Errorf("scan route events: %w", err)
		}
		event.RecordedAt = timeFromUnixNano(recordedNano)
		if changedFlag != nil {
			flag := *changedFlag == 1
			event.RouteChanged = &flag
		}
		if fallbackFlag != nil {
			flag := *fallbackFlag == 1
			event.FallbackUsed = &flag
		}
		event.Warnings = decodeRouteWarnings(warningsJSON)
		result.Events = append(result.Events, event)
	}
	if err := rows.Err(); err != nil {
		return result, fmt.Errorf("scan route events: %w", err)
	}
	return result, nil
}

// routeColumnExpr 返回可用于 SELECT 的 usage_routes 列表达式：列存在时是列名本身，
// 缺列时退化为空串常量。只读旧库不改 schema，读路径必须按列存在性退化，而不是让
// 整条查询报错。列名是包内常量，不接受外部输入。
func (s *Store) routeColumnExpr(column string) string {
	return s.columnExpr("usage_routes", column)
}

// routeTotals 全量精确计数（不受分布抽样上限影响）。
func (s *Store) routeTotals(where string, args []interface{}) (RouteTotals, error) {
	var totals RouteTotals
	rows, ok, err := s.query(fmt.Sprintf(`
SELECT COUNT(*),
       COALESCE(SUM(CASE WHEN scope = 'main_agent' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN scope = 'subagent' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN kind = 'applied' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN kind = 'cleared' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN kind = 'warning' THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN route_changed = 1 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(CASE WHEN fallback_used = 1 THEN 1 ELSE 0 END), 0),
       COALESCE(SUM(candidate_count), 0),
       COUNT(DISTINCT session_id),
       COUNT(DISTINCT CASE WHEN model <> '' THEN model END)
FROM usage_routes
WHERE %s`, where), args...)
	if err != nil {
		return totals, fmt.Errorf("query route totals: %w", err)
	}
	if !ok {
		return totals, nil
	}
	defer rows.Close()
	if !rows.Next() {
		return totals, nil
	}
	var (
		total, mainAgent, subagent, applied, cleared, warnings int64
		changed, fallback, candidates, sessions, models        int64
	)
	if err := rows.Scan(&total, &mainAgent, &subagent, &applied, &cleared, &warnings, &changed, &fallback, &candidates, &sessions, &models); err != nil {
		return totals, fmt.Errorf("scan route totals: %w", err)
	}
	totals = RouteTotals{
		Total:            int(total),
		MainAgent:        int(mainAgent),
		Subagent:         int(subagent),
		Applied:          int(applied),
		Cleared:          int(cleared),
		Warnings:         int(warnings),
		RouteChanged:     int(changed),
		FallbackUsed:     int(fallback),
		CandidateTotal:   int(candidates),
		DistinctSessions: int(sessions),
		DistinctModels:   int(models),
	}
	return totals, rows.Err()
}

// routeEventCount 填充明细总数（COUNT(*) 全量，用于前端分页）。
func (s *Store) routeEventCount(where string, args []interface{}, result *RouteEventsResult) error {
	if result == nil {
		return nil
	}
	rows, ok, err := s.query(fmt.Sprintf(`SELECT COUNT(*) FROM usage_routes WHERE %s`, where), args...)
	if err != nil {
		return fmt.Errorf("count route events: %w", err)
	}
	if !ok {
		return nil
	}
	defer rows.Close()
	if rows.Next() {
		var count int64
		if err := rows.Scan(&count); err != nil {
			return fmt.Errorf("scan route event count: %w", err)
		}
		result.Count = int(count)
	}
	return rows.Err()
}

// routeWhere 构造过滤条件（空维度不参与过滤；时间窗为闭区间）。
func routeWhere(q RouteQuery) (string, []interface{}) {
	clauses := []string{"1=1"}
	args := []interface{}{}
	if !q.From.IsZero() {
		clauses = append(clauses, "recorded_at_unix_nano >= ?")
		args = append(args, q.From.UnixNano())
	}
	if !q.To.IsZero() {
		clauses = append(clauses, "recorded_at_unix_nano <= ?")
		args = append(args, q.To.UnixNano())
	}
	for _, filter := range []struct {
		column string
		value  string
	}{
		{"scope", q.Scope},
		{"kind", q.Kind},
		{"source", q.Source},
		{"provider", q.Provider},
		{"model", q.Model},
		{"difficulty", q.Difficulty},
		{"session_id", q.SessionID},
	} {
		if value := strings.TrimSpace(filter.value); value != "" {
			clauses = append(clauses, filter.column+" = ?")
			args = append(args, value)
		}
	}
	return strings.Join(clauses, " AND "), args
}

// routeBucketAccumulator 汇总 SQL GROUP BY 的结果（合并 Go 侧 TrimSpace 归一后的同值键）。
type routeBucketAccumulator struct {
	order  []string
	counts map[string]*RouteBucket
}

func newRouteBucketAccumulator() *routeBucketAccumulator {
	return &routeBucketAccumulator{counts: map[string]*RouteBucket{}}
}

// addWeighted 按权重累加一个维度值（GROUP BY 已给出每组行数，无需逐行调用）。
func (a *routeBucketAccumulator) addWeighted(key string, count, changed, fallback int) {
	if a == nil || count <= 0 {
		return
	}
	trimmed := strings.TrimSpace(key)
	if trimmed == "" {
		// 空维度（事件未携带）不进桶：避免把「未知」伪装成某个具体取值。
		return
	}
	bucket := a.counts[trimmed]
	if bucket == nil {
		bucket = &RouteBucket{Key: trimmed}
		a.counts[trimmed] = bucket
		a.order = append(a.order, trimmed)
	}
	bucket.Count += count
	bucket.RouteChanged += changed
	bucket.FallbackUsed += fallback
}

// list 返回按 count 倒序、key 升序稳定的桶列表。
func (a *routeBucketAccumulator) list() []RouteBucket {
	buckets := make([]RouteBucket, 0, len(a.order))
	for _, key := range a.order {
		buckets = append(buckets, *a.counts[key])
	}
	sort.SliceStable(buckets, func(i, j int) bool {
		if buckets[i].Count != buckets[j].Count {
			return buckets[i].Count > buckets[j].Count
		}
		return buckets[i].Key < buckets[j].Key
	})
	return buckets
}

// decodeRouteWarnings 解析 warnings_json（坏数据按无告警处理，不阻断查询）。
func decodeRouteWarnings(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	var warnings []string
	if err := json.Unmarshal([]byte(trimmed), &warnings); err != nil {
		return nil
	}
	return warnings
}
