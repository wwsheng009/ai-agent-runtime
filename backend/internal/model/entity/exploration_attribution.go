package entity

import "time"

// ExplorationAttribution 是 ADR-0003 §4.1 的 exploration_attribution 行。
//
// 它记录"一次被 shadow 拦截的 grep/view 调用"在真实结果（baseline）与索引侧
// 候选（candidate）之间的对比口径：coverage = overlap_n / baseline_n，
// economy = candidate_tokens / baseline_tokens，usable = (coverage >= α) AND
// (economy <= 1.0)。口径定义冻结于 ADR-0003 §4.2，任何聚合都必须可由本结构
// 复算（D1）。
//
// 隐私（ADR-0003 §4.6）：QueryHash = sha256(tool + "\x00" + pattern + "\x00" +
// scope)，不存 pattern 明文；ProjectID 存稳定键（工作区路径的哈希前缀），
// 不存绝对路径。
type ExplorationAttribution struct {
	ID        string
	SessionID string
	TurnID    string
	RequestID string
	// Tool 为被拦截的工具名：grep | view。
	Tool string
	// QueryHash 是 sha256(tool\x00pattern\x00scope) 的十六进制；不存明文。
	QueryHash string
	// ProjectID 是工作区稳定键（哈希前缀），不存绝对路径。
	ProjectID string
	// BaselineN = |G|：被拦截调用实际返回的条目数（0 表示零结果调用，
	// 落库但不进 M1/M2/M3 分母，见 ADR-0003 §4.5）。
	BaselineN int
	// CandidateN = |K|：索引侧候选条目数。
	CandidateN int
	// OverlapN = |G ∩ K|。
	OverlapN int
	// BaselineFilesN / OverlapFilesN 是 ADR-0008 §4 的 file-level 口径（只用于
	// grep 通道）：两侧文件集合大小与交集。grep 的 usable 判据以 file_coverage
	// := OverlapFilesN / BaselineFilesN 为准，行级列降为诊断；view 通道口径不变
	// （行级区间覆盖），两列保持 0。历史行/未启用（默认 0）不参与 file-level M1
	// （ADR-0008 §6.2）。
	BaselineFilesN int
	OverlapFilesN  int
	// BaselineTokens / CandidateTokens 是两侧内容的 token 估计值。
	BaselineTokens  int
	CandidateTokens int
	// Coverage = overlap_n / baseline_n；baseline_n == 0 时为 nil（NULL）。
	Coverage *float64
	// Economy = candidate_tokens / baseline_tokens；baseline_tokens == 0 时为 nil。
	Economy *float64
	// Usable = (coverage >= α) AND (economy <= 1.0)，0/1。
	Usable bool
	// Source 是候选解析来源：parser | lsp | heuristic。
	Source string
	// KnowledgeMode 产生本行时的模式：shadow | on。
	KnowledgeMode string
	CreatedAt     time.Time
}
