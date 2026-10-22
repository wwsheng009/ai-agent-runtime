package commands

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// chat_debug_window.go —— T7：调试/SSE 侧的尾巴窗口快照原语。
//
// 语义：`tailWindowSnapshot` 只保留最后 maxCells 个 cell（顺序不变）并返回
// **独立副本**（浅拷贝 cell，Presentation 与 Source 为只读共享），供
// `/debug/chat/*` 与 web SSE 在不物化全量 transcript 的前提下产出尾巴视图。
//
// 边界（与设计 §14.7 gap 归属一致）：窗口首格在重新布局时不再继承其前驱
// 产生的 leading gap —— 与冷区账本记录的 `LeadingGapRows` 语义相同，调用方
// 若需要精确 gap 必须从账本回填（T8 接线时处理）。
//
// 默认路径不变：`maxCells <= 0` 或未超限时直接返回原快照（零拷贝）。

// tailWindowSnapshot 返回尾巴窗口快照。
func tailWindowSnapshot(snap *scene.Snapshot, maxCells int) *scene.Snapshot {
	if snap == nil || maxCells <= 0 || len(snap.Cells) <= maxCells {
		return snap
	}
	start := len(snap.Cells) - maxCells
	cells := make([]*scene.TranscriptCell, 0, maxCells)
	for _, cell := range snap.Cells[start:] {
		if cell == nil {
			continue
		}
		cp := *cell
		cells = append(cells, &cp)
	}
	return &scene.Snapshot{
		SceneID:        snap.SceneID,
		Revision:       snap.Revision,
		ContentVersion: snap.ContentVersion,
		Cells:          cells,
	}
}

// ---------------------------------------------------------------------------
// T8b-4：调试端点窗口参数（设计 §14.10）
//
// `/debug/chat/status`（与 /web/api/status 同源）与 `/debug/chat/screen`
// （与 /web/api/screen?view=tui 同源）过去会把整段 transcript 送进
// LayoutTranscript / RenderText 或逐 cell 物化文本行；这些端点是轮询型的，
// 长会话下成本与历史长度成正比。语义：
//
//   - `?window=N`：只保留最后 N 个 cell（顺序不变，N 超上限钳制）；
//   - `?full=1`（`all|true|yes|on`）或 `?window=0`：显式全量（诊断口径）；
//   - 缺省：residency 开启时套用 chatDebugDefaultWindowCells（默认窗口）；
//     关闭时保持历史全量行为 —— 默认路径逐字节不变（不新增响应字段）。

const (
	// chatDebugDefaultWindowCells 是 residency 开启时调试端点的缺省窗口：约覆盖
	// 最近若干屏，长会话下远小于总量；要全量时显式 full=1。
	chatDebugDefaultWindowCells = 2000
	// chatDebugWindowMaxCells 是 ?window=N 的上限（与 /web/api/screen 的
	// tail 上限同思路：超过上限请显式 full=1，让「全量的成本」显式化）。
	chatDebugWindowMaxCells = 20000
)

// chatDebugTranscriptWindow 描述一次调试读取的 transcript 窗口。
type chatDebugTranscriptWindow struct {
	Cells     int  // >0：只保留最后 Cells 个 cell；0：不裁剪（全量）
	Defaulted bool // 由 residency 缺省窗口产生
	Requested bool // 由 ?window=N 显式请求
}

// windowed 报告本次读取是否裁剪了 transcript。
func (w chatDebugTranscriptWindow) windowed() bool { return w.Cells > 0 }

// chatDebugWindowInfo 是调试响应里的窗口标注（仅在确实裁剪时出现）。
type chatDebugWindowInfo struct {
	Cells      int  `json:"cells"`
	TotalCells int  `json:"total_cells"`
	Defaulted  bool `json:"defaulted,omitempty"`
	Requested  bool `json:"requested,omitempty"`
}

// info 返回窗口标注；未裁剪时返回 nil（响应字段省略，历史形状不变）。
func (w chatDebugTranscriptWindow) info(totalCells int) *chatDebugWindowInfo {
	if !w.windowed() || totalCells <= w.Cells {
		return nil
	}
	return &chatDebugWindowInfo{
		Cells:      w.Cells,
		TotalCells: totalCells,
		Defaulted:  w.Defaulted,
		Requested:  w.Requested,
	}
}

// chatDebugTranscriptWindowForRequest 解析窗口请求。residencyEnabled 为 true 时
// 缺省套用默认窗口，否则缺省 = 全量（历史行为）。
func chatDebugTranscriptWindowForRequest(r *http.Request, residencyEnabled bool) chatDebugTranscriptWindow {
	if chatDebugFullTranscriptRequested(r) {
		return chatDebugTranscriptWindow{}
	}
	if cells := chatDebugWindowCellsParam(r); cells > 0 {
		return chatDebugTranscriptWindow{Cells: cells, Requested: true}
	}
	if residencyEnabled {
		return chatDebugTranscriptWindow{Cells: chatDebugDefaultWindowCells, Defaulted: true}
	}
	return chatDebugTranscriptWindow{}
}

// chatDebugFullTranscriptRequested 解析显式全量请求：`?full=1|all|true|yes|on`
// 或 `?window=0`（与 msg_limit=0/tail=0 的「0 = 不限」直觉一致）。
func chatDebugFullTranscriptRequested(r *http.Request) bool {
	if r == nil {
		return false
	}
	query := r.URL.Query()
	if isTruthyQuery(query.Get("full")) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(query.Get("window"))) {
	case "0", "all", "full":
		return true
	default:
		return false
	}
}

func isTruthyQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on", "all", "full":
		return true
	default:
		return false
	}
}

// chatDebugWindowCellsParam 解析 `?window=N`：缺省/非法/非正数返回 0（不裁剪），
// 超过上限时钳制到 chatDebugWindowMaxCells。
func chatDebugWindowCellsParam(r *http.Request) int {
	if r == nil {
		return 0
	}
	raw := strings.TrimSpace(r.URL.Query().Get("window"))
	if raw == "" {
		return 0
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 0
	}
	if n > chatDebugWindowMaxCells {
		return chatDebugWindowMaxCells
	}
	return n
}

// chatDebugWindowSnapshot 按窗口裁剪快照（未裁剪时零拷贝返回原快照）。
func (w chatDebugTranscriptWindow) snapshot(snap *scene.Snapshot) *scene.Snapshot {
	return tailWindowSnapshot(snap, w.Cells)
}

// chatDebugTailCells 返回 cell 切片的尾部窗口（零拷贝子切片；未裁剪时原切片）。
func chatDebugTailCells(cells []scene.TranscriptCell, maxCells int) []scene.TranscriptCell {
	if maxCells <= 0 || len(cells) <= maxCells {
		return cells
	}
	return cells[len(cells)-maxCells:]
}
