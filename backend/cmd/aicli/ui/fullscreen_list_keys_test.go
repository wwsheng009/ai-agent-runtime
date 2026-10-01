package ui

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// 'a' and 'x' must be ordinary type-to-filter characters.
//
// They used to be reserved as the add/delete shortcut keys, which collided
// with type-to-filter: 'a' is common in model ids (llama, audio, large), so
// searching for one required an undocumented "/" prefix and the key just looked
// dead. Delete now lives on the Delete key and add on the leading list row, so
// no printable character is reserved for them anymore.
func TestFullScreenListRunesAAndXAreSearchCharacters(t *testing.T) {
	for _, r := range []rune{'a', 'A', 'x', 'X'} {
		t.Run(string(r), func(t *testing.T) {
			state := &fullScreenListState{}
			result, done := applyFullScreenListKey(state,
				editorKey{kind: editorKeyRune, r: r}, nil, []int{0, 1}, 12)
			if done {
				t.Fatalf("%q must not close the list, got %+v", r, result)
			}
			if !state.searching {
				t.Fatalf("%q must start a search, searching = false", r)
			}
			if state.query != string(r) {
				t.Fatalf("query = %q, want %q", state.query, string(r))
			}
			if result.DeleteRequested {
				t.Fatalf("%q must not request a delete: %+v", r, result)
			}
		})
	}
}

// The Delete key is the only delete trigger in list mode. Pressing it must
// still close the list with the highlighted row named.
func TestFullScreenListDeleteKeyStillDeletes(t *testing.T) {
	state := &fullScreenListState{selected: 1}
	result, done := applyFullScreenListKey(state,
		editorKey{kind: editorKeyDelete}, nil, []int{2, 5}, 12)
	if !done || !result.DeleteRequested {
		t.Fatalf("Delete key must request a delete: done=%v %+v", done, result)
	}
	if result.Index != 5 {
		t.Fatalf("Index = %d, want the highlighted row 5", result.Index)
	}
}

// Regression: Esc in the add-model free-text stage must return to the model
// list, and it must do so even when OnConfirmText rejects the empty string.
//
// The loop used to run the FreeTextMode validator on *every* finished key,
// including the cancel. chatModelAdditionTextError(provider, "") returns
// "请输入要添加的模型 id", so Esc was treated as "submitted an empty form",
// the stage reopened, and the keypress looked like a total no-op -- the user
// could not leave the add screen at all.
func TestFullScreenListFreeTextEscBypassesConfirmValidator(t *testing.T) {
	var validated []string
	result, _, err := runFullScreenListLoop(context.Background(), FullScreenListOptions{
		Title:        "添加模型",
		FreeTextMode: true,
		OnConfirmText: func(text string) error {
			validated = append(validated, text)
			return fmt.Errorf("请输入要添加的模型 id")
		},
	}, fullScreenListLoopHooks{
		refreshSize: func() (int, int) { return 80, 12 },
		writeFrame:  func(string) error { return nil },
		now:         func() time.Time { return time.Unix(0, 0) },
		readKey: func(context.Context) (editorKey, bool, error) {
			return editorKey{kind: editorKeyCancelPopup}, true, nil
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if !result.Cancelled {
		t.Fatalf("Esc must cancel the free-text stage, got %+v", result)
	}
	if len(validated) != 0 {
		t.Fatalf("OnConfirmText must not run for a cancel, got %v", validated)
	}
}

// The inverse guard: a real submit with an empty value is still validated, so
// the fix cannot simply skip OnConfirmText unconditionally. The first Enter is
// rejected (which must keep the stage open); a following Esc ends the loop, so
// the test terminates and "was the validator consulted?" is still observable.
func TestFullScreenListFreeTextEnterStillValidates(t *testing.T) {
	var validated []string
	step := 0
	result, _, err := runFullScreenListLoop(context.Background(), FullScreenListOptions{
		Title:        "添加模型",
		FreeTextMode: true,
		OnConfirmText: func(text string) error {
			validated = append(validated, text)
			return fmt.Errorf("请输入要添加的模型 id")
		},
	}, fullScreenListLoopHooks{
		refreshSize: func() (int, int) { return 80, 12 },
		writeFrame:  func(string) error { return nil },
		now:         func() time.Time { return time.Unix(0, 0) },
		readKey: func(context.Context) (editorKey, bool, error) {
			step++
			if step == 1 {
				return editorKey{kind: editorKeyEnter}, true, nil
			}
			return editorKey{kind: editorKeyCancelPopup}, true, nil
		},
	})
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if !result.Cancelled {
		t.Fatalf("the rejected Enter must have kept the stage open so Esc could cancel it, got %+v", result)
	}
	if len(validated) != 1 || validated[0] != "" {
		t.Fatalf("OnConfirmText calls = %v, want one call with an empty string", validated)
	}
}
