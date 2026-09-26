package runtimeapi

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// GetSupervisionMetrics 返回 C0-E（§7.3 度量基线）读数：被 runtime 强制取消的 run
// 占比、decision_window_expired 兜底占比、成功完成 → 父 Agent 汇报延迟 P95，以及
// 误杀率的人工复核候选清单。指标 4（单托管 turn 的 resume 次数）在 Notes 里指向
// /supervision/snapshot 的 wake_budget —— 它属于宿主预算账本，不在 durable run 行里。
//
// Query params:
//
//	root_session_id  只读一个 root scope；省略 = 全库（部署基线）
//	since / until    RFC3339 或回看时长（"24h" / "7d"）；省略 = 无界
//	window_limit     窗口读取行数上限（默认 200）
//	max_candidates   误杀复核清单条数上限（默认 20）
//
// 与验收用例（TestSupervisionMetricsReadout_RuntimeCancelSources）走同一读数函数，
// 避免「测试绿」与「线上读数」口径漂移。
func (h *Handler) GetSupervisionMetrics(w http.ResponseWriter, r *http.Request) {
	if h.supervisionUnavailable(w) {
		return
	}
	q := r.URL.Query()
	now := time.Now().UTC()
	since, err := supervision.ParseMetricsWindowValue(q.Get("since"), now)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("since: %w", err))
		return
	}
	until, err := supervision.ParseMetricsWindowValue(q.Get("until"), now)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Errorf("until: %w", err))
		return
	}
	opts := supervision.MetricsSnapshotOptions{
		RootSessionID: strings.TrimSpace(q.Get("root_session_id")),
		Since:         since,
		Until:         until,
	}
	if raw := strings.TrimSpace(q.Get("window_limit")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			h.writeError(w, http.StatusBadRequest, fmt.Errorf("window_limit must be a positive integer"))
			return
		}
		opts.WindowLimit = value
	}
	if raw := strings.TrimSpace(q.Get("max_candidates")); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			h.writeError(w, http.StatusBadRequest, fmt.Errorf("max_candidates must be a positive integer"))
			return
		}
		opts.MaxCandidates = value
	}
	snapshot, err := supervision.CollectMetricsSnapshot(r.Context(), h.getSupervisionStore(), opts)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err)
		return
	}
	h.writeJSON(w, http.StatusOK, snapshot)
}
