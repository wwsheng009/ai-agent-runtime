package ui

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

type chunkedReader struct {
	chunks [][]byte
}

func (r *chunkedReader) Read(p []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := r.chunks[0]
	n := copy(p, chunk)
	if n >= len(chunk) {
		r.chunks = r.chunks[1:]
	} else {
		r.chunks[0] = chunk[n:]
	}
	return n, nil
}

func TestReadInteractiveLineWithHooks_TabAppliesReplacement(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	var completed int
	hooks := LineEditorHooks{
		OnComplete: func(snapshot LineEditorSnapshot) (LineEditorReplacement, bool) {
			if snapshot.Text == "/m" && snapshot.Cursor == 2 {
				completed++
				return LineEditorReplacement{Text: "/model ", Cursor: 7}, true
			}
			return LineEditorReplacement{}, false
		},
	}

	line, err := readInteractiveLineWithHooks(
		strings.NewReader("/m\t\r\n"),
		&output,
		"",
		nil,
		nil,
		&hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != "/model " {
		t.Fatalf("expected tab completion to rewrite line, got %q", line)
	}
	if completed != 1 {
		t.Fatalf("expected one completion callback, got %d", completed)
	}
}

func TestReadInteractiveLineWithHooks_EnterAppliesReplacement(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	var submitted int
	hooks := LineEditorHooks{
		OnSubmit: func(snapshot LineEditorSnapshot) (LineEditorReplacement, bool) {
			if snapshot.Text == "/m" && snapshot.Cursor == 2 {
				submitted++
				return LineEditorReplacement{Text: "/model ", Cursor: 7}, true
			}
			return LineEditorReplacement{}, false
		},
	}

	line, err := readInteractiveLineWithHooks(
		&chunkedReader{chunks: [][]byte{[]byte("/m\n"), []byte("\r")}},
		&output,
		"",
		nil,
		nil,
		&hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != "/model " {
		t.Fatalf("expected submit hook to rewrite line, got %q", line)
	}
	if submitted != 1 {
		t.Fatalf("expected one submit callback, got %d", submitted)
	}
	if got := strings.Count(output.String(), "\r\n"); got != 1 {
		t.Fatalf("expected exactly one echoed submit newline, got %d in %q", got, output.String())
	}
}

func TestReadInteractiveLineWithHooks_CursorMovementEmitsSnapshots(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	snapshots := make([]LineEditorSnapshot, 0, 8)
	hooks := LineEditorHooks{
		OnChange: func(snapshot LineEditorSnapshot) {
			snapshots = append(snapshots, snapshot)
		},
	}

	line, err := readInteractiveLineWithHooks(
		strings.NewReader("/m\x1b[D\r\n"),
		&output,
		"",
		nil,
		nil,
		&hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != "/m" {
		t.Fatalf("expected line to remain /m, got %q", line)
	}
	if len(snapshots) < 4 {
		t.Fatalf("expected multiple snapshots, got %#v", snapshots)
	}
	last := snapshots[len(snapshots)-1]
	if last.Text != "/m" {
		t.Fatalf("expected latest snapshot text to remain /m, got %#v", last)
	}
	if last.Cursor != 1 {
		t.Fatalf("expected cursor snapshot to move left to 1, got %#v", last)
	}
}

func TestReadInteractiveLineWithHooks_OnNavigateConsumesHistoryNavigation(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	var navigations []int
	hooks := LineEditorHooks{
		OnNavigate: func(snapshot LineEditorSnapshot, delta int) bool {
			navigations = append(navigations, delta)
			return true
		},
	}

	line, err := readInteractiveLineWithHooks(
		strings.NewReader("/m\x1b[B\r\n"),
		&output,
		"",
		[]string{"first", "second"},
		nil,
		&hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != "/m" {
		t.Fatalf("expected hook to consume down arrow and preserve line, got %q", line)
	}
	if len(navigations) != 1 || navigations[0] != 1 {
		t.Fatalf("expected one down-navigation callback, got %#v", navigations)
	}
}

func TestReadInteractiveLineWithHooks_OnMoveConsumesCursorMovement(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	var moves []int
	hooks := LineEditorHooks{
		OnMove: func(snapshot LineEditorSnapshot, delta int) bool {
			moves = append(moves, delta)
			return true
		},
	}

	line, err := readInteractiveLineWithHooks(
		strings.NewReader("/m\x1b[D\x1b[C\r\n"),
		&output,
		"",
		nil,
		nil,
		&hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != "/m" {
		t.Fatalf("expected hook to consume left/right arrows and preserve line, got %q", line)
	}
	if len(moves) != 2 || moves[0] != -1 || moves[1] != 1 {
		t.Fatalf("expected left/right movement callbacks, got %#v", moves)
	}
}

func TestReadInteractiveLineWithHooks_EscCancelsPopupHook(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	var canceled int
	hooks := LineEditorHooks{
		OnCancelPopup: func(snapshot LineEditorSnapshot) bool {
			if snapshot.Text != "/m" || snapshot.Cursor != 2 {
				t.Fatalf("unexpected cancel snapshot: %#v", snapshot)
			}
			canceled++
			return true
		},
	}

	line, err := readInteractiveLineWithHooks(
		&chunkedReader{chunks: [][]byte{[]byte("/m"), []byte("\x1b"), []byte("\r")}},
		&output,
		"",
		nil,
		nil,
		&hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != "/m" {
		t.Fatalf("expected esc to cancel popup without changing line, got %q", line)
	}
	if canceled != 1 {
		t.Fatalf("expected one cancel callback, got %d", canceled)
	}
}

func TestReadInteractiveLineWithHooks_OnCancelCanExitModal(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	var canceled int
	hooks := LineEditorHooks{
		OnCancel: func(snapshot LineEditorSnapshot) bool {
			if snapshot.Text != "/m" || snapshot.Cursor != 2 {
				t.Fatalf("unexpected cancel snapshot: %#v", snapshot)
			}
			canceled++
			return true
		},
	}

	line, err := readInteractiveLineWithHooks(
		&chunkedReader{chunks: [][]byte{[]byte("/m"), []byte("\x1b")}},
		&output,
		"",
		nil,
		nil,
		&hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != "" {
		t.Fatalf("expected modal cancel to return empty line, got %q", line)
	}
	if canceled != 1 {
		t.Fatalf("expected one modal cancel callback, got %d", canceled)
	}
}
