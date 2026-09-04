package ui

import (
	"context"
	"testing"
)

func TestFullScreenListDeleteKeysRequestDeletion(t *testing.T) {
	items := []FullScreenListItem{{Title: "one"}, {Title: "two"}, {Title: "three"}}
	tests := []struct {
		name string
		key  editorKey
	}{
		{name: "lowercase x", key: editorKey{kind: editorKeyRune, r: 'x'}},
		{name: "uppercase X", key: editorKey{kind: editorKeyRune, r: 'X'}},
		{name: "delete key", key: editorKey{kind: editorKeyDelete}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := fullScreenListState{selected: 1}
			matches := fullScreenListMatches(items, "")
			result, done := applyFullScreenListKey(&state, test.key, items, matches, 24)
			if !done {
				t.Fatalf("expected key to close the list, done=%v", done)
			}
			if !result.DeleteRequested {
				t.Fatalf("expected DeleteRequested, result=%#v", result)
			}
			if result.Index != 1 {
				t.Fatalf("expected highlighted index 1, got %d", result.Index)
			}
			if result.Cancelled {
				t.Fatal("delete must not be reported as cancelled")
			}
		})
	}
}

func TestFullScreenListDeleteKeysIgnoredInSearchMode(t *testing.T) {
	items := []FullScreenListItem{{Title: "alpha"}, {Title: "beta"}}
	state := fullScreenListState{selected: 0, searching: true, query: "al"}
	matches := fullScreenListMatches(items, state.query)

	result, done := applyFullScreenListKey(&state, editorKey{kind: editorKeyRune, r: 'x'}, items, matches, 24)
	if done || result.DeleteRequested {
		t.Fatalf("x in search mode must extend the query, result=%#v done=%v", result, done)
	}
	if state.query != "alx" {
		t.Fatalf("expected query to extend to %q, got %q", "alx", state.query)
	}

	state.query = "al"
	state.selected = 1
	matches = fullScreenListMatches(items, state.query)
	result, done = applyFullScreenListKey(&state, editorKey{kind: editorKeyDelete}, items, matches, 24)
	if done || result.DeleteRequested {
		t.Fatalf("delete key in search mode must trim the query, result=%#v done=%v", result, done)
	}
	if state.query != "a" {
		t.Fatalf("expected query trimmed to %q, got %q", "a", state.query)
	}
}

func TestFullScreenListDeleteKeyWithNoMatchesKeepsListOpen(t *testing.T) {
	items := []FullScreenListItem{{Title: "alpha"}}
	state := fullScreenListState{selected: 0, searching: true, query: "zzz"}
	matches := fullScreenListMatches(items, state.query)
	if len(matches) != 0 {
		t.Fatalf("expected no matches, got %v", matches)
	}
	result, done := applyFullScreenListKey(&state, editorKey{kind: editorKeyRune, r: 'x'}, items, matches, 24)
	if done || result.DeleteRequested {
		t.Fatalf("x with no matches must not delete or close, result=%#v done=%v", result, done)
	}
}

func TestRunFullScreenListLoopIgnoresDeleteKeyWithoutOnDelete(t *testing.T) {
	keys := []editorKey{
		{kind: editorKeyRune, r: 'x'},
		{kind: editorKeyCancelPopup},
	}
	keyIndex := 0
	result, _, err := runFullScreenListLoop(context.Background(), FullScreenListOptions{
		Items: []FullScreenListItem{{Title: "one"}, {Title: "two"}},
	}, fullScreenListLoopHooks{
		refreshSize: func() (int, int) { return 80, 12 },
		writeFrame:  func(frame string) error { return nil },
		readKey: func(context.Context) (editorKey, bool, error) {
			key := keys[min(keyIndex, len(keys)-1)]
			keyIndex++
			return key, true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.DeleteRequested {
		t.Fatalf("delete key must be ignored when OnDelete is nil, result=%#v", result)
	}
	if !result.Cancelled {
		t.Fatalf("expected the following cancel key to close the list, result=%#v", result)
	}
}

func TestRunFullScreenListLoopReturnsDeleteRequestWithOnDelete(t *testing.T) {
	delCalls := 0
	result, _, err := runFullScreenListLoop(context.Background(), FullScreenListOptions{
		Items:   []FullScreenListItem{{Title: "one"}, {Title: "two"}},
		OnDelete: func(index int) error {
			delCalls++
			return nil
		},
	}, fullScreenListLoopHooks{
		refreshSize: func() (int, int) { return 80, 12 },
		writeFrame:  func(frame string) error { return nil },
		readKey: func(context.Context) (editorKey, bool, error) {
			return editorKey{kind: editorKeyRune, r: 'x'}, true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.DeleteRequested {
		t.Fatalf("expected DeleteRequested with OnDelete configured, result=%#v", result)
	}
	if result.Index != 0 {
		t.Fatalf("expected index 0, got %d", result.Index)
	}
	if delCalls != 0 {
		t.Fatalf("OnDelete is owned by the caller and must not fire from the loop, got %d calls", delCalls)
	}
}