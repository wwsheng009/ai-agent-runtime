package scene

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
)

func TestSubmitAppliesTransactionAndAdvancesRevision(t *testing.T) {
	s := New()
	c := NewController(s)
	rev, applied, err := c.Submit(SceneTransaction{
		Cause: "test",
		Mutations: []CellMutation{
			&AppendCell{Cell: newTestCell(1, KindUser, "u")},
			&AppendCell{Cell: newTestCell(2, KindAssistant, "a")},
		},
		Flush: FlushImmediate,
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if rev != 1 {
		t.Fatalf("revision = %d, want 1", rev)
	}
	if len(applied) != 2 {
		t.Fatalf("applied = %d, want 2", len(applied))
	}
	if s.Len() != 2 {
		t.Fatalf("len = %d, want 2", s.Len())
	}
}

func TestSubmitRollbackOnMutationFailure(t *testing.T) {
	// INV-FRAME-01：一个 Scene transaction 要么完整应用，要么不应用。
	s := New()
	c := NewController(s)
	// 先提交一个 committed cell。
	if _, _, err := c.Submit(SceneTransaction{
		Mutations: []CellMutation{
			&AppendCell{Cell: newTestCell(1, KindUser, "u")},
			&FinalizeCell{ID: 1, Revision: 1, Source: "u-final"},
		},
	}); err != nil {
		t.Fatalf("seed submit: %v", err)
	}
	beforeRev := s.Revision()
	beforeLen := s.Len()

	// 事务前半成功（append 2），后半失败（update 不存在的 99）→ 整体回滚。
	_, _, err := c.Submit(SceneTransaction{
		Cause: "partial",
		Mutations: []CellMutation{
			&AppendCell{Cell: newTestCell(2, KindAssistant, "a")},
			&UpdateCell{ID: 99, Revision: 1, Source: "ghost"},
		},
	})
	if err == nil {
		t.Fatalf("expected submit failure")
	}
	if s.Revision() != beforeRev {
		t.Fatalf("revision after rollback = %d, want %d", s.Revision(), beforeRev)
	}
	if s.Len() != beforeLen {
		t.Fatalf("len after rollback = %d, want %d (appended cell must be removed)", s.Len(), beforeLen)
	}
	if _, ok := s.Cell(2); ok {
		t.Fatalf("cell 2 must not exist after rollback")
	}
	// cell 1 的原内容不受影响。
	if c1, _ := s.Cell(1); c1.Source != "u-final" || c1.Phase != CellCommitted {
		t.Fatalf("cell 1 corrupted by rollback: %+v", c1)
	}
}

func TestSubmitRollbackRestoresUpdatedCell(t *testing.T) {
	s := New()
	c := NewController(s)
	if _, _, err := c.Submit(SceneTransaction{
		Mutations: []CellMutation{&AppendCell{Cell: newTestCell(1, KindAssistant, "v0")}},
	}); err != nil {
		t.Fatal(err)
	}
	// 事务：update 1 成功 + remove 不存在的 2 失败 → update 必须回滚。
	_, _, err := c.Submit(SceneTransaction{
		Mutations: []CellMutation{
			&UpdateCell{ID: 1, Revision: 1, Source: "v1"},
			&RemoveMutableCell{ID: 2, Revision: 1},
		},
	})
	if err == nil {
		t.Fatalf("expected failure")
	}
	if c1, _ := s.Cell(1); c1.Source != "v0" || c1.Revision != 0 {
		t.Fatalf("cell 1 not restored: source=%q rev=%d", c1.Source, c1.Revision)
	}
}

func TestSubmitRejectsInvalidFlushPolicy(t *testing.T) {
	s := New()
	c := NewController(s)
	_, _, err := c.Submit(SceneTransaction{
		Mutations: []CellMutation{&AppendCell{Cell: newTestCell(1, KindUser, "u")}},
		Flush:     FlushPolicy(99),
	})
	if err == nil {
		t.Fatalf("expected invalid flush policy rejection")
	}
	if s.Len() != 0 {
		t.Fatalf("scene mutated despite rejected policy: len=%d", s.Len())
	}
}

func TestSubmitBatchFinalizeAtomicVisibility(t *testing.T) {
	// 批量提交（final assistant + final tool chain）用户不可见中间状态：
	// 一次 Submit 内全部 mutation 完成后 Scene 才对外可见。
	s := New()
	c := NewController(s)
	_, _, err := c.Submit(SceneTransaction{
		Cause: "final batch",
		Mutations: []CellMutation{
			&AppendCell{Cell: newTestCell(1, KindAssistant, "stream")},
			&FinalizeCell{ID: 1, Revision: 1, Source: "final assistant"},
			&AppendCell{Cell: func() TranscriptCell {
				cc := newTestCell(2, KindToolChain, "tool start")
				cc.ChainKey = "chain-1"
				return cc
			}()},
			&FinalizeCell{ID: 2, Revision: 1, Source: "tool done"},
		},
		Flush: FlushImmediate,
	})
	if err != nil {
		t.Fatalf("batch submit: %v", err)
	}
	if s.Revision() != 1 {
		t.Fatalf("revision = %d, want 1", s.Revision())
	}
	if s.Len() != 2 {
		t.Fatalf("len = %d, want 2", s.Len())
	}
	for _, id := range []CellID{1, 2} {
		if c0, _ := s.Cell(id); c0.Phase != CellCommitted {
			t.Fatalf("cell %d phase = %v, want committed", id, c0.Phase)
		}
	}
}

func TestBoundaryKeyString(t *testing.T) {
	k := BoundaryKey{PrevCellID: 1, NextCellID: 2, PolicyVersion: 3}
	if got := k.String(); got != "b:1->2@3" {
		t.Fatalf("BoundaryKey.String() = %q", got)
	}
}

// 确保 boundary 包被本包消费（编译期依赖契约）。
var _ = boundary.GapNone
