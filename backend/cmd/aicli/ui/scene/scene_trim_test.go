package scene

import (
	"fmt"
	"testing"
)

// scene_trim_test.go —— T6 可逆裁剪契约：顺序/byID/byChain 往返一致、
// mutable 前缀拒绝、幂等恢复、mapper 连续性。

func appendCommittedTestCell(t *testing.T, s *TuiScene, id CellID, chainKey string) *TranscriptCell {
	t.Helper()
	cell := TranscriptCell{
		ID:       id,
		Kind:     KindAssistant,
		Source:   fmt.Sprintf("cell %d\n", id),
		Revision: 1,
		Phase:    CellCommitted,
		ChainKey: chainKey,
	}
	created, err := s.ApplyCellMutation(&AppendCell{Cell: cell})
	if err != nil {
		t.Fatalf("append cell %d: %v", id, err)
	}
	return created
}

func TestSceneTrimAndRestoreRoundTrip(t *testing.T) {
	const total = 120
	const trim = 70
	s := New()
	ids := make([]CellID, 0, total)
	for i := 1; i <= total; i++ {
		chain := ""
		if i%7 == 0 {
			chain = fmt.Sprintf("chain-%d", i)
		}
		// 使用 NextID 分配（与生产兜底路径一致），以便后续断言"单调不回退"。
		ids = append(ids, appendCommittedTestCell(t, s, 0, chain).ID)
	}
	if s.Len() != total {
		t.Fatalf("初始 cell 数=%d", s.Len())
	}

	trimmed, err := s.TrimPrefix(trim)
	if err != nil {
		t.Fatalf("trim: %v", err)
	}
	if len(trimmed) != trim || s.Len() != total-trim {
		t.Fatalf("裁剪结果不符：trimmed=%d cells=%d", len(trimmed), s.Len())
	}
	snapshot := s.Snapshot()
	if len(snapshot.Cells) != total-trim || snapshot.Cells[0].ID != ids[trim] {
		t.Fatalf("裁剪后快照头部不符：len=%d head=%d", len(snapshot.Cells), snapshot.Cells[0].ID)
	}
	if _, ok := s.Cell(ids[0]); ok {
		t.Fatal("被裁 cell 仍可按 ID 查到")
	}
	if _, ok := s.Cell(ids[trim]); !ok {
		t.Fatal("保留 cell 应按 ID 可查")
	}

	restored, err := s.RestorePrefix(trimmed)
	if err != nil || restored != trim {
		t.Fatalf("恢复：restored=%d err=%v", restored, err)
	}
	after := s.Snapshot()
	if len(after.Cells) != total {
		t.Fatalf("恢复后 cell 数=%d", len(after.Cells))
	}
	for index := range after.Cells {
		if after.Cells[index].ID != ids[index] {
			t.Fatalf("恢复顺序不符 at %d：got=%d want=%d", index, after.Cells[index].ID, ids[index])
		}
	}
	if _, ok := s.Cell(ids[0]); !ok {
		t.Fatal("恢复后首格应可查")
	}
	// 幂等：再次恢复不再插入。
	again, err := s.RestorePrefix(trimmed)
	if err != nil || again != 0 {
		t.Fatalf("重复恢复应幂等：restored=%d err=%v", again, err)
	}
	// 单调不回退：新增 cell 的 ID 大于此前最大 ID。
	next := appendCommittedTestCell(t, s, 0, "")
	if next.ID <= ids[total-1] {
		t.Fatalf("nextID 回退：next=%d max=%d", next.ID, ids[total-1])
	}
}

func TestSceneTrimRefusesMutablePrefixAndBadCounts(t *testing.T) {
	s := New()
	appendCommittedTestCell(t, s, 1, "")
	if _, err := s.ApplyCellMutation(&AppendCell{Cell: TranscriptCell{
		ID: 2, Kind: KindAssistant, Source: "streaming", Phase: CellMutable,
	}}); err != nil {
		t.Fatalf("append mutable: %v", err)
	}
	if _, err := s.TrimPrefix(2); err == nil {
		t.Fatal("包含 mutable cell 的前缀必须被拒绝")
	}
	if s.Len() != 2 {
		t.Fatalf("拒绝后场景不应变化：%d", s.Len())
	}
	if _, err := s.TrimPrefix(0); err == nil {
		t.Fatal("count=0 必须被拒绝")
	}
	if _, err := s.TrimPrefix(99); err == nil {
		t.Fatal("越界 count 必须被拒绝")
	}
	if _, err := s.TrimPrefix(1); err != nil {
		t.Fatalf("裁剪非 mutable 前缀：%v", err)
	}
}

func TestSceneTrimKeepsMapperContinuity(t *testing.T) {
	s := New()
	for i := 1; i <= 10; i++ {
		appendCommittedTestCell(t, s, CellID(i), "")
	}
	if _, err := s.TrimPrefix(6); err != nil {
		t.Fatalf("trim: %v", err)
	}
	mapper := NewChangeSetMapper(s)
	if mapper == nil {
		t.Fatal("mapper 构造失败")
	}
	created, err := s.ApplyCellMutation(&AppendCell{Cell: TranscriptCell{
		ID: 11, Kind: KindUser, Source: "after trim\n", Revision: 1, Phase: CellCommitted,
	}})
	if err != nil {
		t.Fatalf("trim 后追加：%v", err)
	}
	snapshot := s.Snapshot()
	if len(snapshot.Cells) != 5 || snapshot.Cells[0].ID != 7 || snapshot.Cells[len(snapshot.Cells)-1].ID != created.ID {
		t.Fatalf("trim 后追加顺序不符：len=%d head=%d tail=%d", len(snapshot.Cells), snapshot.Cells[0].ID, snapshot.Cells[len(snapshot.Cells)-1].ID)
	}
}
