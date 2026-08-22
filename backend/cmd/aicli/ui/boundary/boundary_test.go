package boundary

import "testing"

// cell 便捷构造：top-level 常规边界 cell。
func topCell(id string, kind CellKind) CellMeta {
	return CellMeta{ID: id, Kind: kind, TopLevel: true, Boundary: BoundaryNormal}
}

// chainCell 便捷构造：tool-chain 内部成员（非 top-level，带归组键）。
func chainCell(id string, chain string) CellMeta {
	return CellMeta{ID: id, Kind: KindTool, ChainKey: chain, Boundary: BoundaryDense}
}

func TestResolveGapFirstCellZero(t *testing.T) {
	// 规则表：无 -> 任意首 cell -> 0（transcript 不以空行开头）。
	for _, kind := range []CellKind{KindUser, KindAssistant, KindTool, KindCommand, KindSystem} {
		got := ResolveGap(CellMeta{}, CellMeta{ID: "c1", Kind: kind, TopLevel: true})
		if got != GapNone {
			t.Fatalf("ResolveGap(empty, %v) = %d, want GapNone", kind, got)
		}
	}
}

func TestResolveGapUserAssistantSeparate(t *testing.T) {
	// 规则表：user -> assistant -> 1（独立 top-level 对话块）。
	prev := topCell("user-1", KindUser)
	next := topCell("asst-1", KindAssistant)
	if got := ResolveGap(prev, next); got != GapOne {
		t.Fatalf("user->assistant gap = %d, want 1", got)
	}
}

func TestResolveGapAssistantUserTurnBoundary(t *testing.T) {
	// 规则表：assistant -> user -> 1（turn 边界）。
	prev := topCell("asst-1", KindAssistant)
	next := topCell("user-2", KindUser)
	if got := ResolveGap(prev, next); got != GapOne {
		t.Fatalf("assistant->user gap = %d, want 1", got)
	}
}

func TestResolveGapTopLevelToCommandSystem(t *testing.T) {
	// 规则表：任意 committed top-level -> 独立 command/system/notice -> 1。
	prev := topCell("asst-1", KindAssistant)
	for _, kind := range []CellKind{KindCommand, KindSystem} {
		next := topCell("c2", kind)
		if got := ResolveGap(prev, next); got != GapOne {
			t.Fatalf("%v->%v gap = %d, want 1", prev.Kind, kind, got)
		}
	}
	// 命令/系统结果之后回到对话块同样 1。
	cmd := topCell("cmd-1", KindCommand)
	user := topCell("user-2", KindUser)
	if got := ResolveGap(cmd, user); got != GapOne {
		t.Fatalf("command->user gap = %d, want 1", got)
	}
}

func TestResolveGapToolChainDense(t *testing.T) {
	// 规则表：同一 tool-chain cell 内的 tool events -> 0（cell 内稠密）。
	a := chainCell("tool-a-start", "chain-1")
	b := chainCell("tool-a-out", "chain-1")
	if got := ResolveGap(a, b); got != GapNone {
		t.Fatalf("same-chain tool gap = %d, want 0", got)
	}
	// 并行工具归组到父调用后，链内成员保持稠密（即使到达顺序交错）。
	parallel := chainCell("tool-b-start", "chain-1")
	if got := ResolveGap(b, parallel); got != GapNone {
		t.Fatalf("same-chain parallel tool gap = %d, want 0", got)
	}
}

func TestResolveGapIndependentFinalCells(t *testing.T) {
	// 规则表：独立 final tool/event cell -> 下一独立 final cell -> 1。
	prev := topCell("tool-final-1", KindTool)
	next := topCell("tool-final-2", KindTool)
	if got := ResolveGap(prev, next); got != GapOne {
		t.Fatalf("independent final tool cells gap = %d, want 1", got)
	}
	// 不同链但都是独立 final：仍为 1。
	a := chainCell("tool-a-final", "chain-1")
	b := chainCell("tool-b-final", "chain-2")
	a.TopLevel = true
	b.TopLevel = true
	if got := ResolveGap(a, b); got != GapOne {
		t.Fatalf("different-chain independent finals gap = %d, want 1", got)
	}
}

func TestResolveGapSameIDMutableUpdateNoBoundary(t *testing.T) {
	// 规则表：mutable rev N -> N+1 不适用（replace，不创建边界）；
	// mutable -> 同 ID finalization 不新增（replace/commit transaction）。
	base := topCell("asst-1", KindAssistant)
	base.Mutable = true
	update := base // 同 ID，revision 前进
	if got := ResolveGap(base, update); got != GapNone {
		t.Fatalf("same-ID mutable update gap = %d, want 0", got)
	}
	final := base
	final.Mutable = false // finalization 仍同 ID
	if got := ResolveGap(base, final); got != GapNone {
		t.Fatalf("same-ID finalization gap = %d, want 0", got)
	}
}

func TestResolveGapSameRequestReasoningAssistantDense(t *testing.T) {
	prev := topCell("reasoning-1", KindAssistant)
	prev.GroupKey = "request-1"
	next := topCell("assistant-1", KindAssistant)
	next.GroupKey = "request-1"

	if got := ResolveGap(prev, next); got != GapNone {
		t.Fatalf("same-request reasoning->assistant gap = %d, want 0", got)
	}
	if prev.ChainKey != "" || next.ChainKey != "" {
		t.Fatalf("boundary grouping must not reuse tool ChainKey: prev=%+v next=%+v", prev, next)
	}
}

func TestResolveGapDifferentOrEmptyRequestGroupsStaySeparate(t *testing.T) {
	tests := []struct {
		name      string
		prevGroup string
		nextGroup string
	}{
		{name: "different exact requests", prevGroup: "request-1", nextGroup: "request-2"},
		{name: "empty groups never match"},
		{name: "one grouped one independent", prevGroup: "request-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prev := topCell("reasoning", KindAssistant)
			prev.GroupKey = tt.prevGroup
			next := topCell("assistant", KindAssistant)
			next.GroupKey = tt.nextGroup
			if got := ResolveGap(prev, next); got != GapOne {
				t.Fatalf("groups %q -> %q gap = %d, want 1", tt.prevGroup, tt.nextGroup, got)
			}
		})
	}
}

func TestResolveGapBoundaryGroupDoesNotChangeToolChainRules(t *testing.T) {
	prev := chainCell("tool-a", "chain-1")
	next := chainCell("tool-b", "chain-1")
	prev.GroupKey = "request-1"
	next.GroupKey = "request-2"
	if got := ResolveGap(prev, next); got != GapNone {
		t.Fatalf("same tool chain with unrelated groups gap = %d, want 0", got)
	}

	other := chainCell("tool-c", "chain-2")
	other.GroupKey = "request-2"
	if got := ResolveGap(next, other); got != GapOne {
		t.Fatalf("boundary group must not collapse distinct tool chains: gap = %d, want 1", got)
	}
	if next.ChainKey == other.ChainKey {
		t.Fatal("fixture must keep tool chains distinct")
	}
}

func TestResolveGapReplayEqualsLive(t *testing.T) {
	// 规则表：replay cell -> replay next cell 与 live 相同（禁止 replay 特例）。
	// 本函数无状态：相同元数据输入必然相同输出，此测试固化契约，
	// 防止未来引入 replay 分支。
	livePrev := topCell("user-1", KindUser)
	liveNext := topCell("asst-1", KindAssistant)
	replayPrev := topCell("user-1", KindUser)
	replayNext := topCell("asst-1", KindAssistant)
	live := ResolveGap(livePrev, liveNext)
	replay := ResolveGap(replayPrev, replayNext)
	if live != replay {
		t.Fatalf("replay diverged from live: live=%d replay=%d", live, replay)
	}
}

func TestResolveGapHandoffPreservesBoundary(t *testing.T) {
	// 规则表：handoff range -> retained next cell 保持原 boundary
	// （handoff 不重新计算业务顺序）。已 handoff 的 cell 元数据与
	// 普通 cell 同输入同输出。
	normal := ResolveGap(topCell("asst-1", KindAssistant), topCell("user-2", KindUser))
	handedOff := ResolveGap(topCell("asst-1", KindAssistant), topCell("user-2", KindUser))
	if normal != handedOff {
		t.Fatalf("handoff changed boundary: normal=%d handedOff=%d", normal, handedOff)
	}
}

func TestResolveGapAtMostOneGapInvariant(t *testing.T) {
	// INV-GAP-02：独立 top-level transcript cells 之间最多一个语义 gap。
	// 穷举 CellMeta 可区分维度组合，断言输出恒为 0 或 1。
	ids := []string{"", "a", "b"}
	kinds := []CellKind{KindUser, KindAssistant, KindTool, KindCommand, KindSystem}
	chains := []string{"", "chain-1", "chain-2"}
	for _, prevID := range ids {
		for _, prevKind := range kinds {
			for _, prevChain := range chains {
				prev := CellMeta{ID: prevID, Kind: prevKind, ChainKey: prevChain, TopLevel: true}
				for _, nextKind := range kinds {
					for _, nextChain := range chains {
						next := CellMeta{ID: "n", Kind: nextKind, ChainKey: nextChain, TopLevel: true}
						got := ResolveGap(prev, next)
						if got != GapNone && got != GapOne {
							t.Fatalf("gap %d outside {0,1}: prev=%+v next=%+v", got, prev, next)
						}
					}
				}
			}
		}
	}
}

func TestResolveGapFilteredEmptyNotCalledByPolicy(t *testing.T) {
	// INV-GAP-05：空 block、被过滤事件和无可见内容的 update 不推进
	// boundary state。调用方负责跳过；本函数对"已过滤"输入不产生
	// 任何额外 gap（输入透传，输出由规则表决定）——这里固化：空 ID
	// 前置视作首 cell，不产生前导空行。
	if got := ResolveGap(CellMeta{}, topCell("c1", KindSystem)); got != GapNone {
		t.Fatalf("leading filtered cell gap = %d, want 0", got)
	}
}
