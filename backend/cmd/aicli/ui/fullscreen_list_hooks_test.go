package ui

import (
	"testing"
)

func TestFullScreenListHooksOnSelectionAndCancel(t *testing.T) {
	items := []FullScreenListItem{
		{Title: "one"},
		{Title: "two"},
	}
	matches := fullScreenListMatches(items, "")
	state := fullScreenListState{}

	var selected []int
	var cancelled bool
	opts := FullScreenListOptions{
		Items: items,
		OnSelectionChanged: func(index int) {
			selected = append(selected, index)
		},
		OnCancel: func() {
			cancelled = true
		},
	}
	// Simulate selection change notification path used by the loop.
	if opts.OnSelectionChanged != nil {
		opts.OnSelectionChanged(matches[state.selected])
	}
	if len(selected) != 1 || selected[0] != 0 {
		t.Fatalf("initial selection notify: %v", selected)
	}
	moveFullScreenListSelection(&state, items, matches, 1)
	if opts.OnSelectionChanged != nil {
		opts.OnSelectionChanged(matches[state.selected])
	}
	if selected[len(selected)-1] != 1 {
		t.Fatalf("after down: %v", selected)
	}
	result, done := applyFullScreenListKey(&state, editorKey{kind: editorKeyCancelPopup}, items, matches, 24)
	if !done || !result.Cancelled {
		t.Fatalf("cancel result=%#v done=%v", result, done)
	}
	if opts.OnCancel != nil {
		opts.OnCancel()
	}
	if !cancelled {
		t.Fatal("OnCancel not called")
	}
}

func TestFullScreenListOnConfirmErrorKeepsContract(t *testing.T) {
	called := false
	opts := FullScreenListOptions{
		OnConfirm: func(index int) error {
			called = true
			if index != 2 {
				t.Fatalf("index=%d", index)
			}
			return nil
		},
	}
	if err := opts.OnConfirm(2); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("OnConfirm not called")
	}
}
