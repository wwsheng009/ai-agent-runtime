package commands

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestReadChatSessionLineFallbackToBufferedReader(t *testing.T) {
	setChatLegacyConsoleInputMode(false)
	defer setChatLegacyConsoleInputMode(false)

	reader := bufio.NewReader(bytes.NewBufferString("hello world\nsecond line\n"))

	line, err := readChatSessionLine(context.Background(), reader)
	if err != nil || line != "hello world\n" {
		t.Fatalf("first line = %q, err = %v", line, err)
	}
	line, err = readChatSessionLine(context.Background(), reader)
	if err != nil || line != "second line\n" {
		t.Fatalf("second line = %q, err = %v", line, err)
	}
}

// 降级标志开启时，若编辑器探测返回不可用（非控制台/无编辑器），应回退到
// buffered 读取而不是卡死——win7 上管道/重定向输入不受影响。
func TestReadChatSessionLineEnabledButNonConsoleFallsBack(t *testing.T) {
	setChatLegacyConsoleInputMode(true)
	defer setChatLegacyConsoleInputMode(false)

	oldFn := readLegacyConsoleLineFn
	readLegacyConsoleLineFn = func(context.Context) (string, bool, error) {
		return "", false, nil
	}
	defer func() { readLegacyConsoleLineFn = oldFn }()

	reader := bufio.NewReader(strings.NewReader("piped line\n"))
	line, err := readChatSessionLine(context.Background(), reader)
	if err != nil || line != "piped line\n" {
		t.Fatalf("line = %q, err = %v (want fallback read)", line, err)
	}
}

// 编辑器可用时直接返回其行，不经过 buffered reader。
func TestReadChatSessionLineUsesEditorWhenAvailable(t *testing.T) {
	setChatLegacyConsoleInputMode(true)
	defer setChatLegacyConsoleInputMode(false)

	oldFn := readLegacyConsoleLineFn
	readLegacyConsoleLineFn = func(context.Context) (string, bool, error) {
		return "edited line", true, nil
	}
	defer func() { readLegacyConsoleLineFn = oldFn }()

	reader := bufio.NewReader(strings.NewReader("ignored\n"))
	line, err := readChatSessionLine(context.Background(), reader)
	if err != nil || line != "edited line" {
		t.Fatalf("line = %q, err = %v (want editor line)", line, err)
	}
}

func TestReadChatSessionLineSystemModePrefersUnicodeConsoleReader(t *testing.T) {
	setChatConsoleInputMode(chatConsoleInputSystem)
	defer resetChatConsoleInputMode()

	oldSystemFn := readSystemConsoleLineFn
	oldCustomFn := readLegacyConsoleLineFn
	readSystemConsoleLineFn = func(context.Context) (string, bool, error) {
		return "中文输入", true, nil
	}
	readLegacyConsoleLineFn = func(context.Context) (string, bool, error) {
		t.Fatal("custom editor must not run when the system reader succeeds")
		return "", false, nil
	}
	defer func() {
		readSystemConsoleLineFn = oldSystemFn
		readLegacyConsoleLineFn = oldCustomFn
	}()

	line, err := readChatSessionLine(context.Background(), bufio.NewReader(strings.NewReader("ignored\n")))
	if err != nil || line != "中文输入" {
		t.Fatalf("line = %q, err = %v", line, err)
	}
}

func TestReadChatSessionLineAutoModeFallsBackToCustomEditor(t *testing.T) {
	setChatConsoleInputMode(chatConsoleInputAuto)
	defer resetChatConsoleInputMode()

	oldSystemFn := readSystemConsoleLineFn
	oldCustomFn := readLegacyConsoleLineFn
	readSystemConsoleLineFn = func(context.Context) (string, bool, error) {
		return "", false, nil
	}
	readLegacyConsoleLineFn = func(context.Context) (string, bool, error) {
		return "custom fallback", true, nil
	}
	defer func() {
		readSystemConsoleLineFn = oldSystemFn
		readLegacyConsoleLineFn = oldCustomFn
	}()

	line, err := readChatSessionLine(context.Background(), bufio.NewReader(strings.NewReader("ignored\n")))
	if err != nil || line != "custom fallback" {
		t.Fatalf("line = %q, err = %v", line, err)
	}
}

func TestReadChatSessionLineSystemModeDoesNotFallBackToCustomEditor(t *testing.T) {
	setChatConsoleInputMode(chatConsoleInputSystem)
	defer resetChatConsoleInputMode()

	oldSystemFn := readSystemConsoleLineFn
	oldCustomFn := readLegacyConsoleLineFn
	readSystemConsoleLineFn = func(context.Context) (string, bool, error) {
		return "", false, nil
	}
	readLegacyConsoleLineFn = func(context.Context) (string, bool, error) {
		t.Fatal("explicit system mode must not silently switch to the custom editor")
		return "", false, nil
	}
	defer func() {
		readSystemConsoleLineFn = oldSystemFn
		readLegacyConsoleLineFn = oldCustomFn
	}()

	line, err := readChatSessionLine(
		context.Background(),
		bufio.NewReader(strings.NewReader("buffered fallback\n")),
	)
	if err != nil || line != "buffered fallback\n" {
		t.Fatalf("line = %q, err = %v", line, err)
	}
}

func TestReadChatSessionLineCustomModeSkipsSystemReader(t *testing.T) {
	setChatConsoleInputMode(chatConsoleInputCustom)
	defer resetChatConsoleInputMode()

	oldSystemFn := readSystemConsoleLineFn
	oldCustomFn := readLegacyConsoleLineFn
	readSystemConsoleLineFn = func(context.Context) (string, bool, error) {
		t.Fatal("system reader must not run in custom mode")
		return "", false, nil
	}
	readLegacyConsoleLineFn = func(context.Context) (string, bool, error) {
		return "custom only", true, nil
	}
	defer func() {
		readSystemConsoleLineFn = oldSystemFn
		readLegacyConsoleLineFn = oldCustomFn
	}()

	line, err := readChatSessionLine(context.Background(), nil)
	if err != nil || line != "custom only" {
		t.Fatalf("line = %q, err = %v", line, err)
	}
}

func TestReadConfiguredChatConsoleLineDisabledSkipsConsoleReaders(t *testing.T) {
	resetChatConsoleInputMode()

	oldSystemFn := readSystemConsoleLineFn
	oldCustomFn := readLegacyConsoleLineFn
	readSystemConsoleLineFn = func(context.Context) (string, bool, error) {
		t.Fatal("system reader must not run while console input mode is disabled")
		return "", false, nil
	}
	readLegacyConsoleLineFn = func(context.Context) (string, bool, error) {
		t.Fatal("custom reader must not run while console input mode is disabled")
		return "", false, nil
	}
	defer func() {
		readSystemConsoleLineFn = oldSystemFn
		readLegacyConsoleLineFn = oldCustomFn
	}()

	if line, ok, err := readConfiguredChatConsoleLine(context.Background()); ok || err != nil || line != "" {
		t.Fatalf("readConfiguredChatConsoleLine() = (%q, %v, %v), want empty/unhandled", line, ok, err)
	}
}

func TestChatLegacyConsoleInputModeFlag(t *testing.T) {
	setChatLegacyConsoleInputMode(true)
	if !chatLegacyConsoleInputEnabled() {
		t.Fatal("flag should be enabled after set true")
	}
	setChatLegacyConsoleInputMode(false)
	if chatLegacyConsoleInputEnabled() {
		t.Fatal("flag should be disabled after set false")
	}
}
