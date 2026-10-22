package commands

import (
	"context"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// chat_pager_cold_restore.go —— T10（A）冷区读回：把已逐出的 cell 从写穿
// store 取回，前置到 pager 视图（设计 §14.9「pager 虚拟化」的落地形态）。
//
// 语义与边界：
//   - 数据源只有一处：bridge 的冷区账本（ColdLedger）+ 同一 store；
//   - 每个 cell 都经 ValidateTranscriptCellBody 校验（指纹/行数），校验失败即
//     视为"不可重建"，该 cell 不进入视图，并在返回值里体现为 restored<expected；
//   - 读回是**按 pager 打开这一刻**的一次性物化（不是每帧）：pager 只在租约
//     打开时取视图，滚动复用同一份 model；
//   - store 不可用（residency 关闭 / bodyStore=off）时返回 ok=false，调用方
//     保持既有行为（pager 只见驻留窗口）。
const chatPagerColdRestoreLimit = 20000

// restoreColdCellsHeader 返回冷区 cell（按交付顺序）与账本期望数量。
// ok=false 表示没有可用的冷区账本（调用方不做任何改动）。
func restoreColdCellsHeader(session *ChatSession) (cells []scene.TranscriptCell, expected int, ok bool) {
	if session == nil || session.RuntimeEventBridge == nil {
		return nil, 0, false
	}
	bridge := session.RuntimeEventBridge
	bridge.sceneMu.Lock()
	policy := bridge.sceneTrimPolicy
	var residency *ui.TranscriptResidency
	if policy != nil {
		residency = policy.Residency
	}
	bridge.sceneMu.Unlock()
	if residency == nil {
		return nil, 0, false
	}
	expected = residency.Cold.Len()
	if expected == 0 {
		return nil, 0, true
	}
	if expected > chatPagerColdRestoreLimit {
		expected = chatPagerColdRestoreLimit
	}
	sessionID := strings.TrimSpace(residency.SessionID)
	if sessionID == "" || residency.Store == nil {
		return nil, expected, true
	}
	ctx := context.Background()
	cells = make([]scene.TranscriptCell, 0, expected)
	for index := 0; index < expected; index++ {
		ref := residency.Cold.Cells[index]
		body, found, err := residency.Store.Get(ctx, sessionID, ref.ID, ref.Revision)
		if err != nil || !found {
			continue
		}
		cell, err := ui.ValidateTranscriptCellBody(body)
		if err != nil {
			continue
		}
		cells = append(cells, cell)
	}
	return cells, expected, true
}