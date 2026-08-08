package scene

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/boundary"
)

// newTestCell 构造最小 top-level cell。
func newTestCell(id CellID, kind CellKind, source string) TranscriptCell {
	return TranscriptCell{ID: id, Kind: kind, Source: source, Boundary: boundary.BoundaryNormal}
}

func TestSceneAppendAssignsIDAndSequence(t *testing.T) {
	s := New()
	c1 := newTestCell(0, KindUser, "hi")
	got, err := s.ApplyCellMutation(&AppendCell{Cell: c1})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if got.ID == 0 {
		t.Fatalf("expected auto-assigned ID, got 0")
	}
	if got.Sequence != 1 {
		t.Fatalf("first top-level sequence = %d, want 1", got.Sequence)
	}
	c2 := newTestCell(0, KindAssistant, "hello")
	got2, err := s.ApplyCellMutation(&AppendCell{Cell: c2})
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if got2.Sequence != 2 {
		t.Fatalf("second top-level sequence = %d, want 2", got2.Sequence)
	}
}

func TestSceneAppendDuplicateIDRejected(t *testing.T) {
	// INV-SCENE-02：每个 transcript cell 有不可复用的 CellID。
	s := New()
	_, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(7, KindUser, "a")})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	_, err = s.ApplyCellMutation(&AppendCell{Cell: newTestCell(7, KindUser, "b")})
	if err == nil {
		t.Fatalf("expected duplicate ID rejection")
	}
}

func TestSceneToolChainMembersDoNotAdvanceSequence(t *testing.T) {
	// §5.3：Sequence 只在创建 top-level cell 时增加；tool-chain 成员不推进。
	s := New()
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(1, KindUser, "u")}); err != nil {
		t.Fatal(err)
	}
	chain := newTestCell(2, KindToolChain, "tool start")
	chain.ChainKey = "chain-1"
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: chain}); err != nil {
		t.Fatal(err)
	}
	// 链内后续事件通过 mutable update 合并进链首 cell（§7.3：tool events 在 cell 内）。
	if _, err := s.ApplyCellMutation(&UpdateCell{ID: 2, Revision: 1, Source: "tool start\ntool out"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Len(); got != 2 {
		t.Fatalf("len = %d, want 2", got)
	}
	if c, _ := s.Cell(2); c.Sequence != 0 {
		t.Fatalf("chain member sequence = %d, want 0 (not advanced)", c.Sequence)
	}
	// 链后新 top-level cell 的 Sequence 仍从 1 之后继续（链不占序号）。
	u2 := newTestCell(3, KindUser, "v")
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: u2}); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Cell(3); c.Sequence != 2 {
		t.Fatalf("post-chain top-level sequence = %d, want 2", c.Sequence)
	}
}

func TestSceneDuplicateChainKeyRejected(t *testing.T) {
	s := New()
	a := newTestCell(1, KindToolChain, "a")
	a.ChainKey = "chain-1"
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: a}); err != nil {
		t.Fatal(err)
	}
	b := newTestCell(2, KindToolChain, "b")
	b.ChainKey = "chain-1"
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: b}); err == nil {
		t.Fatalf("expected duplicate chain key rejection")
	}
}

func TestSceneUpdateRequiresNewerRevision(t *testing.T) {
	// INV-SCENE-03：旧 revision 不得覆盖新 revision。
	s := New()
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(1, KindAssistant, "v0")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCellMutation(&UpdateCell{ID: 1, Revision: 1, Source: "v1"}); err != nil {
		t.Fatalf("rev 1 update: %v", err)
	}
	if _, err := s.ApplyCellMutation(&UpdateCell{ID: 1, Revision: 1, Source: "stale"}); err == nil {
		t.Fatalf("expected stale revision rejection")
	}
	if _, err := s.ApplyCellMutation(&UpdateCell{ID: 1, Revision: 2, Source: "v2"}); err != nil {
		t.Fatalf("rev 2 update: %v", err)
	}
}

func TestSceneFinalizeIsStateTransitionNotAppend(t *testing.T) {
	// INV-SCENE-04：finalize 是同一 cell 的状态迁移，不 append 新 cell。
	s := New()
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(1, KindAssistant, "stream")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCellMutation(&FinalizeCell{ID: 1, Revision: 2, Source: "final"}); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1 (finalize must not append)", s.Len())
	}
	c, _ := s.Cell(1)
	if c.Phase != CellCommitted {
		t.Fatalf("phase = %v, want committed", c.Phase)
	}
	if c.FinalizedAt == nil {
		t.Fatalf("expected FinalizedAt set")
	}
	// finalize 后不可再 update（committed 不可变）。
	if _, err := s.ApplyCellMutation(&UpdateCell{ID: 1, Revision: 3, Source: "late"}); err == nil {
		t.Fatalf("expected update-after-finalize rejection")
	}
}

func TestSceneRemoveOnlyMutable(t *testing.T) {
	s := New()
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(1, KindAssistant, "a")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCellMutation(&FinalizeCell{ID: 1, Revision: 1, Source: "f"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCellMutation(&RemoveMutableCell{ID: 1, Revision: 2}); err == nil {
		t.Fatalf("expected committed cell removal rejection")
	}
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(2, KindAssistant, "b")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCellMutation(&RemoveMutableCell{ID: 2, Revision: 1}); err != nil {
		t.Fatalf("mutable remove: %v", err)
	}
	if s.Len() != 1 {
		t.Fatalf("len = %d, want 1 after remove", s.Len())
	}
}

func TestSceneSnapshotIsImmutableCopy(t *testing.T) {
	s := New()
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(1, KindUser, "u")}); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()
	// COW：已发布快照引用的 cell 不被后续 mutation 修改。
	if _, err := s.ApplyCellMutation(&UpdateCell{ID: 1, Source: "updated", Revision: 2}); err != nil {
		t.Fatal(err)
	}
	if got := snap.Cells[0].Source; got != "u" {
		t.Fatalf("snapshot cell source = %q, want %q (COW violated)", got, "u")
	}
	if got, _ := s.Cell(1); got.Source != "updated" {
		t.Fatalf("scene cell source = %q, want %q", got.Source, "updated")
	}
	// Scene 继续变更后，旧快照保持原 revision 与 cell 集合。
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(2, KindAssistant, "a")}); err != nil {
		t.Fatal(err)
	}
	if snap.Revision != 0 {
		t.Fatalf("snapshot revision = %d, want 0 (pre-append)", snap.Revision)
	}
	if len(snap.Cells) != 1 {
		t.Fatalf("snapshot cells = %d, want 1", len(snap.Cells))
	}
}

func TestSceneBoundaryMetaProjection(t *testing.T) {
	c := newTestCell(1, KindAssistant, "x")
	c.ChainKey = "chain-1"
	meta := c.BoundaryMeta()
	if meta.ChainKey != "chain-1" || meta.TopLevel {
		t.Fatalf("chain member meta = %+v, want ChainKey set, TopLevel=false", meta)
	}
	top := newTestCell(2, KindCommand, "y")
	meta2 := top.BoundaryMeta()
	if meta2.TopLevel == false || meta2.Kind != boundary.KindCommand {
		t.Fatalf("top-level command meta = %+v", meta2)
	}
	// 非 tool-chain 的 cell 声明 Dense 被规范化为 Normal。
	dense := newTestCell(3, KindSystem, "z")
	dense.Boundary = boundary.BoundaryDense
	if meta3 := dense.BoundaryMeta(); meta3.Boundary != boundary.BoundaryNormal {
		t.Fatalf("non-chain dense normalized to %v, want normal", meta3.Boundary)
	}
}

func TestSceneInsertCellAfterAnchor(t *testing.T) {
	// Tail 锚定插入（设计文档 §1.3 行 12）：交互输出插到锚点 cell 之后，
	// 参与渲染总序（数组顺序）；后续 append 仍排在其后。
	s := New()
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(1, KindUser, "u")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(2, KindCommand, "cmd")}); err != nil {
		t.Fatal(err)
	}
	// 以 U1（id=1）为锚点插入交互输出：渲染顺序应为 [u, interaction, cmd]。
	got, err := s.ApplyCellMutation(&InsertCell{
		After: 1,
		Cell:  newTestCell(3, KindCommand, "/debug 输出"),
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if got.ID != 3 || got.Sequence != 3 {
		t.Fatalf("inserted cell = %+v, want ID 3 Sequence 3", got)
	}
	cells := s.Cells()
	if len(cells) != 3 || cells[0].ID != 1 || cells[1].ID != 3 || cells[2].ID != 2 {
		t.Fatalf("order = [%d %d %d], want [1 3 2]（锚定插入，cmd 仍在后）",
			cells[0].ID, cells[1].ID, cells[2].ID)
	}
	// 锚点缺失：显式失败（INV-FRAME-01 回滚），Scene 不变。
	if _, err := s.ApplyCellMutation(&InsertCell{After: 999, Cell: newTestCell(4, KindCommand, "x")}); err == nil {
		t.Fatalf("expected missing-anchor rejection")
	}
	if s.Len() != 3 {
		t.Fatalf("len = %d, want 3（失败回滚）", s.Len())
	}
	// ID 唯一（INV-SCENE-02）。
	if _, err := s.ApplyCellMutation(&InsertCell{After: 1, Cell: newTestCell(3, KindCommand, "dup")}); err == nil {
		t.Fatalf("expected duplicate ID rejection")
	}
	// 插入后新 append 排在交互输出之后（锚点语义不改变 append 全序）。
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(5, KindUser, "v")}); err != nil {
		t.Fatal(err)
	}
	cells = s.Cells()
	if len(cells) != 4 || cells[3].ID != 5 {
		t.Fatalf("final order = [%d %d %d %d], want 5 at tail",
			cells[0].ID, cells[1].ID, cells[2].ID, cells[3].ID)
	}
}

func TestSceneNextIDNeverReused(t *testing.T) {
	// INV-SCENE-02 语义扩展：即使 remove 后，自动分配的 ID 不复用。
	s := New()
	a, _ := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(0, KindAssistant, "a")})
	if _, err := s.ApplyCellMutation(&RemoveMutableCell{ID: a.ID, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.ApplyCellMutation(&AppendCell{Cell: newTestCell(0, KindAssistant, "b")})
	if b.ID == a.ID {
		t.Fatalf("auto ID %d reused after remove", a.ID)
	}
}
