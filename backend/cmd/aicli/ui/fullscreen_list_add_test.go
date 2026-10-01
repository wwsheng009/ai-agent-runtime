package ui

import (
	"context"
	"testing"
	"time"
)

// The add key ('a') must be opt-in. A caller that never wired OnAdd has to
// keep 'a' as an ordinary search character, and -- critically -- the search it
// starts must behave exactly like any other directly-typed search.
//
// Regression: the first version of the opt-out path appended the rune but left
// state.searching=false, so the *next* keystroke took the default branch,
// which REPLACES the query. "al" silently became "l" for every caller that had
// not opted in. TestRunFullScreenListLoopDebouncesSearchReload types exactly
// "al" and caught it; this test pins the rune-level contract so the cause is
// obvious next time.
func TestApplyFullScreenListKeyAddKeyIsOptIn(t *testing.T) {
	t.Run("not opted in starts a normal search", func(t *testing.T) {
		state := &fullScreenListState{}
		result, done := applyFullScreenListKey(state,
			editorKey{kind: editorKeyRune, r: 'a'}, nil, nil, 12)
		if done {
			t.Fatalf("'a' must not end the list when OnAdd is nil (got %+v)", result)
		}
		if result.AddRequested {
			t.Fatalf("AddRequested must stay false without OnAdd: %+v", result)
		}
		if !state.searching {
			t.Fatal("typing 'a' must enter search mode, like any other printable rune")
		}
		if state.query != "a" {
			t.Fatalf("query = %q, want %q", state.query, "a")
		}
		// The next rune must APPEND, not replace -- this is the exact bug.
		if _, done := applyFullScreenListKey(state,
			editorKey{kind: editorKeyRune, r: 'l'}, nil, nil, 12); done {
			t.Fatal("second rune must not end the list")
		}
		if state.query != "al" {
			t.Fatalf("query = %q, want %q (second rune replaced instead of appending)", state.query, "al")
		}
	})

	t.Run("opted in requests add instead of searching", func(t *testing.T) {
		state := &fullScreenListState{canAdd: true}
		result, done := applyFullScreenListKey(state,
			editorKey{kind: editorKeyRune, r: 'a'}, nil, nil, 12)
		if !done || !result.AddRequested {
			t.Fatalf("opted-in 'a' must close the list with AddRequested: done=%v %+v", done, result)
		}
		if state.query != "" {
			t.Fatalf("add must not leak into the search query: %q", state.query)
		}
	})

	t.Run("add stays available when nothing matches", func(t *testing.T) {
		// "Search a model, find it missing, press a to add it" is the flow the
		// feature exists for, so an empty match list must not disable it.
		state := &fullScreenListState{canAdd: true}
		result, done := applyFullScreenListKey(state,
			editorKey{kind: editorKeyRune, r: 'a'}, nil, []int{}, 12)
		if !done || !result.AddRequested {
			t.Fatalf("add must work with zero matches: done=%v %+v", done, result)
		}
		if result.Index != -1 {
			t.Fatalf("Index = %d, want -1 when nothing is highlighted", result.Index)
		}
	})

	t.Run("add names the highlighted row when there is one", func(t *testing.T) {
		state := &fullScreenListState{canAdd: true}
		result, _ := applyFullScreenListKey(state,
			editorKey{kind: editorKeyRune, r: 'A'}, nil, []int{0, 1, 2}, 12)
		if !result.AddRequested || result.Index != 0 {
			t.Fatalf("Index = %d AddRequested = %v, want 0/true", result.Index, result.AddRequested)
		}
	})
}

// The loop must not close on 'a' for a caller without OnAdd, and must close
// with AddRequested for one that wired it.
func TestRunFullScreenListLoopAddKeyOptInAtLoopLevel(t *testing.T) {
	newHooks := func(pressed *editorKey) fullScreenListLoopHooks {
		step := 0
		return fullScreenListLoopHooks{
			refreshSize: func() (int, int) { return 80, 12 },
			writeFrame:  func(string) error { return nil },
			now:         func() time.Time { return time.Unix(0, 0) },
			readKey: func(context.Context) (editorKey, bool, error) {
				step++
				if step == 1 {
					return *pressed, true, nil
				}
				return editorKey{kind: editorKeyCancelPopup}, true, nil
			},
		}
	}
	items := []FullScreenListItem{{Title: "m-a"}, {Title: "m-b"}}

	t.Run("without OnAdd the list stays open on 'a'", func(t *testing.T) {
		pressed := editorKey{kind: editorKeyRune, r: 'a'}
		result, _, err := runFullScreenListLoop(context.Background(),
			FullScreenListOptions{Title: "选择模型", Items: items}, newHooks(&pressed))
		if err != nil {
			t.Fatalf("loop: %v", err)
		}
		if result.AddRequested {
			t.Fatal("AddRequested must be false when OnAdd is nil")
		}
		if !result.Cancelled {
			t.Fatalf("expected the loop to run to its Esc, got %+v", result)
		}
	})

	t.Run("with OnAdd the list closes with AddRequested", func(t *testing.T) {
		pressed := editorKey{kind: editorKeyRune, r: 'a'}
		result, _, err := runFullScreenListLoop(context.Background(),
			FullScreenListOptions{
				Title: "选择模型",
				Items: items,
				OnAdd: func(int) error { return nil },
			}, newHooks(&pressed))
		if err != nil {
			t.Fatalf("loop: %v", err)
		}
		if !result.AddRequested || result.Cancelled {
			t.Fatalf("expected AddRequested and an open-list exit, got %+v", result)
		}
	})
}
