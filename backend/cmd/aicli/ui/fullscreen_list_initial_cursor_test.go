package ui

import (
	"testing"
)

// seed 用例直接测「原始下标 → 可见位置」的映射：这是 InitialIndex 的全部难点，
// 而渲染循环其余部分（clamp/分页/搜索）已有自己的测试。
func TestSeedFullScreenListInitialCursorMapsOriginalIndex(t *testing.T) {
	items := []FullScreenListItem{{Title: "a"}, {Title: "b"}, {Title: "c"}, {Title: "d"}}

	t.Run("no filter", func(t *testing.T) {
		state := fullScreenListState{}
		matches := fullScreenListMatches(items, "")
		seedFullScreenListInitialCursor(&state, items, matches, 2)
		if state.selected != 2 {
			t.Fatalf("selected = %d, want 2 (原始下标与可见位置此时一致)", state.selected)
		}
	})

	// 搜索态是这条映射存在的理由：过滤后可见位置不再是原始下标。
	// 若直接把 InitialRow 赋给 selected，光标会停到错误的行上。
	t.Run("filtered view renumbers rows", func(t *testing.T) {
		state := fullScreenListState{query: "c"}
		matches := fullScreenListMatches(items, state.query)
		if len(matches) != 1 || matches[0] != 2 {
			t.Fatalf("matches = %v, want [2]", matches)
		}
		seedFullScreenListInitialCursor(&state, items, matches, 2)
		if state.selected != 0 {
			t.Fatalf("selected = %d, want 0 (过滤后只剩一条，可见位置是 0)", state.selected)
		}
	})

	t.Run("target filtered out keeps first row", func(t *testing.T) {
		state := fullScreenListState{}
		matches := fullScreenListMatches(items, "a")
		seedFullScreenListInitialCursor(&state, items, matches, 3)
		if state.selected != 0 {
			t.Fatalf("selected = %d, want 0 (目标不在可见结果里就复位，不停到别的行)", state.selected)
		}
	})

	t.Run("default zero keeps historical behavior", func(t *testing.T) {
		state := fullScreenListState{}
		matches := fullScreenListMatches(items, "")
		seedFullScreenListInitialCursor(&state, items, matches, 0)
		if state.selected != 0 {
			t.Fatalf("selected = %d, want 0", state.selected)
		}
	})

	t.Run("out of range and empty inputs are inert", func(t *testing.T) {
		state := fullScreenListState{selected: 1}
		matches := fullScreenListMatches(items, "")
		seedFullScreenListInitialCursor(&state, items, matches, len(items))
		if state.selected != 1 {
			t.Fatalf("越界下标不得改动光标: %d", state.selected)
		}
		seedFullScreenListInitialCursor(&state, items, nil, 2)
		if state.selected != 1 {
			t.Fatalf("空结果不得改动光标: %d", state.selected)
		}
		seedFullScreenListInitialCursor(nil, items, matches, 2)
	})

	// InitialIndex 之后必须紧跟 clampToEnabled：可见性（offset 窗口）由它负责。
	t.Run("clamp makes the seeded row visible", func(t *testing.T) {
		wide := make([]FullScreenListItem, 0, 40)
		for index := 0; index < 40; index++ {
			wide = append(wide, FullScreenListItem{Title: string(rune('a' + index%26))})
		}
		state := fullScreenListState{}
		matches := fullScreenListMatches(wide, "")
		seedFullScreenListInitialCursor(&state, wide, matches, 35)
		state.clampToEnabled(wide, matches, 8)
		if state.selected != 35 {
			t.Fatalf("selected = %d, want 35", state.selected)
		}
		if state.offset > state.selected || state.selected > state.offset+7 {
			t.Fatalf("选中行必须在可见窗口内: selected=%d offset=%d", state.selected, state.offset)
		}
	})
}

// Options 的新字段必须有不写也能用的默认值：既有选择器一处都不传它，
// 行为要逐字保持「光标从第一行开始」。
func TestFullScreenListOptionsInitialIndexDefaultsToZero(t *testing.T) {
	options := FullScreenListOptions{Title: "x", Items: []FullScreenListItem{{Title: "a"}, {Title: "b"}}}
	if options.InitialIndex != 0 {
		t.Fatalf("InitialIndex 默认值 = %d, want 0（既有调用方零改动）", options.InitialIndex)
	}
}
