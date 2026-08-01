package renderengine

import (
	"reflect"
	"testing"
)

func TestSoftOutputStateMergesOwnedPartial(t *testing.T) {
	state := NewSoftOutputState(4)
	state.Note("hel", false)
	if !state.Partial() {
		t.Fatal("expected an open partial line")
	}
	state.Note("lo\n", true)
	if got := state.Lines(); !reflect.DeepEqual(got, []string{"hello"}) {
		t.Fatalf("merged lines = %#v, want [hello]", got)
	}
	if state.Partial() {
		t.Fatal("completed line must not remain partial")
	}
}

func TestSoftOutputStateDoesNotClaimForeignPartial(t *testing.T) {
	state := NewSoftOutputState(4)
	state.Note("owned\n", false)
	state.Note("foreign\nnext\n", true)
	if got := state.Lines(); !reflect.DeepEqual(got, []string{"next"}) {
		t.Fatalf("foreign partial lines = %#v, want [next]", got)
	}
	if state.Partial() {
		t.Fatal("newline-terminated continuation must not be partial")
	}
}

func TestSoftOutputStateHardCapMarksTrim(t *testing.T) {
	state := NewSoftOutputState(2)
	state.Note("one\ntwo\nthree\n", false)
	if got := state.Lines(); !reflect.DeepEqual(got, []string{"two", "three"}) {
		t.Fatalf("trimmed lines = %#v, want [two three]", got)
	}
	if !state.Trimmed() {
		t.Fatal("expected hard-cap trim marker")
	}
}

func TestSoftOutputStateAdoptAndReplaceResetMetadata(t *testing.T) {
	state := NewSoftOutputState(2)
	state.Note("one\ntwo\nthree\n", false)
	state.Adopt([]string{"new"})
	if got := state.Lines(); !reflect.DeepEqual(got, []string{"new"}) {
		t.Fatalf("adopted lines = %#v, want [new]", got)
	}
	if state.Trimmed() || state.Partial() {
		t.Fatal("adoption must rebase trim and partial metadata")
	}
	state.Replace([]string{"rewritten"})
	if got := state.Lines(); !reflect.DeepEqual(got, []string{"rewritten"}) {
		t.Fatalf("replaced lines = %#v, want [rewritten]", got)
	}
	state.Invalidate()
	if state.Valid() || state.LineCount() != 0 {
		t.Fatal("invalidate must clear ownership")
	}
}
