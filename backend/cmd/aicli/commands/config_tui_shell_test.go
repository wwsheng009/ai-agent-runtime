package commands

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func newShellTUIHarness(input string) (*configTUI, *bytes.Buffer) {
	writer := &bytes.Buffer{}
	return &configTUI{reader: bufio.NewReader(strings.NewReader(input)), writer: writer}, writer
}

func TestPromptShellPreferUpdateAcceptsCommaSeparatedIDs(t *testing.T) {
	tui, _ := newShellTUIHarness("git-bash, BusyBox ,powershell\n")
	prefer, ok, err := tui.promptShellPreferUpdate(runtimeexecutor.ShellConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected the update to be accepted")
	}
	want := []string{"git-bash", "busybox", "powershell"}
	if len(prefer) != len(want) {
		t.Fatalf("prefer = %#v, want %#v", prefer, want)
	}
	for index := range want {
		if prefer[index] != want[index] {
			t.Fatalf("prefer[%d] = %q, want %q", index, prefer[index], want[index])
		}
	}
}

func TestPromptShellPreferUpdateRejectsUnknownIDWithoutWriting(t *testing.T) {
	tui, writer := newShellTUIHarness("git-bash, fish\n")
	prefer, ok, err := tui.promptShellPreferUpdate(runtimeexecutor.ShellConfig{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("unknown candidate must not be accepted, got %#v", prefer)
	}
	if !strings.Contains(writer.String(), "未知候选 ID") {
		t.Fatalf("expected a readable rejection notice, got %q", writer.String())
	}
}

func TestPromptShellPreferUpdateEmptyInputRestoresDefaultChain(t *testing.T) {
	tui, _ := newShellTUIHarness("\n")
	prefer, ok, err := tui.promptShellPreferUpdate(runtimeexecutor.ShellConfig{Prefer: []string{"powershell"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("empty input must clear prefer (back to the built-in chain)")
	}
	if len(prefer) != 0 {
		t.Fatalf("prefer = %#v, want empty", prefer)
	}
}
