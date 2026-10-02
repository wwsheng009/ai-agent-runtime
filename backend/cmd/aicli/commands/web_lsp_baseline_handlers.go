package commands

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	lspbaseline "github.com/wwsheng009/ai-agent-runtime/internal/lsp/baseline"
)

// web_lsp_baseline_handlers.go 实现「LSP 观测」页签的基线区块数据面：
//
//	GET /web/api/lsp/baseline?days=14   §4.3 基线登记表（跨会话，非会话作用域）
//
// 与 /lsp baseline 子命令同源（internal/lsp/baseline）。基线是跨会话聚合，
// 因此不随「当前会话」变化；代价是全库只读扫描（真实库冷扫数秒），用
// 进程内 TTL 缓存兜住页签 15s 自动刷新；未采集项由归因包输出 n/a，端点
// 绝不把未采集渲染成 0（方案 §4.3/§5.4 纪律）。

const (
	// chatWebLSPBaselineDefaultDays 默认窗口（与 /lsp baseline 一致）。
	chatWebLSPBaselineDefaultDays = 14
	// chatWebLSPBaselineMaxDays 窗口上限；0 表示全窗口（显式请求才允许）。
	chatWebLSPBaselineMaxDays = 3650
	// chatWebLSPBaselineCacheTTL 扫描结果缓存时长：全库扫描代价高，自动刷新
	// 必须命中缓存；不同 days 各自缓存。
	chatWebLSPBaselineCacheTTL = 10 * time.Minute
	// 稳定原因码（前端据此渲染，不做数值判断）。
	chatWebLSPBaselineNoRootReason    = "lsp_baseline_no_log_root"
	chatWebLSPBaselineInvalidDaysCode = "lsp_baseline_invalid_days"
	chatWebLSPBaselineFailedCode      = "lsp_baseline_failed"
)

// chatWebLSPBaselineRoots 返回扫描根；测试可替换（默认目录依赖真实 HOME）。
var chatWebLSPBaselineRoots = func() []string {
	return []string{aiclipaths.DefaultChatLogsDir()}
}

// chatWebLSPBaselineDigest 是报告头部读数（事实，不含阈值判断）。
type chatWebLSPBaselineDigest struct {
	Requests          int  `json:"requests"`
	Sessions          int  `json:"sessions"`
	Injected          int  `json:"injected"`
	Degraded          int  `json:"degraded"`
	DiagHit           int  `json:"diag_hit"`
	NoServer          int  `json:"no_server"`
	P50MS             *int `json:"p50_ms"`
	P95MS             *int `json:"p95_ms"`
	EditCalls         int  `json:"edit_calls"`
	ActiveEditCalls   int  `json:"active_edit_calls"`
	AppendedBytes     int  `json:"appended_bytes"`
	TruncatedRequests int  `json:"truncated_requests"`
}

// chatWebLSPBaselineScan 是扫描过程事实。
type chatWebLSPBaselineScan struct {
	Files        int `json:"files"`
	SkippedFiles int `json:"skipped_files"`
	Lines        int `json:"lines"`
	Malformed    int `json:"malformed"`
	SkippedOld   int `json:"skipped_old"`
}

// chatWebLSPBaselineBody 是 /baseline 的响应体。
type chatWebLSPBaselineBody struct {
	SchemaVersion string                   `json:"schema_version"`
	Available     bool                     `json:"available"`
	Reason        string                   `json:"reason,omitempty"`
	Days          int                      `json:"days"`
	Since         string                   `json:"since,omitempty"`
	FirstAt       string                   `json:"first_at,omitempty"`
	LastAt        string                   `json:"last_at,omitempty"`
	Rows          []lspbaseline.Row        `json:"rows"`
	Digest        chatWebLSPBaselineDigest `json:"digest"`
	Scan          chatWebLSPBaselineScan   `json:"scan"`
	Roots         []string                 `json:"roots"`
	CachedAt      time.Time                `json:"cached_at"`
	Note          string                   `json:"note,omitempty"`
}

type chatWebLSPBaselineCacheEntry struct {
	body      chatWebLSPBaselineBody
	expiresAt time.Time
}

var (
	chatWebLSPBaselineCacheMu sync.Mutex
	chatWebLSPBaselineCache   = map[string]chatWebLSPBaselineCacheEntry{}
)

// resetChatWebLSPBaselineCache 清空缓存（测试用）。
func resetChatWebLSPBaselineCache() {
	chatWebLSPBaselineCacheMu.Lock()
	chatWebLSPBaselineCache = map[string]chatWebLSPBaselineCacheEntry{}
	chatWebLSPBaselineCacheMu.Unlock()
}

// chatWebLSPBaselineParseDays 解析 days 查询参数：缺省 14；0 = 全窗口；
// 非法/越界返回 (0,false) 由调用方回 400。
func chatWebLSPBaselineParseDays(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return chatWebLSPBaselineDefaultDays, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > chatWebLSPBaselineMaxDays {
		return 0, false
	}
	return value, true
}

// chatWebLSPBaselineBodyForDays 返回（带 TTL 缓存的）基线报告；available=false
// 只在「日志根不存在 / 归因失败」时出现，且带稳定 reason。
func chatWebLSPBaselineBodyForDays(days int) chatWebLSPBaselineBody {
	now := time.Now().UTC()
	key := strconv.Itoa(days)

	chatWebLSPBaselineCacheMu.Lock()
	if entry, ok := chatWebLSPBaselineCache[key]; ok && now.Before(entry.expiresAt) {
		chatWebLSPBaselineCacheMu.Unlock()
		return entry.body
	}
	chatWebLSPBaselineCacheMu.Unlock()

	roots := chatWebLSPBaselineRoots()
	body := chatWebLSPBaselineBody{
		SchemaVersion: lspObserveSchemaVersion,
		Days:          days,
		Rows:          []lspbaseline.Row{},
		Roots:         roots,
		CachedAt:      now,
	}

	existing := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			existing = append(existing, root)
		}
	}
	if len(existing) == 0 {
		body.Available = false
		body.Reason = chatWebLSPBaselineNoRootReason
		body.Note = "chat-logs 目录不存在：先在 aicli 会话里产生事件，或部署后等待数据积累"
		return body
	}

	opts := lspbaseline.Options{Roots: existing}
	if days > 0 {
		opts.Since = now.AddDate(0, 0, -days)
		body.Since = opts.Since.Format(time.RFC3339)
	}
	// 事实源：分析库（不再回扫 chat-logs）。这正是最初定位到的那个消费方——若它
	// 继续扫日志，"日志随保留策略退化"与"冷启动指标已冻结"就会在面板上原样留存。
	stats, err := lspBaselineFromStore(opts.Since)
	if err != nil {
		body.Available = false
		body.Reason = chatWebLSPBaselineFailedCode
		body.Note = err.Error()
		return body
	}
	body.Available = true
	body.FirstAt = stats.FirstAt
	body.LastAt = stats.LastAt
	body.Rows = lspbaseline.Rows(stats)
	body.Digest = chatWebLSPBaselineDigest{
		Requests:          stats.Requests,
		Sessions:          stats.Sessions,
		Injected:          stats.Injected,
		Degraded:          stats.Degraded,
		DiagHit:           stats.DiagHit,
		NoServer:          stats.NoServer,
		P50MS:             stats.LatencyP50MS,
		P95MS:             stats.LatencyP95MS,
		EditCalls:         stats.EditCalls,
		ActiveEditCalls:   stats.ActiveEdit,
		AppendedBytes:     stats.AppendedBytes,
		TruncatedRequests: stats.Truncated,
	}
	body.Scan = chatWebLSPBaselineScan{
		Files:        stats.Scan.Files,
		SkippedFiles: stats.Scan.SkippedFiles,
		Lines:        stats.Scan.Lines,
		Malformed:    stats.Scan.Malformed,
		SkippedOld:   stats.Scan.SkippedOld,
	}

	chatWebLSPBaselineCacheMu.Lock()
	chatWebLSPBaselineCache[key] = chatWebLSPBaselineCacheEntry{
		body:      body,
		expiresAt: now.Add(chatWebLSPBaselineCacheTTL),
	}
	chatWebLSPBaselineCacheMu.Unlock()
	return body
}
