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
