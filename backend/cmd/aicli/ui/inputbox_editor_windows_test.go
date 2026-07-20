//go:build windows

package ui

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestWaitForInteractiveInputReady_InvalidHandleIsUnsupported(t *testing.T) {
	ready, err := waitForInteractiveInputReady(-1, 0)
	if ready {
		t.Fatal("expected invalid handle not to report ready")
	}
	if err != errInteractiveInputReadinessUnsupported {
		t.Fatalf("expected unsupported readiness error, got %v", err)
	}
}

func TestWaitForInteractiveInputReady_DetectsPipeInput(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	ready, err := waitForInteractiveInputReady(int(reader.Fd()), time.Millisecond)
	if err != nil {
		t.Fatalf("waitForInteractiveInputReady before write: %v", err)
	}
	if ready {
		t.Fatal("expected empty pipe not to report ready")
	}

	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatalf("write pipe: %v", err)
	}
	ready, err = waitForInteractiveInputReady(int(reader.Fd()), time.Second)
	if err != nil {
		t.Fatalf("waitForInteractiveInputReady after write: %v", err)
	}
	if !ready {
		t.Fatal("expected pipe with buffered input to report ready")
	}
}

func TestNextInteractiveKeyReadsFromPipeBackedStdin(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()

	result := make(chan struct {
		key editorKey
		ok  bool
		err error
	}, 1)
	pending := make([]byte, 0, 16)

	go func() {
		key, ok, err := nextInteractiveKey(context.Background(), reader, &pending, reader)
		result <- struct {
			key editorKey
			ok  bool
			err error
		}{key: key, ok: ok, err: err}
	}()

	time.Sleep(20 * time.Millisecond)
	if _, err := writer.Write([]byte("x")); err != nil {
		t.Fatalf("write pipe: %v", err)
	}

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("nextInteractiveKey returned error: %v", got.err)
		}
		if !got.ok || got.key.kind != editorKeyRune || got.key.r != 'x' {
			t.Fatalf("expected rune x, got key=%#v ok=%v", got.key, got.ok)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for pipe-backed input")
	}
}

func TestConsoleKeyEventCanProduceInputAcceptsCtrlVClipboardShortcut(t *testing.T) {
	ctrlV := &consoleKeyEventRecord{
		KeyDown:         1,
		VirtualKeyCode:  windowsVKV,
		UnicodeChar:     0,
		ControlKeyState: windowsLeftCtrlPressed,
	}
	if !consoleKeyEventCanProduceInput(ctrlV) {
		t.Fatal("expected Ctrl+V key event to mark stdin ready for clipboard paste")
	}
	if !consoleKeyEventIsCtrlVDown(ctrlV) {
		t.Fatal("expected Ctrl+V key event to be consumable as explicit clipboard paste")
	}

	ctrlVWithUnicode := *ctrlV
	ctrlVWithUnicode.UnicodeChar = 'v'
	if consoleKeyEventIsCtrlVDown(&ctrlVWithUnicode) {
		t.Fatal("expected Ctrl+V recognizer not to consume normal Unicode input")
	}
}

func TestConsoleKeyEventRecognizesShiftEnterAsNewline(t *testing.T) {
	shiftEnter := &consoleKeyEventRecord{
		KeyDown:         1,
		VirtualKeyCode:  windowsVKReturn,
		UnicodeChar:     '\r',
		ControlKeyState: windowsShiftPressed,
	}
	if !consoleKeyEventIsModifiedEnterDown(shiftEnter) {
		t.Fatal("expected Shift+Enter console event to insert a newline")
	}
	plainEnter := *shiftEnter
	plainEnter.ControlKeyState = 0
	if consoleKeyEventIsModifiedEnterDown(&plainEnter) {
		t.Fatal("expected plain Enter to retain submit behavior")
	}
}

func TestConsoleKeyEventCanProduceInputAcceptsCharactersAndEditingKeys(t *testing.T) {
	character := &consoleKeyEventRecord{
		KeyDown:        1,
		VirtualKeyCode: 0x41,
		UnicodeChar:    'a',
	}
	if !consoleKeyEventCanProduceInput(character) {
		t.Fatal("expected key event with Unicode character to mark stdin ready")
	}

	leftArrow := &consoleKeyEventRecord{
		KeyDown:        1,
		VirtualKeyCode: 0x25, // VK_LEFT
		UnicodeChar:    0,
	}
	if !consoleKeyEventCanProduceInput(leftArrow) {
		t.Fatal("expected navigation key to mark stdin ready")
	}

	keyUp := &consoleKeyEventRecord{
		KeyDown:        0,
		VirtualKeyCode: 0x41,
		UnicodeChar:    'a',
	}
	if consoleKeyEventCanProduceInput(keyUp) {
		t.Fatal("expected key-up event not to mark stdin ready")
	}
}
