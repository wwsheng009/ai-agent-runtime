package scene

import (
	"strconv"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/encoding"
)

// mkItem 构造编码器 item（ID 必须是 "item-{n}"）。
func mkItem(kind encoding.ItemKind, id, cause string, status encoding.ItemStatus, head string) *encoding.Item {
	return &encoding.Item{
		ID:      id,
		Seq:     1,
		Kind:    kind,
		CauseID: cause,
		Status:  status,
		Head:    head,
	}
}

func mkChange(op encoding.Op, it *encoding.Item, rev uint64) encoding.ItemChange {
	return encoding.ItemChange{Op: op, Item: it, Revision: rev}
}

func TestCellIDFromItemID(t *testing.T) {
	cases := []struct {
		id   string
		want CellID
		ok   bool
	}{
		{"item-1", 1, true},
		{"item-42", 42, true},
		{"item-0", 0, true},
		{"item-18446744073709551615", CellID(1<<64 - 1), true},
		{"", 0, false},
		{"item-", 0, false},
		{"item-x", 0, false},
		{"Item-1", 0, false},
		{"item-1a", 0, false},
		{"1", 0, false},
		{"item-18446744073709551616", 0, false}, // 溢出
	}
	for _, c := range cases {
		got, err := CellIDFromItemID(c.id)
		if c.ok && (err != nil || got != c.want) {
			t.Errorf("CellIDFromItemID(%q) = %d, %v; want %d", c.id, got, err, c.want)
		}
		if !c.ok && err == nil {
			t.Errorf("CellIDFromItemID(%q) = %d, nil; want error", c.id, got)
		}
	}
}

func TestMapBasicKinds(t *testing.T) {
	// 每个 top-level kind 的 append 映射（§5 表格）。
	kinds := []struct {
		itemKind encoding.ItemKind
		cellKind CellKind
	}{
		{encoding.KindUser, KindUser},
		{encoding.KindAssistant, KindAssistant},
		{encoding.KindReasoning, KindSupplement},
		{encoding.KindCommand, KindCommand},
		{encoding.KindSystem, KindSystem},
		{encoding.KindUserInteraction, KindCommand}, // /debug、/model 输出按 command cell 呈现
	}
	for i, k := range kinds {
		id := CellID(i + 1)
		it := mkItem(k.itemKind, "item-"+itoa(id), "", encoding.StatusPending, "hello")
		m := NewChangeSetMapper(New())
		tx, err := m.Map(&encoding.ChangeSet{Changes: []encoding.ItemChange{mkChange(encoding.OpAppend, it, 1)}})
		if err != nil {
			t.Fatalf("%s: Map: %v", k.itemKind, err)
		}
		if len(tx.Mutations) != 1 {
			t.Fatalf("%s: mutations = %d, want 1", k.itemKind, len(tx.Mutations))
		}
		ap, ok := tx.Mutations[0].(*AppendCell)
		if !ok {
			t.Fatalf("%s: mutation type = %T, want *AppendCell", k.itemKind, tx.Mutations[0])
		}
		if ap.Cell.Kind != k.cellKind {
			t.Errorf("%s: cell kind = %v, want %v", k.itemKind, ap.Cell.Kind, k.cellKind)
		}
		if ap.Cell.ID != id {
			t.Errorf("%s: cell id = %d, want %d", k.itemKind, ap.Cell.ID, id)
		}
		if ap.Cell.ChainKey != "" {
			t.Errorf("%s: chain key = %q, want \"\" (top-level)", k.itemKind, ap.Cell.ChainKey)
		}
		if ap.Cell.Source != "hello" {
			t.Errorf("%s: source = %q", k.itemKind, ap.Cell.Source)
		}
		if ap.Cell.Revision != 1 {
			t.Errorf("%s: revision = %d, want 1", k.itemKind, ap.Cell.Revision)
		}
		wantPhase := CellMutable
		if !streamedKind(k.itemKind) {
			// 一次性 kind（user/command/system/user_interaction）编码器不产
			// 后续 upsert，append 即终态（与编码器事实一致）。
			wantPhase = CellCommitted
		}
		if ap.Cell.Phase != wantPhase {
			t.Errorf("%s: phase = %v, want %v", k.itemKind, ap.Cell.Phase, wantPhase)
		}
		if ap.Cell.Sequence != 0 { // Sequence 由 Scene 提交时分配
			t.Errorf("%s: sequence = %d, want 0 pre-commit", k.itemKind, ap.Cell.Sequence)
		}
	}
}

func TestMapToolChainMerge(t *testing.T) {
	// tool_call append（链首，ChainKey=Item.ID）+ tool_output append（合并，不新建 cell）。
	s := New()
	m := NewChangeSetMapper(s)
	cs := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindToolCall, "item-2", "", encoding.StatusPending, "tool start"), 1),
		mkChange(encoding.OpAppend, mkItem(encoding.KindToolOutput, "item-3", "item-2", encoding.StatusPending, "tool out"), 1),
	}}
	tx, err := m.Map(cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.Mutations) != 2 {
		t.Fatalf("mutations = %d, want 2", len(tx.Mutations))
	}
	ap, ok := tx.Mutations[0].(*AppendCell)
	if !ok || ap.Cell.Kind != KindToolChain || ap.Cell.ChainKey != "item-2" {
		t.Fatalf("first mutation = %#v, want tool-chain append with ChainKey item-2", tx.Mutations[0])
	}
	up, ok := tx.Mutations[1].(*UpdateCell)
	if !ok {
		t.Fatalf("second mutation = %T, want *UpdateCell (merge)", tx.Mutations[1])
	}
	if up.ID != 2 || up.Revision != 2 || up.Source != "tool start\ntool out" {
		t.Fatalf("merge update = %+v, want id 2 rev 2 merged source", up)
	}

	_, rev, err := m.Apply(cs)
	if err != nil {
		t.Fatal(err)
	}
	if rev != 1 {
		t.Fatalf("revision = %d, want 1", rev)
	}
	cells := s.Cells()
	if len(cells) != 1 {
		t.Fatalf("cells = %d, want 1 (tool output merged into chain head)", len(cells))
	}
	c, _ := s.Cell(2)
	if c == nil || c.Source != "tool start\ntool out" || c.Revision != 2 {
		t.Fatalf("chain head after merge = %+v", c)
	}
	if c.Sequence != 0 {
		t.Fatalf("chain head sequence = %d, want 0 (§5.3: tool-chain 不推进 Sequence)", c.Sequence)
	}
}

func TestMapToolChainFinalizeByCallStatus(t *testing.T) {
	// 链首终态由 tool_call 的终态 upsert 触发；tool_output 终态只合并内容。
	// 输出块按追加式合并（编码器 tool 输出事件尚未接入；输出多次写入时
	// 逐次追加，与"tool events 在 cell 内"的稠密语义一致）。
	s := New()
	m := NewChangeSetMapper(s)
	cs := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindToolCall, "item-2", "", encoding.StatusPending, "call"), 1),
		mkChange(encoding.OpAppend, mkItem(encoding.KindToolOutput, "item-3", "item-2", encoding.StatusPending, "out"), 1),
		mkChange(encoding.OpUpsert, mkItem(encoding.KindToolOutput, "item-3", "item-2", encoding.StatusCompleted, "out v2"), 2),
		mkChange(encoding.OpUpsert, mkItem(encoding.KindToolCall, "item-2", "", encoding.StatusCompleted, "call"), 2),
	}}
	tx, err := m.Map(cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.Mutations) != 4 {
		t.Fatalf("mutations = %d, want 4", len(tx.Mutations))
	}
	last, ok := tx.Mutations[3].(*FinalizeCell)
	if !ok {
		t.Fatalf("last mutation = %T, want *FinalizeCell", tx.Mutations[3])
	}
	if last.ID != 2 {
		t.Fatalf("finalize id = %d, want 2", last.ID)
	}
	if _, _, err := m.Apply(cs); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Cell(2)
	if c == nil || c.Phase != CellCommitted {
		t.Fatalf("chain head phase = %v, want committed", c.Phase)
	}
	if c.Source != "call\nout\nout v2" {
		t.Fatalf("chain head source = %q, want merged final content", c.Source)
	}
	if len(s.Cells()) != 1 {
		t.Fatalf("cells = %d, want 1", len(s.Cells()))
	}
}

func TestMapOrphanToolOutput(t *testing.T) {
	// 带 CauseID 但链首缺失：独立成块（编码器"无父时独立块"），并计数。
	s := New()
	m := NewChangeSetMapper(s)
	cs := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindToolOutput, "item-5", "item-2", encoding.StatusPending, "orphan out"), 1),
	}}
	tx, err := m.Map(cs)
	if err != nil {
		t.Fatal(err)
	}
	if m.OrphanOutputs != 1 {
		t.Fatalf("orphan count = %d, want 1", m.OrphanOutputs)
	}
	ap, ok := tx.Mutations[0].(*AppendCell)
	if !ok || ap.Cell.Kind != KindToolChain {
		t.Fatalf("orphan mutation = %T kind %v, want tool-chain append", tx.Mutations[0], ap.Cell.Kind)
	}
	if ap.Cell.ChainKey != "" {
		t.Fatalf("orphan chain key = %q, want \"\" (independent block)", ap.Cell.ChainKey)
	}
	if _, _, err := m.Apply(cs); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Cell(5)
	if c == nil || c.Sequence != 1 {
		t.Fatalf("orphan cell = %+v, want top-level sequence 1", c)
	}
}

func TestMapNoCauseToolOutputIndependent(t *testing.T) {
	s := New()
	m := NewChangeSetMapper(s)
	cs := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindToolOutput, "item-1", "", encoding.StatusPending, "standalone"), 1),
		mkChange(encoding.OpUpsert, mkItem(encoding.KindToolOutput, "item-1", "", encoding.StatusCompleted, "standalone done"), 2),
	}}
	tx, err := m.Map(cs)
	if err != nil {
		t.Fatal(err)
	}
	if m.OrphanOutputs != 0 {
		t.Fatalf("orphan count = %d, want 0 (no CauseID is a normal independent block)", m.OrphanOutputs)
	}
	if len(tx.Mutations) != 2 {
		t.Fatalf("mutations = %d, want 2", len(tx.Mutations))
	}
	if _, ok := tx.Mutations[1].(*FinalizeCell); !ok {
		t.Fatalf("second mutation = %T, want *FinalizeCell", tx.Mutations[1])
	}
}

func TestMapUpsertUnknownCellFails(t *testing.T) {
	s := New()
	m := NewChangeSetMapper(s)
	cs := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpUpsert, mkItem(encoding.KindAssistant, "item-3", "", encoding.StatusRunning, "delta"), 2),
	}}
	if _, err := m.Map(cs); err == nil {
		t.Fatal("expected error for upsert of unknown cell")
	}
	if _, _, err := m.Apply(cs); err == nil {
		t.Fatal("expected Apply error; scene must be unchanged")
	}
	if s.Revision() != 0 || s.Len() != 0 {
		t.Fatalf("scene mutated on failed apply: rev %d len %d", s.Revision(), s.Len())
	}
}

func TestMapRemove(t *testing.T) {
	s := New()
	m := NewChangeSetMapper(s)

	// mutable cell：remove 成功。
	if _, _, err := m.Apply(&encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindToolCall, "item-2", "", encoding.StatusPending, "x"), 1),
	}}); err != nil {
		t.Fatal(err)
	}
	tx, err := m.Map(&encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpRemove, mkItem(encoding.KindToolCall, "item-2", "", encoding.StatusPending, "x"), 2),
	}})
	if err != nil {
		t.Fatal(err)
	}
	rm, ok := tx.Mutations[0].(*RemoveMutableCell)
	if !ok || rm.ID != 2 || rm.Revision != 2 {
		t.Fatalf("remove mutation = %#v, want RemoveMutableCell id 2 rev 2", tx.Mutations[0])
	}
	if _, _, err := m.Apply(&encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpRemove, mkItem(encoding.KindToolCall, "item-2", "", encoding.StatusPending, "x"), 2),
	}}); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 {
		t.Fatalf("len = %d, want 0 after remove", s.Len())
	}

	// committed cell：remove 是会话级 backtrack，显式失败。
	if _, _, err := m.Apply(&encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindAssistant, "item-4", "", encoding.StatusPending, "a"), 1),
		mkChange(encoding.OpUpsert, mkItem(encoding.KindAssistant, "item-4", "", encoding.StatusCompleted, "a done"), 2),
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Map(&encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpRemove, mkItem(encoding.KindAssistant, "item-4", "", encoding.StatusCompleted, "a done"), 3),
	}}); err == nil {
		t.Fatal("expected error for remove of committed cell")
	}

	// 未知 cell：remove 失败。
	if _, err := m.Map(&encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpRemove, mkItem(encoding.KindUser, "item-9", "", encoding.StatusPending, "u"), 1),
	}}); err == nil {
		t.Fatal("expected error for remove of unknown cell")
	}
}

func TestMapRevisionMonotonicAcrossMerges(t *testing.T) {
	// 回归：tool_output 合并推进 cell revision 后，tool_call 自身 upsert
	// 不得因 revision 碰撞失败（INV-SCENE-03）——映射器用影子状态统一递增。
	// tool_call 的 Head 按编码器事实保持恒定（applyToolFinished 只改
	// Status），因此内容不变，只推进 revision/phase。
	s := New()
	m := NewChangeSetMapper(s)
	cs := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindToolCall, "item-2", "", encoding.StatusPending, "call"), 1),
		mkChange(encoding.OpAppend, mkItem(encoding.KindToolOutput, "item-3", "item-2", encoding.StatusPending, "out1"), 1),
		mkChange(encoding.OpUpsert, mkItem(encoding.KindToolCall, "item-2", "", encoding.StatusRunning, "call"), 2),
		mkChange(encoding.OpUpsert, mkItem(encoding.KindToolOutput, "item-3", "item-2", encoding.StatusPending, "out2"), 2),
		mkChange(encoding.OpUpsert, mkItem(encoding.KindToolCall, "item-2", "", encoding.StatusCompleted, "call"), 3),
	}}
	if _, _, err := m.Apply(cs); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Cell(2)
	if c == nil {
		t.Fatal("chain head missing")
	}
	if c.Revision != 6 {
		// 1 append + 2 merges + 2 upserts = 5；批次收尾 finalize 严格大于
		// 当前（INV-SCENE-03，允许跳号）→ 6。
		t.Fatalf("chain head revision = %d, want 6 (1 append + 2 merges + 2 upserts + finalize)", c.Revision)
	}
	if c.Phase != CellCommitted {
		t.Fatalf("phase = %v, want committed", c.Phase)
	}
	if c.Source != "call\nout1\nout2" {
		t.Fatalf("source = %q, want ordered merge", c.Source)
	}
}

func TestMapToolCallHeadEvolution(t *testing.T) {
	// 防御路径：tool_call 自身文本演进时，已合并输出不得丢失
	// （当前编码器 Head 恒定；此测试锁定拆分替换语义）。
	s := New()
	m := NewChangeSetMapper(s)
	batch := func(changes ...encoding.ItemChange) {
		t.Helper()
		if _, _, err := m.Apply(&encoding.ChangeSet{Changes: changes}); err != nil {
			t.Fatal(err)
		}
	}
	batch(
		mkChange(encoding.OpAppend, mkItem(encoding.KindToolCall, "item-2", "", encoding.StatusPending, "bash -c ls"), 1),
		mkChange(encoding.OpAppend, mkItem(encoding.KindToolOutput, "item-3", "item-2", encoding.StatusPending, "out1"), 1),
	)
	batch(
		mkChange(encoding.OpUpsert, mkItem(encoding.KindToolCall, "item-2", "", encoding.StatusCompleted, "bash -c ls (done)"), 2),
	)
	c, _ := s.Cell(2)
	if c == nil {
		t.Fatal("chain head missing")
	}
	if c.Source != "bash -c ls (done)\nout1" {
		t.Fatalf("source = %q, want evolved head with merged output retained", c.Source)
	}
}

func TestMapFlushPolicy(t *testing.T) {
	s := New()
	m := NewChangeSetMapper(s)
	// 建立 assistant cell，供后续纯 upsert 使用。
	if _, _, err := m.Apply(&encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindAssistant, "item-1", "", encoding.StatusPending, "a"), 1),
	}}); err != nil {
		t.Fatal(err)
	}
	// 纯 upsert（mutable update）→ 可合并 flush。
	tx, err := m.Map(&encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpUpsert, mkItem(encoding.KindAssistant, "item-1", "", encoding.StatusRunning, "d"), 2),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Flush != FlushCoalescable {
		t.Fatalf("flush = %v, want coalescable for pure updates", tx.Flush)
	}
	// 任何结构变化 → 立即 flush。
	tx, err = m.Map(&encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindUser, "item-1", "", encoding.StatusPending, "u"), 1),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Flush != FlushImmediate {
		t.Fatalf("flush = %v, want immediate for append", tx.Flush)
	}
	// finalize 也是结构变化 → 立即。
	tx, err = m.Map(&encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpUpsert, mkItem(encoding.KindAssistant, "item-1", "", encoding.StatusCompleted, "d"), 2),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if tx.Flush != FlushImmediate {
		t.Fatalf("flush = %v, want immediate for finalize", tx.Flush)
	}
}

func TestMapUnknownKindFallsBack(t *testing.T) {
	s := New()
	m := NewChangeSetMapper(s)
	it := mkItem(encoding.ItemKind("mystery_kind"), "item-1", "", encoding.StatusPending, "?")

	// 未知 kind 的 upsert 需要 cell 已存在；先用 append 建立。
	if _, err := m.Map(&encoding.ChangeSet{Changes: []encoding.ItemChange{mkChange(encoding.OpAppend, it, 1)}}); err != nil {
		t.Fatal(err)
	}
	if m.FallbackCount != 1 {
		t.Fatalf("fallback count = %d, want 1", m.FallbackCount)
	}
	if _, _, err := m.Apply(&encoding.ChangeSet{Changes: []encoding.ItemChange{mkChange(encoding.OpAppend, it, 1)}}); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Cell(1)
	if c == nil || c.Kind != KindDiagnostic {
		t.Fatalf("fallback cell kind = %v, want diagnostic", c.Kind)
	}
}

func TestMapTerminalAppend(t *testing.T) {
	// 编码器 append 恒为 pending；宽容处理终态 append 直接落盘 committed。
	s := New()
	m := NewChangeSetMapper(s)
	cs := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindSystem, "item-1", "", encoding.StatusCompleted, "done"), 1),
	}}
	if _, _, err := m.Apply(cs); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Cell(1)
	if c == nil || c.Phase != CellCommitted {
		t.Fatalf("terminal append phase = %v, want committed", c.Phase)
	}
}

func TestApplyEmptyChangeSet(t *testing.T) {
	s := New()
	m := NewChangeSetMapper(s)
	if _, _, err := m.Apply(&encoding.ChangeSet{Changes: nil}); err != nil {
		t.Fatal(err)
	}
	if s.Revision() != 0 {
		t.Fatalf("revision = %d, want 0 (empty changeset must not submit)", s.Revision())
	}
	if _, err := m.Map(nil); err != nil {
		t.Fatal(err)
	}
}

func TestApplyFailureIsAtomic(t *testing.T) {
	// 同批中一个非法 change 使整个事务失败：前面的合法 change 不得生效。
	s := New()
	m := NewChangeSetMapper(s)
	cs := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindUser, "item-1", "", encoding.StatusPending, "u"), 1),
		mkChange(encoding.OpUpsert, mkItem(encoding.KindAssistant, "item-9", "", encoding.StatusRunning, "ghost"), 2),
	}}
	if _, _, err := m.Apply(cs); err == nil {
		t.Fatal("expected Apply error")
	}
	if s.Revision() != 0 || s.Len() != 0 {
		t.Fatalf("scene changed after failed apply: rev %d len %d (INV-FRAME-01)", s.Revision(), s.Len())
	}
}

func TestReplayDeterminism(t *testing.T) {
	// 同一 ChangeSet 序列应用到两个全新 Scene（重放场景）：快照必须一致。
	sequences := [][]encoding.ItemChange{
		{
			mkChange(encoding.OpAppend, mkItem(encoding.KindUser, "item-1", "", encoding.StatusPending, "u1"), 1),
			mkChange(encoding.OpAppend, mkItem(encoding.KindAssistant, "item-2", "", encoding.StatusPending, "a1"), 1),
			mkChange(encoding.OpUpsert, mkItem(encoding.KindAssistant, "item-2", "", encoding.StatusRunning, "a1 delta"), 2),
			mkChange(encoding.OpAppend, mkItem(encoding.KindToolCall, "item-3", "", encoding.StatusPending, "call"), 1),
			mkChange(encoding.OpAppend, mkItem(encoding.KindToolOutput, "item-4", "item-3", encoding.StatusPending, "out"), 1),
			mkChange(encoding.OpUpsert, mkItem(encoding.KindToolCall, "item-3", "", encoding.StatusCompleted, "call done"), 2),
			mkChange(encoding.OpUpsert, mkItem(encoding.KindAssistant, "item-2", "", encoding.StatusCompleted, "a1 final"), 3),
			// 一次性 kind（编码器不产后续 upsert）：append 即终态。
			mkChange(encoding.OpAppend, mkItem(encoding.KindSystem, "item-5", "", encoding.StatusPending, "sys"), 1),
		},
	}

	run := func() []*TranscriptCell {
		s := New()
		m := NewChangeSetMapper(s)
		for _, ch := range sequences[0] {
			cs := &encoding.ChangeSet{Changes: []encoding.ItemChange{ch}}
			if _, _, err := m.Apply(cs); err != nil {
				t.Fatal(err)
			}
		}
		return s.Cells()
	}

	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatalf("replay cell counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca.ID != cb.ID || ca.Kind != cb.Kind || ca.Source != cb.Source ||
			ca.Revision != cb.Revision || ca.Phase != cb.Phase || ca.ChainKey != cb.ChainKey ||
			ca.Sequence != cb.Sequence {
			t.Errorf("replay cell %d differs:\n  a=%+v\n  b=%+v", i, ca, cb)
		}
	}
}

func TestMapNilItemFails(t *testing.T) {
	m := NewChangeSetMapper(New())
	if _, err := m.Map(&encoding.ChangeSet{Changes: []encoding.ItemChange{{Op: encoding.OpAppend, Revision: 1}}}); err == nil {
		t.Fatal("expected error for nil item")
	}
}

func TestMapSameBatchShadowState(t *testing.T) {
	// 同批 append + upsert（终态）作用于同一 cell：finalize 基于影子状态，
	// 不依赖 Scene 提交（Map 尚未提交）。
	s := New()
	m := NewChangeSetMapper(s)
	cs := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindAssistant, "item-1", "", encoding.StatusPending, "a"), 1),
		mkChange(encoding.OpUpsert, mkItem(encoding.KindAssistant, "item-1", "", encoding.StatusCompleted, "a done"), 2),
	}}
	tx, err := m.Map(cs)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.Mutations) != 2 {
		t.Fatalf("mutations = %d, want 2", len(tx.Mutations))
	}
	if _, ok := tx.Mutations[1].(*FinalizeCell); !ok {
		t.Fatalf("second mutation = %T, want *FinalizeCell", tx.Mutations[1])
	}
	if _, _, err := m.Apply(cs); err != nil {
		t.Fatal(err)
	}
	c, _ := s.Cell(1)
	if c == nil || c.Phase != CellCommitted || c.Revision != 3 {
		// append rev1 + 终态 upsert rev2 → 批次收尾 finalize 严格大于当前
		// （INV-SCENE-03，允许跳号）→ rev3。
		t.Fatalf("cell = %+v, want committed rev 3", c)
	}
}

// itoa 是测试辅助（把 CellID 格式化为 "item-{n}" 的数字部分）。
func itoa(v CellID) string {
	return strconv.FormatUint(uint64(v), 10)
}

func TestMapAppendAfterID(t *testing.T) {
	// Tail 锚定插入映射（设计文档 §1.3 行 12）：OpAppend 携带 AfterID 时
	// 映射为 InsertCell（锚点必须已提交），否则 AppendCell。
	s := New()
	m := NewChangeSetMapper(s)
	if _, _, err := m.Apply(&encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindUser, "item-1", "", encoding.StatusCompleted, "u"), 1),
		mkChange(encoding.OpAppend, mkItem(encoding.KindCommand, "item-2", "", encoding.StatusCompleted, "cmd"), 1),
	}}); err != nil {
		t.Fatal(err)
	}
	// 锚定插入 item-3 到 item-1 之后：渲染顺序 [item-1, item-3, item-2]。
	it := mkItem(encoding.KindUserInteraction, "item-3", "", encoding.StatusCompleted, "/debug 输出")
	cs := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		{Op: encoding.OpAppend, Item: it, Revision: 1, AfterID: "item-1"},
	}}
	tx, err := m.Map(cs)
	if err != nil {
		t.Fatalf("Map: %v", err)
	}
	if len(tx.Mutations) != 1 {
		t.Fatalf("mutations = %d, want 1", len(tx.Mutations))
	}
	ins, ok := tx.Mutations[0].(*InsertCell)
	if !ok {
		t.Fatalf("mutation type = %T, want *InsertCell", tx.Mutations[0])
	}
	if ins.After != 1 || ins.Cell.ID != 3 {
		t.Fatalf("insert = After %d CellID %d, want After 1 CellID 3", ins.After, ins.Cell.ID)
	}
	if _, _, err := m.Apply(cs); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	cells := s.Cells()
	if len(cells) != 3 || cells[0].ID != 1 || cells[1].ID != 3 || cells[2].ID != 2 {
		t.Fatalf("order = [%d %d %d], want [1 3 2]", cells[0].ID, cells[1].ID, cells[2].ID)
	}
	// 锚点不存在：显式失败（INV-FRAME-01），Scene 不变。
	bad := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		{Op: encoding.OpAppend, Item: mkItem(encoding.KindUserInteraction, "item-4", "", encoding.StatusCompleted, "x"), Revision: 1, AfterID: "item-999"},
	}}
	if _, err := m.Map(bad); err == nil {
		t.Fatalf("expected missing-anchor error")
	}
	if s.Len() != 3 {
		t.Fatalf("len = %d, want 3（失败不产生变更）", s.Len())
	}
	// AfterID 为空退化为 AppendCell（编码器锚点缺失退化语义）。
	plain := &encoding.ChangeSet{Changes: []encoding.ItemChange{
		mkChange(encoding.OpAppend, mkItem(encoding.KindUserInteraction, "item-5", "", encoding.StatusCompleted, "y"), 1),
	}}
	tx2, err := m.Map(plain)
	if err != nil {
		t.Fatalf("Map plain: %v", err)
	}
	if _, ok := tx2.Mutations[0].(*AppendCell); !ok {
		t.Fatalf("mutation type = %T, want *AppendCell", tx2.Mutations[0])
	}
}
