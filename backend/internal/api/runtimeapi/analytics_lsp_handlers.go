package runtimeapi

// 舰队级 LSP 基线端点（方案 §5.5 比率区块 / §3.3 口径）：
//
//	GET /api/runtime/analytics/lsp/baseline?days=N | ?since=RFC3339
//
// 口径：**直接复用 internal/lsp/baseline.Analyze**——与 TUI `/lsp baseline`、
// 方案 §4.3 基线登记表、scripts/analyze-lsp-baseline.py 是同一实现（两侧由同一
// fixture 数字互锁），因此不存在第二套数字。未采集项按基线约定输出 n/a + 原因，
// 不伪造 0；阈值一律"待标定"，本端点不做告警。
//
// 代价与边界：每次请求做一次 chat-logs 扫描（与 TUI 同价）；面板按需刷新而非
// 高频轮询。若将来需要轮询，先加 TTL 缓存再改前端。

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/aiclipaths"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp/baseline"
)

const lspBaselineSchemaVersion = "runtime.analytics.lsp.v1"

// lspBaselineRoots 是扫描根来源；生产固定为默认 chat-logs 根，测试可覆盖。
var lspBaselineRoots = func() []string {
	return []string{aiclipaths.DefaultChatLogsDir()}
}

type lspBaselinePayload struct {
	SchemaVersion string             `json:"schema_version"`
	GeneratedAt   string             `json:"generated_at"`
	Window        string             `json:"window"`
	Scan          baseline.ScanStats `json:"scan"`
	Stats         baseline.Stats     `json:"stats"`
	Rows          []baseline.Row     `json:"rows"`
}

// GetAnalyticsLSPBaseline 返回舰队级 LSP 基线（§4.3 行 + 原始 Stats）。
func (h *Handler) GetAnalyticsLSPBaseline(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeUsageAdmin(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	opts, window, err := parseLSPBaselineOptions(r)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err)
		return
	}
	opts.Roots = lspBaselineRoots()
	stats, err := baseline.Analyze(opts)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, lspBaselinePayload{
		SchemaVersion: lspBaselineSchemaVersion,
		GeneratedAt:   stats.GeneratedAt,
		Window:        window,
		Scan:          stats.Scan,
		Stats:         stats,
		Rows:          baseline.Rows(stats),
	})
}

// parseLSPBaselineOptions 解析时间窗（since 与 days 互斥；缺省 = 全窗口）。
func parseLSPBaselineOptions(r *http.Request) (baseline.Options, string, error) {
	var opts baseline.Options
	values := r.URL.Query()
	sinceRaw := strings.TrimSpace(values.Get("since"))
	daysRaw := strings.TrimSpace(values.Get("days"))
	if sinceRaw != "" && daysRaw != "" {
		return opts, "", fmt.Errorf("since and days are mutually exclusive")
	}
	if sinceRaw != "" {
		t, err := time.Parse(time.RFC3339, sinceRaw)
		if err != nil {
			return opts, "", fmt.Errorf("since must be RFC3339")
		}
		opts.Since = t
		return opts, "since=" + t.UTC().Format(time.RFC3339), nil
	}
	if daysRaw != "" {
		days, err := strconv.Atoi(daysRaw)
		if err != nil || days <= 0 || days > 3650 {
			return opts, "", fmt.Errorf("days must be a positive integer (<=3650)")
		}
		opts.Since = time.Now().AddDate(0, 0, -days)
		return opts, fmt.Sprintf("days=%d", days), nil
	}
	return opts, "all", nil
}
