package mesh

import (
	"os"
	"testing"
)

// 审计开关（--mesh-journal=false，架构 §9.5 / §10）的机器化契约：
//   - journal 仍然存在、仍然分配 seq（扇入与 SSE 的 seq 与它同源，§6.3）；
//   - 一行都不写（连文件都不建）；
//   - 不算 Failed（Failed 只表示写盘故障，§4.7），扇入照常可推帧。

// TestJournalDisabledAllocatesSeqWithoutWriting 验证 journal 级的「关审计」语义：
// 静默丢弃、不占序号，但 seq 计数器照常前进。
func TestJournalDisabledAllocatesSeqWithoutWriting(t *testing.T) {
	paths := testMeshPaths(t)
	journal := OpenJournalWithOptions(paths, "node-quiet", JournalOptions{
		Disabled: true,
		Now:      newFakeClock().Now,
	})
	if journal == nil {
		t.Fatal("网格根可用时必须返回 journal（审计关闭 ≠ 网格不可用）")
	}
	if !journal.Disabled() {
		t.Fatal("Disabled() 必须如实回显 --mesh-journal=false")
	}
	if seq := journal.Append(JournalNodeStarted, "", map[string]any{"pid": 1}); seq != 0 {
		t.Fatalf("Append = %d, want 0（审计关闭：静默丢弃、不占序号）", seq)
	}
	if seq := journal.NextSeq(); seq != 1 {
		t.Fatalf("NextSeq = %d, want 1（seq 仍分配，扇入不受影响）", seq)
	}
	if _, err := os.Stat(journal.Path()); !os.IsNotExist(err) {
		t.Fatalf("审计关闭时不得创建 journal 文件（stat err=%v）", err)
	}
	if journal.Failed() {
		t.Fatal("审计关闭不是失败（Failed 只表示写盘故障）")
	}
}

// TestHostJournalDisabledKeepsRecordAndFanin 验证开关只影响审计：档案照写、
// 扇入照推（帧的 seq 与 journal 同源），只是 journal 文件不出现。
func TestHostJournalDisabledKeepsRecordAndFanin(t *testing.T) {
	paths := testMeshPaths(t)
	host := NewHost(HostConfig{
		JournalDisabled: true,
		Now:             newFakeClock().Now,
		Warn:            func(string, ...any) {},
	})
	if err := host.Start(); err != nil {
		t.Fatalf("host.Start: %v", err)
	}
	t.Cleanup(host.Close)

	if record := readRecord(t, paths, host.NodeID()); record.NodeID != host.NodeID() {
		t.Fatalf("档案照写：record.NodeID = %q, want %q", record.NodeID, host.NodeID())
	}
	if _, err := os.Stat(paths.JournalPath(host.NodeID())); !os.IsNotExist(err) {
		t.Fatalf("审计关闭时不得写 journal（stat err=%v）", err)
	}
	fanin := host.Fanin()
	if fanin == nil || !fanin.Enabled() {
		t.Fatal("审计关闭不得连带禁用扇入（§9.5：其余功能不受影响）")
	}
	if seq := fanin.PublishLocal("mesh.test", map[string]any{"k": "v"}); seq == 0 {
		t.Fatal("扇入帧必须照常分配 seq（与 journal 同源计数器）")
	}
	if fanin.Published() != 1 {
		t.Fatalf("Published = %d, want 1（审计关闭不影响扇出）", fanin.Published())
	}
}
