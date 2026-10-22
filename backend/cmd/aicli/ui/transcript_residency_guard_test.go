package ui

import (
	"errors"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/scene"
)

// transcript_residency_guard_test.go —— T10 R2/R5 门禁（进程内）：
// 逐出守卫的交付事实投影。真实 provider 轮次曾抓到"未交付即逐出"导致历史
// 在终端消失（store 里有数据、屏幕上一行不剩），这里的断言是该修复的护栏。

// TestDeliveredCellIDsReflectsLedgerState 覆盖 DeliveredCellIDs 的三态。
func TestDeliveredCellIDsReflectsLedgerState(t *testing.T) {
	// 空账本：不可判定（调用方据此不阻断，保持既有装配形态的行为）。
	var empty HistoryEffectQueueState
	if ids, known := empty.DeliveredCellIDs(); known || len(ids) != 0 {
		t.Fatalf("空账本应 known=false：known=%v ids=%d", known, len(ids))
	}

	queue := HistoryEffectQueueState{}
	if err := queue.enqueue(testHistoryCommit(1, 11, 1)); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if _, known := queue.DeliveredCellIDs(); !known {
		t.Fatal("非空账本应 known=true")
	}
	if ids, _ := queue.DeliveredCellIDs(); len(ids) != 0 {
		t.Fatalf("Queued 不得计入已交付：%v", ids)
	}

	// 标记在途 → 交付（真实状态机路径）。
	if err := queue.markInFlight(1, 1); err != nil {
		t.Fatalf("markInFlight: %v", err)
	}
	if err := queue.ack(1, 1, 1); err != nil {
		t.Fatalf("ack: %v", err)
	}
	ids, known := queue.DeliveredCellIDs()
	if !known || !containsCellID(ids, 11) {
		t.Fatalf("交付后应包含 cell 11：known=%v ids=%v", known, ids)
	}
	// 交付后不再有阻断项。
	if blocked, reason := queue.DeliveryBlockers(); blocked {
		t.Fatalf("全部交付后不得阻断：reason=%s", reason)
	}
}

// TestDeliveryBlockersStopsEviction R5：未交付 / 可能部分写入 / 失效中 → 阻断。
func TestDeliveryBlockersStopsEviction(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		queue := HistoryEffectQueueState{}
		if err := queue.enqueue(testHistoryCommit(1, 11, 1)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		blocked, reason := queue.DeliveryBlockers()
		if !blocked || reason != "unacked_rows" {
			t.Fatalf("Queued 应阻断为 unacked_rows：blocked=%v reason=%s", blocked, reason)
		}
	})

	t.Run("partial-write", func(t *testing.T) {
		queue := HistoryEffectQueueState{}
		if err := queue.enqueue(testHistoryCommit(1, 11, 1)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := queue.markInFlight(1, 1); err != nil {
			t.Fatalf("markInFlight: %v", err)
		}
		if err := queue.fail(1, 1, errors.New("write interrupted"), true); err != nil {
			t.Fatalf("fail: %v", err)
		}
		blocked, reason := queue.DeliveryBlockers()
		if !blocked || reason != "projection_unknown" {
			t.Fatalf("可能部分写入应阻断为 projection_unknown：blocked=%v reason=%s", blocked, reason)
		}
	})

	t.Run("invalidation-pending", func(t *testing.T) {
		queue := HistoryEffectQueueState{}
		if err := queue.enqueue(testHistoryCommit(1, 11, 1)); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := queue.markInFlight(1, 1); err != nil {
			t.Fatalf("markInFlight: %v", err)
		}
		if err := queue.ledger.MarkInvalidationPending(1); err != nil {
			t.Fatalf("markInvalidationPending: %v", err)
		}
		blocked, reason := queue.DeliveryBlockers()
		if !blocked || reason != "projection_unknown" {
			t.Fatalf("失效中应阻断为 projection_unknown：blocked=%v reason=%s", blocked, reason)
		}
	})
}

func containsCellID(ids map[scene.CellID]struct{}, want scene.CellID) bool {
	_, ok := ids[want]
	return ok
}