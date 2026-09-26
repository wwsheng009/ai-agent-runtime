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

func TestReadInteractiveLine_ActionKeyReplacementRewritesLine(t *testing.T) {
	var output bytes.Buffer
	hooks := &LineEditorHooks{
		ActionForChord: func(chord string) (string, bool) {
			if chord == "shift+tab" {
				return "app.test.insert", true
			}
			return "", false
		},
		OnActionKey: func(_ LineEditorSnapshot, _ string) LineEditorActionResult {
			replacement := LineEditorReplacement{Text: "[Image #1] ", Cursor: len([]rune("[Image #1] "))}
			return LineEditorActionResult{Claimed: true, Replacement: &replacement}
		},
	}
	line, err := readInteractiveLineWithHooks(
		strings.NewReader("ab\x1b[Zcd\n"),
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
	// 动作键改写当前行（已输入 "ab" 被替换为令牌），随后的输入继续追加。
	if line != "[Image #1] cd" {
		t.Fatalf("动作替换未生效，got %q", line)
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
		OnActionKey: func(_ LineEditorSnapshot, action string) LineEditorActionResult {
			actions = append(actions, action)
			return LineEditorActionResult{Claimed: true}
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
		OnActionKey: func(value LineEditorSnapshot, _ string) LineEditorActionResult {
			snapshot = value
			return LineEditorActionResult{Claimed: true, ExitEditor: true}
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
		OnActionKey: func(LineEditorSnapshot, string) LineEditorActionResult { return LineEditorActionResult{} },
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

// ctrl+v 剪贴板读不到文本时，宿主可以接管（例如读剪贴板图片并插入 [Image #N] 令牌）。
func TestReadInteractiveLine_ClipboardTextEmptyHookRewritesLine(t *testing.T) {
	oldClipboard := readInteractiveClipboardText
	readInteractiveClipboardText = func() (string, error) { return "", nil }
	t.Cleanup(func() { readInteractiveClipboardText = oldClipboard })

	var output bytes.Buffer
	hookCalls := 0
	line, err := readInteractiveLineWithHooks(
		strings.NewReader("\x16\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
		&LineEditorHooks{
			OnClipboardTextEmpty: func(snapshot LineEditorSnapshot) LineEditorActionResult {
				hookCalls++
				next := snapshot.Text + "[Image #1] "
				return LineEditorActionResult{
					Claimed:     true,
					Replacement: &LineEditorReplacement{Text: next, Cursor: len([]rune(next))},
				}
			},
		},
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks returned error: %v", err)
	}
	if hookCalls != 1 {
		t.Fatalf("ctrl+v 无文本时应调用一次兜底钩子，实际 %d 次", hookCalls)
	}
	if strings.TrimSpace(line) != "[Image #1]" {
		t.Fatalf("兜底替换未落到提交行: %q", line)
	}
}

// 未接兜底钩子时保持原有静默行为：剪贴板没有文本就什么都不做。
func TestReadInteractiveLine_ClipboardTextEmptyWithoutHookStaysSilent(t *testing.T) {
	oldClipboard := readInteractiveClipboardText
	readInteractiveClipboardText = func() (string, error) { return "", nil }
	t.Cleanup(func() { readInteractiveClipboardText = oldClipboard })

	var output bytes.Buffer
	line, err := readInteractiveLine(
		strings.NewReader("\x16\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("readInteractiveLine returned error: %v", err)
	}
	if strings.TrimSpace(line) != "" {
		t.Fatalf("没有兜底钩子时不应改写输入行: %q", line)
	}
}

// 粘贴拦截：宿主可以把"整段粘贴 = 一个图片路径"换成 [Image #N] 令牌，且不再插入原文本。
func TestReadInteractiveLine_PasteTextHookReplacesPayload(t *testing.T) {
	var output bytes.Buffer
	payload := `"C:\pics\a.png"`
	calls := 0
	hooks := &LineEditorHooks{
		OnPasteText: func(text string, _ LineEditorSnapshot) LineEditorActionResult {
			calls++
			if text != payload {
				t.Fatalf("钩子应收到归一化后的整段粘贴文本，得到 %q", text)
			}
			replacement := LineEditorReplacement{Text: "[Image #1] ", Cursor: len([]rune("[Image #1] "))}
			return LineEditorActionResult{Claimed: true, Replacement: &replacement}
		},
	}
	line, err := readInteractiveLineWithHooks(
		strings.NewReader("\x1b[200~"+payload+"\x1b[201~\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
		hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("bracketed paste 应调用一次粘贴钩子，实际 %d 次", calls)
	}
	if strings.TrimSpace(line) != "[Image #1]" {
		t.Fatalf("粘贴内容应被替换为令牌，得到 %q", line)
	}
}

// 未命中（钩子返回零值）时粘贴必须原样插入，保持既有语义。
func TestReadInteractiveLine_PasteTextHookFallsBackToPlainInsert(t *testing.T) {
	var output bytes.Buffer
	payload := `"C:\pics\a.png"`
	hooks := &LineEditorHooks{
		OnPasteText: func(string, LineEditorSnapshot) LineEditorActionResult {
			return LineEditorActionResult{}
		},
	}
	line, err := readInteractiveLineWithHooks(
		strings.NewReader("\x1b[200~"+payload+"\x1b[201~\n"),
		&output,
		UserPromptText(0),
		nil,
		nil,
		hooks,
		true,
		false,
	)
	if err != nil {
		t.Fatalf("readInteractiveLineWithHooks returned error: %v", err)
	}
	if line != payload {
		t.Fatalf("未认领的粘贴应原样插入，得到 %q", line)
	}
}
