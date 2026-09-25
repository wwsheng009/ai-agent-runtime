package ui

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestDecodeInteractiveKeyShiftTabProducesChord(t *testing.T) {
	for _, sequence := range [][]byte{[]byte("\x1b[Z"), []byte("\x1b[1;2Z")} {
		decoded, ok := decodeInteractiveKey(sequence)
		if !ok {
			t.Fatalf("decodeInteractiveKey(%q) 未解码", sequence)
		}
		if decoded.key.kind != editorKeyAction || decoded.key.chord != "shift+tab" {
			t.Fatalf("decodeInteractiveKey(%q) = %#v, want editorKeyAction shift+tab", sequence, decoded.key)
		}
	}
}

func TestDecodeInteractiveKeyAltMProducesChord(t *testing.T) {
	decoded, ok := decodeInteractiveKey([]byte("\x1bm"))
	if !ok {
		t.Fatal("decodeInteractiveKey(alt+m) 未解码")
	}
	if decoded.key.kind != editorKeyAction || decoded.key.chord != "alt+m" {
		t.Fatalf("decodeInteractiveKey(alt+m) = %#v, want editorKeyAction alt+m", decoded.key)
	}
}

func TestDecodeInteractiveKeyAltVProducesChord(t *testing.T) {
	decoded, ok := decodeInteractiveKey([]byte("\x1bv"))
	if !ok {
		t.Fatal("decodeInteractiveKey(alt+v) 未解码")
	}
	if decoded.key.kind != editorKeyAction || decoded.key.chord != "alt+v" {
		t.Fatalf("decodeInteractiveKey(alt+v) = %#v, want editorKeyAction alt+v", decoded.key)
	}
}

func TestReadInteractiveLine_ActionKeyClaimConsumesShiftTab(t *testing.T) {
	var output bytes.Buffer
	var actions []string
	hooks := &LineEditorHooks{
		ActionForChord: func(chord string) (string, bool) {
			if chord == "shift+tab" {
				return "app.permission.cycle", true
			}
			return "", false
		},
		OnActionKey: func(_ LineEditorSnapshot, action string) (bool, bool) {
			actions = append(actions, action)
			return true, false
		},
	}
	line, err := readInteractiveLineWithHooks(
		strings.NewReader("ab\x1b[Z\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
		hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != "ab" {
		t.Fatalf("被认领的 shift+tab 不应插入文本，got %q", line)
	}
	if len(actions) != 1 || actions[0] != "app.permission.cycle" {
		t.Fatalf("动作分发 = %#v, want [app.permission.cycle]", actions)
	}
}

func TestReadInteractiveLine_ActionKeyExitEditorSignalPreservesDraft(t *testing.T) {
	var output bytes.Buffer
	var snapshot LineEditorSnapshot
	hooks := &LineEditorHooks{
		ActionForChord: func(chord string) (string, bool) {
			if chord == "ctrl+t" {
				return "app.transcript.pager", true
			}
			return "", false
		},
		OnActionKey: func(value LineEditorSnapshot, _ string) (bool, bool) {
			snapshot = value
			return true, true
		},
	}
	line, err := readInteractiveLineWithHooks(
		strings.NewReader("abc\x14"),
		&output,
		UserPromptText(0),
		nil,
		nil,
		hooks,
		true,
		false,
	)
	if !errors.Is(err, ErrInteractiveInputTranscriptRequested) {
		t.Fatalf("error = %v, want transcript request", err)
	}
	if line != "" || snapshot.Text != "abc" || snapshot.Cursor != 3 {
		t.Fatalf("草稿未保留: line=%q snapshot=%#v", line, snapshot)
	}
}

func TestReadInteractiveLine_UnclaimedActionFallsBackToTranspose(t *testing.T) {
	var output bytes.Buffer
	hooks := &LineEditorHooks{
		ActionForChord: func(chord string) (string, bool) {
			if chord == "ctrl+t" {
				return "app.transcript.pager", true
			}
			return "", false
		},
		OnActionKey: func(LineEditorSnapshot, string) (bool, bool) { return false, false },
	}
	line, err := readInteractiveLineWithHooks(
		strings.NewReader("abc\x14\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
		hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != "acb" {
		t.Fatalf("未认领的 ctrl+t 应回落到 transpose，got %q", line)
	}
}

func TestReadInteractiveLine_CollapsePastedTextDisabledInsertsFullText(t *testing.T) {
	disabled := false
	hooks := &LineEditorHooks{CollapsePastedText: &disabled}
	large := strings.Repeat("a", LargePasteCharThreshold+1)
	var output bytes.Buffer
	line, err := readInteractiveLineWithHooks(
		strings.NewReader("\x1b[200~"+large+"\x1b[201~\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
		hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks: %v", err)
	}
	if line != large {
		t.Fatalf("关闭折叠后应提交全文，got len=%d want len=%d", len(line), len(large))
	}
	if rendered := output.String(); !strings.Contains(rendered, large) {
		t.Fatal("关闭折叠后输入框应显示全文（无占位符）")
	}
}

func TestComposerStateLargePasteCollapseCanBeDisabled(t *testing.T) {
	disabled := false
	composer := NewComposerState()
	composer.SetCollapseLargePaste(&disabled)
	large := strings.Repeat("b", LargePasteCharThreshold+1)
	cursor := composer.HandlePasteAt(0, large)
	if composer.Text() != large {
		t.Fatalf("关闭折叠后应原样插入全文，got len=%d", len(composer.Text()))
	}
	if submitted := composer.SubmitText(); submitted != large {
		t.Fatalf("关闭折叠后提交文本应为全文，got len=%d", len(submitted))
	}
	if cursor != len([]rune(large)) {
		t.Fatalf("cursor = %d, want %d", cursor, len([]rune(large)))
	}
}
