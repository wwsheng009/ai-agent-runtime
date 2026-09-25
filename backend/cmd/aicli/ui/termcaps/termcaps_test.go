package termcaps

import (
	"strings"
	"testing"
)

func envFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDetectWindowsTerminalMatrix(t *testing.T) {
	caps := Detect(envFrom(map[string]string{"WT_SESSION": "abc"}), "windows", true)
	if caps.Kind != KindWindowsTerminal || caps.Name != "Windows Terminal" {
		t.Fatalf("kind/name = %s/%s", caps.Kind, caps.Name)
	}
	for name, support := range map[string]Support{
		"shift+tab": caps.ShiftTab,
		"alt+m":     caps.AltM,
		"多行":        caps.ModifiedEnter,
		"粘贴":        caps.BracketedPaste,
		"剪贴板文本":     caps.ClipboardText,
	} {
		if !support.Supported {
			t.Fatalf("%s 在 Windows Terminal 应为可用，got %+v", name, support)
		}
	}
	if caps.ClipboardImage.Supported {
		t.Fatal("测试环境的 GOOS 未声明剪贴板图片支持时不应报告可用")
	}
	if !strings.Contains(caps.ClipboardImage.Reason, "/attach") {
		t.Fatalf("图片不可用原因应给出替代路径，got %q", caps.ClipboardImage.Reason)
	}
}

func TestDetectLegacyConsoleDegradesWithReasons(t *testing.T) {
	caps := Detect(envFrom(nil), "windows", true)
	if caps.Kind != KindLegacyConsole {
		t.Fatalf("kind = %s, want legacy-console", caps.Kind)
	}
	if caps.ShiftTab.Supported {
		t.Fatal("传统 conhost 不应承诺 shift+tab")
	}
	if caps.ModifiedEnter.Supported || caps.BracketedPaste.Supported {
		t.Fatalf("传统 conhost 的 modified-enter/bracketed paste 应为不可用: %+v", caps)
	}
	if !strings.Contains(caps.ShiftTab.Reason, "alt+m") {
		t.Fatalf("shift+tab 降级原因应给出替代键，got %q", caps.ShiftTab.Reason)
	}
	if caps.ClipboardText.Supported != true {
		t.Fatal("Windows 下剪贴板文本读取应可用")
	}
}

func TestDetectVSCodeWarnsAboutHijack(t *testing.T) {
	caps := Detect(envFrom(map[string]string{"TERM_PROGRAM": "vscode"}), "windows", true)
	if caps.Kind != KindVSCode {
		t.Fatalf("kind = %s, want vscode", caps.Kind)
	}
	if !caps.ShiftTab.Supported || !strings.Contains(caps.ShiftTab.Reason, "alt+m") {
		t.Fatalf("VS Code 应可用但提示 IDE 抢占风险，got %+v", caps.ShiftTab)
	}
}

func TestDetectWSLPrefersHostTerminal(t *testing.T) {
	caps := Detect(envFrom(map[string]string{
		"WT_SESSION":      "abc",
		"WSL_DISTRO_NAME": "Ubuntu-22.04",
	}), "linux", true)
	if caps.Kind != KindWSL {
		t.Fatalf("kind = %s, want wsl", caps.Kind)
	}
	if !strings.Contains(caps.Name, "Ubuntu-22.04") {
		t.Fatalf("WSL 名称应带发行版，got %q", caps.Name)
	}
	if caps.ClipboardText.Supported {
		t.Fatal("Linux 下剪贴板文本读取未实现，必须如实报告不可用")
	}
	if !strings.Contains(caps.ClipboardText.Reason, "bracketed paste") {
		t.Fatalf("剪贴板不可用原因应指向终端自身粘贴，got %q", caps.ClipboardText.Reason)
	}
	if len(caps.Notes) == 0 {
		t.Fatal("WSL 应带宿主限制说明")
	}
}

func TestDetectMinttyAndUnknownTerminal(t *testing.T) {
	mintty := Detect(envFrom(map[string]string{"TERM_PROGRAM": "mintty"}), "windows", true)
	if mintty.Kind != KindMintty || !mintty.ShiftTab.Supported {
		t.Fatalf("mintty 矩阵异常: %+v", mintty)
	}

	unknown := Detect(envFrom(nil), "linux", true)
	if unknown.Kind != KindUnknown {
		t.Fatalf("kind = %s, want unknown", unknown.Kind)
	}
	if unknown.ShiftTab.Supported {
		t.Fatal("未识别终端不应承诺 shift+tab")
	}
	if !strings.Contains(unknown.ShiftTab.Reason, "alt+m") {
		t.Fatalf("未识别终端应给出保守原因，got %q", unknown.ShiftTab.Reason)
	}
}

func TestDetectNonTTYAddsNote(t *testing.T) {
	caps := Detect(envFrom(map[string]string{"WT_SESSION": "abc"}), "windows", false)
	found := false
	for _, note := range caps.Notes {
		if strings.Contains(note, "不是终端") {
			found = true
		}
	}
	if !found {
		t.Fatalf("非 TTY 应给出说明: %#v", caps.Notes)
	}
}

func TestSupportDescribeLabelsAvailability(t *testing.T) {
	if got := (Support{true, "CSI Z"}).Describe(); !strings.HasPrefix(got, "可用（") {
		t.Fatalf("Describe() = %q", got)
	}
	if got := (Support{false, ""}).Describe(); got != "不可用" {
		t.Fatalf("Describe() = %q", got)
	}
}
