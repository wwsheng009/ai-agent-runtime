package usageanalytics

import "time"

// ---------------------------------------------------------------------------
// 路由切换观测契约（主 Agent / 子 Agent 的 route 决策、还原与护栏信号）。
//
// 与 usage_routes 表一一对应；stats 用于总览卡片与分布图，events 用于明细表。
// 语义约定：
//   - route_changed / fallback_used 是**三态**（nil=事件未携带该字段），
//     因此统计里只计 true，未知不并入 false；
//   - 分布桶与 totals 同源同过滤集：两者都走全量 SQL 聚合（GROUP BY / COUNT），
//     桶计数之和恒等于 totals.Total，不存在「图是全量、桶是样本」的口径差。
// ---------------------------------------------------------------------------

// RouteQuery 是路由观测 stats / events 的公共过滤条件。
type RouteQuery struct {
	From       time.Time
	To         time.Time
	Scope      string
	Kind       string
	Source     string
	Provider   string
	Model      string
	Difficulty string
	SessionID  string
	Limit      int
	Offset     int
}

// RouteTotals 是全量精确计数。
type RouteTotals struct {
	Total            int `json:"total"`
	MainAgent        int `json:"main_agent"`
	Subagent         int `json:"subagent"`
	Applied          int `json:"applied"`
	Cleared          int `json:"cleared"`
	Warnings         int `json:"warnings"`
	RouteChanged     int `json:"route_changed"`
	FallbackUsed     int `json:"fallback_used"`
	CandidateTotal   int `json:"candidate_total"`
	DistinctSessions int `json:"distinct_sessions"`
	DistinctModels   int `json:"distinct_models"`
}

// RouteBucket 是一个维度分布桶（key 为维度值，count 为命中行数）。
type RouteBucket struct {
	Key          string `json:"key"`
	Count        int    `json:"count"`
	RouteChanged int    `json:"route_changed"`
	FallbackUsed int    `json:"fallback_used"`
}

// RouteStatsResult 是路由观测总览。
type RouteStatsResult struct {
	SchemaVersion string        `json:"schema_version"`
	GeneratedAt   time.Time     `json:"generated_at"`
	Totals        RouteTotals   `json:"totals"`
	ByScope       []RouteBucket `json:"by_scope"`
	ByKind        []RouteBucket `json:"by_kind"`
	ByReason      []RouteBucket `json:"by_reason"`
	BySource      []RouteBucket `json:"by_source"`
	ByProvider    []RouteBucket `json:"by_provider"`
	ByModel       []RouteBucket `json:"by_model"`
	ByDifficulty  []RouteBucket `json:"by_difficulty"`
	// ByDifficultySource 回答「难度档位是谁定的」：explicit（模型显式声明）/
	// explicit_promoted（显式声明被启发式提升覆盖）/ inferred（未声明且命中提升）/
	// default（未声明且未命中）。与 ByDifficulty 对照即可判断本地提升是否过火。
	ByDifficultySource []RouteBucket `json:"by_difficulty_source"`
	ByRole             []RouteBucket `json:"by_role"`
	// ByTaskType 回答「这一批路由决策都在做什么类别的任务」：task_type 是 v4 的
	// 路由分类轴（封闭枚举，缺省时事件不带该字段）。与 ByRole 并存一个 release，
	// 便于迁移窗口内对照「旧 role 轴」与「新 task_type 轴」的口径差。
	ByTaskType []RouteBucket `json:"by_task_type"`
	Warnings   []RouteBucket `json:"warnings"`
	// SampleSize 是分布桶覆盖的行数（全量精确，等于 Totals.Total）。
	// Sampled 是兼容保留字段：桶不再抽样，当前实现恒为 false。
	Sampled    bool `json:"sampled"`
	SampleSize int  `json:"sample_size"`
}

// RouteEvent 是路由观测明细行。
type RouteEvent struct {
	RecordedAt      time.Time `json:"recorded_at"`
	SessionID       string    `json:"session_id"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`
	ChildSessionID  string    `json:"child_session_id,omitempty"`
	TraceID         string    `json:"trace_id,omitempty"`
	Scope           string    `json:"scope"`
	Kind            string    `json:"kind"`
	AgentID         string    `json:"agent_id,omitempty"`
	Role            string    `json:"role,omitempty"`
	// Goal 是子代理任务目标（发射点按 256 字符截断，见 internal/agent/subagent_route_audit.go）；
	// 主 Agent 行与缺列旧库为空。
	Goal string `json:"goal,omitempty"`
	// TaskType / TaskSubject 是 v4 的路由分类轴与短说明（缺省时事件不带这两个键，
	// 旧库缺列时为空串）；只进审计与聚合，不进 prompt。
	TaskType         string   `json:"task_type,omitempty"`
	TaskSubject      string   `json:"task_subject,omitempty"`
	Step             int      `json:"step"`
	Reason           string   `json:"reason"`
	Source           string   `json:"source,omitempty"`
	Difficulty       string   `json:"difficulty,omitempty"`
	DifficultySource string   `json:"difficulty_source,omitempty"`
	Provider         string   `json:"provider,omitempty"`
	Model            string   `json:"model,omitempty"`
	ReasoningEffort  string   `json:"reasoning_effort,omitempty"`
	RouteChanged     *bool    `json:"route_changed,omitempty"`
	FallbackUsed     *bool    `json:"fallback_used,omitempty"`
	FallbackReason   string   `json:"fallback_reason,omitempty"`
	CandidateCount   int      `json:"candidate_count,omitempty"`
	Warnings         []string `json:"warnings,omitempty"`
	Attempt          int      `json:"attempt,omitempty"`
	MaxAttempts      int      `json:"max_attempts,omitempty"`
	BatchID          string   `json:"batch_id,omitempty"`
}

// RouteEventsResult 是路由观测明细分页。
type RouteEventsResult struct {
	SchemaVersion string       `json:"schema_version"`
	GeneratedAt   time.Time    `json:"generated_at"`
	Events        []RouteEvent `json:"events"`
	Count         int          `json:"count"`
	Limit         int          `json:"limit"`
	Offset        int          `json:"offset"`
}
